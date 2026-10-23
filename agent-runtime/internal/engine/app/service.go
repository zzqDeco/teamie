// Package app orchestrates Agent Runtime application use cases and transaction
// boundaries against pure domain models and outbound repository/gateway ports.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"teamie/agent-runtime/internal/engine/domain"
)

// Service coordinates Agent Runtime application use cases across session leases,
// durable inbox ingestion, schedule wakeup validation, and provenance/context policy.
type Service struct {
	Tx           domain.TxManager
	Sessions     domain.SessionRepository
	Schedules    domain.ScheduleRepository
	Archive      domain.ArchiveRepository
	ToolReceipts domain.ToolReceiptRepository
	IM           domain.IMGateway
	Control      domain.ControlGateway
	Model        domain.ModelGateway
	Sandbox      domain.SandboxGateway
}

// ValidateLease verifies that a worker lease is well-formed and currently held in storage.
func (s *Service) ValidateLease(ctx context.Context, lease *domain.Lease) error {
	if lease == nil || !lease.Valid() {
		return domain.ErrLeaseLost
	}
	if s == nil || s.Sessions == nil {
		return nil
	}
	return s.Sessions.CheckLease(ctx, lease)
}

// CheckRevisionPreemption reports whether an active worker must be canceled due to
// agent disablement, deletion, or revision advancement.
func (s *Service) CheckRevisionPreemption(enabled, deleted bool, currentRevision, workerRevision int64) bool {
	return domain.ShouldPreemptWorker(enabled, deleted, currentRevision, workerRevision)
}

// ResolveRunContextPolicy validates and canonicalizes a Run's context profile and budget snapshot.
func (s *Service) ResolveRunContextPolicy(profile string, raw json.RawMessage) (string, json.RawMessage, error) {
	return domain.CanonicalContextPolicy(profile, raw)
}

// ResolveScheduleRowSources resolves the canonical provenance InputSource set for a schedule row.
func (s *Service) ResolveScheduleRowSources(ctx context.Context, agentID, createdRunID string, sourceRefsJSON []byte, sourceOverflow bool) ([]domain.InputSource, error) {
	var refs []domain.InputSource
	if len(sourceRefsJSON) > 0 {
		if json.Unmarshal(sourceRefsJSON, &refs) != nil {
			return nil, domain.ErrReplayConflict
		}
	}
	if sourceOverflow || len(refs) == 0 {
		if !domain.ValidUUID(createdRunID) || s == nil || s.Archive == nil {
			return nil, domain.ErrContextUnavailable
		}
		return s.Archive.ResolveRunSources(ctx, agentID, createdRunID)
	}
	canonical, err := domain.CanonicalContextSources(refs)
	if err != nil {
		return nil, err
	}
	if len(canonical) == 0 {
		if !domain.ValidUUID(createdRunID) || s == nil || s.Archive == nil {
			return nil, domain.ErrContextUnavailable
		}
		return s.Archive.ResolveRunSources(ctx, agentID, createdRunID)
	}
	return canonical, nil
}

// AuthorizeScheduleInput validates a TEAMIE_SCHEDULE_WAKEUP input against the schedule's
// current status and live IM source ACLs.
func (s *Service) AuthorizeScheduleInput(ctx context.Context, lease *domain.Lease, in domain.Input) error {
	if strings.TrimSpace(in.Body) == "" {
		return domain.ErrContextUnavailable
	}
	parent := in.ParentRunID
	if in.ScheduleID != "" && s != nil && s.Schedules != nil {
		status, createdRunID, found, err := s.Schedules.GetScheduleStatusAndParent(ctx, lease.AgentID, in.ScheduleID)
		if err != nil {
			return err
		}
		if found {
			if status == "canceled" {
				return domain.ErrContextUnavailable
			}
			if parent == "" && createdRunID != "" {
				parent = createdRunID
			}
		}
	}
	var (
		refs []domain.InputSource
		err  error
	)
	if !in.SourceOverflow && len(in.SourceRefs) > 0 {
		refs, err = domain.CanonicalContextSources(in.SourceRefs)
	} else {
		if parent == "" || s == nil || s.Archive == nil {
			return domain.ErrContextUnavailable
		}
		refs, err = s.Archive.ResolveRunSources(ctx, lease.AgentID, parent)
	}
	if err != nil {
		return err
	}
	if len(refs) == 0 || s == nil || s.IM == nil {
		return domain.ErrContextUnavailable
	}
	return s.IM.AuthorizeSources(ctx, refs)
}

// FilterAuthorizedSchedules filters active schedule candidates by re-verifying their
// provenance sources against current IM ACLs, automatically canceling any revoked schedules.
func (s *Service) FilterAuthorizedSchedules(ctx context.Context, agentID string, candidates []domain.ScheduleCandidate) ([]domain.ScheduleItem, error) {
	schedules := make([]domain.ScheduleItem, 0, len(candidates))
	for _, cand := range candidates {
		refs, srcErr := s.ResolveScheduleRowSources(ctx, agentID, cand.CreatedRunID, cand.SourceRefsJSON, cand.SourceOverflow)
		if srcErr == nil {
			if s == nil || s.IM == nil {
				srcErr = domain.ErrContextUnavailable
			} else {
				srcErr = s.IM.AuthorizeSources(ctx, refs)
			}
		}
		if srcErr != nil {
			if errors.Is(srcErr, domain.ErrContextUnavailable) {
				if s != nil && s.Schedules != nil {
					_ = s.Schedules.CancelActiveSchedule(ctx, agentID, cand.View.ScheduleID)
				}
				continue
			}
			return nil, srcErr
		}
		schedules = append(schedules, cand.View)
	}
	return schedules, nil
}

// AuthorizeScheduleListReceipt verifies that every schedule in a cached schedule_list
// tool receipt still has valid live source ACLs, canceling revoked schedules.
func (s *Service) AuthorizeScheduleListReceipt(ctx context.Context, agentID string, cached []byte) error {
	var receipt struct {
		OK   bool `json:"ok"`
		Data struct {
			Schedules []domain.ScheduleItem `json:"schedules"`
		} `json:"data"`
	}
	if json.Unmarshal(cached, &receipt) != nil {
		return domain.ErrReplayConflict
	}
	if !receipt.OK {
		return nil
	}
	if receipt.Data.Schedules == nil {
		return domain.ErrContextUnavailable
	}
	for _, item := range receipt.Data.Schedules {
		if item.ScheduleID == "" || !domain.ValidUUID(agentID) || s == nil || s.Schedules == nil {
			return domain.ErrContextUnavailable
		}
		createdRunID, sourceRefsJSON, sourceOverflow, found, err := s.Schedules.GetScheduleSourcesMeta(ctx, agentID, item.ScheduleID)
		if err != nil {
			return err
		}
		if !found {
			err = domain.ErrContextUnavailable
		} else {
			var refs []domain.InputSource
			refs, err = s.ResolveScheduleRowSources(ctx, agentID, createdRunID, sourceRefsJSON, sourceOverflow)
			if err == nil {
				if s.IM == nil {
					err = domain.ErrContextUnavailable
				} else {
					err = s.IM.AuthorizeSources(ctx, refs)
				}
			}
		}
		if err != nil {
			if errors.Is(err, domain.ErrContextUnavailable) {
				_ = s.Schedules.CancelActiveSchedule(ctx, agentID, item.ScheduleID)
			}
			return err
		}
	}
	return nil
}

// EvaluateScheduleCreation coordinates idempotent schedule creation within a fenced transaction.
// It returns either a created/recovered *domain.ScheduleItem, a user-safe observation error code, or a fatal error.
func (s *Service) EvaluateScheduleCreation(ctx context.Context, lease *domain.Lease, runID, callID string, runInputs []domain.Input, spec domain.ScheduleSpec) (*domain.ScheduleItem, string, error) {
	if _, _, errCode := domain.ValidateSchedulePromptAndTimezone(spec.Prompt, spec.Timezone); errCode != "" {
		return nil, errCode, nil
	}
	var (
		resultItem *domain.ScheduleItem
		obsErrCode string
	)
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Sessions.CheckLeaseForUpdate(txCtx, lease); err != nil {
			return err
		}
		schedID := domain.StableScheduleID(lease.AgentID, runID, callID)
		if s.ToolReceipts != nil {
			if savedSnapshot, found, obsErr := s.ToolReceipts.ReadObservation(txCtx, runID, callID, domain.ScheduleCreateObservationKey); obsErr == nil && found {
				var recovered domain.ScheduleItem
				if json.Unmarshal(savedSnapshot, &recovered) == nil && recovered.ScheduleID == schedID {
					resultItem = &recovered
					return nil
				}
			}
		}
		existing, found, getErr := s.Schedules.FindScheduleByID(txCtx, lease.AgentID, schedID)
		if getErr != nil {
			return getErr
		}
		if found && existing != nil {
			resultItem = existing
			return nil
		}
		now, err := s.Schedules.ClockNow(txCtx)
		if err != nil {
			return err
		}
		validated, code := domain.ValidateScheduleSpec(spec, now)
		if code != "" {
			obsErrCode = code
			return nil
		}
		activeCount, err := s.Schedules.CountActiveSchedules(txCtx, lease.AgentID)
		if err != nil {
			return err
		}
		if quotaCode := domain.CheckScheduleQuota(activeCount); quotaCode != "" {
			obsErrCode = quotaCode
			return nil
		}
		refs, overflow := domain.ExtractInputSources(runInputs)
		if overflow || len(refs) == 0 {
			if s.Archive != nil {
				resolved, srcErr := s.Archive.ResolveRunSources(txCtx, lease.AgentID, runID)
				if srcErr == nil && len(resolved) > 0 {
					refs, overflow = resolved, false
				}
			}
		}
		created, err := s.Schedules.CreateSchedule(txCtx, lease.AgentID, runID, schedID, validated, refs, overflow, now)
		if err != nil {
			return err
		}
		if s.ToolReceipts != nil {
			snapJSON, _ := json.Marshal(created)
			if err := s.ToolReceipts.SaveObservation(txCtx, lease, runID, callID, domain.ScheduleCreateObservationKey, snapJSON); err != nil {
				return err
			}
		}
		resultItem = created
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return resultItem, obsErrCode, nil
}

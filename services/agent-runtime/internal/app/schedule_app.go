package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"teamie/services/agent-runtime/internal/domain"
)

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

// EvaluateScheduleUpdate coordinates idempotent in-place schedule updates within a fenced transaction.
// It returns either the updated/recovered *domain.ScheduleItem, a user-safe observation error code, or a fatal error.
func (s *Service) EvaluateScheduleUpdate(ctx context.Context, lease *domain.Lease, runID, callID, scheduleID string, runInputs []domain.Input, spec domain.ScheduleSpec) (*domain.ScheduleItem, string, error) {
	id, ok := domain.ValidateScheduleCancelID(scheduleID)
	if !ok {
		return nil, "invalid_arguments", nil
	}
	var (
		resultItem *domain.ScheduleItem
		obsErrCode string
	)
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Sessions.CheckLeaseForUpdate(txCtx, lease); err != nil {
			return err
		}
		var recovered *domain.ScheduleItem
		if s.ToolReceipts != nil {
			saved, found, err := s.ToolReceipts.ReadObservation(txCtx, runID, callID, domain.ScheduleUpdateObservationKey)
			if err != nil {
				return err
			}
			if found {
				if json.Unmarshal(saved, &recovered) != nil || recovered == nil || recovered.ScheduleID != id {
					return domain.ErrReplayConflict
				}
			}
		}
		existing, found, getErr := s.Schedules.FindScheduleByID(txCtx, lease.AgentID, id)
		if getErr != nil {
			return getErr
		}
		if !found || existing == nil {
			obsErrCode = "not_found"
			return nil
		}
		if existing.Status != "active" && recovered == nil {
			obsErrCode = "schedule_not_active"
			return nil
		}
		createdRunID, sourceRefsJSON, sourceOverflow, metaFound, metaErr := s.Schedules.GetScheduleSourcesMeta(txCtx, lease.AgentID, id)
		if metaErr != nil {
			return metaErr
		}
		if !metaFound || s.IM == nil {
			return domain.ErrContextUnavailable
		}
		var existingRefs []domain.InputSource
		if metaFound {
			var srcErr error
			existingRefs, srcErr = s.ResolveScheduleRowSources(txCtx, lease.AgentID, createdRunID, sourceRefsJSON, sourceOverflow)
			if srcErr == nil && s.IM != nil {
				srcErr = s.IM.AuthorizeSources(txCtx, existingRefs)
			}
			if srcErr != nil {
				if errors.Is(srcErr, domain.ErrContextUnavailable) {
					_ = s.Schedules.CancelActiveSchedule(txCtx, lease.AgentID, id)
					obsErrCode = "not_found"
					return nil
				}
				return srcErr
			}
		}
		if recovered != nil {
			resultItem = recovered
			return nil
		}
		now, err := s.Schedules.ClockNow(txCtx)
		if err != nil {
			return err
		}
		validated, code := domain.ValidateScheduleUpdateSpec(*existing, spec, now)
		if code != "" {
			obsErrCode = code
			return nil
		}
		refs, overflow := domain.ExtractInputSources(runInputs)
		if overflow || len(refs) == 0 {
			if s.Archive == nil {
				return domain.ErrContextUnavailable
			}
			var err error
			refs, err = s.Archive.ResolveRunSources(txCtx, lease.AgentID, runID)
			if err != nil {
				return err
			}
			if len(refs) == 0 {
				return domain.ErrContextUnavailable
			}
		}
		refs, err = domain.CanonicalContextSources(append(existingRefs, refs...))
		if err != nil {
			return err
		}
		if err = s.IM.AuthorizeSources(txCtx, refs); err != nil {
			return err
		}
		overflow = false
		updated, err := s.Schedules.UpdateSchedule(txCtx, lease.AgentID, id, validated, refs, overflow, now)
		if err != nil {
			return err
		}
		if s.ToolReceipts != nil {
			snapJSON, _ := json.Marshal(updated)
			if err := s.ToolReceipts.SaveObservation(txCtx, lease, runID, callID, domain.ScheduleUpdateObservationKey, snapJSON); err != nil {
				return err
			}
		}
		resultItem = updated
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return resultItem, obsErrCode, nil
}

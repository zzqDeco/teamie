package domain

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// PolicyEngine coordinates pure domain policies across session leases,
// durable inbox ingestion, schedule wakeup validation, and provenance/context policy.
type PolicyEngine struct {
	Tx           TxManager
	Sessions     SessionRepository
	Schedules    ScheduleRepository
	Archive      ArchiveRepository
	ToolReceipts ToolReceiptRepository
	IM           IMGateway
	Control      ControlGateway
	Model        ModelGateway
	Sandbox      SandboxGateway
}

// ValidateLease verifies that a worker lease is well-formed and currently held in storage.
func (p *PolicyEngine) ValidateLease(ctx context.Context, lease *Lease) error {
	if lease == nil || !lease.Valid() {
		return ErrLeaseLost
	}
	if p == nil || p.Sessions == nil {
		return nil
	}
	return p.Sessions.CheckLease(ctx, lease)
}

// CheckRevisionPreemption reports whether an active worker must be canceled due to
// agent disablement, deletion, or revision advancement.
func (p *PolicyEngine) CheckRevisionPreemption(enabled, deleted bool, currentRevision, workerRevision int64) bool {
	return ShouldPreemptWorker(enabled, deleted, currentRevision, workerRevision)
}

// ResolveRunContextPolicy validates and canonicalizes a Run's context profile and budget snapshot.
func (p *PolicyEngine) ResolveRunContextPolicy(profile string, raw json.RawMessage) (string, json.RawMessage, error) {
	return CanonicalContextPolicy(profile, raw)
}

// ResolveScheduleRowSources resolves the canonical provenance InputSource set for a schedule row.
func (p *PolicyEngine) ResolveScheduleRowSources(ctx context.Context, agentID, createdRunID string, sourceRefsJSON []byte, sourceOverflow bool) ([]InputSource, error) {
	var refs []InputSource
	if len(sourceRefsJSON) > 0 {
		if json.Unmarshal(sourceRefsJSON, &refs) != nil {
			return nil, ErrReplayConflict
		}
	}
	if sourceOverflow || len(refs) == 0 {
		if !ValidUUID(createdRunID) || p == nil || p.Archive == nil {
			return nil, ErrContextUnavailable
		}
		return p.Archive.ResolveRunSources(ctx, agentID, createdRunID)
	}
	canonical, err := CanonicalContextSources(refs)
	if err != nil {
		return nil, err
	}
	if len(canonical) == 0 {
		if !ValidUUID(createdRunID) || p == nil || p.Archive == nil {
			return nil, ErrContextUnavailable
		}
		return p.Archive.ResolveRunSources(ctx, agentID, createdRunID)
	}
	return canonical, nil
}

// AuthorizeScheduleInput validates a TEAMIE_SCHEDULE_WAKEUP input against the schedule's
// current status and live IM source ACLs.
func (p *PolicyEngine) AuthorizeScheduleInput(ctx context.Context, lease *Lease, in Input) error {
	if strings.TrimSpace(in.Body) == "" {
		return ErrContextUnavailable
	}
	parent := in.ParentRunID
	if in.ScheduleID != "" && p != nil && p.Schedules != nil {
		status, createdRunID, found, err := p.Schedules.GetScheduleStatusAndParent(ctx, lease.AgentID, in.ScheduleID)
		if err != nil {
			return err
		}
		if found {
			if status == "canceled" {
				return ErrContextUnavailable
			}
			if parent == "" && createdRunID != "" {
				parent = createdRunID
			}
		}
	}
	var (
		refs []InputSource
		err  error
	)
	if !in.SourceOverflow && len(in.SourceRefs) > 0 {
		refs, err = CanonicalContextSources(in.SourceRefs)
	} else {
		if parent == "" || p == nil || p.Archive == nil {
			return ErrContextUnavailable
		}
		refs, err = p.Archive.ResolveRunSources(ctx, lease.AgentID, parent)
	}
	if err != nil {
		return err
	}
	if len(refs) == 0 || p == nil || p.IM == nil {
		return ErrContextUnavailable
	}
	return p.IM.AuthorizeSources(ctx, refs)
}

// FilterAuthorizedSchedules filters active schedule candidates by re-verifying their
// provenance sources against current IM ACLs, automatically canceling any revoked schedules.
func (p *PolicyEngine) FilterAuthorizedSchedules(ctx context.Context, agentID string, candidates []ScheduleCandidate) ([]ScheduleItem, error) {
	schedules := make([]ScheduleItem, 0, len(candidates))
	for _, cand := range candidates {
		refs, srcErr := p.ResolveScheduleRowSources(ctx, agentID, cand.CreatedRunID, cand.SourceRefsJSON, cand.SourceOverflow)
		if srcErr == nil {
			if p == nil || p.IM == nil {
				srcErr = ErrContextUnavailable
			} else {
				srcErr = p.IM.AuthorizeSources(ctx, refs)
			}
		}
		if srcErr != nil {
			if errors.Is(srcErr, ErrContextUnavailable) {
				if p != nil && p.Schedules != nil {
					_ = p.Schedules.CancelActiveSchedule(ctx, agentID, cand.View.ScheduleID)
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
func (p *PolicyEngine) AuthorizeScheduleListReceipt(ctx context.Context, agentID string, cached []byte) error {
	var receipt struct {
		OK   bool `json:"ok"`
		Data struct {
			Schedules []ScheduleItem `json:"schedules"`
		} `json:"data"`
	}
	if json.Unmarshal(cached, &receipt) != nil {
		return ErrReplayConflict
	}
	if !receipt.OK {
		return nil
	}
	if receipt.Data.Schedules == nil {
		return ErrContextUnavailable
	}
	for _, item := range receipt.Data.Schedules {
		if item.ScheduleID == "" || !ValidUUID(agentID) || p == nil || p.Schedules == nil {
			return ErrContextUnavailable
		}
		createdRunID, sourceRefsJSON, sourceOverflow, found, err := p.Schedules.GetScheduleSourcesMeta(ctx, agentID, item.ScheduleID)
		if err != nil {
			return err
		}
		if !found {
			err = ErrContextUnavailable
		} else {
			var refs []InputSource
			refs, err = p.ResolveScheduleRowSources(ctx, agentID, createdRunID, sourceRefsJSON, sourceOverflow)
			if err == nil {
				if p.IM == nil {
					err = ErrContextUnavailable
				} else {
					err = p.IM.AuthorizeSources(ctx, refs)
				}
			}
		}
		if err != nil {
			if errors.Is(err, ErrContextUnavailable) {
				_ = p.Schedules.CancelActiveSchedule(ctx, agentID, item.ScheduleID)
			}
			return err
		}
	}
	return nil
}

// EvaluateScheduleCreation coordinates idempotent schedule creation within a fenced transaction.
// It returns either a created/recovered *ScheduleItem, a user-safe observation error code, or a fatal error.
func (p *PolicyEngine) EvaluateScheduleCreation(ctx context.Context, lease *Lease, runID, callID string, runInputs []Input, spec ScheduleSpec) (*ScheduleItem, string, error) {
	if _, _, errCode := ValidateSchedulePromptAndTimezone(spec.Prompt, spec.Timezone); errCode != "" {
		return nil, errCode, nil
	}
	var (
		resultItem *ScheduleItem
		obsErrCode string
	)
	err := p.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := p.Sessions.CheckLeaseForUpdate(txCtx, lease); err != nil {
			return err
		}
		schedID := StableScheduleID(lease.AgentID, runID, callID)
		if p.ToolReceipts != nil {
			if savedSnapshot, found, obsErr := p.ToolReceipts.ReadObservation(txCtx, runID, callID, ScheduleCreateObservationKey); obsErr == nil && found {
				var recovered ScheduleItem
				if json.Unmarshal(savedSnapshot, &recovered) == nil && recovered.ScheduleID == schedID {
					resultItem = &recovered
					return nil
				}
			}
		}
		existing, found, getErr := p.Schedules.FindScheduleByID(txCtx, lease.AgentID, schedID)
		if getErr != nil {
			return getErr
		}
		if found && existing != nil {
			resultItem = existing
			return nil
		}
		now, err := p.Schedules.ClockNow(txCtx)
		if err != nil {
			return err
		}
		validated, code := ValidateScheduleSpec(spec, now)
		if code != "" {
			obsErrCode = code
			return nil
		}
		activeCount, err := p.Schedules.CountActiveSchedules(txCtx, lease.AgentID)
		if err != nil {
			return err
		}
		if quotaCode := CheckScheduleQuota(activeCount); quotaCode != "" {
			obsErrCode = quotaCode
			return nil
		}
		refs, overflow := ExtractInputSources(runInputs)
		if overflow || len(refs) == 0 {
			if p.Archive != nil {
				resolved, srcErr := p.Archive.ResolveRunSources(txCtx, lease.AgentID, runID)
				if srcErr == nil && len(resolved) > 0 {
					refs, overflow = resolved, false
				}
			}
		}
		created, err := p.Schedules.CreateSchedule(txCtx, lease.AgentID, runID, schedID, validated, refs, overflow, now)
		if err != nil {
			return err
		}
		if p.ToolReceipts != nil {
			snapJSON, _ := json.Marshal(created)
			if err := p.ToolReceipts.SaveObservation(txCtx, lease, runID, callID, ScheduleCreateObservationKey, snapJSON); err != nil {
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

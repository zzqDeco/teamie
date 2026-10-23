package pg

import (
	"context"
	"encoding/json"

	"teamie/services/agent-runtime/internal/domain"
)

// ResolveRunSources recursively walks durable run/task/schedule parent provenance to compute
// the canonical InputSource closure for runID.
func (r *Repository) ResolveRunSources(ctx context.Context, agentID, runID string) ([]domain.InputSource, error) {
	visited := map[string]bool{}
	active := map[string]bool{}
	refs := []domain.InputSource{}
	conn := r.db.Conn(ctx)
	var visit func(string) error
	visit = func(id string) error {
		if active[id] {
			return domain.ErrContextUnavailable
		}
		if visited[id] {
			return nil
		}
		active[id] = true
		defer delete(active, id)
		visited[id] = true
		var raw []byte
		if err := conn.QueryRow(ctx, `SELECT input_json FROM runtime_runs WHERE id=$1 AND agent_id=$2`, id, agentID).Scan(&raw); err != nil {
			return err
		}
		var inputs []domain.Input
		if json.Unmarshal(raw, &inputs) != nil {
			return domain.ErrContextUnavailable
		}
		for _, in := range inputs {
			if in.Kind != "task.notification" && in.Kind != "schedule.wakeup" && in.ConversationID != "" && in.MessageID != "" {
				ref := domain.InputSource{
					EventID:        in.EventID,
					WorkspaceID:    in.WorkspaceID,
					ConversationID: in.ConversationID,
					MessageID:      in.MessageID,
					ReplyToID:      in.ReplyToID,
					SenderID:       in.SenderID,
					SenderKind:     in.SenderKind,
				}
				for _, attachment := range in.Attachments {
					ref.AttachmentIDs = append(ref.AttachmentIDs, attachment.ID)
				}
				refs = append(refs, ref)
			}
			refs = append(refs, in.SourceRefs...)
			parent := in.ParentRunID
			if parent == "" && in.Kind == "task.notification" && len(in.SourceRefs) == 0 {
				if in.TaskID == "" {
					return domain.ErrContextUnavailable
				}
				var storedParent *string
				if err := conn.QueryRow(ctx, `SELECT run_id::text FROM runtime_tasks WHERE id=$1 AND agent_id=$2`, in.TaskID, agentID).Scan(&storedParent); err != nil || storedParent == nil {
					return domain.ErrContextUnavailable
				}
				parent = *storedParent
			}
			if parent == "" && in.Kind == "schedule.wakeup" && len(in.SourceRefs) == 0 {
				if in.ScheduleID == "" {
					return domain.ErrContextUnavailable
				}
				var storedParent *string
				if err := conn.QueryRow(ctx, `SELECT created_run_id::text FROM runtime_schedules WHERE id=$1 AND agent_id=$2`, in.ScheduleID, agentID).Scan(&storedParent); err != nil || storedParent == nil {
					return domain.ErrContextUnavailable
				}
				parent = *storedParent
			}
			if parent != "" {
				if !domain.ValidUUID(parent) {
					return domain.ErrContextUnavailable
				}
				if err := visit(parent); err != nil {
					return err
				}
			} else if in.SourceOverflow {
				return domain.ErrContextUnavailable
			}
		}
		return nil
	}
	if err := visit(runID); err != nil {
		return nil, err
	}
	refs, err := domain.CanonicalContextSources(refs)
	if err == nil && len(refs) == 0 {
		err = domain.ErrContextUnavailable
	}
	return refs, err
}

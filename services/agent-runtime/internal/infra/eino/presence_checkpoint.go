package eino

import (
	"context"

	"teamie/services/agent-runtime/internal/domain"
)

const checkpointPresenceVersion = 1

func (r *Run) checkpointPresence() *domain.CheckpointPresence {
	r.presenceMu.Lock()
	defer r.presenceMu.Unlock()
	return &domain.CheckpointPresence{
		Version: checkpointPresenceVersion, Enabled: r.presenceEnabled,
		WorkspaceID: r.activeWorkspaceID, ConversationID: r.activeConversationID,
		ConvType: r.activeConvType,
	}
}

func validCheckpointPresence(p *domain.CheckpointPresence) bool {
	if p.Version != checkpointPresenceVersion {
		return false
	}
	if p.ConversationID == "" {
		return p.WorkspaceID == "" && p.ConvType == ""
	}
	return p.Enabled && domain.ValidUUID(p.WorkspaceID) && domain.ValidUUID(p.ConversationID) &&
		(p.ConvType == "group" || p.ConvType == "channel" || p.ConvType == "direct")
}

// Select the recovery path once, before configuring the Run. Native resume skips
// completed tools, while receipt replay must start at the immutable initial scope.
func (s *Store) selectRunResume(ctx context.Context, metadata *checkpointMetadata, revision int64, run *Run) (*checkpointMetadata, error) {
	if !resumeCompatibleForRun(metadata, revision, run) {
		return nil, nil
	}
	if metadata.Presence != nil {
		if !validCheckpointPresence(metadata.Presence) {
			return nil, nil
		}
		return metadata, nil
	}
	_, admitted, err := s.DB.ReadObservation(ctx, run.ID, presenceInitObservationCallID, presenceInitObservationKind)
	if err != nil {
		return nil, err
	}
	if admitted {
		// A presence-aware old checkpoint cannot reconstruct its execution scope.
		// Keep the Run and receipts and let the existing full replay rebuild it.
		return nil, nil
	}
	return metadata, nil
}

func (t *Tools) restoreCheckpointPresence(ctx context.Context, run *Run) (bool, error) {
	p := run.resume.Presence
	if p == nil || !p.Enabled {
		// Legacy native checkpoints must not adopt today's feature flag.
		return false, nil
	}
	run.presenceMu.Lock()
	run.presenceEnabled = true
	run.activeWorkspaceID, run.activeConversationID, run.activeConvType = p.WorkspaceID, p.ConversationID, p.ConvType
	run.mcpTools = nil
	run.presenceMu.Unlock()
	if t.Presence != nil {
		t.Presence.restoreActive(p.WorkspaceID, p.ConversationID, p.ConvType)
		if err := t.Presence.Persist(ctx, t.Store, t.Lease); err != nil {
			return false, err
		}
	}
	return true, nil
}

// Restoring execution state is not an IM read or an enter tool call. Preserve
// unread counts, dirty timestamps and the IM read cursor, including new arrivals.
func (p *conversationPresenceState) restoreActive(workspaceID, conversationID, convType string) {
	p.mu.Lock()
	changed := p.activeWorkspaceID != workspaceID || p.activeConversationID != conversationID || p.activeConvType != convType
	p.activeWorkspaceID, p.activeConversationID, p.activeConvType = workspaceID, conversationID, convType
	fn := p.onChange
	p.mu.Unlock()
	if changed && fn != nil {
		fn()
	}
}

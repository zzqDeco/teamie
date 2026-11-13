package domain

import "context"

// PeerInstanceHeader identifies the originating Model Gateway process on internal
// peer synchronization requests so a slot never re-processes its own broadcast.
const PeerInstanceHeader = "X-Teamie-Gateway-Instance"

// PeerBroadcaster propagates run cancellations and catalog/search reconciliations
// across sibling Model Gateway rollout slots on the internal private network.
type PeerBroadcaster interface {
	BroadcastCancel(ctx context.Context, agentID, runID string) error
	BroadcastReconcile(ctx context.Context, resetProviderID string) error
}

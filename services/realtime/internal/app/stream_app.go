// Package app orchestrates Realtime client admission, conversation access
// verification, event broadcast, and typing state transitions.
package app

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"teamie/services/realtime/internal/domain"
)

// StreamApp coordinates Realtime stream admission, outbox hint fanout, presence, and typing forwarding.
type StreamApp struct {
	Identity      domain.SessionValidator
	Chat          domain.AccessAuthority
	Flags         domain.FeatureFlags
	Bus           domain.HintBus
	Policy        *domain.TypingTracker
	Presence      *domain.PresenceTracker
	PresenceStore domain.PresenceStore
}

// HubService is an alias for StreamApp.
type HubService = StreamApp

// CheckDependencies verifies Identity and Chat readiness.
func (s *StreamApp) CheckDependencies(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if s.Identity != nil {
		if err := s.Identity.Ping(ctx); err != nil {
			return err
		}
	}
	if s.Chat != nil {
		return s.Chat.Ping(ctx)
	}
	return nil
}

// CheckConnectionCapacity verifies per-user and global WebSocket connection limits.
func (s *StreamApp) CheckConnectionCapacity(clients []domain.Client, userID string) error {
	n := 0
	for _, c := range clients {
		if c.UserID() == userID {
			n++
		}
	}
	if n >= domain.MaxPeersPerUser || len(clients) >= domain.MaxTotalPeers {
		return domain.ErrTooManyConnections()
	}
	return nil
}

// Deliver verifies sender/recipient membership and broadcasts a realtime hint to matching clients.
func (s *StreamApp) Deliver(ctx context.Context, clients []domain.Client, hint domain.Event) {
	switch hint.Type {
	case "typing":
		if s.Chat == nil {
			return
		}
		access, err := s.Chat.CheckAccess(ctx, hint.UserID, hint.ConversationID)
		if err != nil || !access.Allowed || !access.TypingEnabled {
			return
		}
	case "receipts.changed":
		if hint.ConversationID == "" || (s.Flags != nil && !s.Flags.ReadReceiptsViewEnabled()) {
			return
		}
		if s.Policy != nil && !s.Policy.AdmitReceiptHint(hint.ConversationID) {
			return
		}
	case "presence":
		if hint.UserID == "" || (s.Flags != nil && !s.Flags.PresencePublishEnabled()) {
			return
		}
		if hint.State != "online" && hint.State != "away" && hint.State != "offline" {
			return
		}
	}
	set := make(map[string]bool, len(hint.UserIDs))
	for _, id := range hint.UserIDs {
		set[id] = true
	}
	var targets []domain.Client
	for _, c := range clients {
		if set[c.UserID()] {
			targets = append(targets, c)
		}
	}
	for _, c := range targets {
		switch hint.Type {
		case "typing":
			if c.UserID() == hint.UserID || (s.Flags != nil && !s.Flags.TypingPublishEnabled()) {
				continue
			}
			access, err := s.Chat.CheckAccess(ctx, c.UserID(), hint.ConversationID)
			if err != nil || !access.Allowed || !access.TypingEnabled {
				continue
			}
			if s.Identity != nil {
				if _, err = s.Identity.Validate(ctx, c.Token()); err != nil {
					c.Close()
					continue
				}
			}
			c.Send(hint)
		case "receipts.changed":
			if s.Chat != nil {
				access, err := s.Chat.CheckAccess(ctx, c.UserID(), hint.ConversationID)
				if err != nil || !access.Allowed {
					continue
				}
			}
			c.Send(domain.Event{
				Type:           "receipts.changed",
				ConversationID: hint.ConversationID,
			})
		case "presence":
			c.Send(domain.Event{
				Type:   "presence",
				UserID: hint.UserID,
				State:  hint.State,
			})
		default:
			c.Send(hint)
		}
	}
}

// PublishTyping validates permissions, serializes per-(user, conversation) typing
// state transitions, and publishes or delivers the ephemeral typing hint.
func (s *StreamApp) PublishTyping(ctx context.Context, clients func() []domain.Client, user domain.User, conversationID string, isTyping bool) error {
	if conversationID == "" {
		return domain.ErrInvalidRequest("conversationId is required")
	}
	key := domain.TypingKey{UserID: user.ID, ConversationID: conversationID}
	unlock, err := s.Policy.Lock(ctx, key)
	if err != nil {
		return err
	}
	defer unlock()
	if s.Flags != nil && !s.Flags.TypingPublishEnabled() {
		return domain.ErrFeatureDisabled()
	}
	access, err := s.Chat.CheckAccess(ctx, user.ID, conversationID)
	if err != nil {
		return domain.ErrChatUnavailable()
	}
	if !access.Allowed {
		return domain.ErrForbidden()
	}
	if !access.TypingEnabled {
		return domain.ErrFeatureDisabled()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if isTyping && !s.Policy.AdmitPositive(key) {
		return nil
	}
	hint := domain.Event{
		Type:           "typing",
		UserIDs:        access.MemberIDs,
		ConversationID: conversationID,
		UserID:         user.ID,
		DisplayName:    user.DisplayName,
		IsTyping:       isTyping,
	}
	if s.Bus == nil {
		var current []domain.Client
		if clients != nil {
			current = clients()
		}
		s.Deliver(ctx, current, hint)
		if !isTyping {
			s.Policy.ResetPositive(key)
		}
		return nil
	}
	b, _ := json.Marshal(hint)
	if err = s.Bus.Publish(ctx, domain.HintChannel, b); err != nil {
		return domain.ErrRealtimeUnavailable()
	}
	if !isTyping {
		s.Policy.ResetPositive(key)
	}
	return nil
}

func (s *StreamApp) savePresence(ctx context.Context, userID, state string) {
	if s.PresenceStore != nil {
		_ = s.PresenceStore.SaveUserPresence(ctx, userID, state, 10*time.Minute)
	}
}

func (s *StreamApp) publishPresenceTransition(ctx context.Context, clients func() []domain.Client, userID, state string, exclude domain.Client) {
	s.savePresence(ctx, userID, state)
	if s.Flags != nil && !s.Flags.PresencePublishEnabled() {
		return
	}
	if s.Chat == nil {
		return
	}
	memberIDs, enabled, err := s.Chat.SharedWorkspaceMembers(ctx, userID)
	if err != nil || !enabled || len(memberIDs) == 0 {
		return
	}
	hint := domain.Event{
		Type:    "presence",
		UserIDs: memberIDs,
		UserID:  userID,
		State:   state,
	}
	if s.Bus == nil {
		var current []domain.Client
		if clients != nil {
			for _, cl := range clients() {
				if cl != exclude {
					current = append(current, cl)
				}
			}
		}
		s.Deliver(ctx, current, hint)
		return
	}
	b, _ := json.Marshal(hint)
	_ = s.Bus.Publish(ctx, domain.HintChannel, b)
}

// ConnectPresence registers a human participant's WebSocket connection and publishes any state transition.
func (s *StreamApp) ConnectPresence(ctx context.Context, clients func() []domain.Client, c domain.Client, user domain.User) {
	if s.Presence == nil || user.Kind != "human" {
		return
	}
	changed, state := s.Presence.Connect(c, user.Kind)
	if state != "" {
		s.savePresence(ctx, user.ID, state)
	}
	if changed {
		s.publishPresenceTransition(ctx, clients, user.ID, state, c)
	}
}

// ReportPresence records an active or idle WebSocket presence frame from a human participant.
func (s *StreamApp) ReportPresence(ctx context.Context, clients func() []domain.Client, c domain.Client, user domain.User, clientState string) error {
	if clientState != "active" && clientState != "idle" {
		return domain.ErrInvalidRequest("state must be active or idle")
	}
	if (s.Flags != nil && !s.Flags.PresencePublishEnabled()) || s.Presence == nil || user.Kind != "human" {
		return nil
	}
	changed, state, err := s.Presence.Report(c, user.Kind, clientState)
	if err != nil {
		return err
	}
	if state != "" {
		s.savePresence(ctx, user.ID, state)
	}
	if changed {
		s.publishPresenceTransition(ctx, clients, user.ID, state, nil)
	}
	return nil
}

// DisconnectPresence unregisters a closed WebSocket connection and schedules the 30-second offline grace timer.
func (s *StreamApp) DisconnectPresence(clients func() []domain.Client, c domain.Client) {
	if s.Presence == nil {
		return
	}
	s.Presence.Disconnect(c, func(userID, state string) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.publishPresenceTransition(ctx, clients, userID, state, c)
	})
}

// SweepPresence evaluates idle timeouts and offline grace expirations.
func (s *StreamApp) SweepPresence(ctx context.Context, clients func() []domain.Client) {
	if s.Presence == nil {
		return
	}
	s.Presence.Sweep(func(userID, state string) {
		s.publishPresenceTransition(ctx, clients, userID, state, nil)
	})
}

// WorkspacePresence returns non-offline presence states for human members of workspaceID.
func (s *StreamApp) WorkspacePresence(ctx context.Context, clients func() []domain.Client, user domain.User, workspaceID string) ([]domain.PresenceItem, error) {
	if workspaceID == "" {
		return nil, domain.ErrNotFound()
	}
	if s.Chat == nil {
		return nil, domain.ErrChatUnavailable()
	}
	access, err := s.Chat.CheckWorkspaceAccess(ctx, user.ID, workspaceID)
	if err != nil {
		return nil, domain.ErrChatUnavailable()
	}
	if !access.Allowed {
		return nil, domain.ErrNotFound()
	}
	if (s.Flags != nil && !s.Flags.PresencePublishEnabled()) || !access.PresenceEnabled {
		return []domain.PresenceItem{}, nil
	}
	s.SweepPresence(ctx, clients)
	byUser := make(map[string]string, len(access.MemberIDs))
	if s.PresenceStore != nil && len(access.MemberIDs) > 0 {
		if stored, err := s.PresenceStore.GetUsersPresence(ctx, access.MemberIDs); err == nil {
			for uid, st := range stored {
				if st == "online" || st == "away" {
					byUser[uid] = st
				}
			}
		}
	}
	if s.Presence != nil {
		for _, item := range s.Presence.Snapshot(access.MemberIDs) {
			byUser[item.UserID] = item.State
		}
	}
	out := make([]domain.PresenceItem, 0, len(byUser))
	for _, uid := range access.MemberIDs {
		if st, ok := byUser[uid]; ok && (st == "online" || st == "away") {
			delete(byUser, uid)
			out = append(out, domain.PresenceItem{UserID: uid, State: st})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UserID < out[j].UserID
	})
	return out, nil
}

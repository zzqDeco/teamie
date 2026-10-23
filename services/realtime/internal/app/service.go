// Package app orchestrates Realtime client admission, conversation access
// verification, event broadcast, and typing state transitions.
package app

import (
	"context"
	"encoding/json"
	"time"

	"teamie/services/realtime/internal/domain"
)

// HubService coordinates Realtime application use cases.
type HubService struct {
	Identity domain.SessionValidator
	Chat     domain.AccessAuthority
	Flags    domain.FeatureFlags
	Bus      domain.HintBus
	Policy   *domain.TypingPolicy
}

// CheckDependencies verifies Identity and Chat readiness.
func (s *HubService) CheckDependencies(ctx context.Context) error {
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
func (s *HubService) CheckConnectionCapacity(clients []domain.Client, userID string) error {
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
func (s *HubService) Deliver(ctx context.Context, clients []domain.Client, hint domain.Event) {
	if hint.Type == "typing" {
		if s.Chat == nil {
			return
		}
		access, err := s.Chat.CheckAccess(ctx, hint.UserID, hint.ConversationID)
		if err != nil || !access.Allowed || !access.TypingEnabled {
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
		if hint.Type == "typing" {
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
		}
		c.Send(hint)
	}
}

// PublishTyping validates permissions, serializes per-(user, conversation) typing
// state transitions, and publishes or delivers the ephemeral typing hint.
func (s *HubService) PublishTyping(ctx context.Context, clients func() []domain.Client, user domain.User, conversationID string, isTyping bool) error {
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

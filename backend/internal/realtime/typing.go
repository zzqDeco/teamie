package realtime

import (
	"context"
	"encoding/json"
	"net/http"
	"teamie/backend/internal/api"
	"teamie/backend/internal/chat"
	"time"
)

const typingMinimumInterval = 300 * time.Millisecond

type typingGate struct {
	active       chan struct{}
	refs         int
	lastPositive time.Time
}

// Serialize a participant's signals for one conversation, including the Redis
// publish. An HTTP caller may stop waiting before its server request finishes;
// a subsequent clear must still follow that request rather than be overtaken.
// Idle gates retain only the positive-hint throttle for 300ms, never status.
func (h *Hub) lockTyping(ctx context.Context, user, conversation string) (func(), error) {
	key := user + "/" + conversation
	h.typingMu.Lock()
	if h.typingGates == nil {
		h.typingGates = map[string]*typingGate{}
	}
	now := time.Now()
	for id, gate := range h.typingGates {
		if gate.refs == 0 && now.Sub(gate.lastPositive) >= typingMinimumInterval {
			delete(h.typingGates, id)
		}
	}
	gate := h.typingGates[key]
	if gate == nil {
		gate = &typingGate{active: make(chan struct{}, 1)}
		h.typingGates[key] = gate
	}
	gate.refs++
	h.typingMu.Unlock()
	forget := func() {
		h.typingMu.Lock()
		gate.refs--
		if gate.refs == 0 && time.Since(gate.lastPositive) >= typingMinimumInterval {
			delete(h.typingGates, key)
		}
		h.typingMu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		forget()
		return nil, err
	}
	select {
	case gate.active <- struct{}{}:
		return func() { <-gate.active; forget() }, nil
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
}

func (h *Hub) admitPositiveTyping(user, conversation string) bool {
	h.typingMu.Lock()
	defer h.typingMu.Unlock()
	gate := h.typingGates[user+"/"+conversation]
	now := time.Now()
	if now.Sub(gate.lastPositive) < typingMinimumInterval {
		return false
	}
	gate.lastPositive = now
	return true
}

func (h *Hub) resetPositiveTyping(user, conversation string) {
	h.typingMu.Lock()
	h.typingGates[user+"/"+conversation].lastPositive = time.Time{}
	h.typingMu.Unlock()
}

// Typing belongs to IM and is the same ephemeral signal for every authenticated
// participant. It has no connection to an Agent's private execution state.
func (h *Hub) typing(w http.ResponseWriter, r *http.Request, s api.Session) error {
	w.Header().Set("Cache-Control", "no-store")
	var input struct {
		IsTyping *bool `json:"isTyping"`
	}
	if err := api.Decode(w, r, &input); err != nil {
		return err
	}
	if input.IsTyping == nil {
		return api.Err(400, "invalid_request", "isTyping is required")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := h.publishTyping(ctx, s.User, r.PathValue("id"), *input.IsTyping); err != nil {
		return err
	}
	api.Write(w, http.StatusNoContent, nil)
	return nil
}

func (h *Hub) publishTyping(ctx context.Context, user api.User, conversationID string, isTyping bool) error {
	if conversationID == "" {
		return api.Err(400, "invalid_request", "conversationId is required")
	}
	unlock, err := h.lockTyping(ctx, user.ID, conversationID)
	if err != nil {
		return err
	}
	defer unlock()
	if !h.Flags.Load().Flags["im.typing.publish"] {
		return api.Err(403, "feature_disabled", "Typing is disabled")
	}
	access, err := h.Chat.CheckAccess(ctx, user.ID, conversationID)
	if err != nil {
		return api.Err(503, "chat_unavailable", "Conversation access is temporarily unavailable")
	}
	if access == nil || !access.Allowed {
		return api.Err(403, "forbidden", "Conversation access is required")
	}
	if !access.TypingEnabled {
		return api.Err(403, "feature_disabled", "Typing is disabled")
	}
	// A canceled heartbeat must not publish after its caller has begun cleanup.
	if err = ctx.Err(); err != nil {
		return err
	}
	if isTyping && !h.admitPositiveTyping(user.ID, conversationID) {
		return nil
	}
	hint := Hint{Type: "typing", UserIDs: access.MemberIds, ConversationID: conversationID, UserID: user.ID, DisplayName: user.DisplayName, IsTyping: isTyping}
	if h.Redis == nil {
		h.deliver(ctx, hint)
		if !isTyping {
			h.resetPositiveTyping(user.ID, conversationID)
		}
		return nil
	}
	b, _ := json.Marshal(hint)
	if err = h.Redis.Publish(ctx, chat.HintChannel, b).Err(); err != nil {
		return api.Err(503, "realtime_unavailable", "Typing is temporarily unavailable")
	}
	if !isTyping {
		// A fresh turn immediately after a successful stop must not be
		// swallowed by the previous turn's positive-hint throttle.
		h.resetPositiveTyping(user.ID, conversationID)
	}
	return nil
}

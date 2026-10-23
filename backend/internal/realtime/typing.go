package realtime

import (
	"context"
	"teamie/backend/internal/api"
	"teamie/backend/internal/realtime/domain"
)

func (h *Hub) publishTyping(ctx context.Context, user api.User, conversationID string, isTyping bool) error {
	domUser := domain.User{
		ID:           user.ID,
		Email:        user.Email,
		DisplayName:  user.DisplayName,
		Kind:         user.Kind,
		PlatformRole: user.PlatformRole,
		CreatedAt:    user.CreatedAt,
	}
	return compatErr(h.App().PublishTyping(ctx, h.clients, domUser, conversationID, isTyping))
}

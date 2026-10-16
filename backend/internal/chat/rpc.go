package chat

import (
	"context"
	"errors"
	"teamie/backend/internal/api"
	"teamie/backend/kitex_gen/im"
)

type RPC struct{ Store *Store }

func (h *RPC) CheckAccess(ctx context.Context, r *im.AccessRequest) (*im.AccessResponse, error) {
	c, e := conversationAccess(ctx, h.Store.DB, r.ConversationId, r.UserId)
	if e != nil {
		var ae *api.Error
		if errors.As(e, &ae) && ae.Status == 404 {
			return &im.AccessResponse{Allowed: false, MemberIds: []string{}, TypingEnabled: false}, nil
		}
		return nil, e
	}
	return &im.AccessResponse{Allowed: true, MemberIds: c.MemberIDs, TypingEnabled: h.Store.Flags.Load().Flags["im.typing.publish"]}, nil
}
func (h *RPC) Ping(ctx context.Context, r *im.PingRequest) (*im.PingResponse, error) {
	return &im.PingResponse{Ready: h.Store.Ping(ctx) == nil}, nil
}

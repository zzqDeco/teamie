// Package rpc implements the private inbound Kitex RealtimeService RPC adapter.
package rpc

import (
	"context"
	"errors"

	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/services/realtime/internal/app"
	"teamie/services/realtime/internal/domain"
)

// Server adapts StreamApp to the private Kitex RealtimeService interface (:8883).
type Server struct {
	App          *app.StreamApp
	Clients      func() []domain.Client
	ServiceToken string
}

var _ im.RealtimeService = (*Server)(nil)

func (s *Server) GetPresence(ctx context.Context, r *im.PresenceRequest) (*im.PresenceResponse, error) {
	if s == nil || s.App == nil || r == nil || !domain.ValidServiceKey(s.ServiceToken, r.ServiceToken) {
		return &im.PresenceResponse{
			Ok:        false,
			ErrorCode: "forbidden",
			Presence:  []*im.UserPresence{},
		}, nil
	}
	clients := s.Clients
	if clients == nil {
		clients = s.App.Clients
	}
	items, err := s.App.BatchPresence(ctx, clients, r.UserIds)
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) {
			return &im.PresenceResponse{
				Ok:        false,
				ErrorCode: de.Code,
				Presence:  []*im.UserPresence{},
			}, nil
		}
		return nil, err
	}
	out := make([]*im.UserPresence, 0, len(items))
	for _, item := range items {
		out = append(out, &im.UserPresence{
			UserId: item.UserID,
			State:  item.State,
		})
	}
	return &im.PresenceResponse{
		Ok:       true,
		Presence: out,
	}, nil
}

func (s *Server) Ping(ctx context.Context, _ *im.PingRequest) (*im.PingResponse, error) {
	if s == nil || s.App == nil {
		return &im.PingResponse{Ready: false}, nil
	}
	return &im.PingResponse{Ready: s.App.CheckDependencies(ctx) == nil}, nil
}

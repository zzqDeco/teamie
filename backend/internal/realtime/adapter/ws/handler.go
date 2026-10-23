// Package ws implements the inbound WebSocket and HTTP typing adapter for the
// Realtime service using generated Thrift DTOs (teamie/backend/hertz_gen/im/realtime).
package ws

import (
	"context"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	realtimegen "teamie/backend/hertz_gen/im/realtime"
	"teamie/backend/internal/api"
	"teamie/backend/internal/realtime/app"
	"teamie/backend/internal/realtime/domain"
	"teamie/backend/pkg/httpx"
)

type actorContextKey struct{}

// WithActor attaches the authenticated domain.User to ctx.
func WithActor(ctx context.Context, u domain.User) context.Context {
	return context.WithValue(ctx, actorContextKey{}, u)
}

func actorFromContext(ctx context.Context) (domain.User, bool) {
	u, ok := ctx.Value(actorContextKey{}).(domain.User)
	return u, ok
}

// ToServerFrame converts a domain.Event into a generated RealtimeServerFrame DTO.
func ToServerFrame(ev domain.Event) *realtimegen.RealtimeServerFrame {
	if ev.Type == "typing" {
		convID := ev.ConversationID
		userID := ev.UserID
		displayName := ev.DisplayName
		isTyping := ev.IsTyping
		return &realtimegen.RealtimeServerFrame{
			Type:           ev.Type,
			ConversationId: &convID,
			UserId:         &userID,
			DisplayName:    &displayName,
			IsTyping:       &isTyping,
		}
	}
	return &realtimegen.RealtimeServerFrame{
		Type: ev.Type,
	}
}

// Adapter implements the generated realtimegen.RealtimeWSService contract and
// wires WebSocket and HTTP routes to the Realtime application service.
type Adapter struct {
	Service              func() *app.HubService
	Auth                 api.Identity
	Origin               string
	RevalidationInterval func() time.Duration
	Clients              func() []domain.Client
	RegisterPeer         func(userID, token string, conn *websocket.Conn) (domain.Client, <-chan domain.Event, <-chan struct{})
	UnregisterPeer       func(c domain.Client)
}

var _ realtimegen.RealtimeWSService = (*Adapter)(nil)

func (a *Adapter) ConnectWS(_ context.Context, req *realtimegen.ConnectWSReq) (*realtimegen.EmptyResp, error) {
	if req.GetOrigin() != a.Origin {
		return nil, domain.ErrUntrustedOrigin()
	}
	return &realtimegen.EmptyResp{}, nil
}

func (a *Adapter) PublishTyping(ctx context.Context, req *realtimegen.PublishTypingReq) (*realtimegen.EmptyResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	if !req.IsSetIsTyping() {
		return nil, domain.ErrInvalidRequest("isTyping is required")
	}
	svc := a.Service()
	if err := svc.PublishTyping(ctx, a.Clients, user, req.ID, req.GetIsTyping()); err != nil {
		return nil, err
	}
	return &realtimegen.EmptyResp{}, nil
}

// Routes mounts the Realtime WebSocket and HTTP endpoints onto an http.ServeMux.
func (a *Adapter) Routes() http.Handler {
	m := http.NewServeMux()
	api.Operations(m, func(r *http.Request) error {
		return a.Service().CheckDependencies(r.Context())
	})
	m.HandleFunc("GET /api/v1/ws", a.handleWS)
	m.HandleFunc("POST /api/v1/conversations/{id}/typing", api.Auth(a.Auth, a.Origin, a.handleTyping))
	return m
}

func (a *Adapter) handleTyping(w http.ResponseWriter, r *http.Request, s api.Session) error {
	w.Header().Set("Cache-Control", "no-store")
	var req realtimegen.PublishTypingReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	req.ID = r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	ctx = WithActor(ctx, domain.User{
		ID:           s.User.ID,
		Email:        s.User.Email,
		DisplayName:  s.User.DisplayName,
		Kind:         s.User.Kind,
		PlatformRole: s.User.PlatformRole,
		CreatedAt:    s.User.CreatedAt,
	})
	if _, err := a.PublishTyping(ctx, &req); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusNoContent, nil)
	return nil
}

func (a *Adapter) handleWS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if _, err := a.ConnectWS(r.Context(), &realtimegen.ConnectWSReq{Origin: &origin}); err != nil {
		httpx.WriteError(w, err)
		return
	}
	svc := a.Service()
	ss, err := svc.Identity.Validate(r.Context(), api.Token(r))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	if err := svc.CheckConnectionCapacity(a.Clients(), ss.User.ID); err != nil {
		httpx.WriteError(w, err)
		return
	}
	upgrader := websocket.Upgrader{
		ReadBufferSize:   4096,
		WriteBufferSize:  4096,
		HandshakeTimeout: 5 * time.Second,
		CheckOrigin:      func(r *http.Request) bool { return r.Header.Get("Origin") == a.Origin },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client, queue, done := a.RegisterPeer(ss.User.ID, api.Token(r), conn)
	defer a.UnregisterPeer(client)
	go a.writeLoop(client, conn, queue, done)
	conn.SetReadLimit(4096)
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(60 * time.Second)) })
	for {
		var frame realtimegen.RealtimeClientFrame
		if err = conn.ReadJSON(&frame); err != nil {
			return
		}
		curSvc := a.Service()
		if frame.GetType() != "typing" || frame.GetConversationId() == "" || (curSvc.Flags != nil && !curSvc.Flags.TypingPublishEnabled()) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		current, err := curSvc.Identity.Validate(ctx, client.Token())
		if err != nil {
			cancel()
			return
		}
		_ = curSvc.PublishTyping(ctx, a.Clients, current.User, frame.GetConversationId(), frame.GetIsTyping())
		cancel()
	}
}

func (a *Adapter) writeLoop(client domain.Client, conn *websocket.Conn, queue <-chan domain.Event, done <-chan struct{}) {
	defer client.Close()
	write := func(frame *realtimegen.RealtimeServerFrame) error {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return conn.WriteJSON(frame)
	}
	if write(ToServerFrame(domain.Event{Type: "ready"})) != nil || write(ToServerFrame(domain.Event{Type: "sync.required"})) != nil {
		return
	}
	heartbeat := time.NewTicker(20 * time.Second)
	interval := 10 * time.Second
	if a.RevalidationInterval != nil {
		if d := a.RevalidationInterval(); d > 0 {
			interval = d
		}
	}
	revalidate := time.NewTicker(interval)
	fallback := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()
	defer revalidate.Stop()
	defer fallback.Stop()
	for {
		select {
		case <-done:
			return
		case hint := <-queue:
			if write(ToServerFrame(hint)) != nil {
				return
			}
		case <-fallback.C:
			if write(ToServerFrame(domain.Event{Type: "sync.required"})) != nil {
				return
			}
		case <-revalidate.C:
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, err := a.Service().Identity.Validate(ctx, client.Token())
			cancel()
			if err != nil {
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "session expired"), time.Now().Add(time.Second))
				return
			}
		case <-heartbeat.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		}
	}
}

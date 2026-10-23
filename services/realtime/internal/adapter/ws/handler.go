// Package ws implements the inbound WebSocket and HTTP typing adapter for the
// Realtime service using generated Thrift DTOs and route bindings (teamie/contracts/gen/im/hertz_gen/im/realtime).
package ws

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	realtimegen "teamie/contracts/gen/im/hertz_gen/im/realtime"
	"teamie/pkg/httpx"
	"teamie/services/realtime/internal/app"
	"teamie/services/realtime/internal/domain"
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

type peer struct {
	user  string
	token string
	conn  *websocket.Conn
	queue chan domain.Hint
	done  chan struct{}
	once  sync.Once
}

func (p *peer) UserID() string     { return p.user }
func (p *peer) Token() string      { return p.token }
func (p *peer) Send(h domain.Hint) { p.enqueue(h) }
func (p *peer) Close()             { p.close() }

func (p *peer) close() {
	p.once.Do(func() {
		close(p.done)
		if p.conn != nil {
			_ = p.conn.Close()
		}
	})
}

func (p *peer) enqueue(h domain.Hint) {
	select {
	case <-p.done:
		return
	default:
	}
	select {
	case p.queue <- h:
	default:
	}
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
	App                  *app.HubService
	Service              func() *app.HubService
	Auth                 httpx.Identity
	Origin               string
	RevalidationInterval func() time.Duration
	Clients              func() []domain.Client
	RegisterPeer         func(userID, token string, conn *websocket.Conn) (domain.Client, <-chan domain.Event, <-chan struct{})
	UnregisterPeer       func(c domain.Client)

	mu    sync.Mutex
	peers map[*peer]struct{}
}

var _ realtimegen.RealtimeWSService = (*Adapter)(nil)

func (a *Adapter) hubService() *app.HubService {
	if a.Service != nil {
		return a.Service()
	}
	return a.App
}

// ActiveClients returns a snapshot of connected WebSocket clients.
func (a *Adapter) ActiveClients() []domain.Client {
	if a.Clients != nil {
		return a.Clients()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]domain.Client, 0, len(a.peers))
	for p := range a.peers {
		out = append(out, p)
	}
	return out
}

// CloseAll closes all active WebSocket connections managed by this adapter.
func (a *Adapter) CloseAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for p := range a.peers {
		p.close()
	}
}

func (a *Adapter) registerPeer(userID, token string, conn *websocket.Conn) (domain.Client, <-chan domain.Event, <-chan struct{}) {
	if a.RegisterPeer != nil {
		return a.RegisterPeer(userID, token, conn)
	}
	p := &peer{
		user:  userID,
		token: token,
		conn:  conn,
		queue: make(chan domain.Hint, 16),
		done:  make(chan struct{}),
	}
	a.mu.Lock()
	if a.peers == nil {
		a.peers = map[*peer]struct{}{}
	}
	a.peers[p] = struct{}{}
	a.mu.Unlock()
	return p, p.queue, p.done
}

func (a *Adapter) unregisterPeer(c domain.Client) {
	if a.UnregisterPeer != nil {
		a.UnregisterPeer(c)
		return
	}
	p, ok := c.(*peer)
	if !ok {
		c.Close()
		return
	}
	p.close()
	a.mu.Lock()
	delete(a.peers, p)
	a.mu.Unlock()
}

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
	svc := a.hubService()
	if err := svc.PublishTyping(ctx, a.ActiveClients, user, req.ID, req.GetIsTyping()); err != nil {
		return nil, err
	}
	return &realtimegen.EmptyResp{}, nil
}

// Routes mounts the Realtime WebSocket and HTTP endpoints onto an http.ServeMux
// using the generated realtimegen.RegisterRealtimeWSServiceRoutes router bindings.
func (a *Adapter) Routes() http.Handler {
	m := http.NewServeMux()
	httpx.Operations(m, func(r *http.Request) error {
		return a.hubService().CheckDependencies(r.Context())
	})
	realtimegen.RegisterRealtimeWSServiceRoutes(m, realtimegen.RealtimeWSServiceRouteHandlers{
		ConnectWS:     a.handleWS,
		PublishTyping: httpx.Auth(a.Auth, a.Origin, a.handleTyping),
	})
	return m
}

func (a *Adapter) handleTyping(w http.ResponseWriter, r *http.Request, s httpx.Session) error {
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
	svc := a.hubService()
	ss, err := svc.Identity.Validate(r.Context(), httpx.Token(r))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	if err := svc.CheckConnectionCapacity(a.ActiveClients(), ss.User.ID); err != nil {
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
	client, queue, done := a.registerPeer(ss.User.ID, httpx.Token(r), conn)
	defer a.unregisterPeer(client)
	go a.writeLoop(client, conn, queue, done)
	conn.SetReadLimit(4096)
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(60 * time.Second)) })
	for {
		var frame realtimegen.RealtimeClientFrame
		if err = conn.ReadJSON(&frame); err != nil {
			return
		}
		curSvc := a.hubService()
		if frame.GetType() != "typing" || frame.GetConversationId() == "" || (curSvc.Flags != nil && !curSvc.Flags.TypingPublishEnabled()) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		current, err := curSvc.Identity.Validate(ctx, client.Token())
		if err != nil {
			cancel()
			return
		}
		_ = curSvc.PublishTyping(ctx, a.ActiveClients, current.User, frame.GetConversationId(), frame.GetIsTyping())
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
			_, err := a.hubService().Identity.Validate(ctx, client.Token())
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

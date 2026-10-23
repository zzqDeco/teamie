// Package realtime wires the Realtime 4-layer Clean DDD components
// (domain, app, adapter/ws, infra) and exposes the service composition root.
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"teamie/backend/internal/api"
	"teamie/backend/internal/chat"
	"teamie/backend/internal/config"
	wsadapter "teamie/backend/internal/realtime/adapter/ws"
	"teamie/backend/internal/realtime/app"
	"teamie/backend/internal/realtime/domain"
	"teamie/backend/internal/realtime/infra"
	"teamie/backend/kitex_gen/im"
)

type Access interface {
	CheckAccess(context.Context, string, string) (*im.AccessResponse, error)
	Ping(context.Context) error
}

type Hint = domain.Hint
type Client = domain.Client

type peer struct {
	user  string
	token string
	conn  *websocket.Conn
	queue chan Hint
	done  chan struct{}
	once  sync.Once
}

func (p *peer) UserID() string { return p.user }
func (p *peer) Token() string  { return p.token }
func (p *peer) Send(h Hint)    { p.enqueue(h) }
func (p *peer) Close()         { p.close() }

func (p *peer) close() {
	p.once.Do(func() {
		close(p.done)
		if p.conn != nil {
			_ = p.conn.Close()
		}
	})
}

func (p *peer) enqueue(h Hint) {
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

func compatErr(err error) error {
	if err == nil {
		return nil
	}
	var de *domain.Error
	if errors.As(err, &de) {
		return api.Err(de.Status, de.Code, de.Message)
	}
	return err
}

type Hub struct {
	Identity api.Identity
	Chat     Access
	Redis    *redis.Client
	Flags    config.Flags
	Origin   string
	// Zero uses the production ten-second session revalidation interval.
	RevalidationInterval time.Duration
	mu                   sync.Mutex
	peers                map[*peer]struct{}
	policyOnce           sync.Once
	policy               domain.TypingPolicy
}

func NewHub(identity api.Identity, chatAccess Access, rdb *redis.Client, flags config.Flags, origin string) *Hub {
	return &Hub{
		Identity: identity,
		Chat:     chatAccess,
		Redis:    rdb,
		Flags:    flags,
		Origin:   origin,
	}
}

func (h *Hub) App() *app.HubService {
	var idVal domain.SessionValidator
	if h.Identity != nil {
		idVal = infra.IdentityValidator{Identity: h.Identity}
	}
	var chatAuth domain.AccessAuthority
	if h.Chat != nil {
		chatAuth = infra.ChatAuthority{Chat: h.Chat}
	}
	var bus domain.HintBus
	if h.Redis != nil {
		bus = infra.RedisBus{Client: h.Redis}
	}
	return &app.HubService{
		Identity: idVal,
		Chat:     chatAuth,
		Flags:    infra.FlagsProvider{Flags: &h.Flags},
		Bus:      bus,
		Policy:   &h.policy,
	}
}

func (h *Hub) clients() []domain.Client {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]domain.Client, 0, len(h.peers))
	for p := range h.peers {
		out = append(out, p)
	}
	return out
}

func (h *Hub) registerPeer(userID, token string, conn *websocket.Conn) (domain.Client, <-chan domain.Event, <-chan struct{}) {
	p := &peer{
		user:  userID,
		token: token,
		conn:  conn,
		queue: make(chan Hint, 16),
		done:  make(chan struct{}),
	}
	h.mu.Lock()
	if h.peers == nil {
		h.peers = map[*peer]struct{}{}
	}
	h.peers[p] = struct{}{}
	h.mu.Unlock()
	return p, p.queue, p.done
}

func (h *Hub) unregisterPeer(c domain.Client) {
	p, ok := c.(*peer)
	if !ok {
		c.Close()
		return
	}
	p.close()
	h.mu.Lock()
	delete(h.peers, p)
	h.mu.Unlock()
}

func (h *Hub) Routes() http.Handler {
	adapter := &wsadapter.Adapter{
		Service:              h.App,
		Auth:                 h.Identity,
		Origin:               h.Origin,
		RevalidationInterval: func() time.Duration { return h.RevalidationInterval },
		Clients:              h.clients,
		RegisterPeer:         h.registerPeer,
		UnregisterPeer:       h.unregisterPeer,
	}
	return adapter.Routes()
}

func (h *Hub) deliver(ctx context.Context, hint Hint) {
	h.App().Deliver(ctx, h.clients(), hint)
}

func (h *Hub) Run(ctx context.Context) {
	if h.Redis == nil {
		return
	}
	sub := h.Redis.Subscribe(ctx, chat.HintChannel)
	defer sub.Close()
	ch := sub.Channel(redis.WithChannelSize(128))
	for {
		select {
		case <-ctx.Done():
			h.mu.Lock()
			for p := range h.peers {
				p.close()
			}
			h.mu.Unlock()
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var hint Hint
			if json.Unmarshal([]byte(msg.Payload), &hint) != nil || (hint.Type != "sync.required" && hint.Type != "typing") {
				continue
			}
			ctxCall, cancel := context.WithTimeout(ctx, 3*time.Second)
			h.deliver(ctxCall, hint)
			cancel()
		}
	}
}

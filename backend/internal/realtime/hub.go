package realtime

import (
	"context"
	"encoding/json"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"net/http"
	"sync"
	"teamie/backend/internal/api"
	"teamie/backend/internal/chat"
	"teamie/backend/internal/config"
	"teamie/backend/kitex_gen/im"
	"time"
)

type Access interface {
	CheckAccess(context.Context, string, string) (*im.AccessResponse, error)
	Ping(context.Context) error
}
type Hint struct {
	Type           string   `json:"type"`
	UserIDs        []string `json:"userIds,omitempty"`
	ConversationID string   `json:"conversationId,omitempty"`
	UserID         string   `json:"userId,omitempty"`
	DisplayName    string   `json:"displayName,omitempty"`
	IsTyping       bool     `json:"isTyping"`
}
type peer struct {
	user  string
	token string
	conn  *websocket.Conn
	queue chan Hint
	done  chan struct{}
	once  sync.Once
}

func (p *peer) close() { p.once.Do(func() { close(p.done); _ = p.conn.Close() }) }
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
}

func (h *Hub) Routes() http.Handler {
	m := http.NewServeMux()
	api.Operations(m, func(r *http.Request) error {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if e := h.Identity.Ping(ctx); e != nil {
			return e
		}
		return h.Chat.Ping(ctx)
	})
	m.HandleFunc("GET /api/v1/ws", h.connect)
	return m
}
func (h *Hub) connect(w http.ResponseWriter, r *http.Request) {
	// Browsers always send Origin for WS. An absent header cannot bypass this gate.
	if r.Header.Get("Origin") != h.Origin {
		api.Fail(w, api.Err(403, "untrusted_origin", "WebSocket origin must match this application"))
		return
	}
	ss, e := h.Identity.Validate(r.Context(), api.Token(r))
	if e != nil {
		api.Fail(w, e)
		return
	}
	h.mu.Lock()
	n := 0
	for p := range h.peers {
		if p.user == ss.User.ID {
			n++
		}
	}
	full := n >= 8 || len(h.peers) >= 10000
	h.mu.Unlock()
	if full {
		api.Fail(w, api.Err(429, "too_many_connections", "Close an existing connection and try again"))
		return
	}
	upgrader := websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096, HandshakeTimeout: 5 * time.Second, CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == h.Origin }}
	conn, e := upgrader.Upgrade(w, r, nil)
	if e != nil {
		return
	}
	p := &peer{user: ss.User.ID, token: api.Token(r), conn: conn, queue: make(chan Hint, 16), done: make(chan struct{})}
	h.mu.Lock()
	if h.peers == nil {
		h.peers = map[*peer]struct{}{}
	}
	h.peers[p] = struct{}{}
	h.mu.Unlock()
	defer func() { p.close(); h.mu.Lock(); delete(h.peers, p); h.mu.Unlock() }()
	go h.write(p)
	conn.SetReadLimit(4096)
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(60 * time.Second)) })
	lastTyping := time.Time{}
	for {
		var input struct {
			Type           string `json:"type"`
			ConversationID string `json:"conversationId"`
			IsTyping       bool   `json:"isTyping"`
		}
		if e = conn.ReadJSON(&input); e != nil {
			return
		}
		if input.Type != "typing" || input.ConversationID == "" || time.Since(lastTyping) < 300*time.Millisecond || !h.Flags.Load().Flags["im.typing.publish"] {
			continue
		}
		lastTyping = time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		current, e := h.Identity.Validate(ctx, p.token)
		if e != nil {
			cancel()
			return
		}
		access, e := h.Chat.CheckAccess(ctx, p.user, input.ConversationID)
		if e != nil || !access.Allowed || !access.TypingEnabled {
			cancel()
			continue
		}
		hint := Hint{Type: "typing", UserIDs: access.MemberIds, ConversationID: input.ConversationID, UserID: p.user, DisplayName: current.User.DisplayName, IsTyping: input.IsTyping}
		b, _ := json.Marshal(hint)
		if h.Redis != nil {
			_ = h.Redis.Publish(ctx, chat.HintChannel, b).Err()
		} else {
			h.deliver(ctx, hint)
		}
		cancel()
	}
}
func (h *Hub) write(p *peer) {
	defer p.close()
	write := func(v any) error {
		_ = p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return p.conn.WriteJSON(v)
	}
	if write(map[string]string{"type": "ready"}) != nil || write(map[string]string{"type": "sync.required"}) != nil {
		return
	}
	heartbeat := time.NewTicker(20 * time.Second)
	interval := h.RevalidationInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	revalidate := time.NewTicker(interval)
	fallback := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()
	defer revalidate.Stop()
	defer fallback.Stop()
	for {
		select {
		case <-p.done:
			return
		case hint := <-p.queue:
			// Never expose internal recipient lists to browsers.
			hint.UserIDs = nil
			if write(hint) != nil {
				return
			}
		case <-fallback.C:
			if write(map[string]string{"type": "sync.required"}) != nil {
				return
			}
		case <-revalidate.C:
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, e := h.Identity.Validate(ctx, p.token)
			cancel()
			if e != nil {
				_ = p.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "session expired"), time.Now().Add(time.Second))
				return
			}
		case <-heartbeat.C:
			if e := p.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); e != nil {
				return
			}
		}
	}
}
func (h *Hub) deliver(ctx context.Context, hint Hint) {
	set := map[string]bool{}
	for _, id := range hint.UserIDs {
		set[id] = true
	}
	h.mu.Lock()
	targets := []*peer{}
	for p := range h.peers {
		if set[p.user] {
			targets = append(targets, p)
		}
	}
	h.mu.Unlock()
	for _, p := range targets {
		if hint.Type == "typing" {
			if p.user == hint.UserID || !h.Flags.Load().Flags["im.typing.publish"] {
				continue
			}
			access, e := h.Chat.CheckAccess(ctx, p.user, hint.ConversationID)
			if e != nil || !access.Allowed || !access.TypingEnabled {
				continue
			}
			if _, e = h.Identity.Validate(ctx, p.token); e != nil {
				p.close()
				continue
			}
		}
		p.enqueue(hint)
	}
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

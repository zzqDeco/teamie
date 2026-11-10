package rpcclient

import (
	"context"
	"time"

	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/contracts/gen/im/kitex_gen/im/realtimeservice"
	"teamie/pkg/rpcx"
	"teamie/services/chat/internal/domain"
)

// Realtime wraps the private Kitex RealtimeService client for live presence lookups.
type Realtime struct {
	Client realtimeservice.Client
	Token  string
}

// NewRealtime dials a new Realtime RPC client at the given address.
func NewRealtime(addr, token string) (*Realtime, error) {
	c, err := realtimeservice.NewClient("teamie.realtime", rpcx.ClientOptions(addr, 2*time.Second, time.Second)...)
	return &Realtime{Client: c, Token: token}, err
}

// Presence queries live online/away states for userIDs in bounded batches of 500.
func (c *Realtime) Presence(ctx context.Context, userIDs []string) (map[string]string, error) {
	if c == nil || c.Client == nil || c.Token == "" {
		return nil, domain.ErrPresenceUnavailable()
	}
	unique := make([]string, 0, len(userIDs))
	seen := make(map[string]bool, len(userIDs))
	for _, id := range userIDs {
		if id != "" && !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	out := make(map[string]string, len(unique))
	if len(unique) == 0 {
		r, err := c.Client.GetPresence(ctx, &im.PresenceRequest{
			ServiceToken: c.Token,
			UserIds:      []string{},
		})
		if err != nil || r == nil || !r.Ok {
			return nil, domain.ErrPresenceUnavailable()
		}
		return out, nil
	}
	for start := 0; start < len(unique); start += 500 {
		end := min(start+500, len(unique))
		r, err := c.Client.GetPresence(ctx, &im.PresenceRequest{
			ServiceToken: c.Token,
			UserIds:      unique[start:end],
		})
		if err != nil || r == nil || !r.Ok {
			return nil, domain.ErrPresenceUnavailable()
		}
		for _, p := range r.Presence {
			if p != nil && p.UserId != "" {
				out[p.UserId] = p.State
			}
		}
	}
	return out, nil
}

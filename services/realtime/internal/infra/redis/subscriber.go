// Package redisinfra implements the Redis pub/sub hint bus and feature flag provider for Realtime.
package redisinfra

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
	"teamie/pkg/httpx"
	"teamie/services/realtime/internal/domain"
)

// FlagsProvider adapts httpx.Flags to domain.FeatureFlags.
type FlagsProvider struct {
	Flags *httpx.Flags
}

func (f FlagsProvider) TypingPublishEnabled() bool {
	if f.Flags == nil {
		return true
	}
	return f.Flags.Load().Flags["im.typing.publish"]
}

func (f FlagsProvider) PresencePublishEnabled() bool {
	if f.Flags == nil {
		return true
	}
	return f.Flags.Load().Flags["im.presence.publish"]
}

func (f FlagsProvider) ReadReceiptsViewEnabled() bool {
	if f.Flags == nil {
		return true
	}
	return f.Flags.Load().Flags["im.read_receipts.view"]
}

const PresenceKeyPrefix = "teamie:im:presence:v1:"

// RedisPresenceStore persists disposable human presence state in Redis.
type RedisPresenceStore struct {
	Client *redis.Client
}

func (s RedisPresenceStore) SaveUserPresence(ctx context.Context, userID, state string, ttl time.Duration) error {
	if s.Client == nil || userID == "" {
		return nil
	}
	key := PresenceKeyPrefix + userID
	if state == "" || state == "offline" {
		return s.Client.Del(ctx, key).Err()
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return s.Client.Set(ctx, key, state, ttl).Err()
}

func (s RedisPresenceStore) GetUsersPresence(ctx context.Context, userIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(userIDs))
	if s.Client == nil || len(userIDs) == 0 {
		return out, nil
	}
	keys := make([]string, len(userIDs))
	for i, id := range userIDs {
		keys[i] = PresenceKeyPrefix + id
	}
	vals, err := s.Client.MGet(ctx, keys...).Result()
	if err != nil {
		return out, err
	}
	for i, v := range vals {
		if str, ok := v.(string); ok && (str == "online" || str == "away") {
			out[userIDs[i]] = str
		}
	}
	return out, nil
}

// RedisBus adapts *redis.Client to domain.HintBus.
type RedisBus struct {
	Client *redis.Client
}

func (b RedisBus) Publish(ctx context.Context, channel string, payload []byte) error {
	return b.Client.Publish(ctx, channel, payload).Err()
}

// HintDeliverer delivers decoded Redis hints to connected WebSocket clients.
type HintDeliverer interface {
	Deliver(ctx context.Context, clients []domain.Client, hint domain.Hint)
}

// RunSubscriber subscribes to the Redis HintChannel and dispatches hints to active clients.
func RunSubscriber(ctx context.Context, rdb *redis.Client, deliverer HintDeliverer, clients func() []domain.Client, closeAll func()) {
	if rdb == nil {
		return
	}
	sub := rdb.Subscribe(ctx, domain.HintChannel)
	defer sub.Close()
	ch := sub.Channel(redis.WithChannelSize(128))
	for {
		select {
		case <-ctx.Done():
			if closeAll != nil {
				closeAll()
			}
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var hint domain.Hint
			if json.Unmarshal([]byte(msg.Payload), &hint) != nil {
				continue
			}
			switch hint.Type {
			case "sync.required", "typing", "receipts.changed", "presence":
			default:
				continue
			}
			ctxCall, cancel := context.WithTimeout(ctx, 3*time.Second)
			deliverer.Deliver(ctxCall, clients(), hint)
			cancel()
		}
	}
}

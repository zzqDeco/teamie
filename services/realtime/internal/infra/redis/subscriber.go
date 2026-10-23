// Package redisinfra implements the Redis pub/sub hint bus and feature flag provider for Realtime.
package redisinfra

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
	"teamie/pkg/config"
	"teamie/services/realtime/internal/domain"
)

// FlagsProvider adapts config.Flags to domain.FeatureFlags.
type FlagsProvider struct {
	Flags *config.Flags
}

func (f FlagsProvider) TypingPublishEnabled() bool {
	if f.Flags == nil {
		return true
	}
	return f.Flags.Load().Flags["im.typing.publish"]
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
			if json.Unmarshal([]byte(msg.Payload), &hint) != nil || (hint.Type != "sync.required" && hint.Type != "typing") {
				continue
			}
			ctxCall, cancel := context.WithTimeout(ctx, 3*time.Second)
			deliverer.Deliver(ctxCall, clients(), hint)
			cancel()
		}
	}
}

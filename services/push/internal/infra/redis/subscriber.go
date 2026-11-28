// Package redisinfra subscribes to disposable Chat outbox hints and dispatches
// push.message hints through a bounded worker pool with atomic Redis claim deduplication.
package redisinfra

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"teamie/pkg/observability"
	"teamie/services/push/internal/domain"
)

const (
	hintQueueCapacity = 128
	workerCount       = 4
	claimKeyPrefix    = "teamie:im:push:claim:v1:"
	claimTTL          = 5 * time.Minute
	claimTimeout      = 2 * time.Second
	shutdownGrace     = 15 * time.Second
)

// HintHandler handles one decoded push.message wake hint.
type HintHandler interface {
	HandleMessageHint(ctx context.Context, hint domain.PushMessageHint) error
}

// HintClaimer atomically claims a push.message hint so overlapping instances
// (such as primary + canary slots during online A/B rollout) never duplicate deliveries.
type HintClaimer interface {
	ClaimMessage(ctx context.Context, messageID string, ttl time.Duration) (bool, error)
}

type redisClaimer struct {
	client *goredis.Client
}

func (c redisClaimer) ClaimMessage(ctx context.Context, messageID string, ttl time.Duration) (bool, error) {
	return c.client.SetNX(ctx, claimKeyPrefix+messageID, "1", ttl).Result()
}

// DispatchClaimedHint verifies atomic single-consumer ownership for hint.MessageID
// before invoking handler. Per-delivery deadlines are enforced upon semaphore
// admission inside HandleMessageHint rather than expiring queued waves early.
func DispatchClaimedHint(ctx context.Context, claimer HintClaimer, handler HintHandler, hint domain.PushMessageHint) (bool, error) {
	if handler == nil || hint.Type != "push.message" {
		return false, nil
	}
	msgID := strings.TrimSpace(hint.MessageID)
	if msgID == "" {
		return false, nil
	}
	if claimer != nil {
		claimCtx, cancel := context.WithTimeout(ctx, claimTimeout)
		claimed, err := claimer.ClaimMessage(claimCtx, msgID, claimTTL)
		cancel()
		if err != nil {
			return false, err
		}
		if !claimed {
			return false, nil
		}
	}
	return true, handler.HandleMessageHint(ctx, hint)
}

// RunSubscriber subscribes to domain.HintChannel and dispatches push.message hints
// using a fixed worker pool and bounded queue to preserve the 32 MiB memory envelope.
// When ctx is canceled (shutdown/drain), the subscription stops claiming new hints
// immediately while in-flight claimed deliveries receive a bounded shutdown grace window.
func RunSubscriber(ctx context.Context, rdb *goredis.Client, handler HintHandler) {
	if rdb == nil || handler == nil {
		return
	}
	claimer := redisClaimer{client: rdb}
	workCtx, cancelWork := context.WithCancel(context.Background())
	defer cancelWork()

	queue := make(chan domain.PushMessageHint, hintQueueCapacity)
	var workers sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for hint := range queue {
				if ctx.Err() != nil {
					continue
				}
				taskCtx, taskLog := observability.Begin(workCtx, "push-hint-dispatch")
				claimed, err := DispatchClaimedHint(taskCtx, claimer, handler, hint)
				taskLog.EndRecoverable(
					err,
					zap.String("custom.conversation.id", hint.ConversationID),
					zap.String("custom.message.id", hint.MessageID),
					zap.Bool("custom.push.claimed", claimed),
				)
			}
		}()
	}

	defer func() {
		close(queue)
		done := make(chan struct{})
		go func() {
			workers.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(shutdownGrace):
			cancelWork()
			<-done
		}
	}()

	sub := rdb.Subscribe(ctx, domain.HintChannel)
	defer sub.Close()
	ch := sub.Channel(goredis.WithChannelSize(128))
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var hint domain.PushMessageHint
			if json.Unmarshal([]byte(msg.Payload), &hint) != nil {
				continue
			}
			if hint.Type != "push.message" {
				continue
			}
			select {
			case queue <- hint:
			default:
				observability.L(ctx).Warn(
					"Push hint queue full; dropping wake hint",
					zap.String("event.action", "push-hint-drop"),
					zap.String("custom.conversation.id", hint.ConversationID),
				)
			}
		}
	}
}

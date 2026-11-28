// Package redisinfra subscribes to disposable Chat outbox hints and dispatches
// push.message hints through a bounded worker pool.
package redisinfra

import (
	"context"
	"encoding/json"
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
	hintProcessBudget = 15 * time.Second
)

// HintHandler handles one decoded push.message wake hint.
type HintHandler interface {
	HandleMessageHint(ctx context.Context, hint domain.PushMessageHint) error
}

// RunSubscriber subscribes to domain.HintChannel and dispatches push.message hints
// using a fixed worker pool and bounded queue to preserve the 32 MiB memory envelope.
func RunSubscriber(ctx context.Context, rdb *goredis.Client, handler HintHandler) {
	if rdb == nil || handler == nil {
		return
	}
	queue := make(chan domain.PushMessageHint, hintQueueCapacity)
	var workers sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case hint, ok := <-queue:
					if !ok {
						return
					}
					runCtx, cancel := context.WithTimeout(ctx, hintProcessBudget)
					taskCtx, taskLog := observability.Begin(runCtx, "push-hint-dispatch")
					err := handler.HandleMessageHint(taskCtx, hint)
					taskLog.EndRecoverable(
						err,
						zap.String("custom.conversation.id", hint.ConversationID),
						zap.String("custom.message.id", hint.MessageID),
					)
					cancel()
				}
			}
		}()
	}

	defer func() {
		close(queue)
		workers.Wait()
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

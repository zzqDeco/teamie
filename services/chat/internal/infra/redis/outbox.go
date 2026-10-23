// Package redis implements the disposable Redis outbox hint publisher and background cleanup loops.
package redis

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"teamie/pkg/observability"
	"teamie/services/chat/internal/domain"
)

// Publisher is the subset of go-redis Client used to publish outbox hints.
type Publisher interface {
	Publish(context.Context, string, any) *goredis.IntCmd
}

// Adapter implements domain.OutboxPublisher on top of a Redis Publisher.
type Adapter struct {
	Pub Publisher
}

func (a Adapter) PublishHint(ctx context.Context, channel string, payload []byte) error {
	return a.Pub.Publish(ctx, channel, payload).Err()
}

// OutboxRunner is the subset of the Chat application service used by background outbox and attachment loops.
type OutboxRunner interface {
	PrunePublishedOutbox(ctx context.Context) (int64, error)
	PublishOneOutbox(ctx context.Context, pub domain.OutboxPublisher) (bool, error)
	DeleteAllExpiredPendingAttachments(ctx context.Context) error
}

// RunOutbox periodically prunes expired published outbox rows and publishes pending hints to Redis.
func RunOutbox(ctx context.Context, svc OutboxRunner, rdb Publisher) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pruneCtx, pruneLog := observability.Begin(ctx, "outbox-cleanup")
			_, pruneErr := svc.PrunePublishedOutbox(pruneCtx)
			pruneLog.EndRecoverable(pruneErr)
			for i := 0; i < 100; i++ {
				publishCtx, publishLog := observability.Begin(ctx, "outbox-publish")
				sent, err := svc.PublishOneOutbox(publishCtx, Adapter{Pub: rdb})
				publishLog.EndRecoverable(err, zap.Bool("custom.outbox.published", sent))
				if err != nil || !sent {
					break
				}
			}
		}
	}
}

// RunAttachmentCleanup periodically deletes expired pending attachments.
func RunAttachmentCleanup(ctx context.Context, svc OutboxRunner) {
	timer := time.NewTicker(time.Hour)
	defer timer.Stop()
	for {
		cleanup, cancel := context.WithTimeout(ctx, 30*time.Second)
		_ = svc.DeleteAllExpiredPendingAttachments(cleanup)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

package chat

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"teamie/backend/internal/chat/domain"
	"teamie/backend/pkg/observability"
)

const HintChannel = domain.HintChannel

type outboxPublisher interface {
	Publish(context.Context, string, any) *redis.IntCmd
}

type redisOutboxAdapter struct {
	pub outboxPublisher
}

func (a redisOutboxAdapter) PublishHint(ctx context.Context, channel string, payload []byte) error {
	return a.pub.Publish(ctx, channel, payload).Err()
}

func (s *Store) RunOutbox(ctx context.Context, rdb *redis.Client) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pruneCtx, pruneLog := observability.Begin(ctx, "outbox-cleanup")
			_, pruneErr := s.prunePublishedOutbox(pruneCtx)
			pruneLog.EndRecoverable(pruneErr)
			for i := 0; i < 100; i++ {
				publishCtx, publishLog := observability.Begin(ctx, "outbox-publish")
				sent, e := s.publishOne(publishCtx, rdb)
				publishLog.EndRecoverable(e, zap.Bool("custom.outbox.published", sent))
				if e != nil {
					break
				}
				if !sent {
					break
				}
			}
		}
	}
}

func (s *Store) prunePublishedOutbox(ctx context.Context) (int64, error) {
	return s.App(nil, "").PrunePublishedOutbox(ctx)
}

func (s *Store) publishOne(ctx context.Context, rdb outboxPublisher) (bool, error) {
	return s.App(nil, "").PublishOneOutbox(ctx, redisOutboxAdapter{pub: rdb})
}

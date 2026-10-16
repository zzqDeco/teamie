package chat

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"log/slog"
	"time"
)

const HintChannel = "teamie:im:hints:v1"

func (s *Store) RunOutbox(ctx context.Context, rdb *redis.Client) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for i := 0; i < 100; i++ {
				sent, e := s.publishOne(ctx, rdb)
				if e != nil {
					slog.Warn("outbox hint deferred; HTTP sync remains available", "error", e)
					break
				}
				if !sent {
					break
				}
			}
		}
	}
}
func (s *Store) publishOne(ctx context.Context, rdb *redis.Client) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(ctx)
	var id int64
	var payload []byte
	e = tx.QueryRow(ctx, "SELECT id,payload FROM outbox WHERE published_at IS NULL ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1").Scan(&id, &payload)
	if e == pgx.ErrNoRows {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if e = rdb.Publish(ctx, HintChannel, payload).Err(); e != nil {
		return false, e
	}
	if _, e = tx.Exec(ctx, "UPDATE outbox SET published_at=now() WHERE id=$1", id); e != nil {
		return false, e
	}
	return true, tx.Commit(ctx)
}

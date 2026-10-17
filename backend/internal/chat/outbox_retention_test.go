package chat

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type outboxPublishFunc func(context.Context, string, any) *redis.IntCmd

func (f outboxPublishFunc) Publish(ctx context.Context, channel string, payload any) *redis.IntCmd {
	return f(ctx, channel, payload)
}

func TestOutboxPublicationDeletesOnlyAcknowledgedHints(t *testing.T) {
	ctx := context.Background()
	s, _, _, _, _ := setup(t)
	if _, err := s.DB.Exec(ctx, "TRUNCATE outbox"); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := s.DB.QueryRow(ctx, `INSERT INTO outbox(payload) VALUES('{"type":"sync.required","userIds":["member"]}') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var calls int
	publisher := outboxPublishFunc(func(_ context.Context, channel string, payload any) *redis.IntCmd {
		calls++
		if channel != HintChannel || string(payload.([]byte)) != `{"type": "sync.required", "userIds": ["member"]}` {
			t.Error("publisher received the wrong hint or channel", channel, payload)
		}
		if calls == 1 {
			return redis.NewIntResult(0, errors.New("fixture Redis unavailable"))
		}
		return redis.NewIntResult(0, nil) // Zero subscribers still acknowledges publish.
	})
	if sent, err := s.publishOne(ctx, publisher); sent || err == nil {
		t.Fatal("failed publish was accepted", sent, err)
	}
	var pending bool
	if err := s.DB.QueryRow(ctx, "SELECT published_at IS NULL FROM outbox WHERE id=$1", id).Scan(&pending); err != nil || !pending {
		t.Fatal("failed publish discarded its durable hint", pending, err)
	}
	if sent, err := s.publishOne(ctx, publisher); !sent || err != nil {
		t.Fatal("retry could not publish pending hint", sent, err)
	}
	var count int
	if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM outbox").Scan(&count); err != nil || count != 0 {
		t.Fatal("acknowledged hint retained duplicate payload", count, err)
	}
	if sent, err := s.publishOne(ctx, publisher); sent || err != nil || calls != 2 {
		t.Fatal("drained outbox republished a removed hint", sent, err, calls)
	}
}

func TestOutboxHistoricalCleanupPreservesPendingAndSkipsLockedRows(t *testing.T) {
	ctx := context.Background()
	s, _, _, _, _ := setup(t)
	if _, err := s.DB.Exec(ctx, "TRUNCATE outbox"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO outbox(payload,published_at)
		SELECT jsonb_build_object('legacy',n),now()-interval '30 days' FROM generate_series(1,1002) n;
		INSERT INTO outbox(payload) VALUES('{"pending":true}')`); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var locked int64
	if err := tx.QueryRow(ctx, "SELECT id FROM outbox WHERE published_at IS NOT NULL ORDER BY id LIMIT 1 FOR UPDATE").Scan(&locked); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []int64{1000, 1, 0} {
		deleted, err := s.prunePublishedOutbox(ctx)
		if err != nil || deleted != expected {
			t.Fatal("historical cleanup lost its bounded progress", deleted, expected, err)
		}
	}
	var pending, remaining int
	if err := s.DB.QueryRow(ctx, "SELECT count(*) FILTER(WHERE published_at IS NULL),count(*) FROM outbox").Scan(&pending, &remaining); err != nil || pending != 1 || remaining != 2 {
		t.Fatal("historical cleanup deleted pending or locked data", pending, remaining, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if deleted, err := s.prunePublishedOutbox(ctx); err != nil || deleted != 1 {
		t.Fatal("unlocked historical hint was retained forever", deleted, err)
	}
	if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM outbox WHERE published_at IS NULL").Scan(&pending); err != nil || pending != 1 {
		t.Fatal("final cleanup discarded pending hint", pending, err)
	}
}

func TestOutboxConcurrentPublishersClaimDistinctHints(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, _, _, _, _ := setup(t)
	if _, err := s.DB.Exec(ctx, `TRUNCATE outbox; INSERT INTO outbox(payload) VALUES('{"n":1}'),('{"n":2}')`); err != nil {
		t.Fatal(err)
	}
	ready := make(chan string, 2)
	release := make(chan struct{})
	publisher := outboxPublishFunc(func(ctx context.Context, _ string, payload any) *redis.IntCmd {
		ready <- string(payload.([]byte))
		select {
		case <-release:
			return redis.NewIntResult(1, nil)
		case <-ctx.Done():
			return redis.NewIntResult(0, ctx.Err())
		}
	})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Go(func() {
			if sent, err := s.publishOne(ctx, publisher); !sent || err != nil {
				t.Error("concurrent publisher lost its claimed hint", sent, err)
			}
		})
	}
	claimed := []string{}
	for len(claimed) < 2 {
		select {
		case payload := <-ready:
			claimed = append(claimed, payload)
		case <-ctx.Done():
			t.Fatal("publishers did not independently claim both hints")
		}
	}
	close(release)
	wg.Wait()
	if claimed[0] == claimed[1] {
		t.Fatal("two publishers claimed the same hint")
	}
	var remaining int
	if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM outbox").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("concurrent publication retained payloads", remaining, err)
	}
}

func TestOutboxAcknowledgedPublishWithRollbackRetainsPending(t *testing.T) {
	ctx := context.Background()
	s, _, _, _, _ := setup(t)
	if _, err := s.DB.Exec(ctx, `TRUNCATE outbox;
		INSERT INTO outbox(payload) VALUES('{"retryAfterRollback":true}');
		CREATE FUNCTION reject_outbox_delete() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'fixture delete rollback'; END $$;
		CREATE TRIGGER reject_outbox_delete BEFORE DELETE ON outbox
		FOR EACH ROW EXECUTE FUNCTION reject_outbox_delete()`); err != nil {
		t.Fatal(err)
	}
	calls := 0
	publisher := outboxPublishFunc(func(context.Context, string, any) *redis.IntCmd {
		calls++
		return redis.NewIntResult(1, nil)
	})
	if sent, err := s.publishOne(ctx, publisher); sent || err == nil || calls != 1 {
		t.Fatal("database failure was mistaken for committed publication", sent, calls, err)
	}
	var pending int
	if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM outbox WHERE published_at IS NULL").Scan(&pending); err != nil || pending != 1 {
		t.Fatal("rollback lost the pending publication", pending, err)
	}
	if _, err := s.DB.Exec(ctx, "DROP TRIGGER reject_outbox_delete ON outbox"); err != nil {
		t.Fatal(err)
	}
	if sent, err := s.publishOne(ctx, publisher); !sent || err != nil || calls != 2 {
		t.Fatal("acknowledged but rolled-back hint could not be retried", sent, calls, err)
	}
	if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM outbox").Scan(&pending); err != nil || pending != 0 {
		t.Fatal("successful retry did not remove its payload", pending, err)
	}
}

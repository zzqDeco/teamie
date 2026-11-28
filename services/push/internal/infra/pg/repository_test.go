package pg_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"teamie/pkg/postgres"
	"teamie/services/push/internal/domain"
	"teamie/services/push/internal/infra/pg"
)

func TestPushRepository_MigrateCapReclaimAndDelete(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL_TEST")
	if dbURL == "" {
		t.Skip("DATABASE_URL_TEST not set")
	}
	ctx := context.Background()
	pool, err := postgres.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if err := pg.Migrate(ctx, pool); err != nil {
		t.Fatalf("first migrate failed: %v", err)
	}
	if err := pg.Migrate(ctx, pool); err != nil {
		t.Fatalf("second idempotent migrate failed: %v", err)
	}

	repo := pg.New(postgres.New(pool))
	userA := "push-test-user-a-" + domain.NewID()
	userB := "push-test-user-b-" + domain.NewID()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM push_subscriptions WHERE user_id IN ($1,$2)", userA, userB)
	})

	// Insert 12 subscriptions for userA under cap 10.
	var lastEndpoint string
	for i := 0; i < 12; i++ {
		ep := fmt.Sprintf("https://fcm.googleapis.com/fcm/send/%s-%02d", userA, i)
		lastEndpoint = ep
		err = repo.WithinTx(ctx, func(txCtx context.Context) error {
			if err := repo.LockUserSubscriptions(txCtx, userA); err != nil {
				return err
			}
			_, err := repo.UpsertAndTrim(txCtx, domain.PushSubscription{
				ID:           domain.NewID(),
				UserID:       userA,
				EndpointHash: domain.HashString(ep),
				Endpoint:     ep,
				EndpointHost: "fcm.googleapis.com",
				P256DH:       "p256dh-placeholder",
				Auth:         "auth-placeholder",
				UserAgent:    "TestAgent",
			}, domain.MaxSubscriptionsPerUser)
			return err
		})
		if err != nil {
			t.Fatalf("upsert %d failed: %v", i, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	subsA, err := repo.ListByUser(ctx, userA)
	if err != nil || len(subsA) != domain.MaxSubscriptionsPerUser {
		t.Fatalf("expected %d rows for userA, got %d (err=%v)", domain.MaxSubscriptionsPerUser, len(subsA), err)
	}

	// Shared-device reclaim by userB moves lastEndpoint from userA to userB.
	var reclaimed domain.PushSubscription
	err = repo.WithinTx(ctx, func(txCtx context.Context) error {
		if err := repo.LockUserSubscriptions(txCtx, userB); err != nil {
			return err
		}
		reclaimed, err = repo.UpsertAndTrim(txCtx, domain.PushSubscription{
			ID:           domain.NewID(),
			UserID:       userB,
			EndpointHash: domain.HashString(lastEndpoint),
			Endpoint:     lastEndpoint,
			EndpointHost: "fcm.googleapis.com",
			P256DH:       "p256dh-b",
			Auth:         "auth-b",
			UserAgent:    "TestAgentB",
		}, domain.MaxSubscriptionsPerUser)
		return err
	})
	if err != nil {
		t.Fatalf("reclaim failed: %v", err)
	}
	subsA, _ = repo.ListByUser(ctx, userA)
	subsB, _ := repo.ListByUser(ctx, userB)
	if len(subsA) != 9 || len(subsB) != 1 || subsB[0].UserID != userB {
		t.Fatalf("unexpected reclaim counts A=%d B=%d", len(subsA), len(subsB))
	}

	// Record success & delete by endpoint hash.
	if err := repo.RecordDeliveryResult(ctx, reclaimed.ID, true, ""); err != nil {
		t.Fatalf("record success failed: %v", err)
	}
	removed, err := repo.DeleteByUserEndpointHash(ctx, userB, domain.HashString(lastEndpoint))
	if err != nil || !removed {
		t.Fatalf("delete failed: removed=%v err=%v", removed, err)
	}
}

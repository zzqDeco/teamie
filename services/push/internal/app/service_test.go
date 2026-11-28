package app_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"teamie/services/push/internal/app"
	"teamie/services/push/internal/domain"
)

type fakeFlags struct{ enabled bool }

func (f fakeFlags) Enabled(key string) bool { return key == "im.push.web" && f.enabled }

type memRepo struct {
	mu   sync.Mutex
	subs map[string]domain.PushSubscription // keyed by ID
}

func newMemRepo() *memRepo { return &memRepo{subs: map[string]domain.PushSubscription{}} }

func (r *memRepo) WithinTx(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }
func (r *memRepo) LockUserSubscriptions(context.Context, string) error                { return nil }
func (r *memRepo) Ping(context.Context) error                                         { return nil }

func (r *memRepo) ListByUser(_ context.Context, userID string) ([]domain.PushSubscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.PushSubscription{}
	for _, s := range r.subs {
		if s.UserID == userID {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (r *memRepo) ListByUserIDs(_ context.Context, userIDs []string) ([]domain.PushSubscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	want := map[string]bool{}
	for _, id := range userIDs {
		want[id] = true
	}
	out := []domain.PushSubscription{}
	for _, s := range r.subs {
		if want[s.UserID] {
			out = append(out, s)
		}
	}
	return out, nil
}

func (r *memRepo) UpsertAndTrim(_ context.Context, sub domain.PushSubscription, maxPerUser int) (domain.PushSubscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	for id, existing := range r.subs {
		if existing.EndpointHash == sub.EndpointHash {
			delete(r.subs, id)
			sub.ID = existing.ID
			sub.CreatedAt = existing.CreatedAt
			break
		}
	}
	if sub.CreatedAt.IsZero() {
		sub.CreatedAt = now
	}
	sub.UpdatedAt = now
	r.subs[sub.ID] = sub

	var userSubs []domain.PushSubscription
	for _, s := range r.subs {
		if s.UserID == sub.UserID && s.ID != sub.ID {
			userSubs = append(userSubs, s)
		}
	}
	sort.Slice(userSubs, func(i, j int) bool {
		if userSubs[i].UpdatedAt.Equal(userSubs[j].UpdatedAt) {
			return userSubs[i].ID > userSubs[j].ID
		}
		return userSubs[i].UpdatedAt.After(userSubs[j].UpdatedAt)
	})
	if len(userSubs) > maxPerUser-1 {
		for _, drop := range userSubs[maxPerUser-1:] {
			delete(r.subs, drop.ID)
		}
	}
	return sub, nil
}

func (r *memRepo) DeleteByUserEndpointHash(_ context.Context, userID, endpointHash string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, s := range r.subs {
		if s.UserID == userID && s.EndpointHash == endpointHash {
			delete(r.subs, id)
			return true, nil
		}
	}
	return false, nil
}

func (r *memRepo) DeleteByID(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.subs, id)
	return nil
}

func (r *memRepo) RecordDeliveryResult(_ context.Context, id string, ok bool, errCode string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, exists := r.subs[id]
	if !exists {
		return nil
	}
	if ok {
		now := time.Now().UTC()
		s.LastSuccessAt = &now
		s.LastErrorCode = ""
	} else {
		s.LastErrorCode = errCode
	}
	r.subs[id] = s
	return nil
}

type fakeChat struct {
	mu           sync.Mutex
	calls        int
	lastCands    []string
	result       domain.PushEvaluationResult
}

func (c *fakeChat) EvaluatePushRecipients(_ context.Context, _ string, _ string, candidates []string) (domain.PushEvaluationResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.lastCands = append([]string(nil), candidates...)
	return c.result, nil
}

func (c *fakeChat) Ping(context.Context) error { return nil }

type fakeSender struct {
	mu      sync.Mutex
	sent    []domain.PushSubscription
	goneIDs map[string]bool
}

func (s *fakeSender) Send(_ context.Context, sub domain.PushSubscription, _ domain.VAPIDConfig, _ domain.PushWakePayload) (domain.DeliveryResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, sub)
	if s.goneIDs[sub.Endpoint] {
		return domain.DeliveryResult{StatusCode: 410, Gone: true, Reason: "subscription_gone"}, nil
	}
	return domain.DeliveryResult{StatusCode: 201, Reason: "ok"}, nil
}

func testKeys(t *testing.T) (p256dh, auth string, vapid domain.VAPIDConfig) {
	t.Helper()
	k1, _ := ecdh.P256().GenerateKey(rand.Reader)
	k2, _ := ecdh.P256().GenerateKey(rand.Reader)
	a := make([]byte, 16)
	_, _ = rand.Read(a)
	return base64.RawURLEncoding.EncodeToString(k1.PublicKey().Bytes()),
		base64.RawURLEncoding.EncodeToString(a),
		domain.VAPIDConfig{
			PublicKey:  base64.RawURLEncoding.EncodeToString(k2.PublicKey().Bytes()),
			PrivateKey: base64.RawURLEncoding.EncodeToString(k2.Bytes()),
			Subject:    "mailto:ops@teamie.example",
		}
}

func TestPushService_LifecycleQuotaReclaimAndDelivery(t *testing.T) {
	p256dh, auth, vapid := testKeys(t)
	repo := newMemRepo()
	chat := &fakeChat{}
	sender := &fakeSender{goneIDs: map[string]bool{}}
	flags := &fakeFlags{enabled: false}

	svc := &app.Service{
		Tx:            repo,
		Subscriptions: repo,
		Chat:          chat,
		Flags:         flags,
		Sender:        sender,
		VAPID:         vapid,
	}

	humanA := domain.User{ID: "uA", Kind: "human"}
	humanB := domain.User{ID: "uB", Kind: "human"}
	agent := domain.User{ID: "a1", Kind: "agent"}

	// 1. Agent principals are rejected.
	if _, err := svc.Config(context.Background(), agent); err == nil {
		t.Fatal("agent should not access push config")
	}

	// 2. When im.push.web=false, Config reports enabled=false and Upsert returns feature_disabled.
	cfg, err := svc.Config(context.Background(), humanA)
	if err != nil || cfg.Enabled || cfg.VAPIDPublicKey != "" {
		t.Fatalf("expected disabled config, got %+v err=%v", cfg, err)
	}
	if _, err := svc.UpsertSubscription(context.Background(), humanA, domain.UpsertSubscriptionInput{
		Endpoint: "https://fcm.googleapis.com/fcm/send/ep1",
		P256DH:   p256dh,
		Auth:     auth,
	}); err == nil {
		t.Fatal("upsert must fail when feature flag is off")
	}

	// 3. Enable feature flag: Config exposes VAPID public key, Upsert enforces 10-device cap.
	flags.enabled = true
	cfg, err = svc.Config(context.Background(), humanA)
	if err != nil || !cfg.Enabled || cfg.VAPIDPublicKey != vapid.PublicKey {
		t.Fatalf("expected active config, got %+v err=%v", cfg, err)
	}
	for i := 0; i < 12; i++ {
		_, err = svc.UpsertSubscription(context.Background(), humanA, domain.UpsertSubscriptionInput{
			Endpoint: fmt.Sprintf("https://fcm.googleapis.com/fcm/send/ep-%02d", i),
			P256DH:   p256dh,
			Auth:     auth,
			UserAgent: "Chrome",
		})
		if err != nil {
			t.Fatalf("upsert %d failed: %v", i, err)
		}
		time.Sleep(time.Millisecond)
	}
	subsA, _ := svc.ListSubscriptions(context.Background(), humanA)
	if len(subsA) != domain.MaxSubscriptionsPerUser {
		t.Fatalf("expected %d subscriptions after cap trim, got %d", domain.MaxSubscriptionsPerUser, len(subsA))
	}

	// 4. Shared browser reclaim: humanB registers the same endpoint as humanA -> humanA loses it.
	sharedEP := "https://fcm.googleapis.com/fcm/send/ep-11"
	if _, err := svc.UpsertSubscription(context.Background(), humanB, domain.UpsertSubscriptionInput{
		Endpoint: sharedEP,
		P256DH:   p256dh,
		Auth:     auth,
	}); err != nil {
		t.Fatalf("reclaim upsert failed: %v", err)
	}
	subsA, _ = svc.ListSubscriptions(context.Background(), humanA)
	subsB, _ := svc.ListSubscriptions(context.Background(), humanB)
	if len(subsA) != 9 || len(subsB) != 1 {
		t.Fatalf("expected reclaim split 9/1, got %d/%d", len(subsA), len(subsB))
	}

	// 5. HandleMessageHint with no subscribed candidates skips Chat RPC completely.
	out, err := svc.HandleMessageHint(context.Background(), domain.PushMessageHint{
		Type: "push.message", WorkspaceID: "w1", ConversationID: "c1", MessageID: "m1",
		SenderID: "uX", UserIDs: []string{"uUnsubscribed"},
	})
	if err != nil || out.SubscribedUsers != 0 || chat.calls != 0 {
		t.Fatalf("expected 0 chat RPC calls when no subscribers, got calls=%d out=%+v err=%v", chat.calls, out, err)
	}

	// 6. HandleMessageHint evaluates only subscribed candidates and prunes 410 Gone endpoints.
	sender.goneIDs[sharedEP] = true
	chat.result = domain.PushEvaluationResult{
		WorkspaceID: "w1", ConversationID: "c1", MessageID: "m2",
		RecipientUserIDs: []string{"uB"},
	}
	out, err = svc.HandleMessageHint(context.Background(), domain.PushMessageHint{
		Type: "push.message", WorkspaceID: "w1", ConversationID: "c1", MessageID: "m2",
		SenderID: "uA", UserIDs: []string{"uA", "uB", "uNoSub"},
	})
	if err != nil || chat.calls != 1 || len(chat.lastCands) != 1 || chat.lastCands[0] != "uB" {
		t.Fatalf("unexpected chat RPC args: calls=%d cands=%v err=%v", chat.calls, chat.lastCands, err)
	}
	if out.PrunedGone != 1 || out.Succeeded != 0 {
		t.Fatalf("expected 1 pruned gone subscription, got %+v", out)
	}
	subsB, _ = svc.ListSubscriptions(context.Background(), humanB)
	if len(subsB) != 0 {
		t.Fatalf("expected 410 subscription removed, still have %d", len(subsB))
	}

	// 7. Delete remains allowed even after im.push.web is turned off (logout cleanup invariant).
	flags.enabled = false
	if err := svc.DeleteSubscription(context.Background(), humanA, "https://fcm.googleapis.com/fcm/send/ep-10"); err != nil {
		t.Fatalf("delete must remain allowed when flag is off: %v", err)
	}
	subsA, _ = svc.ListSubscriptions(context.Background(), humanA)
	if len(subsA) != 8 {
		t.Fatalf("expected 8 remaining subscriptions after delete, got %d", len(subsA))
	}
}

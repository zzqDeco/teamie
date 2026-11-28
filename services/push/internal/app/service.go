// Package app orchestrates Web Push subscription management and privacy-safe
// background wake delivery against pure domain rules and outbound ports.
package app

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"teamie/services/push/internal/domain"
)

const (
	defaultMaxDeliveryConcurrency = 8
	defaultDeliveryTimeout        = 10 * time.Second
	maxEvaluateBatchSize          = 500
)

// DeliveryOutcome records aggregate non-sensitive telemetry for one processed wake hint.
type DeliveryOutcome struct {
	SubscribedUsers int
	EligibleUsers   int
	Attempted       int
	Succeeded       int
	PrunedGone      int
	Failed          int
}

// Service coordinates Push HTTP CRUD and background hint dispatch.
type Service struct {
	Tx                     domain.TxManager
	Subscriptions          domain.SubscriptionRepository
	Chat                   domain.ChatPushEvaluator
	Identity               domain.IdentityDirectory
	Flags                  domain.FeatureFlags
	Sender                 domain.WebPushSender
	VAPID                  domain.VAPIDConfig
	RequirePublicHTTPS     *bool
	MaxDeliveryConcurrency int
	DeliveryTimeout        time.Duration
}

func (s *Service) requirePublicHTTPS() bool {
	if s.RequirePublicHTTPS != nil {
		return *s.RequirePublicHTTPS
	}
	return true
}

func (s *Service) maxDeliveryConcurrency() int {
	if s.MaxDeliveryConcurrency > 0 {
		return s.MaxDeliveryConcurrency
	}
	return defaultMaxDeliveryConcurrency
}

func (s *Service) deliveryTimeout() time.Duration {
	if s.DeliveryTimeout > 0 {
		return s.DeliveryTimeout
	}
	return defaultDeliveryTimeout
}

// Active reports whether Web Push is both feature-flagged on and backed by valid VAPID credentials.
func (s *Service) Active() bool {
	return s.Flags != nil && s.Flags.Enabled("im.push.web") && s.VAPID.Configured()
}

// CheckDependencies verifies PostgreSQL, Identity RPC, and Chat RPC readiness.
func (s *Service) CheckDependencies(ctx context.Context) error {
	if err := s.Tx.Ping(ctx); err != nil {
		return err
	}
	if s.Identity != nil {
		if err := s.Identity.Ping(ctx); err != nil {
			return err
		}
	}
	if s.Chat != nil {
		if err := s.Chat.Ping(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Config returns the current Web Push configuration visible to an authenticated human user.
func (s *Service) Config(_ context.Context, actor domain.User) (domain.PushConfig, error) {
	if !actor.IsHuman() {
		return domain.PushConfig{}, domain.ErrForbidden()
	}
	if !s.Active() {
		return domain.PushConfig{
			Enabled:          false,
			MaxSubscriptions: domain.MaxSubscriptionsPerUser,
		}, nil
	}
	return domain.PushConfig{
		Enabled:          true,
		VAPIDPublicKey:   s.VAPID.PublicKey,
		MaxSubscriptions: domain.MaxSubscriptionsPerUser,
	}, nil
}

// ListSubscriptions lists the authenticated human user's persisted push subscriptions (metadata only).
// Persisted rows remain readable even when im.push.web is disabled.
func (s *Service) ListSubscriptions(ctx context.Context, actor domain.User) ([]domain.PushSubscription, error) {
	if !actor.IsHuman() {
		return nil, domain.ErrForbidden()
	}
	subs, err := s.Subscriptions.ListByUser(ctx, actor.ID)
	if err != nil {
		return nil, err
	}
	if subs == nil {
		return []domain.PushSubscription{}, nil
	}
	return subs, nil
}

// UpsertSubscription validates and registers or refreshes a browser push subscription for actor.
// Enforces the per-user cap (10) atomically and reclaims shared-browser endpoints from prior accounts.
func (s *Service) UpsertSubscription(ctx context.Context, actor domain.User, input domain.UpsertSubscriptionInput) (domain.PushSubscription, error) {
	if !actor.IsHuman() {
		return domain.PushSubscription{}, domain.ErrForbidden()
	}
	if !s.Active() {
		return domain.PushSubscription{}, domain.ErrFeatureDisabled()
	}
	endpoint, host, err := domain.ValidateEndpointURL(input.Endpoint, s.requirePublicHTTPS())
	if err != nil {
		return domain.PushSubscription{}, err
	}
	p256dh, err := domain.ValidateP256DH(input.P256DH)
	if err != nil {
		return domain.PushSubscription{}, err
	}
	authSecret, err := domain.ValidateAuthSecret(input.Auth)
	if err != nil {
		return domain.PushSubscription{}, err
	}
	ua := domain.SanitizeUserAgent(input.UserAgent)
	sub := domain.PushSubscription{
		ID:           domain.NewID(),
		UserID:       actor.ID,
		EndpointHash: domain.HashString(endpoint),
		Endpoint:     endpoint,
		EndpointHost: host,
		P256DH:       p256dh,
		Auth:         authSecret,
		UserAgent:    ua,
	}

	var saved domain.PushSubscription
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Tx.LockUserSubscriptions(txCtx, actor.ID); err != nil {
			return err
		}
		saved, err = s.Subscriptions.UpsertAndTrim(txCtx, sub, domain.MaxSubscriptionsPerUser)
		return err
	})
	return saved, err
}

// DeleteSubscription removes a browser push subscription owned by actor.
// Idempotent and intentionally allowed even if im.push.web is disabled (e.g. during logout cleanup).
func (s *Service) DeleteSubscription(ctx context.Context, actor domain.User, rawEndpoint string) error {
	if !actor.IsHuman() {
		return domain.ErrForbidden()
	}
	endpoint := strings.TrimSpace(rawEndpoint)
	if len(endpoint) == 0 || len(endpoint) > domain.MaxEndpointLength || strings.ContainsAny(endpoint, " \t\r\n\x00") {
		return domain.ErrInvalidEndpoint()
	}
	endpointHash := domain.HashString(endpoint)
	return s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Tx.LockUserSubscriptions(txCtx, actor.ID); err != nil {
			return err
		}
		_, err := s.Subscriptions.DeleteByUserEndpointHash(txCtx, actor.ID, endpointHash)
		return err
	})
}

// HandleMessageHint processes one opaque Chat push.message hint, checks subscribers,
// authorizes recipients via Chat RPC (ACL + notification level + online suppression),
// and fans out encrypted wake payloads without ever touching message content.
func (s *Service) HandleMessageHint(ctx context.Context, hint domain.PushMessageHint) (DeliveryOutcome, error) {
	var outcome DeliveryOutcome
	if hint.Type != "push.message" || !s.Active() || s.Sender == nil || s.Chat == nil {
		return outcome, nil
	}
	convID := strings.TrimSpace(hint.ConversationID)
	msgID := strings.TrimSpace(hint.MessageID)
	if convID == "" || msgID == "" || len(hint.UserIDs) == 0 {
		return outcome, nil
	}

	candidates := deduplicateExcluding(hint.UserIDs, hint.SenderID)
	if len(candidates) == 0 {
		return outcome, nil
	}

	// Only evaluate users who actually have at least one stored push subscription.
	listCtx, listCancel := context.WithTimeout(ctx, 5*time.Second)
	subs, err := s.Subscriptions.ListByUserIDs(listCtx, candidates)
	listCancel()
	if err != nil {
		return outcome, err
	}
	if len(subs) == 0 {
		return outcome, nil
	}

	subsByUser := make(map[string][]domain.PushSubscription)
	subscribedUserIDs := make([]string, 0)
	for _, sub := range subs {
		if _, exists := subsByUser[sub.UserID]; !exists {
			subscribedUserIDs = append(subscribedUserIDs, sub.UserID)
		}
		subsByUser[sub.UserID] = append(subsByUser[sub.UserID], sub)
	}
	slices.Sort(subscribedUserIDs)
	outcome.SubscribedUsers = len(subscribedUserIDs)

	workspaceID := ""
	recipientUserIDs := make([]string, 0)
	for start := 0; start < len(subscribedUserIDs); start += maxEvaluateBatchSize {
		end := min(start+maxEvaluateBatchSize, len(subscribedUserIDs))
		evalCtx, evalCancel := context.WithTimeout(ctx, 5*time.Second)
		eval, evalErr := s.Chat.EvaluatePushRecipients(evalCtx, convID, msgID, subscribedUserIDs[start:end])
		evalCancel()
		if evalErr != nil {
			return outcome, evalErr
		}
		if workspaceID == "" && eval.WorkspaceID != "" {
			workspaceID = eval.WorkspaceID
		}
		recipientUserIDs = append(recipientUserIDs, eval.RecipientUserIDs...)
	}
	if workspaceID == "" || len(recipientUserIDs) == 0 {
		return outcome, nil
	}
	outcome.EligibleUsers = len(recipientUserIDs)

	payload := domain.PushWakePayload{
		Type:           "message.wake",
		WorkspaceID:    workspaceID,
		ConversationID: convID,
		MessageID:      msgID,
		URL:            "/app/w/" + workspaceID + "/c/" + convID,
	}

	targets := make([]domain.PushSubscription, 0, len(recipientUserIDs))
	for _, uid := range recipientUserIDs {
		targets = append(targets, subsByUser[uid]...)
	}
	outcome.Attempted = len(targets)
	if len(targets) == 0 {
		return outcome, nil
	}

	sem := make(chan struct{}, s.maxDeliveryConcurrency())
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, target := range targets {
		sub := target
		select {
		case <-ctx.Done():
			mu.Lock()
			outcome.Failed++
			mu.Unlock()
			continue
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			// Start the per-delivery timeout strictly after semaphore admission
			// so later waves receive a full delivery budget instead of expiring while queued.
			sendCtx, cancel := context.WithTimeout(ctx, s.deliveryTimeout())
			res, sendErr := s.Sender.Send(sendCtx, sub, s.VAPID, payload)
			cancel()

			recCtx, recCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer recCancel()

			if res.Gone {
				_ = s.Subscriptions.DeleteByID(recCtx, sub.ID)
				mu.Lock()
				outcome.PrunedGone++
				mu.Unlock()
				return
			}
			if sendErr == nil && res.StatusCode >= 200 && res.StatusCode < 300 {
				_ = s.Subscriptions.RecordDeliveryResult(recCtx, sub.ID, true, "")
				mu.Lock()
				outcome.Succeeded++
				mu.Unlock()
				return
			}
			errCode := res.Reason
			if errCode == "" {
				errCode = "delivery_failed"
			}
			_ = s.Subscriptions.RecordDeliveryResult(recCtx, sub.ID, false, errCode)
			mu.Lock()
			outcome.Failed++
			mu.Unlock()
		}()
	}
	wg.Wait()
	return outcome, nil
}

func deduplicateExcluding(ids []string, exclude string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" || id == exclude || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

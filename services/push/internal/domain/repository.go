package domain

import "context"

// TxManager coordinates atomic transactions and per-user advisory locks.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
	LockUserSubscriptions(ctx context.Context, userID string) error
	Ping(ctx context.Context) error
}

// SubscriptionRepository persists Web Push subscriptions in teamie_push.
type SubscriptionRepository interface {
	ListByUser(ctx context.Context, userID string) ([]PushSubscription, error)
	ListByUserIDs(ctx context.Context, userIDs []string) ([]PushSubscription, error)
	UpsertAndTrim(ctx context.Context, sub PushSubscription, maxPerUser int) (PushSubscription, error)
	DeleteByUserEndpointHash(ctx context.Context, userID, endpointHash string) (bool, error)
	DeleteByID(ctx context.Context, id string) error
	RecordDeliveryResult(ctx context.Context, id string, ok bool, errCode string) error
}

// FeatureFlags exposes Push feature toggles to the application layer.
type FeatureFlags interface {
	Enabled(key string) bool
}

// ChatPushEvaluator queries ChatService.EvaluatePushRecipients over private Kitex RPC.
type ChatPushEvaluator interface {
	EvaluatePushRecipients(ctx context.Context, conversationID, messageID string, candidateUserIDs []string) (PushEvaluationResult, error)
	Ping(ctx context.Context) error
}

// IdentityDirectory verifies Identity RPC readiness.
type IdentityDirectory interface {
	Ping(ctx context.Context) error
}

// WebPushSender encrypts (RFC 8291) and sends (RFC 8292 VAPID) a wake payload to a single push endpoint.
type WebPushSender interface {
	Send(ctx context.Context, sub PushSubscription, vapid VAPIDConfig, payload PushWakePayload) (DeliveryResult, error)
}

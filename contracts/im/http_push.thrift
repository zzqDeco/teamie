namespace go im.push

struct EmptyPushReq {}

struct EmptyResp {}

struct PushConfigResp {
  1: required bool enabled (api.body="enabled")
  2: optional string vapidPublicKey (api.body="vapidPublicKey")
  3: required i32 maxSubscriptions (api.body="maxSubscriptions")
}

struct PushSubscriptionKeysReq {
  1: required string p256dh (api.body="p256dh")
  2: required string auth (api.body="auth")
}

struct UpsertPushSubscriptionReq {
  1: required string endpoint (api.body="endpoint")
  2: required PushSubscriptionKeysReq keys (api.body="keys")
  3: optional string userAgent (api.body="userAgent")
}

struct DeletePushSubscriptionReq {
  1: required string endpoint (api.body="endpoint")
}

struct PushSubscriptionDTO {
  1: required string id (api.body="id")
  2: required string endpointHash (api.body="endpointHash")
  3: required string endpointHost (api.body="endpointHost")
  4: required string userAgent (api.body="userAgent")
  5: required string createdAt (api.body="createdAt")
  6: required string updatedAt (api.body="updatedAt")
  7: optional string lastSuccessAt (api.body="lastSuccessAt")
}

struct PushSubscriptionResp {
  1: required PushSubscriptionDTO subscription (api.body="subscription")
}

struct ListPushSubscriptionsResp {
  1: required list<PushSubscriptionDTO> subscriptions (api.body="subscriptions")
}

service PushHTTPService {
  PushConfigResp GetPushConfig(1: EmptyPushReq req) (api.get="/api/v1/push/config")
  ListPushSubscriptionsResp ListPushSubscriptions(1: EmptyPushReq req) (api.get="/api/v1/push/subscriptions")
  PushSubscriptionResp UpsertPushSubscription(1: UpsertPushSubscriptionReq req) (api.put="/api/v1/push/subscriptions")
  EmptyResp DeletePushSubscription(1: DeletePushSubscriptionReq req) (api.delete="/api/v1/push/subscriptions")
}

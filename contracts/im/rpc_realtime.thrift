namespace go im

struct PresenceRequest {
  1: required string serviceToken
  2: required list<string> userIds
}

struct UserPresence {
  1: required string userId
  2: required string state
}

struct PresenceResponse {
  1: required bool ok
  2: required string errorCode
  3: required list<UserPresence> presence
}

service RealtimeService {
  PresenceResponse GetPresence(1: PresenceRequest request)
  PingResponse Ping(1: PingRequest request)
}

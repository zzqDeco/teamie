namespace go im

// IM participants remain independent from any future Agent runtime.
struct User {
  1: required string id
  2: required string email
  3: required string displayName
  4: required string kind
  5: required string createdAt
  6: required string platformRole
}
struct SessionRequest { 1: required string token }
struct SessionResponse {
  1: required bool valid
  2: optional User user
  3: optional string csrfToken
}
struct UsersRequest { 1: required list<string> ids }
struct UsersResponse { 1: required list<User> users }
struct PingRequest {}
struct PingResponse { 1: required bool ready }
service IdentityService {
  SessionResponse ValidateSession(1: SessionRequest request)
  UsersResponse GetUsers(1: UsersRequest request)
  PingResponse Ping(1: PingRequest request)
}
struct AccessRequest {
  1: required string userId
  2: required string conversationId
}
struct AccessResponse {
  1: required bool allowed
  2: required list<string> memberIds
  3: required bool typingEnabled
}
service ChatService {
  AccessResponse CheckAccess(1: AccessRequest request)
  PingResponse Ping(1: PingRequest request)
}

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
// Generic participant lifecycle; control and runtime service credentials are
// scoped separately. No runtime execution configuration crosses this contract.
struct PrincipalControlRequest {
 1: required string serviceToken
 2: required string principalId
 3: required string displayName
 4: required string kind
 5: required bool enabled
 6: required bool deleted
 7: required i64 revision
}
struct PrincipalResponse { 1: required bool ok, 2: required string errorCode, 3: optional User user }
struct PrincipalTokenRequest { 1: required string serviceToken, 2: required string principalId }
struct PrincipalTokenResponse { 1: required bool ok, 2: required string errorCode, 3: required string token, 4: required string expiresAt }
struct PrincipalAuthRequest { 1: required string serviceToken, 2: required string token }
service IdentityService {
  SessionResponse ValidateSession(1: SessionRequest request)
  UsersResponse GetUsers(1: UsersRequest request)
  PingResponse Ping(1: PingRequest request)
  PrincipalResponse ProvisionPrincipal(1: PrincipalControlRequest request)
  PrincipalTokenResponse IssuePrincipalToken(1: PrincipalTokenRequest request)
  SessionResponse ValidatePrincipalToken(1: PrincipalAuthRequest request)
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
struct ParticipantWorkspacesRequest { 1: required string serviceToken, 2: required string userId }
struct ParticipantWorkspacesResponse { 1: required bool ok, 2: required string errorCode, 3: required list<string> workspaceIds }
struct WorkspaceParticipantsRequest { 1: required string serviceToken, 2: required string workspaceId, 3: required string requesterId }
struct WorkspaceParticipantsResponse { 1: required bool ok, 2: required string errorCode, 3: required list<string> memberIds }
struct PrincipalWorkspacesRequest {
 1: required string serviceToken, 2: required string principalId, 3: required string grantorUserId,
 4: required list<string> workspaceIds, 5: required i64 revision
}
service ChatService {
  AccessResponse CheckAccess(1: AccessRequest request)
  PingResponse Ping(1: PingRequest request)
  ParticipantWorkspacesResponse GetParticipantWorkspaces(1: ParticipantWorkspacesRequest request)
  WorkspaceParticipantsResponse GetWorkspaceParticipants(1: WorkspaceParticipantsRequest request)
  ParticipantWorkspacesResponse SetPrincipalWorkspaces(1: PrincipalWorkspacesRequest request)
}

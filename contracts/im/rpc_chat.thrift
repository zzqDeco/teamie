namespace go im

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

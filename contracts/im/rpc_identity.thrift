namespace go im

struct SessionRequest { 1: required string token }
struct SessionResponse {
  1: required bool valid
  2: optional User user
  3: optional string csrfToken
}
struct UsersRequest { 1: required list<string> ids }
struct UsersResponse { 1: required list<User> users }
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

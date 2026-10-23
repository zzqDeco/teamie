namespace go im.identity

// Public HTTP wire contract for the Teamie Identity service.
// Kept independent from any Agent runtime or Eino types.

struct UserDTO {
  1: required string id (api.body="id")
  2: required string email (api.body="email")
  3: required string displayName (api.body="displayName")
  4: required string kind (api.body="kind")
  5: required string platformRole (api.body="platformRole")
  6: required string createdAt (api.body="createdAt")
}

struct SessionResp {
  1: required UserDTO user (api.body="user")
  2: required string csrfToken (api.body="csrfToken")
}

struct GetRegistrationMetadataReq {}

struct RegistrationMetadataResp {
  1: required string mode (api.body="mode")
  2: required bool bootstrapRequired (api.body="bootstrapRequired")
}

struct RegisterReq {
  1: required string email (api.body="email")
  2: required string password (api.body="password")
  3: required string displayName (api.body="displayName")
  4: required string inviteCode (api.body="inviteCode")
}

struct LoginReq {
  1: required string email (api.body="email")
  2: required string password (api.body="password")
}

struct GetSessionReq {}

struct LogoutReq {}

struct EmptyResp {}

struct ChangePasswordReq {
  1: required string currentPassword (api.body="currentPassword")
  2: required string newPassword (api.body="newPassword")
}

struct UpdateProfileReq {
  1: required string displayName (api.body="displayName")
}

struct UpdateProfileResp {
  1: required UserDTO user (api.body="user")
}

struct RegistrationInviteDTO {
  1: required string id (api.body="id")
  2: required string createdAt (api.body="createdAt")
  3: required string expiresAt (api.body="expiresAt")
  4: optional string usedAt (api.body="usedAt")
  5: optional string revokedAt (api.body="revokedAt")
  6: optional string usedBy (api.body="usedBy")
}

struct CreatedRegistrationInviteDTO {
  1: required string id (api.body="id")
  2: required string createdAt (api.body="createdAt")
  3: required string expiresAt (api.body="expiresAt")
  4: optional string usedAt (api.body="usedAt")
  5: optional string revokedAt (api.body="revokedAt")
  6: optional string usedBy (api.body="usedBy")
  7: required string code (api.body="code")
}

struct ListInvitesReq {}

struct ListInvitesResp {
  1: required list<RegistrationInviteDTO> invites (api.body="invites")
}

struct CreateInviteReq {}

struct CreateInviteResp {
  1: required CreatedRegistrationInviteDTO invite (api.body="invite")
  2: required string url (api.body="url")
}

struct RevokeInviteReq {
  1: required string id (api.path="id")
}

struct ListAdminUsersReq {
  1: optional string cursor (api.query="cursor")
  2: optional string limit (api.query="limit")
  3: optional string q (api.query="q")
}

struct AdminUserPageResp {
  1: required list<UserDTO> users (api.body="users")
  2: optional string cursor (api.body="cursor")
}

struct CreatePasswordResetLinkReq {
  1: required string id (api.path="id")
}

struct PasswordResetDTO {
  1: required string id (api.body="id")
  2: required string createdAt (api.body="createdAt")
  3: required string expiresAt (api.body="expiresAt")
}

struct CreatePasswordResetLinkResp {
  1: required PasswordResetDTO reset (api.body="reset")
  2: required string url (api.body="url")
}

struct InspectPasswordResetReq {
  1: required string token (api.body="token")
}

struct PasswordResetAccountDTO {
  1: required string id (api.body="id")
  2: required string displayName (api.body="displayName")
  3: required string emailMasked (api.body="emailMasked")
}

struct PasswordResetInspectionResp {
  1: required PasswordResetAccountDTO account (api.body="account")
  2: required string expiresAt (api.body="expiresAt")
}

struct ResetPasswordReq {
  1: required string token (api.body="token")
  2: required string newPassword (api.body="newPassword")
}

service IdentityHTTPService {
  RegistrationMetadataResp GetRegistrationMetadata(1: GetRegistrationMetadataReq req) (api.get="/api/v1/auth/registration")
  SessionResp Register(1: RegisterReq req) (api.post="/api/v1/auth/register", api.status="201")
  SessionResp Login(1: LoginReq req) (api.post="/api/v1/auth/login")
  SessionResp GetSession(1: GetSessionReq req) (api.get="/api/v1/auth/session")
  EmptyResp Logout(1: LogoutReq req) (api.post="/api/v1/auth/logout")
  EmptyResp ChangePassword(1: ChangePasswordReq req) (api.post="/api/v1/auth/password")
  UpdateProfileResp UpdateProfile(1: UpdateProfileReq req) (api.patch="/api/v1/auth/profile")
  ListInvitesResp ListInvites(1: ListInvitesReq req) (api.get="/api/v1/auth/admin/invites")
  CreateInviteResp CreateInvite(1: CreateInviteReq req) (api.post="/api/v1/auth/admin/invites", api.status="201")
  EmptyResp RevokeInvite(1: RevokeInviteReq req) (api.delete="/api/v1/auth/admin/invites/:id")
  AdminUserPageResp ListAdminUsers(1: ListAdminUsersReq req) (api.get="/api/v1/auth/admin/users")
  CreatePasswordResetLinkResp CreatePasswordResetLink(1: CreatePasswordResetLinkReq req) (api.post="/api/v1/auth/admin/users/:id/password-reset-links", api.status="201")
  PasswordResetInspectionResp InspectPasswordReset(1: InspectPasswordResetReq req) (api.post="/api/v1/auth/password-reset/inspect")
  EmptyResp ResetPassword(1: ResetPasswordReq req) (api.post="/api/v1/auth/password-reset")
}

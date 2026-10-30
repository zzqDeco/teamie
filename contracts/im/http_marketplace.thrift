namespace go im.marketplace

struct EmptyMarketplaceReq {}

struct EmptyResp {}

struct MarketplaceAppDTO {
  1: required string id (api.body="id")
  2: required string slug (api.body="slug")
  3: required string name (api.body="name")
  4: required string description (api.body="description")
  5: required string webUrl (api.body="webUrl")
  6: required string mcpUrl (api.body="mcpUrl")
  7: required bool enabled (api.body="enabled")
  8: required string createdAt (api.body="createdAt")
  9: required string updatedAt (api.body="updatedAt")
  10: optional string publisherName (api.body="publisherName")
  11: optional bool isOfficial (api.body="isOfficial")
  12: optional bool servingDraft (api.body="servingDraft")
  13: optional i32 publishedVersion (api.body="publishedVersion")
}

struct DiscoveredMCPToolDTO {
  1: required string name (api.body="name")
  2: required string exposedName (api.body="exposedName")
  3: required string description (api.body="description")
  4: optional string inputSchemaJson (api.body="inputSchemaJson")
}

struct EndpointProbeCheckDTO {
  1: required string stage (api.body="stage")
  2: required bool ok (api.body="ok")
  3: required string code (api.body="code")
  4: required string summary (api.body="summary")
}

struct MarketplaceEndpointVerificationDTO {
  1: required bool ok (api.body="ok")
  2: required string verifiedAt (api.body="verifiedAt")
  3: required string webUrl (api.body="webUrl")
  4: required string mcpUrl (api.body="mcpUrl")
  5: optional string mcpServerName (api.body="mcpServerName")
  6: optional string mcpServerVersion (api.body="mcpServerVersion")
  7: optional string mcpProtocolVersion (api.body="mcpProtocolVersion")
  8: required list<EndpointProbeCheckDTO> checks (api.body="checks")
  9: required list<DiscoveredMCPToolDTO> tools (api.body="tools")
}

struct MarketplaceAppReviewDTO {
  1: required string id (api.body="id")
  2: required string appId (api.body="appId")
  3: required i32 revision (api.body="revision")
  4: required string action (api.body="action")
  5: required string actorId (api.body="actorId")
  6: optional string actorName (api.body="actorName")
  7: required string note (api.body="note")
  8: required string webUrl (api.body="webUrl")
  9: required string mcpUrl (api.body="mcpUrl")
  10: required string createdAt (api.body="createdAt")
}

struct DeveloperMarketplaceAppDTO {
  1: required string id (api.body="id")
  2: required string slug (api.body="slug")
  3: required string name (api.body="name")
  4: required string description (api.body="description")
  5: required string webUrl (api.body="webUrl")
  6: required string mcpUrl (api.body="mcpUrl")
  7: required bool enabled (api.body="enabled")
  8: required string createdAt (api.body="createdAt")
  9: required string updatedAt (api.body="updatedAt")
  10: optional string ownerId (api.body="ownerId")
  11: optional string ownerDisplayName (api.body="ownerDisplayName")
  12: required bool isOfficial (api.body="isOfficial")
  13: required bool isPublished (api.body="isPublished")
  14: required i32 publishedVersion (api.body="publishedVersion")
  15: required string reviewStatus (api.body="reviewStatus")
  16: required string draftName (api.body="draftName")
  17: required string draftDescription (api.body="draftDescription")
  18: required string draftWebUrl (api.body="draftWebUrl")
  19: required string draftMcpUrl (api.body="draftMcpUrl")
  20: required bool hasDraftChanges (api.body="hasDraftChanges")
  21: required i32 submissionRevision (api.body="submissionRevision")
  22: required string submissionNote (api.body="submissionNote")
  23: optional string submittedAt (api.body="submittedAt")
  24: optional string reviewedAt (api.body="reviewedAt")
  25: required string reviewNote (api.body="reviewNote")
  26: required bool adminSuspended (api.body="adminSuspended")
  27: required string suspendedReason (api.body="suspendedReason")
  28: optional string lastVerifiedAt (api.body="lastVerifiedAt")
  29: optional MarketplaceEndpointVerificationDTO verificationReport (api.body="verificationReport")
}

struct MarketplaceAppResp {
  1: required MarketplaceAppDTO app (api.body="app")
}

struct CreatedMarketplaceAppResp {
  1: required MarketplaceAppDTO app (api.body="app")
  2: required string signingSecret (api.body="signingSecret")
}

struct RotateMarketplaceAppSecretResp {
  1: required MarketplaceAppDTO app (api.body="app")
  2: required string signingSecret (api.body="signingSecret")
}

struct MarketplaceAppListResp {
  1: required list<MarketplaceAppDTO> apps (api.body="apps")
}

struct DeveloperMarketplaceAppResp {
  1: required DeveloperMarketplaceAppDTO app (api.body="app")
}

struct DeveloperMarketplaceAppDetailResp {
  1: required DeveloperMarketplaceAppDTO app (api.body="app")
  2: required list<MarketplaceAppReviewDTO> reviews (api.body="reviews")
}

struct DeveloperMarketplaceAppListResp {
  1: required list<DeveloperMarketplaceAppDTO> apps (api.body="apps")
}

struct CreatedDeveloperMarketplaceAppResp {
  1: required DeveloperMarketplaceAppDTO app (api.body="app")
  2: required string signingSecret (api.body="signingSecret")
}

struct RotateDeveloperMarketplaceAppSecretResp {
  1: required DeveloperMarketplaceAppDTO app (api.body="app")
  2: required string signingSecret (api.body="signingSecret")
}

struct VerifyMarketplaceAppResp {
  1: required DeveloperMarketplaceAppDTO app (api.body="app")
  2: required MarketplaceEndpointVerificationDTO verification (api.body="verification")
}

struct CreateMarketplaceAppReq {
  1: required string slug (api.body="slug")
  2: required string name (api.body="name")
  3: optional string description (api.body="description")
  4: required string webUrl (api.body="webUrl")
  5: required string mcpUrl (api.body="mcpUrl")
  6: optional bool enabled (api.body="enabled")
}

struct CreateDeveloperMarketplaceAppReq {
  1: required string slug (api.body="slug")
  2: required string name (api.body="name")
  3: optional string description (api.body="description")
  4: optional string webUrl (api.body="webUrl")
  5: optional string mcpUrl (api.body="mcpUrl")
}

struct UpdateMarketplaceAppReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: optional string slug (api.body="slug")
  3: optional string name (api.body="name")
  4: optional string description (api.body="description")
  5: optional string webUrl (api.body="webUrl")
  6: optional string mcpUrl (api.body="mcpUrl")
  7: optional bool enabled (api.body="enabled")
}

struct SubmitDeveloperMarketplaceAppReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: optional string note (api.body="note")
}

struct ReviewAdminMarketplaceAppReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: required i32 expectedRevision (api.body="expectedRevision")
  3: optional string note (api.body="note")
}

struct RejectAdminMarketplaceAppReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: required i32 expectedRevision (api.body="expectedRevision")
  3: required string note (api.body="note")
}

struct SuspendAdminMarketplaceAppReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: required string reason (api.body="reason")
}

struct MarketplaceAppIDReq {
  1: required string id (api.path="id")
}

struct MarketplaceWorkspaceIDReq {
  1: required string id (api.path="id")
}

struct MarketplaceConversationIDReq {
  1: required string id (api.path="id")
}

struct InstallWorkspaceAppReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: required string appId (api.body="appId")
}

struct UninstallWorkspaceAppReq {
  1: required string id (api.path="id")
  2: required string appId (api.path="appId")
}

struct CreateConversationAppSessionReq {
  1: required string id (api.path="id")
  2: required string appId (api.path="appId")
}

struct ConversationAppSessionResp {
  1: required MarketplaceAppDTO app (api.body="app")
  2: required string token (api.body="token")
  3: required string expiresAt (api.body="expiresAt")
}

service MarketplaceHTTPService {
  DeveloperMarketplaceAppListResp ListDeveloperMarketplaceApps(1: EmptyMarketplaceReq req) (api.get="/api/v1/developer/apps")
  CreatedDeveloperMarketplaceAppResp CreateDeveloperMarketplaceApp(1: CreateDeveloperMarketplaceAppReq req) (api.post="/api/v1/developer/apps", api.status="201")
  DeveloperMarketplaceAppDetailResp GetDeveloperMarketplaceApp(1: MarketplaceAppIDReq req) (api.get="/api/v1/developer/apps/:id")
  DeveloperMarketplaceAppResp UpdateDeveloperMarketplaceApp(1: UpdateMarketplaceAppReq req) (api.patch="/api/v1/developer/apps/:id")
  VerifyMarketplaceAppResp VerifyDeveloperMarketplaceApp(1: MarketplaceAppIDReq req) (api.post="/api/v1/developer/apps/:id/verify")
  DeveloperMarketplaceAppDetailResp SubmitDeveloperMarketplaceApp(1: SubmitDeveloperMarketplaceAppReq req) (api.post="/api/v1/developer/apps/:id/submit")
  DeveloperMarketplaceAppDetailResp WithdrawDeveloperMarketplaceApp(1: MarketplaceAppIDReq req) (api.post="/api/v1/developer/apps/:id/withdraw")
  RotateDeveloperMarketplaceAppSecretResp RotateDeveloperMarketplaceAppSecret(1: MarketplaceAppIDReq req) (api.post="/api/v1/developer/apps/:id/rotate-secret")
  EmptyResp DeleteDeveloperMarketplaceApp(1: MarketplaceAppIDReq req) (api.delete="/api/v1/developer/apps/:id")
  DeveloperMarketplaceAppListResp ListAdminMarketplaceApps(1: EmptyMarketplaceReq req) (api.get="/api/v1/admin/marketplace/apps")
  DeveloperMarketplaceAppDetailResp GetAdminMarketplaceApp(1: MarketplaceAppIDReq req) (api.get="/api/v1/admin/marketplace/apps/:id")
  CreatedMarketplaceAppResp CreateAdminMarketplaceApp(1: CreateMarketplaceAppReq req) (api.post="/api/v1/admin/marketplace/apps", api.status="201")
  MarketplaceAppResp UpdateAdminMarketplaceApp(1: UpdateMarketplaceAppReq req) (api.patch="/api/v1/admin/marketplace/apps/:id")
  VerifyMarketplaceAppResp VerifyAdminMarketplaceApp(1: MarketplaceAppIDReq req) (api.post="/api/v1/admin/marketplace/apps/:id/verify")
  DeveloperMarketplaceAppDetailResp ApproveAdminMarketplaceApp(1: ReviewAdminMarketplaceAppReq req) (api.post="/api/v1/admin/marketplace/apps/:id/approve")
  DeveloperMarketplaceAppDetailResp RejectAdminMarketplaceApp(1: RejectAdminMarketplaceAppReq req) (api.post="/api/v1/admin/marketplace/apps/:id/reject")
  DeveloperMarketplaceAppDetailResp SuspendAdminMarketplaceApp(1: SuspendAdminMarketplaceAppReq req) (api.post="/api/v1/admin/marketplace/apps/:id/suspend")
  DeveloperMarketplaceAppDetailResp UnsuspendAdminMarketplaceApp(1: MarketplaceAppIDReq req) (api.post="/api/v1/admin/marketplace/apps/:id/unsuspend")
  RotateMarketplaceAppSecretResp RotateAdminMarketplaceAppSecret(1: MarketplaceAppIDReq req) (api.post="/api/v1/admin/marketplace/apps/:id/rotate-secret")
  EmptyResp DeleteAdminMarketplaceApp(1: MarketplaceAppIDReq req) (api.delete="/api/v1/admin/marketplace/apps/:id")
  MarketplaceAppListResp ListMarketplaceCatalog(1: EmptyMarketplaceReq req) (api.get="/api/v1/marketplace/catalog")
  MarketplaceAppListResp ListWorkspaceApps(1: MarketplaceWorkspaceIDReq req) (api.get="/api/v1/workspaces/:id/apps")
  MarketplaceAppResp InstallWorkspaceApp(1: InstallWorkspaceAppReq req) (api.post="/api/v1/workspaces/:id/apps", api.status="201")
  EmptyResp UninstallWorkspaceApp(1: UninstallWorkspaceAppReq req) (api.delete="/api/v1/workspaces/:id/apps/:appId")
  MarketplaceAppListResp ListConversationApps(1: MarketplaceConversationIDReq req) (api.get="/api/v1/conversations/:id/apps")
  ConversationAppSessionResp CreateConversationAppSession(1: CreateConversationAppSessionReq req) (api.post="/api/v1/conversations/:id/apps/:appId/session")
}

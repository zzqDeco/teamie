namespace go agent.gateway

// HTTP and NDJSON stream wire contract for the Teamie Model Gateway service.
// Covers administrator configuration endpoints and internal runtime model/web endpoints.

struct WebSearchConfigDTO {
  1: required string provider (api.body="provider")
  2: required bool enabled (api.body="enabled")
  3: required bool hasApiKey (api.body="hasApiKey")
  4: required bool configured (api.body="configured")
  5: optional string updatedAt (api.body="updatedAt")
}

struct ModelRoutingConfigDTO {
  1: required string defaultModelId (api.body="defaultModelId")
  2: required string effectiveModelId (api.body="effectiveModelId")
}

struct UpdateAdminModelRoutingReq {
  1: required string defaultModelId (api.body="defaultModelId")
}

struct UpdateAdminWebSearchConfigReq {
  1: optional bool enabled (api.body="enabled")
  2: optional string apiKey (api.body="apiKey")
  3: optional bool clearApiKey (api.body="clearApiKey")
}

struct ModelConfigDTO {
  1: required string baseUrl (api.body="baseUrl")
  2: required string model (api.body="model")
  3: required i64 contextWindowTokens (api.body="contextWindowTokens")
  4: required bool hasApiKey (api.body="hasApiKey")
  5: required bool enabled (api.body="enabled")
  6: required bool configured (api.body="configured")
  7: required WebSearchConfigDTO webSearch (api.body="webSearch")
  8: required string updatedAt (api.body="updatedAt")
}

struct AdminModelProfileDTO {
  1: required string id (api.body="id")
  2: required string displayName (api.body="displayName")
  3: required string description (api.body="description")
  4: required string baseUrl (api.body="baseUrl")
  5: required string model (api.body="model")
  6: required i64 contextWindowTokens (api.body="contextWindowTokens")
  7: required bool hasApiKey (api.body="hasApiKey")
  8: required bool enabled (api.body="enabled")
  9: required bool configured (api.body="configured")
  10: required bool isDefault (api.body="isDefault")
  11: required string updatedAt (api.body="updatedAt")
}

struct GetModelCatalogReq {}

struct PublicModelOptionDTO {
  1: required string id (api.body="id")
  2: required string displayName (api.body="displayName")
  3: required string description (api.body="description")
  4: required i64 contextWindowTokens (api.body="contextWindowTokens")
  5: required bool isDefault (api.body="isDefault")
}

struct ModelCatalogResp {
  1: required list<PublicModelOptionDTO> models (api.body="models")
  2: optional string defaultModelId (api.body="defaultModelId")
}

struct GetAdminModelConfigReq {}

struct AdminModelConfigResp {
  1: required ModelConfigDTO config (api.body="config")
  2: required string status (api.body="status")
  3: optional list<AdminModelProfileDTO> models (api.body="models")
  4: optional string defaultModelId (api.body="defaultModelId")
  5: required ModelRoutingConfigDTO routing (api.body="routing")
  6: required WebSearchConfigDTO webSearch (api.body="webSearch")
}

struct UpdateAdminModelConfigReq {
  1: required string baseUrl (api.body="baseUrl")
  2: required string model (api.body="model")
  3: optional string apiKey (api.body="apiKey")
  4: required bool enabled (api.body="enabled")
  5: optional bool clearApiKey (api.body="clearApiKey")
  6: optional i64 contextWindowTokens (api.body="contextWindowTokens")
  7: optional bool webSearchEnabled (api.body="webSearchEnabled")
  8: optional string webSearchApiKey (api.body="webSearchApiKey")
  9: optional bool clearWebSearchApiKey (api.body="clearWebSearchApiKey")
}

struct CreateAdminModelProfileReq {
  1: optional string id (api.body="id")
  2: optional string displayName (api.body="displayName")
  3: optional string description (api.body="description")
  4: required string baseUrl (api.body="baseUrl")
  5: required string model (api.body="model")
  6: optional string apiKey (api.body="apiKey")
  7: required bool enabled (api.body="enabled")
  8: optional i64 contextWindowTokens (api.body="contextWindowTokens")
  // Field 9 retired: default selection is updated through the routing API.
}

struct UpdateAdminModelProfileReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: optional string displayName (api.body="displayName")
  3: optional string description (api.body="description")
  4: optional string baseUrl (api.body="baseUrl")
  5: optional string model (api.body="model")
  6: optional string apiKey (api.body="apiKey")
  7: optional bool clearApiKey (api.body="clearApiKey")
  8: optional bool enabled (api.body="enabled")
  9: optional i64 contextWindowTokens (api.body="contextWindowTokens")
  // Field 10 retired: default selection is updated through the routing API.
}

struct DeleteAdminModelProfileReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
}

struct TestAdminModelProfileReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
}

struct TestAdminModelConfigReq {}

struct TestAdminModelConfigResp {
  1: required bool ok (api.body="ok")
  2: required string model (api.body="model")
  3: required i64 latencyMs (api.body="latencyMs")
}

struct ChatCallReq {
  1: required string ownerId (api.body="ownerId")
  2: required string agentId (api.body="agentId")
  3: required string sessionId (api.body="sessionId")
  4: required string runId (api.body="runId")
  5: required string requestJson (api.body="requestJson")
  6: optional i32 messageLimit (api.body="messageLimit")
  7: optional string expectedModelConfigId (api.header="X-Teamie-Model-Config-Id")
  8: optional string modelId (api.body="modelId")
}

struct ChatCallResp {
  1: required bool ok (api.body="ok")
  2: optional string responseJson (api.body="responseJson")
  3: optional string errorCode (api.body="errorCode")
  4: optional string errorMessage (api.body="errorMessage")
  5: optional i32 statusCode (api.body="statusCode")
  6: optional string modelConfigId (api.body="modelConfigId")
}

struct StreamEventFrameDTO {
  1: required string type (api.body="type")
  2: optional string responseJson (api.body="responseJson")
  3: optional string errorCode (api.body="errorCode")
  4: optional string errorMessage (api.body="errorMessage")
  5: optional i32 statusCode (api.body="statusCode")
  6: optional string modelConfigId (api.body="modelConfigId")
}

struct WebSearchReq {
  1: required string ownerId (api.body="ownerId")
  2: required string agentId (api.body="agentId")
  3: required string sessionId (api.body="sessionId")
  4: required string runId (api.body="runId")
  5: required string query (api.body="query")
  6: optional i32 maxResults (api.body="maxResults")
  7: optional string timeRange (api.body="timeRange")
  8: optional list<string> includeDomains (api.body="includeDomains")
  9: optional list<string> excludeDomains (api.body="excludeDomains")
}

struct WebSearchResultDTO {
  1: required string title (api.body="title")
  2: required string url (api.body="url")
  3: required string snippet (api.body="snippet")
  4: optional string publishedDate (api.body="publishedDate")
  5: optional double score (api.body="score")
}

struct WebSearchResp {
  1: required bool ok (api.body="ok")
  2: optional list<WebSearchResultDTO> results (api.body="results")
  3: optional string errorCode (api.body="errorCode")
  4: optional string errorMessage (api.body="errorMessage")
  5: optional i32 statusCode (api.body="statusCode")
}

struct WebExtractReq {
  1: required string ownerId (api.body="ownerId")
  2: required string agentId (api.body="agentId")
  3: required string sessionId (api.body="sessionId")
  4: required string runId (api.body="runId")
  5: required list<string> urls (api.body="urls")
}

struct WebExtractResultDTO {
  1: required string url (api.body="url")
  2: required string content (api.body="content")
}

struct WebExtractFailureDTO {
  1: required string url (api.body="url")
  2: optional string error (api.body="error")
}

struct WebExtractResp {
  1: required bool ok (api.body="ok")
  2: optional list<WebExtractResultDTO> results (api.body="results")
  3: optional list<WebExtractFailureDTO> failedResults (api.body="failedResults")
  4: optional string errorCode (api.body="errorCode")
  5: optional string errorMessage (api.body="errorMessage")
  6: optional i32 statusCode (api.body="statusCode")
}

service ModelGatewayHTTPService {
  ModelCatalogResp GetModelCatalog(1: GetModelCatalogReq req) (api.get="/api/v1/model-gateway/catalog")
  AdminModelConfigResp GetAdminModelConfig(1: GetAdminModelConfigReq req) (api.get="/api/v1/admin/model-gateway")
  AdminModelConfigResp UpdateAdminWebSearchConfig(1: UpdateAdminWebSearchConfigReq req) (api.patch="/api/v1/admin/model-gateway/web-search")
  AdminModelConfigResp UpdateAdminModelRouting(1: UpdateAdminModelRoutingReq req) (api.put="/api/v1/admin/model-gateway/routing")
  // Retired combined write: returns 409 config_api_changed without side effects.
  AdminModelConfigResp UpdateAdminModelConfig(1: UpdateAdminModelConfigReq req) (api.put="/api/v1/admin/model-gateway")
  TestAdminModelConfigResp TestAdminModelConfig(1: TestAdminModelConfigReq req) (api.post="/api/v1/admin/model-gateway/test")
  AdminModelConfigResp CreateAdminModelProfile(1: CreateAdminModelProfileReq req) (api.post="/api/v1/admin/model-gateway/models", api.status="201")
  AdminModelConfigResp UpdateAdminModelProfile(1: UpdateAdminModelProfileReq req) (api.patch="/api/v1/admin/model-gateway/models/:id")
  AdminModelConfigResp DeleteAdminModelProfile(1: DeleteAdminModelProfileReq req) (api.delete="/api/v1/admin/model-gateway/models/:id")
  TestAdminModelConfigResp TestAdminModelProfile(1: TestAdminModelProfileReq req) (api.post="/api/v1/admin/model-gateway/models/:id/test")
  ChatCallResp GenerateChat(1: ChatCallReq req) (api.post="/internal/v1/chat")
  StreamEventFrameDTO StreamChat(1: ChatCallReq req) (api.post="/internal/v1/chat/stream")
  WebSearchResp WebSearch(1: WebSearchReq req) (api.post="/internal/v1/web/search")
  WebExtractResp WebExtract(1: WebExtractReq req) (api.post="/internal/v1/web/extract")
}

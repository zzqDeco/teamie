namespace go agent.gateway

// HTTP and NDJSON stream wire contract for the Teamie Model Gateway service.
// Covers administrator configuration endpoints and internal runtime model/web endpoints.

struct WebSearchConfigDTO {
  1: required string provider (api.body="provider")
  2: required bool enabled (api.body="enabled")
  3: required bool hasApiKey (api.body="hasApiKey")
  4: required bool configured (api.body="configured")
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

struct GetAdminModelConfigReq {}

struct AdminModelConfigResp {
  1: required ModelConfigDTO config (api.body="config")
  2: required string status (api.body="status")
}

struct UpdateAdminModelConfigReq {
  1: required string baseUrl (api.body="baseUrl")
  2: required string model (api.body="model")
  3: optional string apiKey (api.body="apiKey")
  4: required bool enabled (api.body="enabled")
  5: required bool clearApiKey (api.body="clearApiKey")
  6: optional i64 contextWindowTokens (api.body="contextWindowTokens")
  7: optional bool webSearchEnabled (api.body="webSearchEnabled")
  8: optional string webSearchApiKey (api.body="webSearchApiKey")
  9: required bool clearWebSearchApiKey (api.body="clearWebSearchApiKey")
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
  AdminModelConfigResp GetAdminModelConfig(1: GetAdminModelConfigReq req) (api.get="/api/v1/admin/model-gateway")
  AdminModelConfigResp UpdateAdminModelConfig(1: UpdateAdminModelConfigReq req) (api.put="/api/v1/admin/model-gateway")
  TestAdminModelConfigResp TestAdminModelConfig(1: TestAdminModelConfigReq req) (api.post="/api/v1/admin/model-gateway/test")
  ChatCallResp GenerateChat(1: ChatCallReq req) (api.post="/internal/v1/chat")
  WebSearchResp WebSearch(1: WebSearchReq req) (api.post="/internal/v1/web/search")
  WebExtractResp WebExtract(1: WebExtractReq req) (api.post="/internal/v1/web/extract")
}

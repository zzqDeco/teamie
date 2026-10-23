namespace go agent


// Public, versioned service contracts. IM services do not import this runtime API.
struct AgentDefinition {
  1: required string id
  2: required string ownerId
  3: required string name
  4: required string description
  5: required list<string> workspaceIds
  6: required bool enabled
  7: required i64 revision
  8: required string createdAt
  9: required string updatedAt
  10: optional string deletedAt
}
struct Ack { 1: required bool ok }
struct CancelRequest {
  1: required string serviceToken
  2: required string agentId
  3: required string runId
}
struct PingRequest { 1: optional string serviceToken }
struct PingResponse {
  1: required bool ready
  2: optional bool configured
  3: optional bool enabled
  4: optional i64 contextWindowTokens
  5: optional string modelConfigId
}


struct AgentListRequest { 1: required string serviceToken }
struct AgentListResponse { 1: required list<AgentDefinition> agents }
struct AgentRequest { 1: required string serviceToken, 2: required string agentId }
struct AgentResponse { 1: optional AgentDefinition agent }
struct RuntimeReport {
  1: required string serviceToken
  2: required string agentId
  3: required string runtimeState
  4: required string sessionId
  5: optional string lastActiveAt
  6: optional string lastError
}
service AgentControlService {
  AgentListResponse ListAgents(1: AgentListRequest request)
  AgentResponse GetAgent(1: AgentRequest request)
  Ack ReportRuntime(1: RuntimeReport request)
  PingResponse Ping(1: PingRequest request)
}


// Runtime internals remain opaque to IM and Control Plane persistence.
struct RuntimeRequest {
  1: required string serviceToken
  2: required string agentId
  3: required string ownerId
  4: optional string resourceId
  5: optional string cursor
  6: optional i32 limit
}
struct RuntimeResponse {
  1: required bool ok
  2: optional string dataJson
  3: optional string errorCode
  4: optional string errorMessage
  5: optional i32 statusCode
}
service AgentRuntimeService {
  RuntimeResponse State(1: RuntimeRequest request)
  RuntimeResponse Runs(1: RuntimeRequest request)
  RuntimeResponse Run(1: RuntimeRequest request)
  RuntimeResponse Tasks(1: RuntimeRequest request)
  RuntimeResponse Task(1: RuntimeRequest request)
  RuntimeResponse Events(1: RuntimeRequest request)
  RuntimeResponse CancelRun(1: RuntimeRequest request)
  RuntimeResponse CancelTask(1: RuntimeRequest request)
  RuntimeResponse RetryRun(1: RuntimeRequest request)
}


struct GatewayChatRequest {
  1: required string serviceToken
  2: required string ownerId
  3: required string agentId
  4: required string sessionId
  5: required string runId
  6: required string requestJson
}
struct GatewayChatResponse {
  1: required bool ok
  2: optional string responseJson
  3: optional string errorCode
  4: optional string errorMessage
  5: optional i32 statusCode
  6: optional string modelConfigId
}
service ModelGatewayService {
  GatewayChatResponse Chat(1: GatewayChatRequest request)
  Ack Cancel(1: CancelRequest request)
  PingResponse Ping(1: PingRequest request)
}


struct SandboxExecRequest {
  1: required string serviceToken
  2: required string agentId
  3: required string runId
  4: required string requestId
  5: required string command
  6: required i32 timeoutSeconds
  // Ephemeral attempt identity for cancellation; runId/requestId remain durable.
  7: optional string executionId
}
struct SandboxExecResponse {
  1: required bool ok
  2: required i32 exitCode
  3: required string stdout
  4: required string stderr
  5: required bool truncated
  6: optional string errorCode
  7: optional string errorMessage
}
// A task ID is a durable logical command identity; transport retries never rerun it.
struct SandboxStartRequest {
  1: required string serviceToken
  2: required string agentId
  3: required string runId
  4: required string taskId
  5: required string command
  // Zero/omitted has no command deadline. RPC timeouts never stop the command.
  6: optional i32 timeoutSeconds
  // Omitted/0 preserves legacy bounded capture. 1 uses durable paged output.
  7: optional i32 outputVersion
}
struct SandboxTaskRequest {
  1: required string serviceToken
  2: required string agentId
  3: required string taskId
  4: optional i64 offset
  5: optional i32 limit
}
struct SandboxTaskResponse {
  1: required bool ok
  2: required string taskId
  3: required string state
  4: optional i32 exitCode
  5: optional string stdout
  6: optional string stderr
  7: optional bool truncated
  8: optional i64 nextOffset
  9: optional string errorCode
  10: optional string errorMessage
  11: optional i64 outputBytes
  12: optional i64 outputLimitBytes
  13: optional string outputStatus
  14: optional bool hasMore
  15: optional string outputSha256
  // Output retention is separate from the immutable task result/receipt.
  16: optional string outputExpiresAt
  17: optional i64 outputRetentionSeconds
  18: optional string outputAvailability
}
struct SandboxWorkspaceRequest {
  1: required string serviceToken
  2: required string agentId
  3: required string operation
  4: required string argsJson
  5: optional string requestId
}
struct SandboxWorkspaceResponse {
  1: required bool ok
  2: optional string dataJson
  3: optional string errorCode
  4: optional string errorMessage
}
// Explicit binary import. Content never uses the text Workspace API or enters
// a model observation. The service validates sha256, confines path and persists
// a stable request receipt before atomically publishing a new file.
struct SandboxImportRequest {
  1: required string serviceToken
  2: required string agentId
  3: required string runId
  4: required string requestId
  5: required string path
  6: required binary content
  7: required string sha256
}
// A bounded binary read, not execution or a model observation. Runtime alone
// transfers these bytes to ordinary IM upload APIs under its own principal.
struct SandboxExportRequest {
  1: required string serviceToken
  2: required string agentId
  3: required string path
}
struct SandboxExportResponse {
  1: required bool ok
  2: optional binary content
  3: optional string sha256
  4: optional string errorCode
}
service SandboxService {
  SandboxExecResponse Exec(1: SandboxExecRequest request)
  Ack Cancel(1: CancelRequest request)
  PingResponse Ping(1: PingRequest request)
  SandboxTaskResponse Start(1: SandboxStartRequest request)
  SandboxTaskResponse Get(1: SandboxTaskRequest request)
  SandboxTaskResponse ReadOutput(1: SandboxTaskRequest request)
  SandboxTaskResponse CancelTask(1: SandboxTaskRequest request)
  SandboxWorkspaceResponse Workspace(1: SandboxWorkspaceRequest request)
  SandboxWorkspaceResponse ImportFile(1: SandboxImportRequest request)
  SandboxExportResponse ExportFile(1: SandboxExportRequest request)
}


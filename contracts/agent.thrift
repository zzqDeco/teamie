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
}
service AgentControlService {
  AgentListResponse ListAgents(1: AgentListRequest request)
  AgentResponse GetAgent(1: AgentRequest request)
  Ack ReportRuntime(1: RuntimeReport request)
  PingResponse Ping(1: PingRequest request)
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
service SandboxService {
  SandboxExecResponse Exec(1: SandboxExecRequest request)
  Ack Cancel(1: CancelRequest request)
  PingResponse Ping(1: PingRequest request)
}

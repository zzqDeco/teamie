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
  11: optional string modelId
}
struct Ack { 1: required bool ok }
struct CancelRequest {
  1: required string serviceToken
  2: required string agentId
  3: required string runId
}
struct PingRequest {
  1: optional string serviceToken
  2: optional string modelId
}
struct PingResponse {
  1: required bool ready
  2: optional bool configured
  3: optional bool enabled
  4: optional i64 contextWindowTokens
  5: optional string modelConfigId
}

namespace go agent.control

// Public HTTP wire contract for the Teamie Agent Control Plane service.
// Browser responses expose only the 10-field public profile and never leak
// internal runtime state, session IDs, revisions, or unrequested workspace IDs.

struct PublicAgentDTO {
  1: required string id (api.body="id")
  2: required string principalId (api.body="principalId")
  3: required string ownerId (api.body="ownerId")
  4: required string ownerName (api.body="ownerName")
  5: required string name (api.body="name")
  6: required string description (api.body="description")
  7: required list<string> workspaceIds (api.body="workspaceIds")
  8: required bool enabled (api.body="enabled")
  9: required string createdAt (api.body="createdAt")
  10: required string updatedAt (api.body="updatedAt")
  11: optional string modelId (api.body="modelId")
}

struct ListOwnedAgentsReq {}

struct AgentListResp {
  1: required list<PublicAgentDTO> agents (api.body="agents")
}

struct CreateAgentReq {
  1: optional string name (api.body="name")
  2: optional string description (api.body="description")
  3: optional list<string> workspaceIds (api.body="workspaceIds")
  4: optional bool enabled (api.body="enabled")
  5: optional string modelId (api.body="modelId")
}

struct GetAgentProfileReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
}

struct UpdateAgentReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: optional string name (api.body="name")
  3: optional string description (api.body="description")
  4: optional list<string> workspaceIds (api.body="workspaceIds")
  5: optional bool enabled (api.body="enabled")
  6: optional string modelId (api.body="modelId")
}

struct DeleteAgentReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
}

struct ListWorkspaceAgentsReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
}

struct AgentResp {
  1: required PublicAgentDTO agent (api.body="agent")
}

struct EmptyResp {}

service AgentControlHTTPService {
  AgentListResp ListOwnedAgents(1: ListOwnedAgentsReq req) (api.get="/api/v1/agents")
  AgentResp CreateAgent(1: CreateAgentReq req) (api.post="/api/v1/agents", api.status="201")
  AgentResp GetAgentProfile(1: GetAgentProfileReq req) (api.get="/api/v1/agents/:id")
  AgentResp UpdateAgent(1: UpdateAgentReq req) (api.patch="/api/v1/agents/:id")
  EmptyResp DeleteAgent(1: DeleteAgentReq req) (api.delete="/api/v1/agents/:id")
  AgentListResp ListWorkspaceAgents(1: ListWorkspaceAgentsReq req) (api.get="/api/v1/workspaces/:id/agents")
}

namespace go agent

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

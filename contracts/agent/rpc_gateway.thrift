namespace go agent

struct GatewayChatRequest {
  1: required string serviceToken
  2: required string ownerId
  3: required string agentId
  4: required string sessionId
  5: required string runId
  6: required string requestJson
  7: optional string modelId
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

namespace go agent

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

namespace go im.realtime

// Public WebSocket and HTTP wire contract for the Teamie Realtime service.
// Kept independent from any Agent runtime or Eino types.

struct ConnectWSReq {
  1: optional string origin (api.header="Origin")
}

struct RealtimeClientFrame {
  1: required string type (api.body="type")
  2: optional string conversationId (api.body="conversationId")
  3: optional bool isTyping (api.body="isTyping")
}

struct RealtimeServerFrame {
  1: required string type (api.body="type")
  2: optional string conversationId (api.body="conversationId")
  3: optional string userId (api.body="userId")
  4: optional string displayName (api.body="displayName")
  5: optional bool isTyping (api.body="isTyping")
}

struct PublishTypingReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: optional bool isTyping (api.body="isTyping")
}

struct EmptyResp {}

service RealtimeWSService {
  EmptyResp ConnectWS(1: ConnectWSReq req) (api.get="/api/v1/ws")
  EmptyResp PublishTyping(1: PublishTypingReq req) (api.post="/api/v1/conversations/:id/typing")
}

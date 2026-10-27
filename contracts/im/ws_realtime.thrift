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
  4: optional string state (api.body="state")
}

struct RealtimeServerFrame {
  1: required string type (api.body="type")
  2: optional string conversationId (api.body="conversationId")
  3: optional string userId (api.body="userId")
  4: optional string displayName (api.body="displayName")
  5: optional bool isTyping (api.body="isTyping")
  6: optional string state (api.body="state")
}

struct PublishTypingReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: optional bool isTyping (api.body="isTyping")
}

struct PresenceItemDTO {
  1: required string userId (api.body="userId")
  2: required string state (api.body="state")
}

struct GetWorkspacePresenceReq {
  1: required string id (api.path="id")
}

struct WorkspacePresenceResp {
  1: required list<PresenceItemDTO> items (api.body="items")
}

struct EmptyResp {}

service RealtimeWSService {
  EmptyResp ConnectWS(1: ConnectWSReq req) (api.get="/api/v1/ws")
  EmptyResp PublishTyping(1: PublishTypingReq req) (api.post="/api/v1/conversations/:id/typing")
  WorkspacePresenceResp GetWorkspacePresence(1: GetWorkspacePresenceReq req) (api.get="/api/v1/workspaces/:id/presence")
}

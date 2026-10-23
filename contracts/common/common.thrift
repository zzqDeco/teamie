namespace go common

struct ErrorPayload {
    1: required string code (api.body="code", go.tag="json:\"code\"")
    2: required string message (api.body="message", go.tag="json:\"message\"")
}

struct ErrorEnvelope {
    1: required ErrorPayload error (api.body="error", go.tag="json:\"error\"")
}

struct PageMeta {
    1: optional string nextCursor (api.body="nextCursor", go.tag="json:\"nextCursor,omitempty\"")
    2: optional bool hasMore (api.body="hasMore", go.tag="json:\"hasMore,omitempty\"")
}

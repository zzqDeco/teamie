// Package modeltransport describes the private runtime/gateway envelope. It has
// no Eino dependency: independent IM services never import model/runtime types.
package modeltransport

import "encoding/json"

const Version = "teamie-agentic-chat-v1"

const GeneratePath = "/internal/v1/chat"
const StreamPath = GeneratePath + "/stream"

// A serialized native response is limited to 2 MiB. JSON-encoding it as the
// responseJson string can double its size; attribution/error fields add 4 KiB.
const MaxHTTPResponseBytes = 2*(2<<20) + 4096

// CallRequest carries attribution for either private HTTP generation mode.
// The service credential belongs only in the Authorization header.
type CallRequest struct {
	OwnerID     string `json:"ownerId"`
	AgentID     string `json:"agentId"`
	SessionID   string `json:"sessionId"`
	RunID       string `json:"runId"`
	RequestJSON string `json:"requestJson"`
}

// Messages use Eino's lossless HumanReadableSerializer; Options contain common
// Eino model options only. The gateway alone constructs the provider request.
type Request struct {
	Protocol string          `json:"protocol"`
	Messages json.RawMessage `json:"messages"`
	Options  json.RawMessage `json:"options"`
	Stream   bool            `json:"stream"`
}

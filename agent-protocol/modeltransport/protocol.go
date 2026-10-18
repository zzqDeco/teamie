// Package modeltransport describes the private runtime/gateway envelope. It has
// no Eino dependency: independent IM services never import model/runtime types.
package modeltransport

import "encoding/json"

const Version = "teamie-agentic-chat-v1"

const GeneratePath = "/internal/v1/chat"
const StreamPath = GeneratePath + "/stream"

// These private transport ceilings accommodate million-token model windows.
// Token admission remains the runtime's frozen per-Run policy. Streaming wire
// overhead is separate from the size of one reconstructed native response.
const MaxRequestBytes = 16 << 20
const MaxResponseBytes = 8 << 20
const MaxOutputTokens = 131072
const LegacyMaxInputMessages = 128
const MaxInputMessages = 65536
const MaxProviderStreamBytes = 128 << 20
const MaxHTTPStreamBytes = 256 << 20
const MaxHTTPResponseBytes = 2*MaxResponseBytes + 4096
const MaxHTTPStreamFrameBytes = MaxHTTPResponseBytes

// CallRequest carries attribution for either private HTTP generation mode.
// The service credential belongs only in the Authorization header.
type CallRequest struct {
	OwnerID     string `json:"ownerId"`
	AgentID     string `json:"agentId"`
	SessionID   string `json:"sessionId"`
	RunID       string `json:"runId"`
	RequestJSON string `json:"requestJson"`
	// Zero preserves historical admission. New Runs explicitly carry their
	// frozen input-message ceiling outside the hashed native request.
	MessageLimit int `json:"messageLimit,omitempty"`
}

func (r CallRequest) InputMessageLimit() int {
	if r.MessageLimit == 0 {
		return LegacyMaxInputMessages
	}
	return r.MessageLimit
}

// Messages use Eino's lossless HumanReadableSerializer; Options contain common
// Eino model options only. The gateway alone constructs the provider request.
type Request struct {
	Protocol string          `json:"protocol"`
	Messages json.RawMessage `json:"messages"`
	Options  json.RawMessage `json:"options"`
	Stream   bool            `json:"stream"`
}

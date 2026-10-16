// Package modeltransport describes the private runtime/gateway envelope. It has
// no Eino dependency: independent IM services never import model/runtime types.
package modeltransport

import "encoding/json"

const Version = "teamie-agentic-chat-v1"

// Messages use Eino's lossless HumanReadableSerializer; Options contain common
// Eino model options only. The gateway alone constructs the provider request.
type Request struct {
	Protocol string          `json:"protocol"`
	Messages json.RawMessage `json:"messages"`
	Options  json.RawMessage `json:"options"`
	Stream   bool            `json:"stream"`
}

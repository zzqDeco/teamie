package modeltransport

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageAdmissionDefaultsPreserveHistoricalEnvelope(t *testing.T) {
	call := CallRequest{OwnerID: "owner", AgentID: "agent", SessionID: "session", RunID: "run", RequestJSON: "{}"}
	raw, err := json.Marshal(call)
	if err != nil || strings.Contains(string(raw), "messageLimit") || call.InputMessageLimit() != 128 {
		t.Fatal("historical transport acquired a new field or message ceiling", err)
	}
	call.MessageLimit = MaxInputMessages
	raw, err = json.Marshal(call)
	if err != nil || !strings.Contains(string(raw), `"messageLimit":65536`) || call.InputMessageLimit() != MaxInputMessages {
		t.Fatal("new Run did not declare its extended admission", err)
	}
}

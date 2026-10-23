package gateway_test

import (
	"encoding/json"
	"strings"
	"testing"

	"teamie/contracts/gen/agent/modeltransport"
)

func TestMessageAdmissionDefaultsPreserveHistoricalEnvelope(t *testing.T) {
	call := modeltransport.CallRequest{OwnerID: "owner", AgentID: "agent", SessionID: "session", RunID: "run", RequestJSON: "{}"}
	raw, err := json.Marshal(call)
	if err != nil || strings.Contains(string(raw), "messageLimit") || call.InputMessageLimit() != 128 {
		t.Fatal("historical transport acquired a new field or message ceiling", err)
	}
	call.MessageLimit = modeltransport.MaxInputMessages
	raw, err = json.Marshal(call)
	if err != nil || !strings.Contains(string(raw), `"messageLimit":65536`) || call.InputMessageLimit() != modeltransport.MaxInputMessages {
		t.Fatal("new Run did not declare its extended admission", err)
	}
}

func TestCountingConfigIDNormalizesAndStaysOpaque(t *testing.T) {
	if modeltransport.CountingConfigID("", "gpt-5.4") != "" || modeltransport.CountingConfigID("https://provider.example/v1", "") != "" {
		t.Fatal("incomplete gateway config produced a counting config ID")
	}
	idA := modeltransport.CountingConfigID("https://provider-a.example/v1", "model-a")
	idASlash := modeltransport.CountingConfigID("  https://provider-a.example/v1/// ", " model-a ")
	if idA == "" || idA != idASlash || !strings.HasPrefix(idA, "modelcfg:v1:") {
		t.Fatalf("unexpected canonical CountingConfigID: %q vs %q", idA, idASlash)
	}
	if strings.Contains(idA, "provider-a") || strings.Contains(idA, "model-a") {
		t.Fatalf("CountingConfigID leaked raw URL or model name: %q", idA)
	}
	idBModel := modeltransport.CountingConfigID("https://provider-a.example/v1", "model-b")
	idBURL := modeltransport.CountingConfigID("https://provider-b.example/v1", "model-a")
	if idBModel == idA || idBURL == idA || idBModel == idBURL {
		t.Fatalf("CountingConfigID did not distinguish model or baseURL changes: %q %q %q", idA, idBModel, idBURL)
	}
}

func TestWebTransportEnvelopeSerialization(t *testing.T) {
	if modeltransport.WebSearchPath != "/internal/v1/web/search" || modeltransport.WebExtractPath != "/internal/v1/web/extract" {
		t.Fatalf("unexpected web paths: %s %s", modeltransport.WebSearchPath, modeltransport.WebExtractPath)
	}
	searchReq := modeltransport.WebSearchRequest{OwnerID: "o", AgentID: "a", SessionID: "s", RunID: "r", Query: "teamie"}
	raw, err := json.Marshal(searchReq)
	if err != nil || strings.Contains(string(raw), "includeDomains") {
		t.Fatalf("unexpected search request JSON: %s %v", raw, err)
	}
	extractRes := modeltransport.WebExtractResponse{OK: true, Results: []modeltransport.WebExtractResult{{URL: "https://example.com", Content: "# Title"}}}
	raw, err = json.Marshal(extractRes)
	if err != nil || strings.Contains(string(raw), "failedResults") {
		t.Fatalf("unexpected extract response JSON: %s %v", raw, err)
	}
}

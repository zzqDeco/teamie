package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"teamie/contracts/gen/agent/kitex_gen/agent"
)

// The native Runtime manifest grew beyond 32 tools when DM/group/schedule tools
// were enabled. Exercise the real AgenticClient and policy, not a gateway mock.
func TestExpandedToolManifestReachesProvider(t *testing.T) {
	for _, size := range []int{33, 128, 129} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			var calls atomic.Int32
			fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var request struct {
					Tools []json.RawMessage `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Tools) != size {
					t.Errorf("tool manifest was lost: count=%d error=%v", len(request.Tools), err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"fixture","object":"chat.completion","created":1,"model":"configured","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":1,"total_tokens":13}}`))
			}))
			defer fixture.Close()
			crypto := testCipher(t)
			key, err := crypto.Encrypt("fixture-key-not-a-provider-secret")
			if err != nil {
				t.Fatal(err)
			}
			s := New(&memoryStore{c: singleModel(ModelProfile{BaseURL: fixture.URL + "/v1", Model: "configured", CipherKey: key, Enabled: true})}, crypto, testToken)
			s.Provider = Provider{HTTP: fixture.Client()}
			tools := make([]any, size)
			for i := range tools {
				tools[i] = map[string]any{"type": "function", "function": map[string]any{"name": fmt.Sprintf("runtime_tool_%d", i), "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}
			}
			raw, err := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hello"}}, "tools": tools})
			if err != nil {
				t.Fatal(err)
			}
			response, err := fixtureHTTPChat(t, s, context.Background(), &agent.GatewayChatRequest{ServiceToken: testToken, OwnerId: "owner", AgentId: "agent", SessionId: "session", RunId: "run", RequestJson: fixtureNativeRequest(string(raw), false)})
			if err != nil {
				t.Fatal(err)
			}
			if size <= 128 {
				if !response.Ok || calls.Load() != 1 {
					t.Fatalf("valid expanded manifest rejected: code=%s message=%s calls=%d", response.GetErrorCode(), response.GetErrorMessage(), calls.Load())
				}
			} else if response.Ok || response.GetErrorCode() != "invalid_request" || calls.Load() != 0 {
				t.Fatalf("oversized manifest reached provider: response=%v calls=%d", response, calls.Load())
			}
		})
	}
}

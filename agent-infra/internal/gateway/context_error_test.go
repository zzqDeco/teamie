package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderContextErrorRequiresStructuredRecognizedFailure(t *testing.T) {
	for _, test := range []struct {
		body string
		want string
	}{
		{`{"error":{"code":"context_length_exceeded","message":"fixture-secret input"}}`, "context_overflow"},
		{`{"error":{"code":"InvalidParameter","message":"Range of input length should be [1, 129024]"}}`, "context_overflow"},
		{`{"error":{"code":"InternalError.Algo.InvalidParameter","message":"Range of input length should be [1, 8192]"}}`, "context_overflow"},
		{`{"error":{"code":"InvalidParameter","message":"Range of max_tokens should be [1, 4096]"}}`, ""},
		{`{"error":{"code":"invalid_api_key","message":"context_length_exceeded"}}`, ""},
		{`{"error":{"code":400,"message":"context_length_exceeded"}}`, ""},
		{`{"message":"context_length_exceeded"}`, ""},
		{`{"choices":[{"message":{"content":"context_length_exceeded"}}]}`, ""},
		{`{"error":{"code":"InvalidParameter","message":"quoted Range of input length should be [1, 8192]"}}`, ""},
		{strings.Repeat("x", 16385), ""},
	} {
		err := providerContextError([]byte(test.body))
		if guardErrorCode(err) != test.want {
			t.Fatalf("classification=%s want=%s", guardErrorCode(err), test.want)
		}
		if err != nil && strings.Contains(err.Error(), "fixture-secret") {
			t.Fatal("provider text escaped the classification boundary")
		}
	}
}

func TestOfficialProviderContextFailureUnaryAndStreaming(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, status := range []int{400, 413, 200} {
			for _, sse := range []bool{false, true} {
				if sse && (!streaming || status != 200) {
					continue
				}
				t.Run(fmt.Sprintf("stream=%t/status=%d/sse=%t", streaming, status, sse), func(t *testing.T) {
					fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body := `{"error":{"code":"context_length_exceeded","message":"fixture-secret context detail"}}`
						if sse {
							w.Header().Set("Content-Type", "text/event-stream")
							body = "data: " + body + "\n\n"
						} else {
							w.Header().Set("Content-Type", "application/json")
						}
						w.WriteHeader(status)
						fmt.Fprint(w, body)
					}))
					defer fixture.Close()
					provider := Provider{fixture.Client()}
					config := Config{BaseURL: fixture.URL + "/v1", Model: "fixture"}
					var err error
					raw := fixtureNativeRequest(`{"messages":[{"role":"user","content":"fixture"}]}`, streaming)
					if streaming {
						_, _, err = provider.Stream(context.Background(), config, "fixture-secret", raw, func(string) error {
							t.Fatal("failure became a model chunk")
							return nil
						})
					} else {
						_, _, _, err = provider.Chat(context.Background(), config, "fixture-secret", raw)
					}
					if guardErrorCode(err) != "context_overflow" || strings.Contains(err.Error(), "fixture-secret") {
						t.Fatal("context failure lost sanitization/classification", err)
					}
				})
			}
		}
	}
}

func TestContextErrorDoesNotPromoteOtherHTTPFailures(t *testing.T) {
	for _, status := range []int{401, 429, 500} {
		fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "context_length_exceeded"}})
		}))
		_, _, _, err := (Provider{fixture.Client()}).Chat(context.Background(), Config{BaseURL: fixture.URL + "/v1", Model: "fixture"}, "fixture-key", fixtureNativeRequest(`{"messages":[{"role":"user","content":"fixture"}]}`, false))
		fixture.Close()
		if err == nil || guardErrorCode(err) == "context_overflow" {
			t.Fatal("unrelated HTTP failure triggered compaction", status, err)
		}
	}
}

package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestOfficialQwenSummaryDisablesThinkingWithoutChangingOrdinaryTurns(t *testing.T) {
	for _, test := range []struct {
		name, model string
		choice      schema.ToolChoice
		tools       bool
		disabled    bool
	}{
		{"summary", "qwen3.8-max", schema.ToolChoiceForbidden, false, true},
		{"snapshot summary", "qwen3.8-max-0902", schema.ToolChoiceForbidden, false, true},
		{"ordinary turn", "qwen3.8-max", schema.ToolChoiceAllowed, true, false},
		{"forbidden with manifest", "qwen3.8-max", schema.ToolChoiceForbidden, true, false},
		{"other provider", "other-model", schema.ToolChoiceForbidden, false, false},
		{"thinking-only model", "qwen3.8-2.4t-a95b", schema.ToolChoiceForbidden, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			observed := make(chan map[string]json.RawMessage, 1)
			fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-summary-key" {
					t.Error("official adapter lost configured endpoint or credential")
				}
				var request map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				observed <- request
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"summary-wire-fixture","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic summary"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`)
			}))
			defer fixture.Close()
			opts := model.Options{AgenticToolChoice: &schema.AgenticToolChoice{Type: test.choice}}
			if test.tools {
				opts.Tools = []*schema.ToolInfo{{Name: "im_send_message"}}
			}
			raw := nativeRequest(t, []*schema.AgenticMessage{schema.UserAgenticMessage("synthetic private input")}, opts, false)
			if _, _, _, err := (Provider{fixture.Client()}).Chat(context.Background(), Config{BaseURL: fixture.URL + "/v1", Model: test.model}, "synthetic-summary-key", raw); err != nil {
				t.Fatal(err)
			}
			request := <-observed
			thinking, exists := request["enable_thinking"]
			if exists != test.disabled || exists && string(thinking) != "false" {
				t.Fatal("summary-only thinking policy did not reach the top-level official wire")
			}
			if request["extra_body"] != nil || request["thinking_budget"] != nil || request["reasoning_effort"] != nil || string(request["max_completion_tokens"]) != "2048" || string(request["parallel_tool_calls"]) != "false" || string(request["n"]) != "1" {
				t.Fatal("thinking policy changed existing output limits or request guards")
			}
			if test.disabled && (string(request["tool_choice"]) != `"none"` || request["tools"] != nil && string(request["tools"]) != "[]") {
				t.Fatal("summary acquired tool access")
			}
		})
	}
}

func TestThinkingBoundaryAcceptsOnlyBoolean(t *testing.T) {
	for _, value := range []string{"true", "false", "null", `"false"`, "0", "{}", "[]"} {
		raw := fmt.Sprintf(`{"messages":[{"role":"user","content":"fixture"}],"enable_thinking":%s}`, value)
		normalized, err := normalizeRequest(raw, "qwen3.8-max")
		valid := value == "true" || value == "false"
		if (err == nil) != valid || valid && !strings.Contains(string(normalized), `"enable_thinking":`+value) {
			t.Fatalf("thinking option %s: accepted=%v want=%v", value, err == nil, valid)
		}
	}
}

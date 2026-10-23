package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go.opentelemetry.io/otel/propagation"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	rpcx "teamie/services/llm-gateway/internal/infra/rpcclient"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/zap"
	"teamie/pkg/observability"
)

type gatewayLogBuffer struct {
	mu    sync.Mutex
	value bytes.Buffer
}

func (b *gatewayLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.value.Write(p)
}
func (b *gatewayLogBuffer) snapshot() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.value.String()
}

func TestProviderCompletionLogsAreCorrelatedAndContentFree(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprint(broken), func(t *testing.T) {
			var logs gatewayLogBuffer
			undo := zap.ReplaceGlobals(observability.New(observability.Config{Service: "teamie.llm-gateway", Output: &logs}))
			t.Cleanup(undo)
			previous := otel.GetTracerProvider()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
			otel.SetTracerProvider(provider)
			t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })
			service, direct := streamFixtureService(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if broken {
					_, _ = io.WriteString(w, "data: private-invalid-provider-chunk\n\n")
					return
				}
				_, _ = io.WriteString(w, "data: "+strings.ReplaceAll(delta, "one", "private-output-text")+"\n\ndata: "+finalDelta+"\n\ndata: [DONE]\n\n")
			})
			direct.Close()
			server := httptest.NewServer(observability.HTTP(service.Routes(rpcx.AdminAuth{})))
			defer server.Close()
			request := streamRequest(t, context.Background(), server.URL, "diagnostic-run:diagnostic-attempt")
			request.Header.Set("traceparent", "00-1234567890abcdef1234567890abcdef-0123456789abcdef-00")
			request = request.WithContext(observability.Propagator().Extract(request.Context(), propagation.HeaderCarrier(request.Header)))
			response, err := observability.Client(nil, "teamie.llm-gateway").Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			count := 0
			for _, line := range strings.Split(logs.snapshot(), "\n") {
				var event map[string]any
				if json.Unmarshal([]byte(line), &event) != nil || event["event.action"] != "model-provider-call" {
					continue
				}
				count++
				if event["custom.runtime.run_id"] != "diagnostic-run" || event["custom.runtime.attempt_id"] != "diagnostic-attempt" || event["trace.id"] != "1234567890abcdef1234567890abcdef" || event["event.duration"] == nil {
					t.Fatal("provider attribution missing", event)
				}
				want := "success"
				wantStage := "terminal-write"
				if broken {
					want = "failure"
					wantStage = "provider-stream"
				}
				if event["event.outcome"] != want {
					t.Fatal("provider outcome incorrect", event)
				}
				if event["custom.model.failure_stage"] != wantStage {
					t.Fatal("provider failure stage overwritten by later audit/terminal work", event)
				}
			}
			if count != 1 {
				t.Fatalf("provider completion count %d", count)
			}
			for _, private := range []string{"private-output-text", "private-invalid-provider-chunk", "fixture-provider-stream-secret", testToken} {
				if strings.Contains(logs.snapshot(), private) {
					t.Fatalf("provider credential/content leaked: %s", private)
				}
			}
		})
	}
}

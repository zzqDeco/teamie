package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cloudwego/eino/schema"
)

type lifetimeTransport func(*http.Request) (*http.Response, error)

func (f lifetimeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func lifetimeChunk(t *testing.T, output io.Writer, text string) error {
	t.Helper()
	message := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlockChunk(&schema.AssistantGenText{Text: text}, &schema.StreamingMeta{Index: 0})}}
	raw, err := (&schema.HumanReadableSerializer{}).Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return json.NewEncoder(output).Encode(map[string]string{"type": "chunk", "responseJson": string(raw)})
}

func TestNativeActiveStreamExceedsFormerTwoMinuteBudget(t *testing.T) {
	s, lease, run := nativeModelRun(t)
	// Only transport time is virtual. SQL setup/fencing and the complete model
	// receipt still use the disposable real PostgreSQL database.
	synctest.Test(t, func(t *testing.T) {
		client := &http.Client{Transport: lifetimeTransport(func(r *http.Request) (*http.Response, error) {
			if _, ok := r.Context().Deadline(); ok {
				t.Fatal("runtime added a model-stream deadline")
			}
			reader, writer := io.Pipe()
			stop := context.AfterFunc(r.Context(), func() { _ = writer.CloseWithError(r.Context().Err()) })
			go func() {
				defer stop()
				defer writer.Close()
				for i := 0; i < 9; i++ {
					select {
					case <-time.After(time.Minute):
					case <-r.Context().Done():
						return
					}
					if lifetimeChunk(t, writer, "complete ") != nil {
						return
					}
				}
				_ = json.NewEncoder(writer).Encode(map[string]string{"type": "done"})
			}()
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-ndjson"}}, Body: reader, Request: r}, nil
		})}
		gateway := &GatewayModel{Store: s, Lease: lease, AgentID: lease.AgentID, StreamURL: "http://gateway.example/internal/v1/chat/stream", HTTP: client}
		start := time.Now()
		reader, err := gateway.Stream(WithRun(context.Background(), run), inputMessages(run.Inputs))
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		var chunks []*schema.AgenticMessage
		for {
			chunk, err := reader.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			chunks = append(chunks, chunk)
		}
		result, err := schema.ConcatAgenticMessages(chunks)
		if err != nil || time.Since(start) != 9*time.Minute || agenticText(result) != strings.Repeat("complete ", 9) {
			t.Fatalf("active native stream was truncated after %s: %v", time.Since(start), err)
		}
	})
	var raw []byte
	if err := s.DB.QueryRow(context.Background(), `SELECT response FROM runtime_model_calls WHERE run_id=$1`, run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	cached, err := decodeAgenticMessage(raw)
	if err != nil || agenticText(cached) != strings.Repeat("complete ", 9) {
		t.Fatal("long stream did not persist its complete receipt", err)
	}
}

func TestNativeStreamWaitsForDelayedGatewayHeaders(t *testing.T) {
	if testing.Short() {
		t.Skip("real 16s gateway header-delay regression")
	}
	s, lease, run := nativeModelRun(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-time.After(16 * time.Second):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_ = lifetimeChunk(t, w, "complete")
		_ = json.NewEncoder(w).Encode(map[string]string{"type": "done"})
	}))
	defer fixture.Close()
	gateway := &GatewayModel{Store: s, Lease: lease, AgentID: lease.AgentID, StreamURL: fixture.URL}
	reader, err := gateway.Stream(WithRun(context.Background(), run), inputMessages(run.Inputs))
	if err != nil {
		t.Fatal("gateway inference header wait was truncated", err)
	}
	defer reader.Close()
	for {
		_, err := reader.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if count(t, s, `SELECT count(*) FROM runtime_model_calls`) != 1 {
		t.Fatal("complete delayed response lacks its durable receipt")
	}
}

func TestNativeCallerCancelStopsSilentGatewayWithoutReceipt(t *testing.T) {
	for _, headers := range []bool{false, true} {
		t.Run(map[bool]string{false: "waiting_for_headers", true: "silent_body"}[headers], func(t *testing.T) {
			s, lease, run := nativeModelRun(t)
			entered, canceled := make(chan struct{}), make(chan struct{})
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if headers {
					w.Header().Set("Content-Type", "application/x-ndjson")
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				}
				close(entered)
				<-r.Context().Done()
				close(canceled)
			}))
			ctx, cancel := context.WithCancel(WithRun(context.Background(), run))
			defer func() { cancel(); fixture.Close() }()
			gateway := &GatewayModel{Store: s, Lease: lease, AgentID: lease.AgentID, StreamURL: fixture.URL}
			finished := make(chan error, 1)
			go func() {
				reader, err := gateway.Stream(ctx, inputMessages(run.Inputs))
				if err == nil {
					defer reader.Close()
					_, err = reader.Recv()
				}
				finished <- err
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("silent gateway request did not start")
			}
			cancel()
			select {
			case err := <-finished:
				if err == nil || err == io.EOF {
					t.Fatal("canceled model was treated as a complete response")
				}
			case <-time.After(time.Second):
				t.Fatal("caller cancel left the silent model call blocked")
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("caller cancel did not reach the HTTP handler")
			}
			if count(t, s, `SELECT count(*) FROM runtime_model_calls`) != 0 {
				t.Fatal("canceled silent stream persisted a model receipt")
			}
		})
	}
}

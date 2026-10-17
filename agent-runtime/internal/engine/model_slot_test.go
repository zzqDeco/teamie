package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/google/uuid"
)

func modelSlotFixture(t *testing.T) ([2]*GatewayModel, [2]*Run, [2]context.Context, *nativeRichGateway) {
	t.Helper()
	s, firstLease, first := contextModelRun(t)
	ctx := context.Background()
	secondLease, err := s.Acquire(ctx, uuid.NewString(), "second-model-session")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Release(context.Background(), secondLease) })
	inputs := []Input{{EventID: "1", Body: "second independent Session", ConversationID: "second-conversation", MessageID: "second-message"}}
	if err = s.Ingest(ctx, secondLease, "1", inputs); err != nil {
		t.Fatal(err)
	}
	second, err := s.StartRunWithContextPolicy(ctx, secondLease, inputs, first.ContextProfile, first.ContextBudget)
	if err != nil {
		t.Fatal(err)
	}
	slots := make(chan struct{}, 1)
	first.Front, second.Front = &frontSlot{slots: slots}, &frontSlot{slots: slots}
	gateway := &nativeRichGateway{output: assistantText("completed model operation")}
	models := [2]*GatewayModel{{Store: s, Lease: firstLease, AgentID: firstLease.AgentID, Client: gateway}, {Store: s, Lease: secondLease, AgentID: secondLease.AgentID, Client: gateway}}
	return models, [2]*Run{first, second}, [2]context.Context{WithRun(ctx, first), WithRun(ctx, second)}, gateway
}

func TestCompletedModelCallDoesNotHoldAnotherSessionUntilTurnEnd(t *testing.T) {
	models, runs, contexts, gateway := modelSlotFixture(t)
	// The worker reserves the initial slot before preparing its first model.
	if err := runs[0].Front.acquire(contexts[0]); err != nil {
		t.Fatal(err)
	}
	defer runs[0].releaseFront()
	if _, err := models[0].Generate(contexts[0], inputMessages(runs[0].Inputs)); err != nil {
		t.Fatal(err)
	}
	// Deliberately leave the first logical Run active. Tools/history work in
	// that Session must not prevent a different Session's next model request.
	ctx, cancel := context.WithTimeout(contexts[1], 2*time.Second)
	defer cancel()
	if _, err := models[1].Generate(ctx, inputMessages(runs[1].Inputs)); err != nil {
		t.Fatal("completed model call retained the host slot until its whole Turn ended", err)
	}
	if gateway.calls.Load() != 2 {
		t.Fatal("second Session did not reach the gateway")
	}
}

func TestModelSlotReleasesCachedAndRejectedCalls(t *testing.T) {
	for _, mode := range []string{"cached_generate", "invalid_generate", "missing_stream_gateway"} {
		t.Run(mode, func(t *testing.T) {
			models, runs, contexts, gateway := modelSlotFixture(t)
			if err := runs[0].Front.acquire(contexts[0]); err != nil {
				t.Fatal(err)
			}
			defer runs[0].releaseFront()
			switch mode {
			case "cached_generate":
				if _, err := models[0].Generate(contexts[0], inputMessages(runs[0].Inputs)); err != nil {
					t.Fatal(err)
				}
				runs[0].Step.Store(0)
				if _, err := models[0].Generate(contexts[0], inputMessages(runs[0].Inputs)); err != nil || gateway.calls.Load() != 1 {
					t.Fatal("cached model call repeated upstream work", err)
				}
			case "invalid_generate":
				if _, err := models[0].Generate(contexts[0], inputMessages(runs[0].Inputs), model.WithMaxTokens(0)); err == nil {
					t.Fatal("invalid request reached the gateway")
				}
			case "missing_stream_gateway":
				if _, err := models[0].Stream(contexts[0], inputMessages(runs[0].Inputs)); err == nil {
					t.Fatal("missing stream gateway succeeded")
				}
			}
			ctx, cancel := context.WithTimeout(contexts[1], 2*time.Second)
			defer cancel()
			if _, err := models[1].Generate(ctx, inputMessages(runs[1].Inputs)); err != nil {
				t.Fatal("cache/error path leaked a model permit", err)
			}
		})
	}
}

func TestLiveStreamRetainsPermitUntilCompletionOrConsumerClose(t *testing.T) {
	for _, mode := range []string{"complete", "consumer_close"} {
		t.Run(mode, func(t *testing.T) {
			models, runs, contexts, gateway := modelSlotFixture(t)
			finish := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := lifetimeChunk(t, w, "live model response"); err != nil {
					return
				}
				w.(http.Flusher).Flush()
				select {
				case <-finish:
					_, _ = fmt.Fprintln(w, `{"type":"done"}`)
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			models[0].StreamURL = server.URL
			reader, err := models[0].Stream(contexts[0], inputMessages(runs[0].Inputs))
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			if _, err = reader.Recv(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(contexts[1], 3*time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := models[1].Generate(ctx, inputMessages(runs[1].Inputs)); result <- err }()
			select {
			case err = <-result:
				t.Fatal("another Session bypassed an active stream's model permit", err)
			case <-time.After(100 * time.Millisecond):
			}
			if gateway.calls.Load() != 0 {
				t.Fatal("more than one model request was active")
			}
			if mode == "consumer_close" {
				reader.Close()
			} else {
				close(finish)
				for {
					_, err = reader.Recv()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if err = <-result; err != nil {
				t.Fatal("completed/closed stream did not release its permit", err)
			}
		})
	}
}

func TestModelAuthorizationIsRecheckedAfterWaitingForSlot(t *testing.T) {
	models, runs, contexts, gateway := modelSlotFixture(t)
	if err := runs[0].Front.acquire(contexts[0]); err != nil {
		t.Fatal(err)
	}
	defer runs[0].releaseFront()
	var disabled atomic.Bool
	models[1].Authorize = func(context.Context) error {
		if disabled.Load() {
			return &ModelError{Code: "principal_disabled"}
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(contexts[1], 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := models[1].Generate(ctx, inputMessages(runs[1].Inputs)); result <- err }()
	disabled.Store(true)
	runs[0].releaseFront()
	if err := <-result; SafeError(err) != "principal_disabled" || gateway.calls.Load() != 0 {
		t.Fatal("a disabled queued Agent reached the model gateway", err)
	}
}

func TestCachedStreamDoesNotRetainAProviderPermit(t *testing.T) {
	models, runs, contexts, _ := modelSlotFixture(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = lifetimeChunk(t, w, "cached complete response")
		_, _ = fmt.Fprintln(w, `{"type":"done"}`)
	}))
	defer server.Close()
	models[0].StreamURL = server.URL
	reader, err := models[0].Stream(contexts[0], inputMessages(runs[0].Inputs))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for {
		_, err = reader.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	runs[0].Step.Store(0)
	cached, err := models[0].Stream(contexts[0], inputMessages(runs[0].Inputs))
	if err != nil {
		t.Fatal(err)
	}
	defer cached.Close() // Intentionally unconsumed: there is no live provider.
	ctx, cancel := context.WithTimeout(contexts[1], 2*time.Second)
	defer cancel()
	if _, err = models[1].Generate(ctx, inputMessages(runs[1].Inputs)); err != nil || requests.Load() != 1 {
		t.Fatal("cached stream blocked another Session or repeated upstream", err)
	}
}

func TestOldModelCleanupCannotReleaseANewerCallPermit(t *testing.T) {
	f := &frontSlot{slots: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	first, err := f.modelPermit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first()
	second, err := f.modelPermit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	first() // A delayed stream/retry cleanup is idempotent and call-owned.
	if len(f.slots) != 1 {
		t.Fatal("old cleanup released the newer request's permit")
	}
	f.release() // Finishing the logical Turn must not release a live request.
	if len(f.slots) != 1 {
		t.Fatal("Turn cleanup released a live request's permit")
	}
}

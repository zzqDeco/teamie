package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/backend/kitex_gen/im"
)

type typingEvent struct {
	conversation string
	active       bool
}

func nextTyping(t *testing.T, events <-chan typingEvent, conversation string, active bool) {
	t.Helper()
	select {
	case event := <-events:
		if event.conversation != conversation || event.active != active {
			t.Fatalf("typing = %#v, want %s %v", event, conversation, active)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("missing typing for %s %v", conversation, active)
	}
}

func TestTypingRefreshIsSourceScopedAndStopsBeforeNextTurn(t *testing.T) {
	conversation, another := uuid.NewString(), uuid.NewString()
	events := make(chan typingEvent, 16)
	ticks := make(chan time.Time, 4)
	inputs := []Input{{ConversationID: conversation}, {ConversationID: conversation}, {Kind: "task.notification", ConversationID: another}, {ConversationID: "invalid"}}
	stop := beginTyping(context.Background(), inputs, func(ctx context.Context, id string, active bool) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		events <- typingEvent{id, active}
		return nil
	}, func(context.Context) error { return nil }, ticks)
	nextTyping(t, events, conversation, true)
	ticks <- time.Now()
	nextTyping(t, events, conversation, true)
	stop()
	nextTyping(t, events, conversation, false)
	// A queued old tick cannot come after the cleanup or a subsequent turn.
	ticks <- time.Now()
	stop()
	select {
	case event := <-events:
		t.Fatalf("finished/background/unrelated conversation emitted typing: %#v", event)
	default:
	}
	stopNext := beginTyping(context.Background(), []Input{{ConversationID: another}}, func(_ context.Context, id string, active bool) error {
		events <- typingEvent{id, active}
		return nil
	}, func(context.Context) error { return nil }, make(chan time.Time))
	nextTyping(t, events, another, true)
	stopNext()
	nextTyping(t, events, another, false)
}

func TestTypingCancellationFencesInflightHeartbeatAndStillClears(t *testing.T) {
	conversation := uuid.NewString()
	events := make(chan typingEvent, 8)
	ticks := make(chan time.Time, 1)
	blocked := make(chan struct{})
	var calls atomic.Int32
	stop := beginTyping(context.Background(), []Input{{ConversationID: conversation}}, func(ctx context.Context, id string, active bool) error {
		if active && calls.Add(1) > 1 {
			close(blocked)
			<-ctx.Done()
			return ctx.Err()
		}
		if ctx.Err() != nil {
			t.Error("cleanup reused the canceled turn context")
		}
		events <- typingEvent{id, active}
		return nil
	}, func(context.Context) error { return nil }, ticks)
	nextTyping(t, events, conversation, true)
	ticks <- time.Now()
	<-blocked
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	nextTyping(t, events, conversation, false)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("typing cancellation did not join the in-flight request")
	}
	select {
	case event := <-events:
		t.Fatalf("late heartbeat survived cancellation: %#v", event)
	default:
	}
}

func TestTypingRechecksLeaseRevocationAndRunCancellation(t *testing.T) {
	for _, failure := range []error{ErrLeaseLost, &ModelError{Code: "principal_disabled"}, &ModelError{Code: "run_cancelled"}, &ModelError{Code: "runtime_disabled"}} {
		t.Run(failure.Error(), func(t *testing.T) {
			conversation := uuid.NewString()
			events := make(chan typingEvent, 8)
			ticks := make(chan time.Time, 2)
			var revoked atomic.Bool
			stop := beginTyping(context.Background(), []Input{{ConversationID: conversation}}, func(_ context.Context, id string, active bool) error {
				events <- typingEvent{id, active}
				return nil
			}, func(context.Context) error {
				if revoked.Load() {
					return failure
				}
				return nil
			}, ticks)
			nextTyping(t, events, conversation, true)
			revoked.Store(true)
			ticks <- time.Now()
			nextTyping(t, events, conversation, false)
			stop()
			ticks <- time.Now()
			select {
			case event := <-events:
				t.Fatalf("revoked turn emitted typing: %#v", event)
			default:
			}
		})
	}
}

func TestTypingTransportFailureIsBestEffortAndCleanupIsBounded(t *testing.T) {
	conversation := uuid.NewString()
	events := make(chan typingEvent, 8)
	ticks := make(chan time.Time, 1)
	stop := beginTyping(context.Background(), []Input{{ConversationID: conversation}}, func(ctx context.Context, id string, active bool) error {
		events <- typingEvent{id, active}
		if !active {
			<-ctx.Done()
		}
		return errors.New("transport unavailable")
	}, func(context.Context) error { return nil }, ticks)
	nextTyping(t, events, conversation, true)
	ticks <- time.Now()
	nextTyping(t, events, conversation, true)
	started := time.Now()
	stop()
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("cleanup exceeded its request deadline: %s", elapsed)
	}
	nextTyping(t, events, conversation, false)
}

func TestCanceledIdleAndBackgroundOnlyWorkNeverTypes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		ctx    context.Context
		inputs []Input
	}{
		{ctx, []Input{{ConversationID: uuid.NewString()}}},
		{context.Background(), nil},
		{context.Background(), []Input{{Kind: "task.notification", ConversationID: uuid.NewString()}}},
	} {
		stop := beginTyping(tc.ctx, tc.inputs, func(context.Context, string, bool) error {
			t.Error("idle, canceled or background-only work emitted typing")
			return nil
		}, func(context.Context) error { return nil }, make(chan time.Time))
		stop()
	}
}

type httpTypingFixture struct {
	mu     sync.Mutex
	events []typingEvent
	fail   bool
}

func (f *httpTypingFixture) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) || r.Header.Get("X-Teamie-Service-Key") != "fixture-im-secret" || r.Header.Get("Origin") != "" {
		w.WriteHeader(401)
		return
	}
	var input map[string]any
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) != 6 || parts[5] != "typing" || json.NewDecoder(r.Body).Decode(&input) != nil || len(input) != 1 {
		w.WriteHeader(400)
		return
	}
	active, ok := input["isTyping"].(bool)
	if !ok {
		w.WriteHeader(400)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, typingEvent{parts[4], active})
	if f.fail {
		w.WriteHeader(503)
		return
	}
	w.WriteHeader(204)
}

func (f *httpTypingFixture) snapshot() []typingEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]typingEvent(nil), f.events...)
}

func attachTypingFixture(t *testing.T, e *Engine) *httpTypingFixture {
	t.Helper()
	f := &httpTypingFixture{}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	e.Config.IMRealtimeURL = server.URL
	return f
}

type typingGatewayFixture struct {
	*fixtureGateway
	entered chan struct{}
	release chan struct{}
	fail    bool
}

func (g *typingGatewayFixture) Chat(ctx context.Context, _ *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	select {
	case g.entered <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if g.fail {
		code := "invalid_request"
		return &wire.GatewayChatResponse{Ok: false, ErrorCode: &code}, nil
	}
	raw := `{"choices":[{"message":{"role":"assistant","content":"PRIVATE_INPUT_RESULT"},"finish_reason":"stop"}]}`
	return &wire.GatewayChatResponse{Ok: true, ResponseJson: modelFixtureResponse(raw)}, nil
}

type typingControlFixture struct {
	*fixtureControl
	disabled atomic.Bool
}

type rotatingTypingIdentity struct {
	fixtureIdentity
	issued atomic.Int32
}

func (i *rotatingTypingIdentity) IssuePrincipalToken(context.Context, *im.PrincipalTokenRequest, ...callopt.Option) (*im.PrincipalTokenResponse, error) {
	// Match the real Identity contract: a second issuance replaces the sole
	// current principal token, invalidating the first cached credential.
	token := strings.Repeat(string(rune('a'+i.issued.Add(1)-1)), 64)
	return &im.PrincipalTokenResponse{Ok: true, Token: token, ExpiresAt: time.Now().Add(15 * time.Minute).Format(time.RFC3339Nano)}, nil
}

type blockedTypingIdentity struct {
	rotatingTypingIdentity
	entered chan struct{}
	release chan struct{}
	blocked atomic.Bool
}

func (i *blockedTypingIdentity) IssuePrincipalToken(ctx context.Context, r *im.PrincipalTokenRequest, opts ...callopt.Option) (*im.PrincipalTokenResponse, error) {
	if i.blocked.CompareAndSwap(false, true) {
		close(i.entered)
		<-i.release
	}
	return i.rotatingTypingIdentity.IssuePrincipalToken(ctx, r, opts...)
}

func TestTypingSharesRotatingCredentialsAndCanCancelDuringRefresh(t *testing.T) {
	identity := &blockedTypingIdentity{entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(identity.release) }) }
	defer unblock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	imc := &IM{BaseURL: server.URL, RealtimeURL: server.URL, AgentID: uuid.NewString(), ServiceKey: "fixture-im-secret", Identity: identity}
	first := make(chan error, 1)
	go func() { first <- imc.Call(context.Background(), http.MethodGet, "/lookup", nil, nil) }()
	<-identity.entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- imc.Typing(ctx, uuid.NewString(), false) }()
	select {
	case err := <-finished:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("typing did not respect its credential-wait deadline: %v", err)
		}
	case <-time.After(time.Second):
		unblock()
		t.Fatal("typing cleanup waited on another IM call's credential refresh")
	}
	second := make(chan error, 1)
	go func() { second <- imc.Call(context.Background(), http.MethodGet, "/lookup", nil, nil) }()
	unblock()
	for _, result := range []<-chan error{first, second} {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	if err := imc.Typing(context.Background(), uuid.NewString(), true); err != nil {
		t.Fatal(err)
	}
	if issued := identity.issued.Load(); issued != 1 {
		t.Fatalf("IM and typing rotated competing tokens: %d issuances", issued)
	}
}

func (c *typingControlFixture) GetAgent(_ context.Context, r *wire.AgentRequest, _ ...callopt.Option) (*wire.AgentResponse, error) {
	return &wire.AgentResponse{Agent: &wire.AgentDefinition{Id: r.AgentId, Enabled: !c.disabled.Load(), Revision: 1}}, nil
}

func TestNativeTypingClearsOnCancellationLeaseLossRevocationAndError(t *testing.T) {
	for _, ending := range []string{"cancel", "lease_lost", "disabled", "model_error", "typing_unavailable"} {
		t.Run(ending, func(t *testing.T) {
			s := testStore(t)
			e, f, base, control := fixtureEngine(t, s)
			identity := &rotatingTypingIdentity{}
			e.Identity = identity
			typing := attachTypingFixture(t, e)
			typing.fail = ending == "typing_unavailable"
			cp := &typingControlFixture{fixtureControl: control}
			e.Control = cp
			gateway := &typingGatewayFixture{fixtureGateway: base, entered: make(chan struct{}, 2), release: make(chan struct{}, 2), fail: ending == "model_error"}
			e.Gateway = gateway
			f.add("silent native typing termination", "")
			cancel, done := startFixtureWorker(t, e, f)
			select {
			case <-gateway.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("native model did not start")
			}
			waitFor(t, func() bool { return len(typing.snapshot()) > 0 })
			switch ending {
			case "cancel":
				cancel()
			case "lease_lost":
				// Verify a real refresh before handing its expired lease to a
				// different writer, rather than substituting a fake fence.
				waitFor(t, func() bool { return len(typing.snapshot()) >= 2 })
				if _, err := s.DB.Exec(context.Background(), "UPDATE runtime_agents SET lease_until=now()-interval '1 second' WHERE agent_id=$1", f.agent); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Acquire(context.Background(), f.agent, "replacement-writer"); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				cp.disabled.Store(true)
			default:
				gateway.release <- struct{}{}
			}
			waitFor(t, func() bool {
				events := typing.snapshot()
				return len(events) > 1 && !events[len(events)-1].active
			})
			if ending == "model_error" {
				waitFor(t, func() bool { return count(t, s, "SELECT count(*) FROM runtime_runs WHERE state='failed'") == 1 })
			}
			if ending == "typing_unavailable" {
				waitFor(t, func() bool { return count(t, s, "SELECT count(*) FROM runtime_runs WHERE state='completed'") == 1 })
			}
			cancel()
			<-done
			if got := identity.issued.Load(); got != 1 {
				t.Fatalf("typing minted a competing principal token: %d issuances", got)
			}
			events := typing.snapshot()
			cleared := false
			for _, event := range events {
				if event.conversation != f.conversation || cleared && event.active {
					t.Fatalf("stale or unrelated typing after termination: %#v", events)
				}
				cleared = cleared || !event.active
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.sent) != 0 {
				t.Fatal("typing or model failure generated a durable IM message")
			}
		})
	}
}

func TestNativeTurnTypingWaitsForModelSlotAndClearsOnCompletion(t *testing.T) {
	s := testStore(t)
	e, f, base, cp := fixtureEngine(t, s)
	typing := attachTypingFixture(t, e)
	f.add("silent first conversation", "")
	other := uuid.NewString()
	blocking := &typingGatewayFixture{fixtureGateway: base, entered: make(chan struct{}, 2), release: make(chan struct{}, 2)}
	e.Gateway = blocking
	e.slots <- struct{}{} // Another Agent holds the host's foreground slot.
	cancel, done := startFixtureWorker(t, e, f)
	waitFor(t, func() bool { return count(t, s, "SELECT count(*) FROM runtime_runs WHERE state='running'") == 1 })
	if events := typing.snapshot(); len(events) != 0 {
		t.Fatalf("queued work advertised model activity: %#v", events)
	}
	// Holding the model call gives typing a stable observable active window.
	<-e.slots
	select {
	case <-blocking.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("native turn did not start")
	}
	waitFor(t, func() bool { return len(typing.snapshot()) > 0 })
	first := typing.snapshot()
	if first[0] != (typingEvent{f.conversation, true}) {
		t.Fatalf("current source did not type: %#v", first)
	}
	blocking.release <- struct{}{}
	waitFor(t, func() bool {
		events := typing.snapshot()
		return len(events) >= 2 && !events[len(events)-1].active && count(t, s, "SELECT count(*) FROM runtime_runs WHERE state='completed'") == 1
	})
	f.add("silent second conversation", other)
	select {
	case <-blocking.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("second native turn did not start")
	}
	waitFor(t, func() bool {
		for _, event := range typing.snapshot() {
			if event == (typingEvent{other, true}) {
				return true
			}
		}
		return false
	})
	blocking.release <- struct{}{}
	waitFor(t, func() bool { return count(t, s, "SELECT count(*) FROM runtime_runs WHERE state='completed'") == 2 })
	cancel()
	<-done
	events := typing.snapshot()
	startedOther := false
	for _, event := range events {
		if event.conversation == other && event.active {
			startedOther = true
		}
		if startedOther && event.conversation == f.conversation && event.active {
			t.Fatal("global Session history leaked activity to its previous conversation")
		}
	}
	if !startedOther || events[len(events)-1] != (typingEvent{other, false}) {
		t.Fatalf("second conversation typing lifecycle incomplete: %#v", events)
	}
	// Restarting an idle Session has no new source inputs and emits no typing.
	before := len(events)
	cp.mu.Lock()
	reports := len(cp.states)
	cp.mu.Unlock()
	cancel, done = startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		return len(cp.states) > reports
	})
	cancel()
	<-done
	if events = typing.snapshot(); len(events) != before {
		t.Fatalf("idle restart manufactured typing: %#v", events[before:])
	}
}

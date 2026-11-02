package eino

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
)

func TestPresenceSnapshotAndPromptByteIdentityWhenMarketplaceEnabled(t *testing.T) {
	def := &wire.AgentDefinition{Id: "ag-1", OwnerId: "ow-1", Name: "Alice", Description: "chill peer"}
	budget := ContextBudgetSnapshot{
		EventDrivenInputs:  true,
		HumanPresenceV2:    true,
		DirectMessages:     true,
		StructuredMentions: true,
		FileDelivery:       true,
		WebSearch:          true,
		ScheduledWakeups:   true,
	}

	baseline := humanPresenceV2Instruction(def, budget, true, false)
	explicitEnabled := humanPresenceV2Instruction(def, budget, true, false, false)
	if baseline != explicitEnabled {
		t.Fatal("expected byte-identical v2 prompt when MarketplaceDisabled=false")
	}
	if !strings.Contains(baseline, "mcp__<slug>__<tool>") {
		t.Fatal("expected MCP guidance when MarketplaceDisabled=false")
	}

	purePresence := humanPresenceV2Instruction(def, budget, true, false, true)
	if strings.Contains(purePresence, "mcp__") || strings.Contains(purePresence, "TEAMIE_APP_NOTIFICATION") {
		t.Fatalf("expected zero MCP references when MarketplaceDisabled=true, got:\n%s", purePresence)
	}
	if !strings.Contains(purePresence, "im_enter_conversation") || !strings.Contains(purePresence, "TEAMIE_UNREAD_DIGEST") {
		t.Fatalf("expected native presence guidance when MarketplaceDisabled=true, got:\n%s", purePresence)
	}

	snapDefault, err := json.Marshal(presenceInitSnapshot{
		WorkspaceID:         "ws-1",
		ConversationID:      "conv-1",
		ConvType:            "group",
		MarketplaceDisabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(snapDefault), "marketplaceDisabled") {
		t.Fatalf("expected marketplaceDisabled=false to be omitted for replay hash compatibility, got %s", snapDefault)
	}
}

type decoupledPresenceGateway struct {
	*behaviorGateway
	calls  atomic.Int32
	paused chan struct{}
}

func (g *decoupledPresenceGateway) Chat(ctx context.Context, req *wire.GatewayChatRequest, opts ...callopt.Option) (*wire.GatewayChatResponse, error) {
	if g.calls.Add(1) == 3 {
		close(g.paused)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return g.behaviorGateway.Chat(ctx, req, opts...)
}

func TestNativePresenceDecoupledFromMarketplaceFlagAndMidRunToggleSafe(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	enableContextFixture(t, e)
	e.UnreadDigestInterval = 30 * time.Millisecond

	writeFlags := func(runtimeOn, presenceOn, marketplaceOn bool) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{
			"revision": "decouple-test",
			"flags": map[string]bool{
				"agents.runtime.v3":      runtimeOn,
				"agents.tasks.execute":   true,
				"agents.context.v1":      true,
				"agents.presence.v1":     presenceOn,
				"im.marketplace.enabled": marketplaceOn,
			},
		})
		if err := os.WriteFile(e.Config.FlagsFile, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Start with native presence ON and Marketplace OFF!
	writeFlags(true, true, false)

	targetGroup := uuid.NewString()
	var marketplaceCalls atomic.Int32
	var targetMessagesSent atomic.Int32

	im := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/bootstrap":
			_ = json.NewEncoder(w).Encode(Bootstrap{
				Conversations: []Conversation{
					{ID: f.conversation, WorkspaceID: f.workspace, Type: "group", Name: "Lobby"},
					{ID: targetGroup, WorkspaceID: f.workspace, Type: "group", Name: "WarRoom"},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/conversations/"+targetGroup+"/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messages": []Message{{ID: uuid.NewString(), ConversationID: targetGroup, SenderID: uuid.NewString(), Body: "Need status update", Seq: 1, CreatedAt: time.Now().UTC().Format(time.RFC3339)}},
				"hasMore":  false,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/conversations/"+targetGroup+"/messages":
			targetMessagesSent.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": Message{ID: uuid.NewString(), ConversationID: targetGroup, Seq: 2, CreatedAt: time.Now().UTC().Format(time.RFC3339)},
			})
		case strings.HasSuffix(r.URL.Path, "/apps") || strings.HasSuffix(r.URL.Path, "/session"):
			marketplaceCalls.Add(1)
			http.Error(w, `{"error":{"code":"feature_disabled"}}`, http.StatusForbidden)
		default:
			f.serve(w, r)
		}
	}))
	defer im.Close()
	e.Config.IMURL = im.URL
	e.Config.MarketplaceURL = im.URL

	// Persist initial presence in Lobby (f.conversation), so a message in WarRoom (targetGroup)
	// coalesces into a TEAMIE_UNREAD_DIGEST rather than interrupting immediately.
	lease, err := s.Acquire(ctx, f.agent, e.owner)
	if err != nil {
		t.Fatal(err)
	}
	presence := newConversationPresenceState(30*time.Millisecond, nil)
	presence.Enter(f.workspace, f.conversation, "group")
	if err := presence.Persist(ctx, s, lease); err != nil {
		t.Fatal(err)
	}
	s.Release(ctx, lease)

	f.add("Need status update", targetGroup)

	var sawNotInConversation atomic.Bool
	var sawDigestEnvelope atomic.Bool

	gateway := &decoupledPresenceGateway{
		behaviorGateway: &behaviorGateway{fixtureGateway: base},
		paused:          make(chan struct{}),
	}
	gateway.respond = func(index int, req behaviorRequest) (chatMessage, error) {
		rawMessages := req.Binding.RequestJson
		if strings.Contains(rawMessages, "mcp__") {
			return chatMessage{}, fmt.Errorf("unexpected mcp__ reference in decoupled presence prompt")
		}
		if strings.Contains(rawMessages, "TEAMIE_UNREAD_DIGEST") {
			sawDigestEnvelope.Store(true)
		}
		for _, tl := range req.Tools {
			if strings.HasPrefix(tl.Function.Name, "mcp__") {
				return chatMessage{}, fmt.Errorf("unexpected mounted MCP tool %q when marketplace disabled", tl.Function.Name)
			}
		}

		switch index {
		case 0:
			// Speaking in targetGroup before entering must be blocked by checkSendPresence!
			return toolPathCall("im_send_message", "send-before-enter", sendArgs{
				ConversationID: targetGroup,
				Body:           "premature",
			})
		case 1:
			if strings.Contains(rawMessages, "not_in_conversation") {
				sawNotInConversation.Store(true)
			}
			return toolPathCall("im_enter_conversation", "enter-warroom", map[string]any{
				"conversationId": targetGroup,
			})
		case 2:
			return toolPathCall("im_send_message", "send-in-warroom", sendArgs{
				ConversationID: targetGroup,
				Body:           "Status is all green",
			})
		case 3:
			return chatMessage{Role: "assistant", Content: "PRIVATE_DONE"}, nil
		default:
			return chatMessage{}, fmt.Errorf("unexpected model step %d", index)
		}
	}
	e.Gateway = gateway

	cancel, done := startFixtureWorker(t, e, f)
	select {
	case <-gateway.paused:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for mid-Run checkpoint boundary")
	}

	// Stop worker cleanly via runtime flag, then flip BOTH flags mid-Run to prove
	// frozen presence_snapshot/v1 + CheckpointPresence survive flag toggles without replay conflict!
	writeFlags(false, false, true)
	waitFor(t, func() bool { return s.checkpointExists(ctx, "turnloop:"+f.agent) })
	cancel()
	<-done

	metadata, err := s.ResumeMetadata(ctx, "turnloop:"+f.agent)
	if err != nil || metadata == nil || metadata.Presence == nil || !metadata.Presence.MarketplaceDisabled {
		t.Fatalf("expected checkpoint presence with MarketplaceDisabled=true, got %+v (%v)", metadata, err)
	}
	if metadata.Presence.ConversationID != targetGroup {
		t.Fatalf("expected checkpoint active conversation %s, got %+v", targetGroup, metadata.Presence)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE runtime_runs SET next_attempt=now() WHERE id=$1`, metadata.RunID); err != nil {
		t.Fatal(err)
	}

	writeFlags(true, false, true)
	cancel2, done2 := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE id=$1 AND state='completed'`, metadata.RunID) == 1
	})
	cancel2()
	<-done2
	gateway.snapshot(t)

	if !sawDigestEnvelope.Load() {
		t.Fatal("expected non-active conversation message to arrive as TEAMIE_UNREAD_DIGEST")
	}
	if !sawNotInConversation.Load() {
		t.Fatal("expected im_send_message before im_enter_conversation to return not_in_conversation")
	}
	if got := targetMessagesSent.Load(); got != 1 {
		t.Fatalf("expected exactly 1 delivered message in targetGroup after enter + resume, got %d", got)
	}
	if got := marketplaceCalls.Load(); got != 0 {
		t.Fatalf("expected 0 Marketplace HTTP calls when im.marketplace.enabled=false at Run start, got %d", got)
	}
}

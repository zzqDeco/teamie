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
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
)

type presenceResumeGateway struct {
	*behaviorGateway
	calls  atomic.Int32
	paused chan struct{}
}

func (g *presenceResumeGateway) Chat(ctx context.Context, req *wire.GatewayChatRequest, opts ...callopt.Option) (*wire.GatewayChatResponse, error) {
	if g.calls.Add(1) == 2 {
		close(g.paused)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return g.behaviorGateway.Chat(ctx, req, opts...)
}

// Exercise the actual Worker, native Eino checkpoint, durable receipts, IM HTTP
// tools and MCP dispatch. Only model decisions and remote applications are fixtures.
func TestNativePresenceResumeAfterCompletedConversationTools(t *testing.T) {
	for _, mode := range []string{"group", "channel", "direct", "leave", "legacy_replay", "flag_disabled"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s := testStore(t)
			e, f, base, _ := fixtureEngine(t, s)
			enableContextFixture(t, e)
			runtimeEnabled := true
			writeFlags := func(enabled bool) {
				t.Helper()
				flags, _ := json.Marshal(map[string]any{"flags": map[string]bool{"agents.runtime.v3": runtimeEnabled, "agents.tasks.execute": true, "agents.context.v1": true, "im.marketplace.enabled": enabled}})
				if err := os.WriteFile(e.Config.FlagsFile, flags, 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeFlags(true)
			target, kind := uuid.NewString(), mode
			if mode == "legacy_replay" {
				kind = "group"
			}
			if mode == "leave" || mode == "flag_disabled" {
				kind = "channel"
			}
			var historyReads, mcpEffects atomic.Int32
			m := server.NewMCPServer("checkpoint-board", "1", server.WithToolCapabilities(true))
			m.AddTool(mcp.NewTool("ping"), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				if req.Header.Get("Authorization") != "Bearer "+target {
					return nil, fmt.Errorf("wrong conversation credential")
				}
				mcpEffects.Add(1)
				return mcp.NewToolResultText("accepted"), nil
			})
			remote := httptest.NewServer(server.NewStreamableHTTPServer(m))
			defer remote.Close()
			app := imMarketplaceApp{ID: uuid.NewString(), Slug: "board", Name: "Board", MCPURL: remote.URL, Enabled: true}
			im := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/api/v1/bootstrap":
					_ = json.NewEncoder(w).Encode(Bootstrap{Conversations: []Conversation{{ID: f.conversation, WorkspaceID: f.workspace, Type: "group"}, {ID: target, WorkspaceID: f.workspace, Type: kind}}})
				case r.Method == "GET" && r.URL.Path == "/api/v1/conversations/"+target+"/messages":
					historyReads.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"messages": []Message{}, "hasMore": false})
				case strings.HasSuffix(r.URL.Path, "/apps"):
					apps := []imMarketplaceApp{}
					if kind == "group" && strings.Contains(r.URL.Path, target) {
						apps = append(apps, app)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"apps": apps})
				case strings.HasSuffix(r.URL.Path, "/session"):
					_ = json.NewEncoder(w).Encode(imConversationAppSession{App: app, Token: target, ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)})
				default:
					f.serve(w, r)
				}
			}))
			defer im.Close()
			e.Config.IMURL = im.URL
			lease, err := s.Acquire(ctx, f.agent, e.owner)
			if err != nil {
				t.Fatal(err)
			}
			presence := newConversationPresenceState(time.Minute, nil)
			presence.Enter(f.workspace, f.conversation, "group")
			if err := presence.Persist(ctx, s, lease); err != nil {
				t.Fatal(err)
			}
			s.Release(ctx, lease)
			f.add("Continue after changing the active conversation.", "")
			gateway := &presenceResumeGateway{behaviorGateway: &behaviorGateway{fixtureGateway: base}, paused: make(chan struct{})}
			gateway.respond = func(index int, _ behaviorRequest) (chatMessage, error) {
				switch index {
				case 0:
					if mode == "leave" {
						return toolPathCall("im_leave_conversation", "leave-before-checkpoint", map[string]any{})
					}
					return toolPathCall("im_enter_conversation", "enter-before-checkpoint", map[string]any{"conversationId": target})
				case 1:
					message, err := toolPathCall("im_send_message", "send-after-checkpoint", sendArgs{ConversationID: target, Body: "resumed in target"})
					if err == nil && kind == "group" {
						ping, err := toolPathCall("mcp__board__ping", "mcp-after-checkpoint", map[string]any{})
						if err != nil {
							return chatMessage{}, err
						}
						message.ToolCalls = append(message.ToolCalls, ping.ToolCalls...)
					}
					return message, err
				case 2:
					return chatMessage{Role: "assistant", Content: "PRIVATE_DONE"}, nil
				default:
					return chatMessage{}, fmt.Errorf("unexpected live model call %d", index)
				}
			}
			e.Gateway = gateway
			cancel, done := startFixtureWorker(t, e, f)
			select {
			case <-gateway.paused:
			case <-time.After(10 * time.Second):
				t.Fatal("did not reach post-tool checkpoint boundary")
			}
			// Request the native Stop path while its parent stays alive. Canceling
			// the parent first may legitimately fall back to receipt replay.
			runtimeEnabled = false
			writeFlags(true)
			waitFor(t, func() bool { return s.checkpointExists(ctx, "turnloop:"+f.agent) })
			cancel()
			<-done
			metadata, err := s.ResumeMetadata(ctx, "turnloop:"+f.agent)
			if err != nil || metadata == nil || metadata.Presence == nil || len(metadata.Targets) == 0 {
				t.Fatalf("native checkpoint missing: %+v %v", metadata, err)
			}
			expectedActive := target
			if mode == "leave" {
				expectedActive = ""
			}
			if metadata.Presence.ConversationID != expectedActive {
				t.Fatalf("wrong checkpoint scope: %+v", metadata.Presence)
			}
			if mode == "legacy_replay" {
				payload, _, _, err := s.DB.GetCheckpoint(ctx, "turnloop:"+f.agent)
				if err != nil {
					t.Fatal(err)
				}
				metadata.Presence = nil
				raw, _ := json.Marshal(metadata)
				lease, err = s.Acquire(ctx, f.agent, e.owner)
				if err != nil {
					t.Fatal(err)
				}
				if err = s.DB.SetCheckpoint(ctx, lease, "turnloop:"+f.agent, payload, raw); err != nil {
					t.Fatal(err)
				}
				s.Release(ctx, lease)
			}
			runtimeEnabled = true
			writeFlags(mode != "flag_disabled")
			if _, err := s.DB.Exec(ctx, `UPDATE runtime_runs SET next_attempt=now() WHERE id=$1`, metadata.RunID); err != nil {
				t.Fatal(err)
			}
			cancel, done = startFixtureWorker(t, e, f)
			waitFor(t, func() bool {
				return count(t, s, `SELECT count(*) FROM runtime_runs WHERE id=$1 AND state='completed'`, metadata.RunID) == 1
			})
			cancel()
			<-done
			gateway.snapshot(t)
			nativeResumes := count(t, s, `SELECT cardinality(context_attempts) FROM runtime_runs WHERE id=$1`, metadata.RunID)
			if (mode == "legacy_replay" && nativeResumes != 0) || (mode != "legacy_replay" && nativeResumes != 1) {
				t.Fatalf("wrong recovery path for %s: %d native resumes", mode, nativeResumes)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if mode == "leave" {
				if len(f.sent) != 0 {
					t.Fatal("leave was undone by native resume")
				}
			} else {
				if len(f.sent) != 1 || f.posts != 1 {
					t.Fatalf("resumed send effects: %d messages, %d posts", len(f.sent), f.posts)
				}
				for _, sent := range f.sent {
					if sent.ConversationID != target {
						t.Fatal("sent in initial scope")
					}
				}
				if historyReads.Load() != 1 {
					t.Fatalf("completed enter fetched history again: %d", historyReads.Load())
				}
			}
			if kind == "group" && mcpEffects.Load() != 1 {
				t.Fatalf("MCP did not resume in target exactly once: %d", mcpEffects.Load())
			}
			if count(t, s, `SELECT count(DISTINCT session_id) FROM runtime_session_events`) != 1 {
				t.Fatal("resume forked the Agent Session")
			}
		})
	}
}

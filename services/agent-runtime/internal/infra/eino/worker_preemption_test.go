package eino

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/kitex/client/callopt"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
)

type preemptionSandbox struct {
	workspaceFixture
	beforeWrite func(context.Context) error
}

func (s *preemptionSandbox) Workspace(ctx context.Context, r *wire.SandboxWorkspaceRequest, opts ...callopt.Option) (*wire.SandboxWorkspaceResponse, error) {
	if r.Operation == "write" && s.beforeWrite != nil {
		if err := s.beforeWrite(ctx); err != nil {
			return nil, err
		}
	}
	if r.Operation != "edit" {
		return s.workspaceFixture.Workspace(ctx, r, opts...)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.GetRequestId() == "" {
		return nil, fmt.Errorf("edit missing stable mutation identity")
	}
	if cached, ok := s.receipts[r.GetRequestId()]; ok {
		return &wire.SandboxWorkspaceResponse{Ok: true, DataJson: &cached}, nil
	}
	var args struct{ Path, Old, New string }
	if err := json.Unmarshal([]byte(r.ArgsJson), &args); err != nil {
		return nil, err
	}
	content, exists := s.files[args.Path]
	if !exists || !strings.Contains(content, args.Old) {
		code := "file_unavailable"
		return &wire.SandboxWorkspaceResponse{ErrorCode: &code}, nil
	}
	s.files[args.Path] = strings.Replace(content, args.Old, args.New, 1)
	s.effects++
	raw := `{"updated":true}`
	s.receipts[r.GetRequestId()] = raw
	return &wire.SandboxWorkspaceResponse{Ok: true, DataJson: &raw}, nil
}

func TestRelevantInputPreemptionCompletesFileToolsBeforeNextTurn(t *testing.T) {
	for _, arrival := range []string{"during_model", "during_write"} {
		t.Run(arrival, func(t *testing.T) {
			s := testStore(t)
			e, f, base, _ := fixtureEngine(t, s)
			enableContextFixture(t, e)
			behaviorSource(t, f, "Save the initial note to /workspace/note.md.", "", "")
			stage := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			sandbox := &preemptionSandbox{}
			if arrival == "during_write" {
				sandbox.beforeWrite = func(ctx context.Context) error {
					close(stage)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
			e.Sandbox = sandbox

			// Seeing the following sync poll proves the previous tick finished
			// ingesting and pushing event 2 while the model/file call still waits.
			// This avoids relying on a sleep to race the preemption request.
			nextPoll := make(chan struct{})
			var pollOnce sync.Once
			im := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/sync" && r.URL.Query().Get("cursor") == "2" {
					pollOnce.Do(func() { close(nextPoll) })
				}
				f.serve(w, r)
			}))
			t.Cleanup(im.Close)
			e.Config.IMURL = im.URL
			var fresh Message
			gateway := &behaviorGateway{fixtureGateway: base}
			gateway.respond = func(index int, request behaviorRequest) (chatMessage, error) {
				switch index {
				case 0:
					if arrival == "during_model" {
						close(stage)
						<-release
					}
					return toolPathCall("write_file", "save-initial-note", map[string]any{"file_path": "/workspace/note.md", "content": "initial note"})
				case 1:
					var receipts int
					if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM runtime_tool_calls WHERE name='native:write_file' AND result IS NOT NULL`).Scan(&receipts); err != nil {
						return chatMessage{}, err
					}
					if receipts != 1 {
						return chatMessage{}, fmt.Errorf("next turn started without the completed file-write receipt")
					}
					if !strings.Contains(request.Messages[len(request.Messages)-1].Content, fresh.Body) {
						return chatMessage{}, fmt.Errorf("fresh input did not enter the next serial turn")
					}
					return toolPathCall("edit_file", "edit-existing-note", map[string]any{"file_path": "/workspace/note.md", "old_string": "initial", "new_string": "updated"})
				case 2:
					return toolPathCall("im_send_message", "confirm-updated-note", sendArgs{ConversationID: fresh.ConversationID, ReplyToID: fresh.ID, Body: "Updated the saved note."})
				case 3:
					return chatMessage{Role: "assistant", Content: "PRIVATE_DONE"}, nil
				default:
					return chatMessage{}, fmt.Errorf("unexpected model call %d", index)
				}
			}
			e.Gateway = gateway
			cancel, done := startFixtureWorker(t, e, f)
			select {
			case <-stage:
			case <-time.After(5 * time.Second):
				t.Fatal("initial model/file call did not start")
			}
			fresh = behaviorSource(t, f, "Now edit the saved note and confirm here.", "", "")
			select {
			case <-nextPoll:
			case <-time.After(5 * time.Second):
				t.Fatal("new input was not pushed while the original call waited")
			}
			if count(t, s, `SELECT count(*) FROM runtime_inputs WHERE state='pending'`) != 1 || count(t, s, `SELECT count(*) FROM runtime_runs`) != 1 {
				t.Fatal("new input did not remain buffered behind the active operation")
			}
			unblock()
			waitFor(t, func() bool {
				gateway.mu.Lock()
				failed := gateway.failure != nil
				gateway.mu.Unlock()
				return failed || count(t, s, `SELECT count(*) FROM runtime_runs WHERE state IN ('completed','failed')`) > 0
			})
			cancel()
			<-done
			if len(gateway.snapshot(t)) != 4 {
				t.Fatal("unexpected model work across the handoff")
			}
			if count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='interrupted' AND safe_error='new_input'`) != 1 || count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) != 1 || count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='failed'`) != 0 {
				t.Fatal("file-tool round did not finish before the new-input handoff")
			}
			if count(t, s, `SELECT count(*) FROM runtime_inputs WHERE state='done'`) != 2 || count(t, s, `SELECT count(DISTINCT session_id) FROM runtime_session_events`) != 1 {
				t.Fatal("handoff lost an input or created a second Session")
			}
			if count(t, s, `SELECT count(*) FROM runtime_tool_calls WHERE name IN ('native:write_file','native:edit_file','im_send_message') AND result IS NOT NULL`) != 3 {
				t.Fatal("file or IM effects lost their durable receipts")
			}
			sandbox.mu.Lock()
			content, effects := sandbox.files["/workspace/note.md"], sandbox.effects
			sandbox.mu.Unlock()
			if content != "updated note" || effects != 2 {
				t.Fatal("write/edit did not complete exactly once", content, effects)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.posts != 1 || len(f.sent) != 1 {
				t.Fatal("confirmation was not sent exactly once")
			}
		})
	}
}

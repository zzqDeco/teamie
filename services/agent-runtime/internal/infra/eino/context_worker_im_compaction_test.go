package eino

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	"teamie/contracts/gen/agent/hertz_gen/agent/modeltransport"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
)

const compactionIMReadCall = "compaction-read-historical-B"

// Keep the production Worker, native tool wrappers and archive callbacks real.
// The first model turn reads an older IM message which was never a fresh input;
// subsequent moderate private finals naturally reach the default 16K trigger.
type imCompactionWorkerGateway struct {
	*compactionWorkerGateway
	pathMu       sync.Mutex
	conversation string
	message      string
	step         int
	ref          string
	failure      error
}

func (g *imCompactionWorkerGateway) Chat(ctx context.Context, r *wire.GatewayChatRequest, opts ...callopt.Option) (*wire.GatewayChatResponse, error) {
	g.pathMu.Lock()
	defer g.pathMu.Unlock()
	var request toolPathRequest
	if err := decodeFixtureRequest(r.RequestJson, &request); err != nil {
		return nil, err
	}
	if len(request.Tools) == 0 {
		return g.compactionWorkerGateway.Chat(ctx, r, opts...)
	}
	g.step++
	var response chatMessage
	switch g.step {
	case 1:
		response, _ = toolPathCall("im_get_message", compactionIMReadCall, getMessageArgs{ConversationID: g.conversation, MessageID: g.message})
	case 2:
		last := request.Messages[len(request.Messages)-1]
		g.ref = regexp.MustCompile(`context:v1:[a-f0-9-]+:tool_trunc:[a-f0-9]+`).FindString(last.Content)
		if last.Role != "tool" || g.ref == "" || !strings.HasPrefix(last.Content, "Archived private context: ") || strings.Contains(last.Content, "HISTORICAL_B_PRIVATE_TAIL") {
			g.failure = fmt.Errorf("actual IM result was not replaced with a bounded archive notice")
			return nil, g.failure
		}
		response, _ = toolPathCall("context_read", "compaction-read-archive-B", map[string]any{"ref": g.ref, "limit": 4096})
	case 3:
		last := request.Messages[len(request.Messages)-1]
		if last.Role != "tool" || !strings.Contains(last.Content, "HISTORICAL_B_PRIVATE_BEGIN") || !strings.Contains(last.Content, `"hasMore":true`) || strings.Contains(last.Content, "Archived private context: ") {
			g.failure = fmt.Errorf("historical IM archive could not be read without recursive offload")
			return nil, g.failure
		}
		return g.compactionWorkerGateway.Chat(ctx, r, opts...)
	default:
		return g.compactionWorkerGateway.Chat(ctx, r, opts...)
	}
	g.compactionWorkerGateway.mu.Lock()
	g.requests = append(g.requests, []byte(r.RequestJson))
	g.compactionWorkerGateway.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": response, "finish_reason": "stop"}}})
	return &wire.GatewayChatResponse{Ok: true, ResponseJson: modelFixtureResponse(string(raw))}, nil
}

func compactionOriginalIMNotice(t *testing.T, s *Store, ref string) {
	t.Helper()
	var receipt []byte
	if err := s.DB.QueryRow(context.Background(), `SELECT result FROM runtime_tool_calls WHERE name='im_get_message' AND call_id=$1`, compactionIMReadCall).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(receipt), "HISTORICAL_B_PRIVATE_BEGIN") || !strings.Contains(string(receipt), "HISTORICAL_B_PRIVATE_TAIL") {
		t.Fatal("authenticated IM receipt lost the complete historical message")
	}
	rows, err := s.DB.Query(context.Background(), `SELECT payload FROM runtime_session_events WHERE kind='message' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var raw []byte
		var event adk.SessionEvent[*schema.AgenticMessage]
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err = decodeSessionPayload(raw, &event); err != nil {
			t.Fatal(err)
		}
		if event.Message == nil {
			continue
		}
		for _, block := range event.Message.ContentBlocks {
			if block == nil || block.FunctionToolResult == nil || block.FunctionToolResult.CallID != compactionIMReadCall {
				continue
			}
			result := block.FunctionToolResult
			if result.Name != "im_get_message" || len(result.Content) != 1 || result.Content[0].Text == nil {
				t.Fatal("unexpected persisted native IM tool result")
			}
			text := result.Content[0].Text.Text
			if ref == "" || !strings.HasPrefix(text, "Archived private context: "+ref) || strings.Contains(text, "HISTORICAL_B_PRIVATE_TAIL") {
				t.Fatal("original native message event did not contain the actual reduced notice")
			}
			found = true
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("native original tool message event was not durably persisted")
	}
	t.Log("actual original IM ToolResult event is a reduced notice; authenticated receipt retains the complete B message")
}

func TestContextWorkerCompactsHistoricalIMNoticeWithRealSources(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	enableContextFixture(t, e)
	historical := Message{ID: uuid.NewString(), ConversationID: uuid.NewString(), SenderID: uuid.NewString(), SenderName: "Bob", SenderKind: "human", Seq: 42, CreatedAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), Body: "HISTORICAL_B_PRIVATE_BEGIN\n" + strings.Repeat("B historical message keeps independently authorized facts. ", 110) + "\nHISTORICAL_B_PRIVATE_TAIL"}
	// B is available only through the authenticated history tool. Do not enqueue
	// a fresh event that could accidentally supply B's scope to the Run.
	f.messages[historical.ID] = &historical
	gateway := &imCompactionWorkerGateway{compactionWorkerGateway: &compactionWorkerGateway{fixtureGateway: base}, conversation: historical.ConversationID, message: historical.ID}
	e.Gateway = gateway
	f.add("IM_COMPACTION_REQUEST_1 inspect the referenced historical message privately. "+strings.Repeat("Retain source facts and verified outcomes for later follow-up. ", 12), "")
	f.mu.Lock()
	first := *f.events[0]["message"].(*Message)
	f.mu.Unlock()
	cancel, done := startFixtureWorker(t, e, f)
	for turn := 1; turn <= 4; turn++ {
		if turn > 1 {
			f.add(fmt.Sprintf("IM_COMPACTION_REQUEST_%d ", turn)+strings.Repeat("Retain source facts and verified outcomes for later follow-up. ", 12), uuid.NewString())
		}
		waitFor(t, func() bool {
			return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state IN ('completed','failed')`) >= turn
		})
		if turn == 1 {
			gateway.pathMu.Lock()
			ref := gateway.ref
			gateway.pathMu.Unlock()
			compactionOriginalIMNotice(t, s, ref)
		}
		if completed := count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`); completed != turn {
			var state, code string
			if err := s.DB.QueryRow(context.Background(), `SELECT state,safe_error FROM runtime_runs ORDER BY started_at DESC LIMIT 1`).Scan(&state, &code); err != nil {
				t.Fatal(err)
			}
			gateway.pathMu.Lock()
			step, failure := gateway.step, gateway.failure
			gateway.pathMu.Unlock()
			t.Fatalf("historical IM notice/source closure blocked default-budget Worker Run %d: state=%s code=%s modelStep=%d fixtureError=%v", turn, state, code, step, failure)
		}
	}
	cancel()
	<-done
	if count(t, s, `SELECT count(*) FROM runtime_runs WHERE context_profile=$1`, contextProfileV1) != 4 || count(t, s, `SELECT count(DISTINCT session_id) FROM runtime_session_events`) != 1 {
		t.Fatal("IM compaction changed the single Session/run policy")
	}
	if count(t, s, `SELECT count(*) FROM runtime_inputs WHERE payload->>'messageId'=$1`, historical.ID) != 0 {
		t.Fatal("historical B incorrectly became a fresh runtime input")
	}
	f.mu.Lock()
	posts, sent := f.posts, len(f.sent)
	f.mu.Unlock()
	if posts != 0 || sent != 0 {
		t.Fatal("private tool output, final or summary was automatically posted to IM")
	}
	gateway.compactionWorkerGateway.mu.Lock()
	regular, summaries := gateway.regular, gateway.summaries
	requests := append([][]byte{}, gateway.requests...)
	gateway.compactionWorkerGateway.mu.Unlock()
	if regular != 4 || summaries == 0 {
		t.Fatal("four moderate private finals did not naturally trigger prefix summary", regular, summaries)
	}
	budget, _ := makeContextBudgetSnapshot(16384)
	for i, request := range requests {
		measurement, err := measureContextRequest(request)
		if err != nil || !budget.fits(measurement) {
			t.Fatal("actual model request exceeded frozen default capacity", i, measurement, err)
		}
		var envelope modeltransport.Request
		var opts model.Options
		if json.Unmarshal(request, &envelope) != nil || json.Unmarshal(envelope.Options, &opts) != nil {
			t.Fatal("invalid native model request")
		}
		if len(opts.Tools) == 0 {
			if opts.AgenticToolChoice == nil || opts.AgenticToolChoice.Type != schema.ToolChoiceForbidden {
				t.Fatal("native summary could execute tools")
			}
		} else if len(opts.Tools) != 21 {
			t.Fatal("test did not exercise the complete production tool manifest", len(opts.Tools))
		}
	}
	rows, err := s.DB.Query(context.Background(), `SELECT run_id::text,payload FROM runtime_session_events WHERE kind='messages_replaced' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	type replacement struct {
		run   string
		event adk.SessionEvent[*schema.AgenticMessage]
	}
	replacements := []replacement{}
	for rows.Next() {
		var item replacement
		var raw []byte
		if err = rows.Scan(&item.run, &raw); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err = decodeSessionPayload(raw, &item.event); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		replacements = append(replacements, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, replacement := range replacements {
		if replacement.event.MessagesReplaced == nil {
			continue
		}
		for _, message := range *replacement.event.MessagesReplaced {
			if !isContextSummary(message) {
				continue
			}
			ref, _ := message.Extra[contextArchiveRefKey].(string)
			content, sources := compactionArchiveClosure(t, s, ref, map[string]bool{})
			if !strings.Contains(content, "HISTORICAL_B_PRIVATE_TAIL") || !strings.Contains(content, "IM_COMPACTION_REQUEST_1") || !strings.Contains(content, "PRIVATE_PREFIX_FINAL_") {
				continue
			}
			var hasA, hasB bool
			for _, source := range sources {
				hasA = hasA || source.MessageID == first.ID && source.ConversationID == first.ConversationID && source.SenderID == first.SenderID
				hasB = hasB || source.MessageID == historical.ID && source.ConversationID == historical.ConversationID && source.SenderID == historical.SenderID
			}
			if !hasA || !hasB {
				t.Fatal("real prefix archive DAG lost the original A input or authenticated B history scope", hasA, hasB)
			}
			var raw []byte
			if err = s.DB.QueryRow(context.Background(), `SELECT input_json FROM runtime_runs WHERE id=$1`, replacement.run).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), first.ID) || strings.Contains(string(raw), historical.ID) {
				t.Fatal("prefix archive was not exercised from a later independent Run")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("native messages_replaced did not durably link the original IM prefix and full parent archive")
	}
	t.Logf("four Worker Runs completed with %d native summaries and %d budget-valid model requests; prefix DAG retains real A and historical B", summaries, len(requests))
}

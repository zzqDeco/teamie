package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/modeltransport"
)

type compactionWorkerGateway struct {
	*fixtureGateway
	mu                 sync.Mutex
	regular, summaries int
	requests           [][]byte
}

func (g *compactionWorkerGateway) Chat(_ context.Context, r *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var request toolPathRequest
	if err := decodeFixtureRequest(r.RequestJson, &request); err != nil {
		return nil, err
	}
	g.requests = append(g.requests, []byte(r.RequestJson))
	text := "Earlier private work is retained in its source-scoped prefix archive; preserve the original request references and verified results."
	if len(request.Tools) == 0 {
		g.summaries++
	} else {
		g.regular++
		text = fmt.Sprintf("PRIVATE_PREFIX_FINAL_%02d\n", g.regular) + strings.Repeat("Private work keeps source facts and receipts for later verification. ", 85)
	}
	raw, _ := (&schema.HumanReadableSerializer{}).Marshal(assistantText(text))
	value := string(raw)
	return &wire.GatewayChatResponse{Ok: true, ResponseJson: &value}, nil
}

func compactionArchiveClosure(t *testing.T, s *Store, ref string, seen map[string]bool) (string, []InputSource) {
	t.Helper()
	if seen[ref] {
		return "", nil
	}
	seen[ref] = true
	if len(seen) > 32 {
		t.Fatal("unexpected cyclic or unbounded prefix closure")
	}
	runID, _, key, err := parseContextRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	var content, sourceJSON, parentJSON []byte
	if err = s.DB.QueryRow(context.Background(), `SELECT content,sources,source_artifact_refs FROM runtime_context_artifacts WHERE run_id=$1 AND artifact_key=$2`, runID, key).Scan(&content, &sourceJSON, &parentJSON); err != nil {
		t.Fatal("summary did not reference a committed real archive", err)
	}
	var sources []InputSource
	var parents []string
	if json.Unmarshal(sourceJSON, &sources) != nil || json.Unmarshal(parentJSON, &parents) != nil {
		t.Fatal("invalid persisted archive closure")
	}
	all := string(content)
	for _, parent := range parents {
		text, refs := compactionArchiveClosure(t, s, parent, seen)
		all += "\n" + text
		sources = append(sources, refs...)
	}
	return all, sources
}

func TestContextWorkerCompactsRealSessionPrefixWithDefaultBudget(t *testing.T) {
	s := testStore(t)
	e, f, base, _ := fixtureEngine(t, s)
	enableContextFixture(t, e)
	gateway := &compactionWorkerGateway{fixtureGateway: base}
	e.Gateway = gateway
	f.add("COMPACTION_REQUEST_1 "+strings.Repeat("Retain these source-scoped facts and verified outcomes for later follow-up. ", 12), "")
	cancel, done := startFixtureWorker(t, e, f)
	for turn := 1; turn <= 4; turn++ {
		if turn > 1 {
			f.add(fmt.Sprintf("COMPACTION_REQUEST_%d ", turn)+strings.Repeat("Retain these source-scoped facts and verified outcomes for later follow-up. ", 12), uuid.NewString())
		}
		waitFor(t, func() bool {
			return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state IN ('completed','failed')`) >= turn
		})
		if completed := count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`); completed != turn {
			var state, code string
			_ = s.DB.QueryRow(context.Background(), `SELECT state,safe_error FROM runtime_runs ORDER BY created_at DESC LIMIT 1`).Scan(&state, &code)
			t.Fatal("default-budget worker did not finish each separate input", turn, state, code)
		}
	}
	cancel()
	<-done
	if count(t, s, `SELECT count(*) FROM runtime_runs WHERE context_profile=$1`, contextProfileV1) != 4 || count(t, s, `SELECT count(DISTINCT session_id) FROM runtime_session_events`) != 1 {
		t.Fatal("compaction changed the single Session/run policy")
	}
	f.mu.Lock()
	posts, sent := f.posts, len(f.sent)
	messages := map[string]Message{}
	for id, msg := range f.messages {
		messages[id] = *msg
	}
	f.mu.Unlock()
	if posts != 0 || sent != 0 {
		t.Fatal("private finals or summaries were automatically posted to IM")
	}
	gateway.mu.Lock()
	regular, summaries := gateway.regular, gateway.summaries
	requests := append([][]byte{}, gateway.requests...)
	gateway.mu.Unlock()
	if regular != 4 || summaries == 0 {
		t.Fatal("four moderate private outputs did not trigger native prefix summarization", regular, summaries)
	}
	budget, _ := makeContextBudgetSnapshot(16384)
	var initialMessages []*schema.AgenticMessage
	var initialOptions model.Options
	seenSummary, seenAfterSummary := false, false
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
				t.Fatal("native summary was allowed to execute tools")
			}
			seenSummary = true
			var decoded behaviorRequest
			if err = decodeFixtureRequest(string(request), &decoded); err != nil {
				t.Fatal(err)
			}
			behaviorHasGuidance(t, decoded.Messages[len(decoded.Messages)-1].Content, "history already inspected", "do not create a follow-up task to poll", "explicit requests to verify an original")
		} else if len(opts.Tools) != 21 {
			t.Fatal("test did not exercise the full production tool manifest", len(opts.Tools))
		} else {
			var decoded behaviorRequest
			if err = decodeFixtureRequest(string(request), &decoded); err != nil {
				t.Fatal(err)
			}
			behaviorHasGuidance(t, behaviorSystem(t, decoded), "teamie-event-driven-inputs-v1", "sufficient facts already in context", "Runtime delivers new inputs")
			seenAfterSummary = seenAfterSummary || seenSummary
		}
		if i == 0 {
			initialOptions = opts
			if err = (&schema.HumanReadableSerializer{}).Unmarshal(envelope.Messages, &initialMessages); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !seenAfterSummary {
		t.Fatal("event-driven guidance was not checked on a post-compaction model request")
	}
	rows, err := s.DB.Query(context.Background(), `SELECT seq,run_id::text,payload FROM runtime_session_events WHERE kind='messages_replaced' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	type replacement struct {
		seq   int64
		run   string
		event adk.SessionEvent[*schema.AgenticMessage]
	}
	replacements := []replacement{}
	for rows.Next() {
		var item replacement
		var raw []byte
		if err = rows.Scan(&item.seq, &item.run, &raw); err != nil {
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
	var firstSummarySeq int64
	for _, replacement := range replacements {
		if replacement.event.MessagesReplaced == nil {
			continue
		}
		for _, message := range *replacement.event.MessagesReplaced {
			if !isContextSummary(message) {
				continue
			}
			if firstSummarySeq == 0 {
				firstSummarySeq = replacement.seq
			}
			ref, ok := message.Extra[contextArchiveRefKey].(string)
			if !ok {
				t.Fatal("summary replacement lost its server-owned archive ref")
			}
			content, sources := compactionArchiveClosure(t, s, ref, map[string]bool{})
			if !strings.Contains(content, "PRIVATE_PREFIX_FINAL_") || !strings.Contains(content, "COMPACTION_REQUEST_") {
				t.Fatal("prefix archive lost original private assistant/input content")
			}
			var raw []byte
			if err = s.DB.QueryRow(context.Background(), `SELECT input_json FROM runtime_runs WHERE id=$1`, replacement.run).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var current []Input
			if json.Unmarshal(raw, &current) != nil {
				t.Fatal("invalid run input")
			}
			currentIDs := map[string]bool{}
			for _, input := range current {
				currentIDs[input.MessageID] = true
			}
			previous := false
			for _, source := range sources {
				actual, exists := messages[source.MessageID]
				if !exists || source.ConversationID != actual.ConversationID || source.SenderID != actual.SenderID {
					t.Fatal("prefix source did not match a real fixture input", source.MessageID)
				}
				previous = previous || !currentIDs[source.MessageID]
			}
			if !previous {
				t.Fatal("prefix was incorrectly attributed only to the compactor Run")
			}
		}
	}
	if firstSummarySeq == 0 {
		t.Fatal("native summary did not persist a real messages_replaced boundary")
	}
	// The actual Session before its first summary, combined with the unchanged
	// full policy/tool manifest, must naturally cross the default trigger.
	before := []*schema.AgenticMessage{initialMessages[0]}
	rows, err = s.DB.Query(context.Background(), `SELECT payload FROM runtime_session_events WHERE kind='message' AND seq<$1 ORDER BY seq`, firstSummarySeq)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var raw []byte
		var event adk.SessionEvent[*schema.AgenticMessage]
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err = decodeSessionPayload(raw, &event); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if event.Message != nil {
			before = append(before, event.Message)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	measurement, err := measureContext(before, initialOptions.Tools, budget.OutputReserveTokens, false)
	if err != nil || !budget.shouldCompact(measurement) {
		t.Fatal("fixture did not naturally cross the default 16K trigger", measurement, err)
	}
	t.Logf("four worker Runs completed with %d native summaries; pre-compaction estimate=%d tokens/%d bytes", summaries, measurement.EstimatedTokens, measurement.TransportBytes)
}

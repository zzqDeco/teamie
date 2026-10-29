package eino

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
)

func TestNextCronTimeAndValidation(t *testing.T) {
	base := time.Date(2026, 10, 22, 1, 14, 30, 0, time.UTC) // Thursday 01:14:30 UTC (09:14:30 Asia/Shanghai)

	next15, err := nextCronTime("*/15 * * * *", "UTC", base)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 22, 1, 15, 0, 0, time.UTC); !next15.Equal(want) {
		t.Fatalf("got %s, want %s", next15, want)
	}

	// 09:30 Asia/Shanghai -> 01:30 UTC on the same day
	nextShanghai, err := nextCronTime("30 9 * * 1-5", "Asia/Shanghai", base)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 22, 1, 30, 0, 0, time.UTC); !nextShanghai.Equal(want) {
		t.Fatalf("got %s, want %s", nextShanghai, want)
	}

	// Sunday=7 support: 2026-10-25 is Sunday
	nextSunday, err := nextCronTime("0 0 * * 7", "UTC", base)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC); !nextSunday.Equal(want) {
		t.Fatalf("got %s, want %s", nextSunday, want)
	}

	// Leap day Feb 29 (next is 2028-02-29)
	nextLeap, err := nextCronTime("0 12 29 2 *", "UTC", base)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2028, 2, 29, 12, 0, 0, 0, time.UTC); !nextLeap.Equal(want) {
		t.Fatalf("got %s, want %s", nextLeap, want)
	}

	// DST fall-back in America/New_York on 2026-11-01 (01:00 occurs twice: 05:00 UTC EDT and 06:00 UTC EST).
	// Verify advancing past the repeated 01:00 hour never loops and lands on 02:00 EST (07:00 UTC).
	for _, afterUTC := range []time.Time{
		time.Date(2026, 11, 1, 5, 15, 0, 0, time.UTC), // 01:15 EDT (first 01:xx)
		time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC),  // 01:00 EST (second 01:xx)
		time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC), // 01:30 EST (second 01:xx)
	} {
		nextDST, err := nextCronTime("0 2 * * *", "America/New_York", afterUTC)
		if err != nil {
			t.Fatal(err)
		}
		if want := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC); !nextDST.Equal(want) {
			t.Fatalf("DST fallback from %s: got %s, want %s", afterUTC, nextDST, want)
		}
	}

	for _, bad := range []struct {
		expr string
		tz   string
	}{
		{"", "UTC"},
		{"* * * *", "UTC"},
		{"60 * * * *", "UTC"},
		{"* 24 * * *", "UTC"},
		{"* * 0 * *", "UTC"},
		{"* * * 13 *", "UTC"},
		{"* * * * 8", "UTC"},
		{"*/0 * * * *", "UTC"},
		{"5-2 * * * *", "UTC"},
		{"10/2 * * * *", "UTC"},
		{"0 9 * * *", "Invalid/Zone_Name"},
	} {
		if _, err := nextCronTime(bad.expr, bad.tz, base); err == nil {
			t.Fatalf("expected error for cron=%q tz=%q", bad.expr, bad.tz)
		}
	}
}

func TestScheduleToolsDrainCoalescingAndContextProvenance(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	lease, err := s.Acquire(ctx, uuid.NewString(), "schedule-tools-test")
	if err != nil {
		t.Fatal(err)
	}

	convID := uuid.NewString()
	msgID := uuid.NewString()
	wsID := uuid.NewString()
	senderID := uuid.NewString()
	imSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/conversations/"+convID+"/messages/"+msgID {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": Message{
					ID:             msgID,
					ConversationID: convID,
					SenderID:       senderID,
					SenderName:     "Alice",
					SenderKind:     "human",
					Body:           "Set up a reminder and heartbeat",
					Seq:            1,
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer imSrv.Close()

	imc := &IM{
		BaseURL:    imSrv.URL,
		ServiceKey: "im-secret",
		AgentID:    lease.AgentID,
		HTTP:       imSrv.Client(),
		token:      "valid-principal-token",
		expiry:     time.Now().Add(time.Hour),
	}

	inMsg := Input{
		EventID:        "101",
		WorkspaceID:    wsID,
		ConversationID: convID,
		MessageID:      msgID,
		SenderID:       senderID,
		SenderName:     "Alice",
		SenderKind:     "human",
		Body:           "Set up a reminder and heartbeat",
		Seq:            1,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err = s.Ingest(ctx, lease, "101", []Input{inMsg}); err != nil {
		t.Fatal(err)
	}

	budgetBytes, err := newRunContextBudget(128000, true, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.StartRunWithContextPolicy(ctx, lease, []Input{inMsg}, contextProfileV1, budgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.configureRun(ctx, lease, run, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	runCtx := WithRun(ctx, run)

	toolBuilder := &Tools{
		Store:   s,
		Lease:   lease,
		IM:      imc,
		OwnerID: uuid.NewString(),
		AgentID: lease.AgentID,
	}
	schedTools, err := toolBuilder.BuildScheduleTools()
	if err != nil {
		t.Fatal(err)
	}
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: schedTools, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	invokeSched := func(callID, name, rawArgs string) (string, error) {
		out, err := node.Invoke(runCtx, &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:       callID,
				Type:     "function",
				Function: schema.FunctionCall{Name: name, Arguments: rawArgs},
			}},
		})
		if err != nil {
			return "", err
		}
		return out[0].Content, nil
	}

	// 1. runtime_clock deterministic receipt replay
	clockOut1, err := invokeSched("call-clock-1", "runtime_clock", `{"timezone":"Asia/Shanghai"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(clockOut1, `"ok":true`) || !strings.Contains(clockOut1, `"timezone":"Asia/Shanghai"`) || !strings.Contains(clockOut1, `"offsetSeconds":28800`) {
		t.Fatalf("unexpected runtime_clock output: %s", clockOut1)
	}
	time.Sleep(15 * time.Millisecond)
	clockOut2, err := invokeSched("call-clock-1", "runtime_clock", `{"timezone":"Asia/Shanghai"}`)
	if err != nil {
		t.Fatal(err)
	}
	if clockOut1 != clockOut2 {
		t.Fatalf("runtime_clock replay mismatch: %s vs %s", clockOut1, clockOut2)
	}

	// 2. schedule_create (once, interval, cron)
	onceOut, err := invokeSched("call-sched-once", "schedule_create", `{"kind":"once","delaySeconds":5,"prompt":"Check build status once"}`)
	if err != nil {
		t.Fatal(err)
	}
	onceID := stableScheduleID(run, "call-sched-once")
	if !strings.Contains(onceOut, `"ok":true`) || !strings.Contains(onceOut, onceID) {
		t.Fatalf("unexpected schedule_create once output: %s", onceOut)
	}

	intervalOut, err := invokeSched("call-sched-interval", "schedule_create", `{"kind":"interval","intervalSeconds":60,"maxRuns":3,"prompt":"Heartbeat check"}`)
	if err != nil {
		t.Fatal(err)
	}
	intervalID := stableScheduleID(run, "call-sched-interval")
	if !strings.Contains(intervalOut, `"ok":true`) || !strings.Contains(intervalOut, intervalID) {
		t.Fatalf("unexpected schedule_create interval output: %s", intervalOut)
	}

	cronOut, err := invokeSched("call-sched-cron", "schedule_create", `{"kind":"cron","cronExpr":"0 9 * * 1-5","timezone":"Asia/Shanghai","prompt":"Morning standup summary"}`)
	if err != nil {
		t.Fatal(err)
	}
	cronID := stableScheduleID(run, "call-sched-cron")
	if !strings.Contains(cronOut, `"ok":true`) || !strings.Contains(cronOut, cronID) {
		t.Fatalf("unexpected schedule_create cron output: %s", cronOut)
	}

	// 3. schedule_list returns active schedules ordered by next_run_at
	listOut, err := invokeSched("call-sched-list", "schedule_list", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listOut, onceID) || !strings.Contains(listOut, intervalID) || !strings.Contains(listOut, cronID) {
		t.Fatalf("schedule_list missing created schedules: %s", listOut)
	}

	// 4. schedule_cancel cancels cron schedule
	cancelOut, err := invokeSched("call-sched-cancel", "schedule_cancel", `{"scheduleId":"`+cronID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cancelOut, `"status":"canceled"`) {
		t.Fatalf("unexpected schedule_cancel output: %s", cancelOut)
	}

	// Finish initial Run so its session acceptance is recorded
	if err = s.FinishRun(ctx, lease, run, "completed", "", false); err != nil {
		t.Fatal(err)
	}

	// 5. Move once and interval schedules into the past (interval 5 minutes ago to verify missed-run coalescing)
	if _, err = s.DB.Exec(ctx, `UPDATE runtime_schedules SET next_run_at=clock_timestamp()-interval '10 seconds' WHERE id=$1`, onceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, `UPDATE runtime_schedules SET next_run_at=clock_timestamp()-interval '310 seconds' WHERE id=$1`, intervalID); err != nil {
		t.Fatal(err)
	}

	eng := &Engine{Store: s}
	if err = eng.drainSchedules(ctx, lease); err != nil {
		t.Fatal(err)
	}

	// Verify once schedule is now completed with run_count=1
	var onceStatus string
	var onceCount int
	if err = s.DB.QueryRow(ctx, `SELECT status,run_count FROM runtime_schedules WHERE id=$1`, onceID).Scan(&onceStatus, &onceCount); err != nil {
		t.Fatal(err)
	}
	if onceStatus != "completed" || onceCount != 1 {
		t.Fatalf("expected once schedule completed with run_count=1, got status=%s count=%d", onceStatus, onceCount)
	}

	// Verify interval schedule coalesced missed intervals into a single wakeup and advanced next_run_at > now
	var ivStatus string
	var ivCount int
	var ivNextAfterNow bool
	if err = s.DB.QueryRow(ctx, `SELECT status,run_count,next_run_at>clock_timestamp() FROM runtime_schedules WHERE id=$1`, intervalID).Scan(&ivStatus, &ivCount, &ivNextAfterNow); err != nil {
		t.Fatal(err)
	}
	if ivStatus != "active" || ivCount != 1 || !ivNextAfterNow {
		t.Fatalf("expected interval schedule active with run_count=1 and future next_run_at, got status=%s count=%d future=%v", ivStatus, ivCount, ivNextAfterNow)
	}

	// Verify pending inputs include the two TEAMIE_SCHEDULE_WAKEUP inputs
	pending, err := s.Pending(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending schedule.wakeup inputs, got %d", len(pending))
	}
	msgs := inputMessages(pending)
	if len(msgs) != 2 || !strings.HasPrefix(agenticText(msgs[0]), "TEAMIE_SCHEDULE_WAKEUP ") {
		t.Fatalf("expected TEAMIE_SCHEDULE_WAKEUP prefix, got %q", agenticText(msgs[0]))
	}

	// Start a wakeup Run and verify ContextArchive provenance resolution for TEAMIE_SCHEDULE_WAKEUP
	wakeupRun, err := s.StartRunWithContextPolicy(ctx, lease, pending[:1], contextProfileV1, budgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.configureRun(ctx, lease, wakeupRun, 1, 2, nil); err != nil {
		t.Fatal(err)
	}
	archive := &ContextArchive{Store: s, Lease: lease, IM: imc}
	sources, matched, err := archive.inputMessageSources(WithRun(ctx, wakeupRun), wakeupRun, msgs[0])
	if err != nil || !matched || len(sources) != 1 || sources[0].ConversationID != convID || sources[0].MessageID != msgID {
		t.Fatalf("expected TEAMIE_SCHEDULE_WAKEUP provenance to resolve to original IM source, got matched=%v sources=%+v err=%v", matched, sources, err)
	}

	// Verify cancelAgentTasks cancels remaining active schedules
	eng.cancelAgentTasks(ctx, lease.AgentID)
	var activeRemaining int
	if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM runtime_schedules WHERE agent_id=$1 AND status='active'`, lease.AgentID).Scan(&activeRemaining); err != nil {
		t.Fatal(err)
	}
	if activeRemaining != 0 {
		t.Fatalf("expected 0 active schedules after cancelAgentTasks, got %d", activeRemaining)
	}
}

func TestScheduleQuotaEnforcement(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	lease, err := s.Acquire(ctx, uuid.NewString(), "schedule-quota-test")
	if err != nil {
		t.Fatal(err)
	}
	inMsg := Input{
		EventID:        "201",
		WorkspaceID:    uuid.NewString(),
		ConversationID: uuid.NewString(),
		MessageID:      uuid.NewString(),
		SenderID:       uuid.NewString(),
		SenderName:     "Bob",
		SenderKind:     "human",
		Body:           "Create schedules",
		Seq:            1,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err = s.Ingest(ctx, lease, "201", []Input{inMsg}); err != nil {
		t.Fatal(err)
	}
	budgetBytes, err := newRunContextBudget(128000, true, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.StartRunWithContextPolicy(ctx, lease, []Input{inMsg}, contextProfileV1, budgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.configureRun(ctx, lease, run, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	runCtx := WithRun(ctx, run)
	schedTools, err := (&Tools{Store: s, Lease: lease, AgentID: lease.AgentID}).BuildScheduleTools()
	if err != nil {
		t.Fatal(err)
	}
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: schedTools, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	invokeCreate := func(callID, rawArgs string) (string, error) {
		out, err := node.Invoke(runCtx, &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:       callID,
				Type:     "function",
				Function: schema.FunctionCall{Name: "schedule_create", Arguments: rawArgs},
			}},
		})
		if err != nil {
			return "", err
		}
		return out[0].Content, nil
	}
	for i := 0; i < maxActiveSchedulesPerAgent; i++ {
		out, err := invokeCreate("quota-"+uuid.NewString(), `{"kind":"interval","intervalSeconds":120,"prompt":"tick"}`)
		if err != nil || !strings.Contains(out, `"ok":true`) {
			t.Fatalf("schedule %d failed: %v %s", i, err, out)
		}
	}
	overOut, err := invokeCreate("quota-overflow", `{"kind":"interval","intervalSeconds":120,"prompt":"overflow"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(overOut, `"error":"schedule_limit_exceeded"`) {
		t.Fatalf("expected schedule_limit_exceeded, got %s", overOut)
	}
}

func TestScheduledWakeupBatchCompactsWithin16KWindow(t *testing.T) {
	for _, human := range []bool{false, true} {
		t.Run(fmt.Sprint(human), func(t *testing.T) {
			p, archive, _ := testPipeline(t, 16384)
			sources := []InputSource{}
			for i := 0; i < 9; i++ {
				sources = append(sources, InputSource{
					EventID:        fmt.Sprint(100 + i),
					WorkspaceID:    "11111111-1111-4111-8111-111111111111",
					ConversationID: "22222222-2222-4222-8222-222222222222",
					MessageID:      fmt.Sprintf("33333333-3333-4333-8333-%012d", i),
					SenderID:       "44444444-4444-4444-8444-444444444444",
					SenderKind:     "human",
				})
			}
			inputs := []Input{}
			for i := 0; i < 16; i++ {
				inputs = append(inputs, Input{
					Kind:           "schedule.wakeup",
					EventID:        fmt.Sprint(200 + i),
					ScheduleID:     fmt.Sprintf("sched-batch-%02d", i),
					NotificationID: fmt.Sprintf("schedule:sched-batch-%02d:1", i),
					Body:           fmt.Sprintf("Scheduled heartbeat check #%02d", i),
					CreatedAt:      "2026-10-22T06:00:00Z",
					ParentRunID:    "55555555-5555-4555-8555-555555555555",
					SourceRefs:     sources,
				})
			}
			if human {
				inputs = append(inputs, Input{
					EventID:        "300",
					WorkspaceID:    sources[0].WorkspaceID,
					ConversationID: sources[0].ConversationID,
					MessageID:      "66666666-6666-4666-8666-666666666666",
					SenderID:       sources[0].SenderID,
					SenderKind:     "human",
					Body:           strings.Repeat("Handle this latest human instruction alongside scheduled wakeups. ", 15),
				})
			}
			ctx := WithRun(context.Background(), &Run{Inputs: inputs})
			messages := append([]*schema.AgenticMessage{schema.SystemAgenticMessage("trusted policy")}, inputMessages(inputs)...)
			tools := []*schema.ToolInfo{{Name: "fixture_manifest", Desc: strings.Repeat("manifest ", 2400)}}
			out, err := p.compact(ctx, messages, tools, false)
			if err != nil {
				t.Fatal("batched schedule.wakeup inputs failed compaction in 16K window", err)
			}
			m, err := measureContext(out, tools, p.cfg.Budget.OutputReserveTokens, false)
			if err != nil || !p.cfg.Budget.fits(m) {
				t.Fatal("compacted request exceeds 16K capacity", m, err)
			}
			current := agenticText(messages[len(messages)-1])
			foundLatest := false
			for _, message := range out {
				foundLatest = foundLatest || agenticText(message) == current
			}
			if !foundLatest {
				t.Fatal("lost the latest input message during batch wakeup compaction")
			}
			retained := ""
			for _, message := range out {
				retained += agenticText(message)
			}
			for _, text := range archive.content {
				retained += text
			}
			for i := 0; i < 16; i++ {
				if !strings.Contains(retained, fmt.Sprintf("sched-batch-%02d", i)) {
					t.Fatal("lost durable schedule wakeup provenance", i)
				}
			}
		})
	}
}

func TestScheduleToolsWithNativeToolReceiptsAndSourceAuthorization(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	lease, err := s.Acquire(ctx, uuid.NewString(), "schedule-worker-middleware-test")
	if err != nil {
		t.Fatal(err)
	}

	convID := uuid.NewString()
	msgID := uuid.NewString()
	wsID := uuid.NewString()
	senderID := uuid.NewString()
	var imQueries atomic.Int32
	var sourceRevoked atomic.Bool
	imSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/conversations/"+convID+"/messages/"+msgID {
			imQueries.Add(1)
			if sourceRevoked.Load() {
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "forbidden"}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": Message{
					ID:             msgID,
					ConversationID: convID,
					SenderID:       senderID,
					SenderName:     "Alice",
					SenderKind:     "human",
					Body:           "Create a schedule via worker middleware",
					Seq:            1,
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer imSrv.Close()

	imc := &IM{
		BaseURL:    imSrv.URL,
		ServiceKey: "im-secret",
		AgentID:    lease.AgentID,
		HTTP:       imSrv.Client(),
		token:      "valid-principal-token",
		expiry:     time.Now().Add(time.Hour),
	}

	inMsg := Input{
		EventID:        "301",
		WorkspaceID:    wsID,
		ConversationID: convID,
		MessageID:      msgID,
		SenderID:       senderID,
		SenderName:     "Alice",
		SenderKind:     "human",
		Body:           "Create a schedule via worker middleware",
		Seq:            1,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err = s.Ingest(ctx, lease, "301", []Input{inMsg}); err != nil {
		t.Fatal(err)
	}
	budgetBytes, err := newRunContextBudget(128000, true, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.StartRunWithContextPolicy(ctx, lease, []Input{inMsg}, contextProfileV1, budgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.configureRun(ctx, lease, run, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	runCtx := WithRun(ctx, run)

	schedTools, err := (&Tools{Store: s, Lease: lease, IM: imc, AgentID: lease.AgentID}).BuildScheduleTools()
	if err != nil {
		t.Fatal(err)
	}
	receipts := &nativeToolReceipts{Store: s, Lease: lease, IM: imc}
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: schedTools, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	schedID := stableScheduleID(run, "mw-create")

	for _, tc := range []struct {
		callID string
		name   string
		args   string
		expect string
	}{
		{"mw-clock", "runtime_clock", `{"timezone":"UTC"}`, `"ok":true`},
		{"mw-create", "schedule_create", `{"kind":"interval","intervalSeconds":60,"prompt":"Worker middleware wakeup"}`, schedID},
		{"mw-list", "schedule_list", `{}`, schedID},
		{"mw-cancel", "schedule_cancel", `{"scheduleId":"` + schedID + `"}`, `"status":"canceled"`},
	} {
		if !privateReadTool(tc.name) {
			t.Fatalf("expected privateReadTool(%q)=true so nativeToolReceipts does not double-record receipts", tc.name)
		}
		wrapped, err := receipts.WrapInvokableToolCall(runCtx, func(_ context.Context, _ string, _ ...tool.Option) (string, error) {
			out, invokeErr := node.Invoke(runCtx, &schema.Message{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:       tc.callID,
					Type:     "function",
					Function: schema.FunctionCall{Name: tc.name, Arguments: tc.args},
				}},
			})
			if invokeErr != nil {
				return "", invokeErr
			}
			return out[0].Content, nil
		}, &adk.ToolContext{Name: tc.name, CallID: tc.callID})
		if err != nil {
			t.Fatal(err)
		}
		out, err := wrapped(runCtx, tc.args)
		if err != nil || !strings.Contains(out, tc.expect) {
			t.Fatalf("tool %s through nativeToolReceipts failed: err=%v out=%s", tc.name, err, out)
		}
	}

	if err = s.FinishRun(ctx, lease, run, "completed", "", false); err != nil {
		t.Fatal(err)
	}

	// Re-activate schedule, drain wakeup, then revoke IM source and verify authorizeScheduleInput queries IM and rejects it.
	if _, err = s.DB.Exec(ctx, `UPDATE runtime_schedules SET status='active',next_run_at=clock_timestamp()-interval '5 seconds' WHERE id=$1`, schedID); err != nil {
		t.Fatal(err)
	}
	eng := &Engine{Store: s}
	if err = eng.drainSchedules(ctx, lease); err != nil {
		t.Fatal(err)
	}
	pending, err := s.Pending(ctx, lease)
	if err != nil || len(pending) != 1 {
		t.Fatalf("expected 1 pending wakeup, got %d err=%v", len(pending), err)
	}
	imQueries.Store(0)
	if err = eng.authorizeScheduleInput(ctx, imc, lease, pending[0]); err != nil {
		t.Fatalf("expected accessible source to pass authorizeScheduleInput, got %v", err)
	}
	if imQueries.Load() == 0 {
		t.Fatal("authorizeScheduleInput did not query IM source permissions")
	}

	sourceRevoked.Store(true)
	imQueries.Store(0)
	if err = eng.authorizeScheduleInput(ctx, imc, lease, pending[0]); !errors.Is(err, errContextUnavailable) {
		t.Fatalf("expected revoked source to return errContextUnavailable, got %v", err)
	}
	if imQueries.Load() == 0 {
		t.Fatal("authorizeScheduleInput did not query IM when rejecting revoked source")
	}
}

type scheduleWorkerGateway struct {
	*fixtureGateway
	step atomic.Int32
}

func (g *scheduleWorkerGateway) Chat(_ context.Context, r *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	g.mu.Lock()
	g.calls++
	g.requests = append(g.requests, []byte(r.RequestJson))
	cfgID := g.configIDLocked()
	g.mu.Unlock()

	var request struct {
		Messages []chatMessage `json:"messages"`
	}
	if err := decodeFixtureRequest(r.RequestJson, &request); err != nil {
		return nil, err
	}
	var result chatMessage
	switch g.step.Add(1) {
	case 1:
		result = chatMessage{
			Role: "assistant",
			ToolCalls: []schema.ToolCall{
				{ID: "w-clock", Type: "function", Function: schema.FunctionCall{Name: "runtime_clock", Arguments: `{"timezone":"UTC"}`}},
				{ID: "w-create-keep", Type: "function", Function: schema.FunctionCall{Name: "schedule_create", Arguments: `{"kind":"interval","intervalSeconds":60,"prompt":"Wakeup after source check"}`}},
				{ID: "w-create-cancel", Type: "function", Function: schema.FunctionCall{Name: "schedule_create", Arguments: `{"kind":"once","delaySeconds":300,"prompt":"To be canceled"}`}},
				{ID: "w-list", Type: "function", Function: schema.FunctionCall{Name: "schedule_list", Arguments: `{}`}},
			},
		}
	case 2:
		// Find w-create-cancel's scheduleId from tool result and cancel it
		cancelID := ""
		for _, m := range request.Messages {
			if m.Role == "tool" && strings.Contains(m.Content, "To be canceled") {
				var obs struct {
					OK   bool `json:"ok"`
					Data struct {
						Schedule scheduleView `json:"schedule"`
					} `json:"data"`
				}
				if json.Unmarshal([]byte(m.Content), &obs) == nil && obs.Data.Schedule.ScheduleID != "" {
					cancelID = obs.Data.Schedule.ScheduleID
					break
				}
			}
		}
		result = chatMessage{
			Role: "assistant",
			ToolCalls: []schema.ToolCall{
				{ID: "w-cancel", Type: "function", Function: schema.FunctionCall{Name: "schedule_cancel", Arguments: `{"scheduleId":"` + cancelID + `"}`}},
			},
		}
	default:
		result = chatMessage{Role: "assistant", Content: "Done"}
	}
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": result, "finish_reason": "stop"}},
		"usage":   map[string]int{"prompt_tokens": 20, "completion_tokens": 5, "total_tokens": 25},
	})
	return &wire.GatewayChatResponse{Ok: true, ResponseJson: modelFixtureResponse(string(b)), ModelConfigId: cfgID}, nil
}

func TestWorkerScheduleToolsAndRevokedWakeupEndToEnd(t *testing.T) {
	s := testStore(t)
	e, f, baseGW, _ := fixtureEngine(t, s)
	flagsPath := filepath.Join(t.TempDir(), "flags.json")
	if err := os.WriteFile(flagsPath, []byte(`{"revision":"schedule-worker-v1","flags":{"agents.runtime.v3":true,"agents.tasks.execute":true,"agents.context.v1":true,"agents.schedule.v1":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	e.Config.FlagsFile = flagsPath
	gw := &scheduleWorkerGateway{fixtureGateway: baseGW}
	e.Gateway = gw

	f.add("Please check clock and set up schedules", "")
	cancel, done := startFixtureWorker(t, e, f)
	waitFor(t, func() bool { return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) == 1 })
	cancel()
	<-done

	if count(t, s, `SELECT count(*) FROM runtime_tool_calls WHERE name IN ('runtime_clock','schedule_create','schedule_list','schedule_cancel') AND result IS NOT NULL`) != 5 {
		t.Fatal("real Worker did not commit all 5 schedule tool receipts without conflict")
	}
	if count(t, s, `SELECT count(*) FROM runtime_schedules WHERE status='active'`) != 1 || count(t, s, `SELECT count(*) FROM runtime_schedules WHERE status='canceled'`) != 1 {
		t.Fatal("expected 1 active schedule and 1 canceled schedule after Worker tool round")
	}

	// Revoke the originating IM message and make the remaining active schedule due.
	f.mu.Lock()
	f.messages = map[string]*Message{}
	readsBefore := f.reads
	f.mu.Unlock()
	gw.mu.Lock()
	callsBefore := gw.calls
	gw.mu.Unlock()

	if _, err := s.DB.Exec(context.Background(), `UPDATE runtime_schedules SET next_run_at=clock_timestamp()-interval '10 seconds' WHERE status='active'`); err != nil {
		t.Fatal(err)
	}

	cancel2, done2 := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_inputs WHERE payload->>'kind'='schedule.wakeup' AND state='skipped'`) == 1
	})
	cancel2()
	<-done2

	f.mu.Lock()
	readsAfter := f.reads
	f.mu.Unlock()
	gw.mu.Lock()
	callsAfter := gw.calls
	gw.mu.Unlock()

	if readsAfter <= readsBefore {
		t.Fatalf("expected Worker wakeup validation to query IM permissions, readsBefore=%d readsAfter=%d", readsBefore, readsAfter)
	}
	if callsAfter != callsBefore {
		t.Fatalf("revoked schedule wakeup reached the model: callsBefore=%d callsAfter=%d", callsBefore, callsAfter)
	}
	if count(t, s, `SELECT count(*) FROM runtime_schedules WHERE status='active'`) != 0 {
		t.Fatal("revoked schedule remained active after source permission failure")
	}
}

func review59StartScheduleRun(t *testing.T, s *Store, l *Lease, in Input) *Run {
	t.Helper()
	ctx := context.Background()
	if err := s.Ingest(ctx, l, in.EventID, []Input{in}); err != nil {
		t.Fatal(err)
	}
	budget, err := newRunContextBudget(128000, false, false, false, true)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.StartRunWithContextPolicy(ctx, l, []Input{in}, contextProfileV1, budget)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.configureRun(ctx, l, run, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	return run
}

func review59InvokeSchedule(t *testing.T, node *compose.ToolsNode, run *Run, call, name string, args any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := node.Invoke(WithRun(context.Background(), run), &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: call, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: string(raw)}}}})
	if err != nil {
		return "", err
	}
	return out[0].Content, nil
}

func TestReview59CreateReceiptWriteFailureRecovery(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	l, err := s.Acquire(ctx, uuid.NewString(), "review59-receipt-failure")
	if err != nil {
		t.Fatal(err)
	}
	in := Input{EventID: "1", WorkspaceID: uuid.NewString(), ConversationID: uuid.NewString(), MessageID: uuid.NewString(), SenderID: uuid.NewString(), SenderKind: "human", Body: "Schedule a future check", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	run := review59StartScheduleRun(t, s, l, in)
	available, err := (&Tools{Store: s, Lease: l, AgentID: l.AgentID}).BuildScheduleTools()
	if err != nil {
		t.Fatal(err)
	}
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: available, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	// The schedule transaction commits, but the following ToolResult UPDATE fails.
	// This is the same durable state as a process stopping between those commits.
	_, err = s.DB.Exec(ctx, `CREATE FUNCTION review59_fail_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.name='schedule_create' AND OLD.result IS NULL AND NEW.result IS NOT NULL THEN RAISE EXCEPTION 'injected receipt write failure'; END IF; RETURN NEW; END; $$; CREATE TRIGGER review59_fail_receipt BEFORE UPDATE ON runtime_tool_calls FOR EACH ROW EXECUTE FUNCTION review59_fail_receipt();`)
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(150 * time.Millisecond)
	args := scheduleCreateArgs{Kind: "once", RunAt: due.Format(time.RFC3339Nano), Prompt: "Receipt recovery fixture"}
	_, err = review59InvokeSchedule(t, node, run, "absolute-time-call", "schedule_create", args)
	if err == nil {
		t.Fatal("expected injected ToolResult failure")
	}
	if count(t, s, `SELECT count(*) FROM runtime_schedules WHERE agent_id=$1`, l.AgentID) != 1 || count(t, s, `SELECT count(*) FROM runtime_tool_calls WHERE run_id=$1 AND call_id='absolute-time-call' AND result IS NULL`, run.ID) != 1 {
		t.Fatal("fault injection did not reach the post-effect/pre-receipt state")
	}
	if _, err = s.DB.Exec(ctx, `DROP TRIGGER review59_fail_receipt ON runtime_tool_calls; DROP FUNCTION review59_fail_receipt();`); err != nil {
		t.Fatal(err)
	}
	if delay := time.Until(due.Add(25 * time.Millisecond)); delay > 0 {
		time.Sleep(delay)
	}
	out, err := review59InvokeSchedule(t, node, run, "absolute-time-call", "schedule_create", args)
	if err != nil || !strings.Contains(out, `"ok":true`) {
		t.Fatalf("committed schedule recovered as failure: response=%s err=%v schedules=%d", out, err, count(t, s, `SELECT count(*) FROM runtime_schedules WHERE agent_id=$1 AND status='active'`, l.AgentID))
	}
}

func TestReview59ScheduleListRechecksRevokedSources(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	f, server := newFixtureIM(t)
	l, err := s.Acquire(ctx, f.agent, "review59-schedule-list")
	if err != nil {
		t.Fatal(err)
	}
	imc := &IM{BaseURL: server.URL, ServiceKey: "fixture-im-secret", AgentID: f.agent, HTTP: server.Client(), token: strings.Repeat("a", 64), expiry: time.Now().Add(time.Hour)}
	available, err := (&Tools{Store: s, Lease: l, AgentID: f.agent, IM: imc}).BuildScheduleTools()
	if err != nil {
		t.Fatal(err)
	}
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: available, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	toInput := func(id string, msg Message) Input {
		return Input{EventID: id, WorkspaceID: f.workspace, ConversationID: msg.ConversationID, MessageID: msg.ID, SenderID: msg.SenderID, SenderKind: msg.SenderKind, Body: msg.Body, CreatedAt: msg.CreatedAt}
	}
	sourceA := behaviorSource(t, f, "Private source A", "", "")
	runA := review59StartScheduleRun(t, s, l, toInput("1", sourceA))
	out, err := review59InvokeSchedule(t, node, runA, "schedule-a", "schedule_create", scheduleCreateArgs{Kind: "once", DelaySeconds: 3600, Prompt: "PRIVATE_FROM_REVOKED_SOURCE_A"})
	if err != nil || !strings.Contains(out, `"ok":true`) {
		t.Fatalf("create fixture: %s %v", out, err)
	}
	if err = s.FinishRun(ctx, l, runA, "completed", "", false); err != nil {
		t.Fatal(err)
	}
	sourceB := behaviorSource(t, f, "Allowed source B", uuid.NewString(), "")
	runB := review59StartScheduleRun(t, s, l, toInput("2", sourceB))
	out, err = review59InvokeSchedule(t, node, runB, "list-before-revocation", "schedule_list", scheduleListArgs{})
	if err != nil || !strings.Contains(out, "PRIVATE_FROM_REVOKED_SOURCE_A") {
		t.Fatal("initial list fixture did not expose its authorized schedule", err)
	}
	// Also archive the schedule_list output into ContextArchive under runB and verify it retains sourceA's provenance.
	archive := &ContextArchive{Store: s, Lease: l, IM: imc}
	archivedRef, err := archive.ArchiveToolPath(WithRun(ctx, runB), "tool_trunc", contextRealIMDetail("schedule_list", "list-before-revocation", scheduleListArgs{}, out), out)
	if err != nil {
		t.Fatalf("ArchiveToolPath for schedule_list failed: %v", err)
	}

	f.mu.Lock()
	delete(f.messages, sourceA.ID)
	readsBefore := f.reads
	f.mu.Unlock()
	if _, err = imc.Message(ctx, sourceA.ConversationID, sourceA.ID); err == nil {
		t.Fatal("source A was not revoked")
	}
	if _, err = imc.Message(ctx, sourceB.ConversationID, sourceB.ID); err != nil {
		t.Fatal("source B should remain accessible", err)
	}
	f.mu.Lock()
	readsBefore = f.reads
	f.mu.Unlock()
	for _, call := range []string{"fresh-list-after-revocation", "list-before-revocation"} {
		f.mu.Lock()
		callReadsBefore := f.reads
		f.mu.Unlock()
		out, err = review59InvokeSchedule(t, node, runB, call, "schedule_list", scheduleListArgs{})
		f.mu.Lock()
		callReads := f.reads - callReadsBefore
		f.mu.Unlock()
		if callReads == 0 {
			t.Errorf("call=%s did not query IM source permissions", call)
		}
		if err != nil {
			continue
		}
		if strings.Contains(out, "PRIVATE_FROM_REVOKED_SOURCE_A") {
			f.mu.Lock()
			reads := f.reads - readsBefore
			f.mu.Unlock()
			t.Errorf("call=%s returned revoked schedule prompt; IM source reads=%d", call, reads)
		}
	}
	if _, err = archive.Read(WithRun(ctx, runB), archivedRef, 0, 1024); !errors.Is(err, errContextUnavailable) {
		t.Fatalf("expected ContextArchive.Read on archived schedule_list from revoked sourceA to fail with errContextUnavailable, got %v", err)
	}
}

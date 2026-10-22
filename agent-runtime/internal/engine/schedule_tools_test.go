package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
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

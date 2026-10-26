package eino

import (
	"context"
	"strings"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"testing"
)

func TestPublishedScheduleAndMentionProtocolRemainsStable(t *testing.T) {
	if digest([]byte(mentionGuidance)) != "f81d2e2c6dba1a4bdf20b5ea3e5857cd90bc9583a25860f322c74492121d86e6" || digest([]byte(scheduleWakeupGuidance)) != "5c3b3d33544a7a29f5df5024015336b068eea1c6e103195d02e91228330942b0" {
		t.Fatal("published Run prompt bytes changed")
	}
	for _, autonomous := range []bool{false, true} {
		available, err := (&Tools{}).BuildScheduleTools(autonomous)
		if err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, item := range available {
			info, err := item.Info(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, info.Name)
		}
		want := "runtime_clock,schedule_create,schedule_list,schedule_cancel"
		if autonomous {
			want = "runtime_clock,schedule_create,schedule_update,schedule_list,schedule_cancel"
		}
		if strings.Join(names, ",") != want {
			t.Fatalf("tool manifest changed: %v", names)
		}
	}
	budget := ContextBudgetSnapshot{DirectMessages: true}
	if strings.Contains(contextInstruction(&wire.AgentDefinition{}, budget), "Cold-start 1-hour heartbeat") {
		t.Fatal("advertised disabled scheduling")
	}
	budget.ScheduledWakeups = true
	if !strings.Contains(contextInstruction(&wire.AgentDefinition{}, budget), "Cold-start 1-hour heartbeat") {
		t.Fatal("missing admitted heartbeat guidance")
	}
}

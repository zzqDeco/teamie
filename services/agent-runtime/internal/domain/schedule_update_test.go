package domain

import (
	"testing"
	"time"
)

func TestScheduleUpdateKeepsFiniteQuotaAndDueTime(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	interval := int64(3600)
	maxRuns := 3
	for _, kind := range []string{"once", "interval", "cron"} {
		before := ScheduleItem{Kind: kind, IntervalSeconds: &interval, CronExpr: "0 * * * *", Timezone: "UTC", Prompt: "old", MaxRuns: &maxRuns, RunCount: 1, NextRunAt: now.Add(15 * time.Minute).Format(time.RFC3339)}
		got, code := ValidateScheduleUpdateSpec(before, ScheduleSpec{Prompt: "new"}, now)
		if code != "" || got.Prompt != "new" || got.NextRunAt.Format(time.RFC3339) != before.NextRunAt || got.MaxRuns == nil || (kind != "once" && *got.MaxRuns != 3) {
			t.Fatalf("%s partial update lost schedule state: %#v %s", kind, got, code)
		}
	}
}

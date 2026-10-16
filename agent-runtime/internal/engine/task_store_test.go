package engine

import (
	"context"
	bg "github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/backgroundtask/storetest"
	"github.com/google/uuid"
	"testing"
	"time"
)

func newTaskTestStore(tb testing.TB) *TaskStore {
	tb.Helper()
	t := tb.(*testing.T)
	return &TaskStore{Store: testStore(t), AgentID: uuid.NewString()}
}
func expireTask(tb testing.TB, s bg.TaskStore, t *bg.Task) {
	tb.Helper()
	p := s.(*TaskStore)
	if _, err := p.Store.DB.Exec(context.Background(), `UPDATE runtime_tasks SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, t.Spec.ID); err != nil {
		tb.Fatal(err)
	}
}
func TestPostgresNativeTaskStore(t *testing.T) {
	storetest.RunTaskStoreConformance(t, storetest.TaskStoreConfig{New: func(tb testing.TB) bg.TaskStore { return newTaskTestStore(tb) }, ExpireActiveAttempt: expireTask})
}
func TestPostgresNativeTaskEvents(t *testing.T) {
	storetest.RunTaskEventStoreConformance(t, storetest.TaskEventStoreConfig{New: func(tb testing.TB) (bg.TaskStore, bg.TaskEventStore) { s := newTaskTestStore(tb); return s, s }})
}
func TestPostgresNativeNotifications(t *testing.T) {
	storetest.RunNotificationOutboxConformance(t, storetest.NotificationOutboxConfig{New: func(tb testing.TB) (bg.TaskStore, bg.NotificationOutbox) { s := newTaskTestStore(tb); return s, s }, ExpireLease: func(tb testing.TB, o bg.NotificationOutbox, _ time.Duration) {
		s := o.(*TaskStore)
		if _, err := s.Store.DB.Exec(context.Background(), `UPDATE runtime_task_notifications SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
			tb.Fatal(err)
		}
	}})
}
func TestPostgresNativeNotificationWriter(t *testing.T) {
	storetest.RunNotificationWriterConformance(t, storetest.NotificationWriterConfig{New: func(tb testing.TB) (bg.TaskStore, bg.NotificationOutbox) { s := newTaskTestStore(tb); return s, s }, ExpireActiveAttempt: expireTask})
}

package eino

import (
	"context"
	"errors"
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

// Eino .35's harness requests 20ms leases while asserting several PostgreSQL
// round trips before Ack. Expiry is already controlled by its SQL hook, so use
// a long test lease rather than depending on CI scheduling to beat that window.
type conformanceNotificationOutbox struct {
	*TaskStore
	receiveDelay time.Duration
}

func (o *conformanceNotificationOutbox) Receive(ctx context.Context, request *bg.ReceiveNotificationsRequest) (*bg.ReceiveNotificationsResult, error) {
	if request == nil || request.LeaseDuration <= 0 {
		return o.TaskStore.Receive(ctx, request)
	}
	copied := *request
	copied.LeaseDuration = time.Minute
	result, err := o.TaskStore.Receive(ctx, &copied)
	if err == nil && o.receiveDelay > 0 {
		// A real delayed database round trip exceeds the harness's original
		// lease. All exclusion/redelivery/stale-receipt/Ack assertions still run.
		_, err = o.Store.DB.Exec(ctx, `SELECT pg_sleep($1)`, o.receiveDelay.Seconds())
	}
	return result, err
}

func TestPostgresNativeNotifications(t *testing.T) {
	for _, test := range []struct {
		name  string
		delay time.Duration
	}{{"normal", 0}, {"slow_round_trips", 30 * time.Millisecond}} {
		t.Run(test.name, func(t *testing.T) {
			storetest.RunNotificationOutboxConformance(t, storetest.NotificationOutboxConfig{New: func(tb testing.TB) (bg.TaskStore, bg.NotificationOutbox) {
				s := newTaskTestStore(tb)
				return s, &conformanceNotificationOutbox{TaskStore: s, receiveDelay: test.delay}
			}, ExpireLease: func(tb testing.TB, o bg.NotificationOutbox, _ time.Duration) {
				s := o.(*conformanceNotificationOutbox)
				if _, err := s.Store.DB.Exec(context.Background(), `UPDATE runtime_task_notifications SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
					tb.Fatal(err)
				}
			}})
		})
	}
}

func TestPostgresNativeNotificationLeaseStillHonorsRequestedDuration(t *testing.T) {
	s := newTaskTestStore(t)
	ctx := context.Background()
	_, err := s.Create(ctx, &bg.CreateTaskRequest{Spec: bg.Spec{ID: uuid.NewString(), ExecutorKey: "fixture", SessionID: "fixture"}, LeaseExpiryPolicy: bg.LeaseExpiryRetry})
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := s.Receive(ctx, &bg.ReceiveNotificationsRequest{Limit: 1, LeaseDuration: 20 * time.Millisecond})
	if err != nil || len(delivered.Deliveries) != 1 {
		t.Fatal("initial notification was not leased", err)
	}
	if _, err = s.Store.DB.Exec(ctx, `SELECT pg_sleep(0.03)`); err != nil {
		t.Fatal(err)
	}
	if err = s.Ack(ctx, delivered.Deliveries[0].Receipt); !errors.Is(err, bg.ErrLeaseLost) {
		t.Fatal("production outbox did not expire the caller's 20ms lease", err)
	}
}

func TestPostgresNativeNotificationWriter(t *testing.T) {
	storetest.RunNotificationWriterConformance(t, storetest.NotificationWriterConfig{New: func(tb testing.TB) (bg.TaskStore, bg.NotificationOutbox) { s := newTaskTestStore(tb); return s, s }, ExpireActiveAttempt: expireTask})
}

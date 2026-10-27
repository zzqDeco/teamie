package eino

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"teamie/services/agent-runtime/internal/domain"
)

func TestReadReceiptsCoalesceRetriesAndPreserveInflightHigherCursor(t *testing.T) {
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	sender := newReadReceiptSender()
	sender.now = func() time.Time { return now }
	conv := uuid.NewString()
	sender.recordInputs([]Input{{ConversationID: conv, Seq: 1}, {ConversationID: conv, Seq: 3}, {Kind: "task.notification", ConversationID: conv, Seq: 100}})
	if next, wait := sender.take(now); next != nil || wait != 2*time.Second {
		t.Fatalf("initial coalescing: %+v %v", next, wait)
	}
	now = now.Add(500 * time.Millisecond)
	sender.recordInputs([]Input{{ConversationID: conv, Seq: 6}})
	now = now.Add(1500 * time.Millisecond)
	target, _ := sender.take(now)
	if target == nil || target.seq != 6 {
		t.Fatalf("coalesced target: %+v", target)
	}
	sender.complete(target, &APIError{Status: 503}, false)
	now = now.Add(time.Second)
	if next, _ := sender.take(now); next != nil {
		t.Fatal("failed attempts must also observe the two-second interval")
	}
	now = now.Add(time.Second)
	target, _ = sender.take(now)
	if target == nil || target.seq != 6 {
		t.Fatalf("retry lost pending sequence: %+v", target)
	}
	sender.recordInputs([]Input{{ConversationID: conv, Seq: 8}})
	sender.complete(target, nil, false)
	now = now.Add(2 * time.Second)
	target, _ = sender.take(now)
	if target == nil || target.seq != 8 {
		t.Fatalf("in-flight acknowledgement consumed a newer input: %+v", target)
	}
	sender.complete(target, nil, false)
	now = now.Add(time.Minute)
	if next, _ := sender.take(now); next != nil {
		t.Fatalf("acknowledged sequence was resent: %+v", next)
	}
}

func TestReadReceiptsDeniedCursorIsSuppressedOnlyUntilHigherInput(t *testing.T) {
	now := time.Now()
	sender := newReadReceiptSender()
	sender.now = func() time.Time { return now }
	conv := uuid.NewString()
	sender.recordInputs([]Input{{ConversationID: conv, Seq: 1}})
	now = now.Add(2 * time.Second)
	target, _ := sender.take(now)
	sender.complete(target, &APIError{Status: 404}, true)
	if sender.entries[conv].ackedSeq != 0 {
		t.Fatal("denied read was recorded as a successful acknowledgement")
	}
	now = now.Add(time.Minute)
	sender.recordInputs([]Input{{ConversationID: conv, Seq: 1}})
	if next, _ := sender.take(now); next != nil {
		t.Fatal("repeated recovery of denied input caused a busy retry")
	}
	sender.recordInputs([]Input{{ConversationID: conv, Seq: 2}})
	if next, _ := sender.take(now); next == nil || next.seq != 2 {
		t.Fatalf("new durable input remained suppressed: %+v", next)
	}
}

type receiptTransport func(*http.Request) (*http.Response, error)

func (fn receiptTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func startFastReadSender(t *testing.T, store *Store, lease *Lease, markRead func(context.Context, string, int64) error) (*readReceiptSender, func()) {
	t.Helper()
	sender := newReadReceiptSender()
	sender.interval = 20 * time.Millisecond
	stop := sender.start(context.Background(), store, lease, func(context.Context) error { return nil }, markRead)
	t.Cleanup(stop)
	return sender, stop
}

func receivedSeq(f *fixtureIM, conv string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readReceipts[conv]
}

// A blocked read POST cannot hold up the next committed inbox page, and worker
// shutdown must cancel and join the request before releasing its lease.
func TestReadReceiptsSlowPOSTDoesNotBlockIngestAndStopJoins(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	engine, im, _, _ := fixtureEngine(t, store)
	lease, err := store.Acquire(ctx, im.agent, "read-receipt-ingest")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Release(ctx, lease) })
	started := make(chan struct{})
	var once sync.Once
	var attempts atomic.Int32
	imc := &IM{BaseURL: engine.Config.IMURL, AgentID: im.agent, Identity: engine.Identity, ServiceKey: engine.Config.IMToken, HTTP: &http.Client{Transport: receiptTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/read") {
			attempts.Add(1)
			once.Do(func() { close(started) })
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return http.DefaultTransport.RoundTrip(r)
	})}}
	sender, stop := startFastReadSender(t, store, lease, imc.MarkRead)
	im.add("first input", "")
	inputs, err := engine.ingest(ctx, imc, lease)
	if err != nil {
		t.Fatal(err)
	}
	sender.recordInputs(inputs)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("read POST never started")
	}
	im.add("second input while read is blocked", "")
	done := make(chan error, 1)
	go func() {
		inputs, err := engine.ingest(ctx, imc, lease)
		if err == nil {
			sender.recordInputs(inputs)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(150 * time.Millisecond):
		stop()
		<-done
		t.Fatal("durable ingest remained blocked on read POST")
	}
	if cursor, err := store.Cursor(ctx, lease); err != nil || cursor != "2" {
		t.Fatalf("durable cursor=%s err=%v", cursor, err)
	}
	joined := make(chan struct{})
	go func() { stop(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("sender did not cancel and join its in-flight read")
	}
	sender.recordInputs([]Input{{ConversationID: im.conversation, Seq: 100}})
	if attempts.Load() != 1 {
		t.Fatalf("stopped sender kept sending: %d", attempts.Load())
	}
}

func proxyReadReceipts(t *testing.T, engine *Engine, read func(http.ResponseWriter, *http.Request) bool) {
	t.Helper()
	origin, err := url.Parse(engine.Config.IMURL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(origin)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/read") && read(w, r) {
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	engine.Config.IMURL = server.URL
}

func otherReceiptEngine(engine *Engine) *Engine {
	return &Engine{Store: engine.Store, Config: engine.Config, Control: engine.Control, Gateway: engine.Gateway, Sandbox: engine.Sandbox, Identity: engine.Identity, owner: uuid.NewString()}
}

// No newer message is needed to finish confirmation after a Runtime restart.
func TestReadReceiptsWorkerRestartRecoversCoalescedDurableInput(t *testing.T) {
	store := testStore(t)
	engine, im, gateway, _ := fixtureEngine(t, store)
	gateway.ready = false
	im.add("first", "")
	cancel, done := startFixtureWorker(t, engine, im)
	waitFor(t, func() bool { return receivedSeq(im, im.conversation) == 1 })
	im.add("second", "")
	waitFor(t, func() bool {
		return count(t, store, "SELECT cursor FROM runtime_agents WHERE agent_id=$1", im.agent) == 2
	})
	cancel()
	<-done
	if seq := receivedSeq(im, im.conversation); seq != 1 {
		t.Fatalf("test missed the coalescing fault window: readSeq=%d", seq)
	}
	restarted := otherReceiptEngine(engine)
	_, _ = startFixtureWorker(t, restarted, im)
	waitFor(t, func() bool { return receivedSeq(im, im.conversation) == 2 })
}

func TestReadReceiptsSameEngineNewLeaseRecoversAfterDeniedRead(t *testing.T) {
	for _, status := range []int{403, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			store := testStore(t)
			engine, im, gateway, _ := fixtureEngine(t, store)
			gateway.ready = false
			var deny atomic.Bool
			deny.Store(true)
			var denied atomic.Int32
			proxyReadReceipts(t, engine, func(w http.ResponseWriter, _ *http.Request) bool {
				if deny.Load() {
					denied.Add(1)
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{"error":{"code":"not_found"}}`)
					return true
				}
				return false
			})
			im.add("durably received before access changed", "")
			cancel, done := startFixtureWorker(t, engine, im)
			waitFor(t, func() bool { return denied.Load() > 0 })
			cancel()
			<-done
			deny.Store(false)
			_, _ = startFixtureWorker(t, engine, im)
			waitFor(t, func() bool { return receivedSeq(im, im.conversation) == 1 })
		})
	}
}

func TestReadReceiptsReacquiredWorkerRecoversAnotherOwnersInput(t *testing.T) {
	store := testStore(t)
	engine, im, gateway, _ := fixtureEngine(t, store)
	gateway.ready = false
	var fail atomic.Bool
	var failures atomic.Int32
	proxyReadReceipts(t, engine, func(w http.ResponseWriter, _ *http.Request) bool {
		if fail.Load() {
			failures.Add(1)
			w.WriteHeader(503)
			return true
		}
		return false
	})
	im.add("first owner input", "")
	cancel, done := startFixtureWorker(t, engine, im)
	waitFor(t, func() bool { return receivedSeq(im, im.conversation) == 1 })
	cancel()
	<-done
	fail.Store(true)
	im.add("another owner received this", "")
	other := otherReceiptEngine(engine)
	cancel, done = startFixtureWorker(t, other, im)
	waitFor(t, func() bool { return failures.Load() > 0 })
	cancel()
	<-done
	fail.Store(false)
	_, _ = startFixtureWorker(t, engine, im)
	waitFor(t, func() bool { return receivedSeq(im, im.conversation) == 2 })
}

func TestReadReceiptInputPagesIncludeEveryStateAndAdvanceInvalidRows(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	agent, conv := uuid.NewString(), uuid.NewString()
	lease, err := store.Acquire(ctx, agent, "receipt-pages")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Release(ctx, lease)
	inputs := []Input{
		{EventID: "1", ConversationID: "invalid", Seq: 1},
		{EventID: "2", ConversationID: conv, Seq: 0},
		{EventID: "3", ConversationID: conv, Seq: 3},
		{EventID: "4", ConversationID: conv, Seq: 400, Kind: "task.notification"},
		{EventID: "5", ConversationID: conv, Seq: 5},
		{EventID: "6", ConversationID: conv, Seq: 6},
		{EventID: "7", ConversationID: conv, Seq: 700, Kind: "schedule.wakeup"},
		{EventID: "10", ConversationID: conv, Seq: 10},
	}
	if err := store.Ingest(ctx, lease, "10", inputs); err != nil {
		t.Fatal(err)
	}
	for eventID, state := range map[string]string{"3": "done", "5": "failed", "6": "skipped"} {
		if _, err := store.DB.Exec(ctx, "UPDATE runtime_inputs SET state=$3 WHERE agent_id=$1 AND event_id=$2", agent, eventID, state); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.ReadReceiptInputs(ctx, lease, "0", "6", 2)
	if err != nil || len(page.Items) != 0 || !page.HasMore || page.NextEventID != "2" {
		t.Fatalf("filtered page did not advance: %+v %v", page, err)
	}
	var ids []string
	after := page.NextEventID
	for {
		page, err = store.ReadReceiptInputs(ctx, lease, after, "10", 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			ids = append(ids, item.EventID)
		}
		if !page.HasMore {
			break
		}
		after = page.NextEventID
	}
	if fmt.Sprint(ids) != "[3 5 6 10]" {
		t.Fatalf("states/kinds/numeric ordering: %v", ids)
	}
	page, err = store.ReadReceiptInputs(ctx, lease, "5", "6", 2)
	if err != nil || len(page.Items) != 1 || page.Items[0].Seq != 6 {
		t.Fatalf("fixed upper bound was not respected: %+v %v", page, err)
	}
	store.Release(ctx, lease)
	if _, err := store.ReadReceiptInputs(ctx, lease, "0", "10", 2); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("old lease read recovery page: %v", err)
	}
}

type receiptRecoveryRepository struct {
	domain.RuntimeRepository
	calls atomic.Int32
	block chan struct{}
}

func (r *receiptRecoveryRepository) ListReadReceiptInputs(ctx context.Context, lease *Lease, after, through string, limit int) (domain.ReadReceiptInputPage, error) {
	if r.calls.Add(1) == 1 {
		if r.block != nil {
			close(r.block)
			<-ctx.Done()
			return domain.ReadReceiptInputPage{}, ctx.Err()
		}
		return domain.ReadReceiptInputPage{}, errors.New("temporary recovery failure")
	}
	return r.RuntimeRepository.ListReadReceiptInputs(ctx, lease, after, through, limit)
}

func TestReadReceiptsRecoveryRetriesWithoutNewSyncAndCancels(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprint(blocked), func(t *testing.T) {
			ctx := context.Background()
			store := testStore(t)
			agent, conv := uuid.NewString(), uuid.NewString()
			lease, err := store.Acquire(ctx, agent, "recovery-errors")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Release(ctx, lease) })
			if err := store.Ingest(ctx, lease, "1", []Input{{EventID: "1", ConversationID: conv, Seq: 1}}); err != nil {
				t.Fatal(err)
			}
			repo := &receiptRecoveryRepository{RuntimeRepository: store.DB}
			if blocked {
				repo.block = make(chan struct{})
			}
			store.DB = repo
			var ack atomic.Int64
			_, stop := startFastReadSender(t, store, lease, func(_ context.Context, _ string, seq int64) error { ack.Store(seq); return nil })
			if !blocked {
				waitFor(t, func() bool { return ack.Load() == 1 })
				return
			}
			<-repo.block
			// Recovery has no write transaction/lock around the inbox cursor.
			if err := store.Ingest(ctx, lease, "2", []Input{{EventID: "2", ConversationID: conv, Seq: 2}}); err != nil {
				t.Fatal(err)
			}
			joined := make(chan struct{})
			go func() { stop(); close(joined) }()
			select {
			case <-joined:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("blocked recovery did not cancel/join")
			}
			if ack.Load() != 0 {
				t.Fatal("blocked/canceled recovery emitted a read")
			}
		})
	}
}

type receiptPagingRepository struct {
	domain.RuntimeRepository
	mu       sync.Mutex
	throughs []string
	once     sync.Once
	append   func(context.Context) error
}

func (r *receiptPagingRepository) ListReadReceiptInputs(ctx context.Context, lease *Lease, after, through string, limit int) (domain.ReadReceiptInputPage, error) {
	r.mu.Lock()
	r.throughs = append(r.throughs, through)
	r.mu.Unlock()
	page, err := r.RuntimeRepository.ListReadReceiptInputs(ctx, lease, after, through, limit)
	if err == nil {
		r.once.Do(func() { err = r.append(ctx) })
	}
	return page, err
}

func TestReadReceiptsRecoveryPaginatesAtFixedCommittedCursor(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	engine, im, _, _ := fixtureEngine(t, store)
	lease, err := store.Acquire(ctx, im.agent, "paged-recovery")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Release(ctx, lease) })
	inputs := make([]Input, 0, 513)
	for seq := 1; seq <= 513; seq++ {
		inputs = append(inputs, Input{EventID: fmt.Sprint(seq), ConversationID: im.conversation, Seq: int64(seq)})
	}
	if err := store.Ingest(ctx, lease, "513", inputs); err != nil {
		t.Fatal(err)
	}
	repo := &receiptPagingRepository{RuntimeRepository: store.DB}
	repo.append = func(ctx context.Context) error {
		// Simulate commit after recovery captured its upper bound and before
		// its process delivered the corresponding in-memory Record wakeup.
		return store.Ingest(ctx, lease, "514", []Input{{EventID: "514", ConversationID: im.conversation, Seq: 514}})
	}
	store.DB = repo
	imc := &IM{BaseURL: engine.Config.IMURL, AgentID: im.agent, Identity: engine.Identity, ServiceKey: engine.Config.IMToken}
	_, stop := startFastReadSender(t, store, lease, imc.MarkRead)
	waitFor(t, func() bool { return receivedSeq(im, im.conversation) == 513 })
	stop()
	repo.mu.Lock()
	throughs := append([]string(nil), repo.throughs...)
	repo.mu.Unlock()
	if len(throughs) != 3 || fmt.Sprint(throughs) != "[513 513 513]" {
		t.Fatalf("recovery did not page to a fixed snapshot: %v", throughs)
	}
	store.Release(ctx, lease)
	lease, err = store.Acquire(ctx, im.agent, "after-paged-restart")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = startFastReadSender(t, store, lease, imc.MarkRead)
	waitFor(t, func() bool { return receivedSeq(im, im.conversation) == 514 })
}

func TestReadReceiptRecoveryDoesNotWaitForInboxWriteLock(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	agent, conv := uuid.NewString(), uuid.NewString()
	lease, err := store.Acquire(ctx, agent, "read-page-lock")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Release(ctx, lease)
	if err := store.Ingest(ctx, lease, "1", []Input{{EventID: "1", ConversationID: conv, Seq: 1}}); err != nil {
		t.Fatal(err)
	}
	locked, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- store.DB.WithinTx(ctx, func(tx context.Context) error {
			if _, err := store.DB.Exec(tx, "SELECT agent_id FROM runtime_agents WHERE agent_id=$1 FOR UPDATE", agent); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	pageCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	page, err := store.ReadReceiptInputs(pageCtx, lease, "0", "1", 2)
	cancel()
	close(release)
	if txErr := <-done; txErr != nil {
		t.Fatal(txErr)
	}
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("recovery tried to obtain an inbox writer lock: %+v %v", page, err)
	}
}

func TestReadReceiptsRecheckAuthorizationAndLeaseBeforeSending(t *testing.T) {
	for _, guardErr := range []error{ErrLeaseLost, &ModelError{Code: "principal_disabled"}, &ModelError{Code: "runtime_disabled"}} {
		t.Run(guardErr.Error(), func(t *testing.T) {
			store := testStore(t)
			agent, conv := uuid.NewString(), uuid.NewString()
			lease, err := store.Acquire(context.Background(), agent, "guarded-read")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Release(context.Background(), lease)
			if err := store.Ingest(context.Background(), lease, "1", []Input{{EventID: "1", ConversationID: conv, Seq: 1}}); err != nil {
				t.Fatal(err)
			}
			sender := newReadReceiptSender()
			sender.interval = 20 * time.Millisecond
			var calls atomic.Int32
			done := make(chan struct{})
			go func() {
				defer close(done)
				sender.run(context.Background(), store, lease, func(context.Context) error { return guardErr }, func(context.Context, string, int64) error { calls.Add(1); return nil })
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("revoked sender did not exit")
			}
			if calls.Load() != 0 {
				t.Fatal("read started after authorization/lease was revoked")
			}
		})
	}
}

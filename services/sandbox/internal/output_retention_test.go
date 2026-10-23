package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"teamie/contracts/gen/agent/kitex_gen/agent"
)

func retentionClock(s *Service) func(time.Time) {
	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC).UnixNano())
	s.OutputNow = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	return func(now time.Time) { clock.Store(now.UnixNano()) }
}

func seedOutputReceipt(t *testing.T, s *Service, taskID, state string, cleanup bool, text string) *taskReceipt {
	t.Helper()
	r := progressiveStart()
	r.TaskId = taskID
	fp := digest(r.Command + "\x00" + time.Duration(r.GetTimeoutSeconds()).String())
	fp = digest(fp + "/output-v1")
	key := taskKey(r.AgentId, taskID)
	receipt := &taskReceipt{Version: 2, AgentID: r.AgentId, TaskID: taskID, RunID: r.RunId, Fingerprint: fp, Command: r.Command, State: state, CleanupDone: cleanup, OutputStatus: "complete", ContainerName: "teamie-task-" + key[:32], Result: Result{ExitCode: 0}}
	var wire []byte
	for offset := 0; offset < len(text); {
		n := min(outputChunkBytes, len(text)-offset)
		wire = append(wire, encodeSpool(spoolRecord{Stream: 1, Timestamp: "2026-10-17T00:00:00Z", Chunk: uint32(receipt.OutputFrames), Data: []byte(text[offset : offset+n])})...)
		receipt.OutputBytes += int64(n)
		receipt.OutputFrames++
		offset += n
	}
	receipt.OutputFileBytes = int64(len(wire))
	if err := os.WriteFile(s.outputPath(key), wire, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.loadSpoolLocked(key, receipt); err != nil {
		t.Fatal(err)
	}
	if err := s.finishOutputLocked(key, receipt); err != nil {
		t.Fatal(err)
	}
	receipt.Result.Truncated = len(text) > OutputLimit
	if err := s.saveTaskLocked(key, receipt); err != nil {
		t.Fatal(err)
	}
	s.Tasks[key] = receipt
	return receipt
}

func diskTask(t *testing.T, s *Service, taskID string) (taskReceipt, []byte) {
	t.Helper()
	raw, err := os.ReadFile(s.taskPath(taskKey("agent-a", taskID)))
	if err != nil {
		t.Fatal(err)
	}
	var receipt taskReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt, raw
}

func TestOutputRetentionRenewalExpiryAndReplay(t *testing.T) {
	engine := newStampedFixture()
	s := fixture(t, engine)
	setTime := retentionClock(s)
	now := s.outputTime()
	text := strings.Repeat("large-output-", 7000)
	original := *seedOutputReceipt(t, s, "native-task", "completed", true, text)
	first, _ := s.Start(context.Background(), progressiveStart())
	if !first.Ok || first.GetOutputAvailability() != "retained" || first.GetOutputRetentionSeconds() != 86400 || first.GetOutputExpiresAt() != now.Add(OutputRetention).Format(time.RFC3339Nano) {
		t.Fatal("completed output did not advertise its full initial retention", first)
	}
	setTime(now.Add(12 * time.Hour))
	before, raw := diskTask(t, s, "native-task")
	unauthorized := taskRequest()
	unauthorized.ServiceToken = "invalid"
	if res, _ := s.ReadOutput(context.Background(), unauthorized); res.GetErrorCode() != "unauthorized" {
		t.Fatal(res)
	}
	_, after := diskTask(t, s, "native-task")
	if !bytes.Equal(raw, after) {
		t.Fatal("unauthenticated read renewed retention")
	}
	renewed, _ := s.Get(context.Background(), taskRequest())
	expires := now.Add(36 * time.Hour)
	if !renewed.Ok || renewed.GetOutputExpiresAt() != expires.Format(time.RFC3339Nano) || before.OutputExpiresAt == nil {
		t.Fatal("authenticated read did not renew retention", renewed)
	}
	offset, limit := int64(0), int32(4)
	request := taskRequest()
	request.Offset, request.Limit = &offset, &limit
	page, _ := s.ReadOutput(context.Background(), request)
	if !page.Ok || page.GetStdout() != text[:4] || !page.GetHasMore() || page.GetNextOffset() != 4 {
		t.Fatal("retained output became unreadable", page)
	}
	_, raw = diskTask(t, s, "native-task")
	s.ReadOutput(context.Background(), request)
	_, after = diskTask(t, s, "native-task")
	if !bytes.Equal(raw, after) {
		t.Fatal("read at the same clock rewrote the durable retention receipt")
	}
	setTime(now)
	s.Get(context.Background(), taskRequest())
	_, after = diskTask(t, s, "native-task")
	if !bytes.Equal(raw, after) {
		t.Fatal("clock rollback shortened retention")
	}

	setTime(expires)
	expired, _ := s.ReadOutput(context.Background(), request)
	if expired.Ok || expired.GetErrorCode() != "output_expired" || expired.State != original.State || expired.GetOutputAvailability() != "expired" || expired.GetOutputSha256() != original.OutputSHA256 || expired.GetStdout() != "" || expired.GetStderr() != "" || expired.GetHasMore() {
		t.Fatal("expired read lost terminal result or exposed bytes", expired)
	}
	if _, err := os.Stat(s.outputPath(taskKey("agent-a", "native-task"))); !os.IsNotExist(err) {
		t.Fatal("expired spool retained shared capacity", err)
	}
	stored, _ := diskTask(t, s, "native-task")
	if stored.OutputExpiredAt == nil || stored.OutputExpiredAt.Before(expires) || !reflect.DeepEqual(stored.Result, original.Result) || stored.Fingerprint != original.Fingerprint || stored.RunID != original.RunID || stored.OutputSHA256 != original.OutputSHA256 || stored.OutputBytes != original.OutputBytes || stored.OutputFrames != original.OutputFrames || stored.OutputFileBytes != original.OutputFileBytes {
		t.Fatal("expiry rewrote immutable task facts")
	}
	for _, operation := range []func() (*agent.SandboxTaskResponse, error){
		func() (*agent.SandboxTaskResponse, error) { return s.Start(context.Background(), progressiveStart()) },
		func() (*agent.SandboxTaskResponse, error) { return s.Get(context.Background(), taskRequest()) },
	} {
		result, _ := operation()
		if !result.Ok || result.State != original.State || result.GetOutputSha256() != original.OutputSHA256 || result.GetOutputAvailability() != "expired" || result.GetStdout() != original.Result.Stdout {
			t.Fatal("expired task replay lost the original terminal result", result)
		}
	}
	changed := progressiveStart()
	changed.Command += " changed"
	if result, _ := s.Start(context.Background(), changed); result.GetErrorCode() != "request_conflict" {
		t.Fatal("expiry forgot task fingerprint", result)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.creates != 0 || engine.starts != 0 {
		t.Fatal("expiry replay reran a command")
	}
}

func TestOutputRetentionPreservesActiveUnknownAndUncleaned(t *testing.T) {
	s := fixture(t, newStampedFixture())
	retentionClock(s)
	past := s.outputTime().Add(-OutputRetention)
	var expected int64
	for _, state := range []string{"not_started", "running", "unknown", "completed"} {
		cleanup := state != "completed"
		receipt := seedOutputReceipt(t, s, state, state, cleanup, "still-needed")
		receipt.OutputExpiresAt = &past
		if err := s.saveTaskLocked(taskKey("agent-a", state), receipt); err != nil {
			t.Fatal(err)
		}
		expected += receipt.OutputFileBytes
	}
	s.TaskMu.Lock()
	used, err := s.outputUsedLocked()
	s.TaskMu.Unlock()
	if err != nil || used != expected {
		t.Fatal("uncertain or active output was reclaimed", used, expected, err)
	}
	for _, state := range []string{"not_started", "running", "unknown", "completed"} {
		if _, err := os.Stat(s.outputPath(taskKey("agent-a", state))); err != nil {
			t.Fatal("unreclaimable output missing", state, err)
		}
	}
	// A spool without the exact matching task receipt cannot authorize deletion.
	if err := os.WriteFile(filepath.Join(s.StateRoot, "orphan.output"), []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	s.TaskMu.Lock()
	used, err = s.outputUsedLocked()
	s.TaskMu.Unlock()
	if err != nil || used != expected+6 {
		t.Fatal("orphan output was silently reclaimed", used, err)
	}
}

func TestOutputRetentionLegacyReceiptCompatibility(t *testing.T) {
	s := fixture(t, newStampedFixture())
	retentionClock(s)
	legacy := &taskReceipt{Version: 1, AgentID: "agent-a", TaskID: "native-task", RunID: "legacy-run", State: "completed", CleanupDone: true, Result: Result{ExitCode: 0, Stdout: "legacy preview"}}
	if err := s.saveTaskLocked(taskKey(legacy.AgentID, legacy.TaskID), legacy); err != nil {
		t.Fatal(err)
	}
	_, before := diskTask(t, s, legacy.TaskID)
	result, _ := s.Get(context.Background(), taskRequest())
	_, after := diskTask(t, s, legacy.TaskID)
	if !result.Ok || result.GetStdout() != legacy.Result.Stdout || result.OutputExpiresAt != nil || result.OutputRetentionSeconds != nil || result.OutputAvailability != nil || !bytes.Equal(before, after) {
		t.Fatal("v1 receipt bytes or response changed", result)
	}
	// A pre-retention v2 receipt receives a full grace period when first seen;
	// its old UpdatedAt is not grounds for deleting still-readable output.
	task := seedOutputReceipt(t, s, "older-v2", "completed", true, "durable legacy output")
	task.UpdatedAt = s.outputTime().Add(-30 * 24 * time.Hour)
	if err := writeAtomicJSON(s.taskPath(taskKey(task.AgentID, task.TaskID)), task); err != nil {
		t.Fatal(err)
	}
	s.TaskMu.Lock()
	used, err := s.outputUsedLocked()
	s.TaskMu.Unlock()
	if err != nil || used != task.OutputFileBytes || task.OutputExpiresAt == nil || !task.OutputExpiresAt.Equal(s.outputTime().Add(OutputRetention)) || task.OutputExpiredAt != nil {
		t.Fatal("pre-retention v2 output was expired without a grace period", err)
	}
}

func TestOutputRetentionRestartCompletesTombstoneDeletion(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(fmt.Sprint("already-unlinked=", removed), func(t *testing.T) {
			engine := newStampedFixture()
			s := fixture(t, engine)
			setTime := retentionClock(s)
			now := s.outputTime()
			task := seedOutputReceipt(t, s, "native-task", "completed", true, "immutable terminal output")
			key := taskKey(task.AgentID, task.TaskID)
			expires, expired := now.Add(OutputRetention), now.Add(OutputRetention)
			task.OutputExpiresAt, task.OutputExpiredAt = &expires, &expired
			if err := s.saveTaskLocked(key, task); err != nil {
				t.Fatal(err)
			}
			if removed {
				if err := os.Remove(s.outputPath(key)); err != nil {
					t.Fatal(err)
				}
			}
			setTime(expired)
			s.Close()
			for restart := 0; restart < 2; restart++ {
				reopened, err := New(engine, token, image, s.WorkspaceRoot, s.StateRoot)
				if err != nil {
					t.Fatal(err)
				}
				retentionClock(reopened)(expired)
				if err := reopened.Recover(context.Background()); err != nil {
					t.Fatal(err)
				}
				result, _ := reopened.Start(context.Background(), progressiveStart())
				page, _ := reopened.ReadOutput(context.Background(), taskRequest())
				if !result.Ok || result.State != "completed" || result.GetOutputSha256() != task.OutputSHA256 || result.GetStdout() != task.Result.Stdout || page.GetErrorCode() != "output_expired" {
					t.Fatal("restart lost the expired terminal receipt", result, page)
				}
				if _, err := os.Stat(reopened.outputPath(key)); !os.IsNotExist(err) {
					t.Fatal("restart did not finish tombstone deletion", err)
				}
				reopened.Close()
			}
			engine.mu.Lock()
			defer engine.mu.Unlock()
			if engine.creates != 0 || engine.starts != 0 || engine.kills != 0 {
				t.Fatal("expired task was relaunched during recovery")
			}
		})
	}
}

func TestOutputRetentionSharedQuotaRecoversOnlyAfterExpiry(t *testing.T) {
	engine := newStampedFixture()
	s := fixture(t, engine)
	setTime := retentionClock(s)
	now := s.outputTime()
	// Eight valid per-task spools fill the 64 MiB shared limit including the
	// record headers. Each receipt continues to own its original full output.
	const frames = 256
	text := strings.Repeat("x", int(DurableOutputLimit)-frames*(10+len("2026-10-17T00:00:00Z")))
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("quota-%d", i)
		receipt := seedOutputReceipt(t, s, id, "completed", true, text)
		if receipt.OutputFileBytes != DurableOutputLimit || receipt.OutputFrames != frames {
			t.Fatal("fixture did not fill the exact shared quota")
		}
		if err := s.retainOutputLocked(taskKey("agent-a", id), receipt, false); err != nil {
			t.Fatal(err)
		}
	}
	setTime(now.Add(OutputRetention - time.Nanosecond))
	blocked, _ := s.Start(context.Background(), progressiveStart())
	if blocked.Ok || blocked.GetErrorCode() != "sandbox_capacity" {
		t.Fatal("unexpired output was evicted to accept a new command", blocked)
	}
	active := seedOutputReceipt(t, s, "active-output", "running", false, "")
	s.TaskMu.Lock()
	err := s.appendOutputLocked(taskKey("agent-a", active.TaskID), active, spoolRecord{Stream: 1, Timestamp: "2026-10-17T00:00:00Z", Data: []byte("new output")})
	s.TaskMu.Unlock()
	if !errors.Is(err, errOutputLimit) || active.OutputStatus != "limited" || active.OutputBytes != 0 {
		t.Fatal("shared cap did not remain an explicit output limit", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(s.outputPath(taskKey("agent-a", fmt.Sprintf("quota-%d", i)))); err != nil {
			t.Fatal("unexpired spool was removed", err)
		}
	}
	setTime(now.Add(OutputRetention))
	s.TaskMu.Lock()
	used, err := s.outputUsedLocked()
	s.TaskMu.Unlock()
	if err != nil || used != 0 {
		t.Fatal("expired terminal output did not return shared capacity", used, err)
	}
	accepted, _ := s.Start(context.Background(), progressiveStart())
	if !accepted.Ok {
		t.Fatal("new command could not use reclaimed capacity", accepted)
	}
	awaitAnchor(t, s)
	engine.finish(taskName(), 0)
	awaitTask(t, s, completed)
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("quota-%d", i)
		request := taskRequest()
		request.TaskId = id
		result, _ := s.Get(context.Background(), request)
		page, _ := s.ReadOutput(context.Background(), request)
		if !result.Ok || result.State != "completed" || result.GetOutputAvailability() != "expired" || result.GetOutputSha256() == "" || page.GetErrorCode() != "output_expired" {
			t.Fatal("quota reclamation forgot the terminal task", id, result, page)
		}
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.creates != 1 || engine.starts != 1 {
		t.Fatal("retention reran historical commands", engine.creates, engine.starts)
	}
}

func TestOutputRetentionTombstoneMustPersistBeforeDeletion(t *testing.T) {
	s := fixture(t, newStampedFixture())
	setTime := retentionClock(s)
	now := s.outputTime()
	task := seedOutputReceipt(t, s, "native-task", "completed", true, "still durable")
	key := taskKey(task.AgentID, task.TaskID)
	if err := s.retainOutputLocked(key, task, false); err != nil {
		t.Fatal(err)
	}
	setTime(now.Add(OutputRetention))
	path := s.taskPath(key)
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if result, _ := s.Get(context.Background(), taskRequest()); result.GetErrorCode() != "receipt_unavailable" {
		t.Fatal("failed tombstone persistence was hidden", result)
	}
	if task.OutputExpiredAt != nil {
		t.Fatal("in-memory tombstone preceded durable persistence")
	}
	if raw, err := os.ReadFile(s.outputPath(key)); err != nil || len(raw) == 0 {
		t.Fatal("output was deleted before its expiration was durable", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	// Simulate unlink being unavailable after the tombstone was persisted.
	spool := s.outputPath(key)
	if err := os.Rename(spool, spool+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(spool, 0700); err != nil {
		t.Fatal(err)
	}
	if result, _ := s.Get(context.Background(), taskRequest()); result.GetErrorCode() != "receipt_unavailable" {
		t.Fatal("failed unlink was hidden", result)
	}
	stored, _ := diskTask(t, s, task.TaskID)
	if stored.OutputExpiredAt == nil || stored.Fingerprint != task.Fingerprint || stored.OutputSHA256 != task.OutputSHA256 {
		t.Fatal("failed unlink lost its durable tombstone")
	}
	if err := os.Remove(spool); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(spool+".saved", spool); err != nil {
		t.Fatal(err)
	}
	result, _ := s.ReadOutput(context.Background(), taskRequest())
	if result.GetErrorCode() != "output_expired" {
		t.Fatal("retry did not finish the durable unlink", result)
	}
	if _, err := os.Stat(spool); !os.IsNotExist(err) {
		t.Fatal("retry did not reclaim output", err)
	}
}

func TestOutputRetentionRestartDoesNotRenewDeadline(t *testing.T) {
	engine := newStampedFixture()
	s := fixture(t, engine)
	setTime := retentionClock(s)
	now := s.outputTime()
	task := seedOutputReceipt(t, s, "native-task", "completed", true, "retained through restart")
	key := taskKey(task.AgentID, task.TaskID)
	if err := s.retainOutputLocked(key, task, false); err != nil {
		t.Fatal(err)
	}
	expires := *task.OutputExpiresAt
	setTime(now.Add(OutputRetention - time.Nanosecond))
	s.Close()
	reopened, err := New(engine, token, image, s.WorkspaceRoot, s.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	setTime = retentionClock(reopened)
	setTime(now.Add(OutputRetention - time.Nanosecond))
	if err := reopened.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, _ := diskTask(t, reopened, task.TaskID)
	if recovered.OutputExpiresAt == nil || !recovered.OutputExpiresAt.Equal(expires) || recovered.OutputExpiredAt != nil {
		t.Fatal("restart silently extended or shortened retention")
	}
	if _, err := os.Stat(reopened.outputPath(key)); err != nil {
		t.Fatal("restart deleted unexpired output", err)
	}
	setTime(expires)
	page, _ := reopened.ReadOutput(context.Background(), taskRequest())
	if page.GetErrorCode() != "output_expired" {
		t.Fatal("recovered deadline did not expire on time", page)
	}
}

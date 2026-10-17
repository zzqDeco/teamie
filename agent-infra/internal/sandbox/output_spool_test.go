package sandbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"teamie/agent-protocol/kitex_gen/agent"
)

// Timestamp identities belong to the fixture daemon, not command text. Rotation
// removes records without renumbering the remaining daemon timestamps.
type stampedFixture struct {
	*managedFixture
	stampMu     sync.Mutex
	anchors     map[string]string
	firstRecord map[string]int
	service     *Service
	releases    int
}

func newStampedFixture() *stampedFixture {
	return &stampedFixture{managedFixture: newManagedFixture(), anchors: map[string]string{}, firstRecord: map[string]int{}}
}
func (f *stampedFixture) Create(ctx context.Context, j Job) (string, error) {
	id, e := f.managedFixture.Create(ctx, j)
	f.stampMu.Lock()
	f.anchors[j.Name] = j.OutputAnchor
	f.stampMu.Unlock()
	return id, e
}
func (f *stampedFixture) StartContainer(ctx context.Context, id string) error {
	if e := f.managedFixture.StartContainer(ctx, id); e != nil {
		return e
	}
	f.stampMu.Lock()
	anchor := f.anchors[id]
	f.stampMu.Unlock()
	if anchor != "" {
		f.output(id, 1, anchor+"\n")
	}
	return nil
}
func (f *stampedFixture) ReleaseOutput(_ context.Context, id string) error {
	f.stampMu.Lock()
	defer f.stampMu.Unlock()
	f.releases++
	if f.releases > 1 {
		return fmt.Errorf("stdin already closed after exec")
	}
	if f.service != nil {
		b, e := os.ReadFile(f.service.taskPath(taskKey("agent-a", "native-task")))
		var receipt taskReceipt
		if e != nil || json.Unmarshal(b, &receipt) != nil || receipt.OutputAnchorTime == "" {
			return fmt.Errorf("command released before durable daemon anchor")
		}
	}
	return nil
}
func (f *stampedFixture) AttachTimestamped(ctx context.Context, id string) (io.ReadCloser, error) {
	raw, e := f.managedFixture.Attach(ctx, id, true)
	if e != nil {
		return nil, e
	}
	f.stampMu.Lock()
	sequence := f.firstRecord[id]
	f.stampMu.Unlock()
	r, w := io.Pipe()
	go func() {
		defer raw.Close()
		defer w.Close()
		var h [8]byte
		for {
			if _, e := io.ReadFull(raw, h[:]); e != nil {
				if e != io.EOF {
					w.CloseWithError(e)
				}
				return
			}
			data := make([]byte, binary.BigEndian.Uint32(h[4:]))
			if _, e = io.ReadFull(raw, data); e != nil {
				w.CloseWithError(e)
				return
			}
			stamp := time.Date(2026, 10, 17, 0, 0, 0, sequence, time.UTC).Format(time.RFC3339Nano)
			sequence++
			data = append([]byte(stamp+" "), data...)
			binary.BigEndian.PutUint32(h[4:], uint32(len(data)))
			if _, e = w.Write(h[:]); e != nil {
				return
			}
			if _, e = w.Write(data); e != nil {
				return
			}
		}
	}()
	return r, nil
}
func (f *stampedFixture) rotate(id string, records int) {
	f.managedFixture.mu.Lock()
	defer f.managedFixture.mu.Unlock()
	c := f.containers[id]
	offset := 0
	for i := 0; i < records; i++ {
		offset += 8 + int(binary.BigEndian.Uint32(c.wire[offset+4:]))
	}
	c.wire = append([]byte(nil), c.wire[offset:]...)
	f.stampMu.Lock()
	f.firstRecord[id] += records
	f.stampMu.Unlock()
}
func progressiveStart() *agent.SandboxStartRequest {
	r := startRequest()
	v := int32(1)
	r.OutputVersion = &v
	return r
}
func awaitBytes(t *testing.T, s *Service, n int64) *agent.SandboxTaskResponse {
	return awaitTask(t, s, func(r *agent.SandboxTaskResponse) bool { return r.GetOutputBytes() == n })
}
func awaitDiskTask(t *testing.T, s *Service, predicate func(*agent.SandboxTaskResponse) bool) *agent.SandboxTaskResponse {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var result *agent.SandboxTaskResponse
	for time.Now().Before(deadline) {
		result, _ = s.Get(context.Background(), taskRequest())
		if predicate(result) {
			return result
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("disk-backed task did not finish: state=%s bytes=%d status=%s", result.State, result.GetOutputBytes(), result.GetOutputStatus())
	return nil
}
func awaitAnchor(t *testing.T, s *Service) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		s.taskMu.Lock()
		receipt := s.tasks[taskKey("agent-a", "native-task")]
		ready := receipt != nil && receipt.OutputAnchorTime != ""
		s.taskMu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("launch anchor was not persisted")
}

func TestDurableOutputBeyondPreviewAndBinaryPages(t *testing.T) {
	f := newStampedFixture()
	s := fixture(t, f)
	f.service = s
	if r, _ := s.Start(context.Background(), progressiveStart()); !r.Ok {
		t.Fatal(r)
	}
	awaitAnchor(t, s)
	text := strings.Repeat("abc", 100000) + string([]byte{0xff, 0x00, 0xfe})
	f.output(taskName(), 1, text)
	f.output(taskName(), 2, "stderr")
	awaitBytes(t, s, int64(len(text)+6))
	f.finish(taskName(), 0)
	result := awaitTask(t, s, completed)
	if result.GetOutputStatus() != "complete" || !result.GetHasMore() || len(result.GetStdout())+len(result.GetStderr()) != OutputLimit || result.GetOutputSha256() == "" {
		t.Fatal("missing complete output handle", result)
	}
	var all, errout strings.Builder
	offset := int64(0)
	limit := int32(16384)
	for {
		r := taskRequest()
		r.Offset = &offset
		r.Limit = &limit
		page, _ := s.ReadOutput(context.Background(), r)
		if !page.Ok {
			t.Fatal(page)
		}
		all.WriteString(page.GetStdout())
		errout.WriteString(page.GetStderr())
		if page.GetNextOffset() <= offset {
			t.Fatal("output cursor stuck", page)
		}
		offset = page.GetNextOffset()
		if !page.GetHasMore() {
			break
		}
	}
	if all.String() != text || errout.String() != "stderr" {
		t.Fatal("canonical binary output lost", all.Len(), errout.Len())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.kills != 0 {
		t.Fatal("preview threshold killed command")
	}
}

func TestDurableOutputRestartAdoptsTimestampOverlapIncludingRotation(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(fmt.Sprint("rotation=", rotate), func(t *testing.T) {
			f := newStampedFixture()
			s := fixture(t, f)
			f.service = s
			r := progressiveStart()
			s.Start(context.Background(), r)
			awaitAnchor(t, s)
			for i := 0; i < 5; i++ {
				f.output(taskName(), 1, "repeated\n")
			}
			awaitBytes(t, s, 45)
			s.Close()
			if rotate {
				f.rotate(taskName(), 4)
			} // Older persisted records may rotate; the durable tail remains.
			f.output(taskName(), 2, "after-detach\n")
			reopened, e := New(f, token, image, s.WorkspaceRoot, s.StateRoot)
			if e != nil {
				t.Fatal(e)
			}
			defer reopened.Close()
			f.service = reopened
			if e = reopened.Recover(context.Background()); e != nil {
				t.Fatal(e)
			}
			awaitBytes(t, reopened, 58)
			f.finish(taskName(), 0)
			result := awaitTask(t, reopened, completed)
			if result.GetOutputStatus() != "complete" || result.GetStdout() != strings.Repeat("repeated\n", 5) || result.GetStderr() != "after-detach\n" {
				t.Fatal("restart lost/duplicated output", result)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.starts != 1 || f.creates != 1 || f.kills != 0 {
				t.Fatal("restart reran/killed command", f.starts, f.creates, f.kills)
			}
		})
	}
}

func TestDurableOutputSilentRunningRestartDoesNotReleaseClosedStdin(t *testing.T) {
	f := newStampedFixture()
	s := fixture(t, f)
	f.service = s
	s.Start(context.Background(), progressiveStart())
	awaitAnchor(t, s)
	waitUntil := time.Now().Add(time.Second)
	for time.Now().Before(waitUntil) {
		s.taskMu.Lock()
		released := s.tasks[taskKey("agent-a", "native-task")].OutputGateReleased
		s.taskMu.Unlock()
		if released {
			break
		}
		time.Sleep(time.Millisecond)
	}
	s.Close()
	reopened, e := New(f, token, image, s.WorkspaceRoot, s.StateRoot)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	f.service = reopened
	if e = reopened.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	awaitTask(t, reopened, running)
	f.output(taskName(), 1, "later")
	awaitBytes(t, reopened, 5)
	f.finish(taskName(), 0)
	result := awaitTask(t, reopened, completed)
	if result.GetStdout() != "later" || result.GetOutputStatus() != "complete" {
		t.Fatal(result)
	}
	f.stampMu.Lock()
	defer f.stampMu.Unlock()
	if f.releases != 1 {
		t.Fatal("normal restart repeated stdin release", f.releases)
	}
}

func TestDurableOutputRealGapRepeatedTextIsUnknownAndNeverRerun(t *testing.T) {
	f := newStampedFixture()
	s := fixture(t, f)
	f.service = s
	r := progressiveStart()
	s.Start(context.Background(), r)
	awaitAnchor(t, s)
	f.output(taskName(), 1, "same")
	awaitBytes(t, s, 4)
	s.Close()
	f.rotate(taskName(), 2)
	f.output(taskName(), 1, "same")
	reopened, e := New(f, token, image, s.WorkspaceRoot, s.StateRoot)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	f.service = reopened
	if e = reopened.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	result := awaitTask(t, reopened, func(r *agent.SandboxTaskResponse) bool { return r.State == "unknown" })
	if result.GetOutputStatus() != "unknown" || result.GetOutputBytes() != 4 || result.GetStdout() != "same" {
		t.Fatal(result)
	}
	reopened.Start(context.Background(), r)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.starts != 1 || f.kills != 1 {
		t.Fatal("unknown effect rerun", f.starts, f.kills)
	}
}

func TestDurableOutputTimestampOverlapOneByteChangeFailsWhileStillRunning(t *testing.T) {
	f := newStampedFixture()
	s := fixture(t, f)
	f.service = s
	s.Start(context.Background(), progressiveStart())
	awaitAnchor(t, s)
	f.output(taskName(), 1, "same")
	awaitBytes(t, s, 4)
	s.Close()
	f.mu.Lock()
	wire := f.containers[taskName()].wire
	wire[len(wire)-1] ^= 1
	f.mu.Unlock()
	reopened, e := New(f, token, image, s.WorkspaceRoot, s.StateRoot)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	f.service = reopened
	if e = reopened.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	result := awaitTask(t, reopened, func(r *agent.SandboxTaskResponse) bool { return r.State == "unknown" })
	if result.GetOutputStatus() != "unknown" || result.GetStdout() != "same" {
		t.Fatal("corrupt overlap replaced canonical output", result)
	}
}

type cutFrameFixture struct{ *stampedFixture }

func (f *cutFrameFixture) AttachTimestamped(_ context.Context, id string) (io.ReadCloser, error) {
	f.stampMu.Lock()
	anchor := f.anchors[id]
	f.stampMu.Unlock()
	var wire bytes.Buffer
	for i, data := range []string{anchor + "\n", "part"} {
		payload := []byte(time.Date(2026, 10, 17, 0, 0, 0, i, time.UTC).Format(time.RFC3339Nano) + " " + data)
		var h [8]byte
		h[0] = 1
		size := len(payload)
		if i == 1 {
			size += 5
		}
		binary.BigEndian.PutUint32(h[4:], uint32(size))
		wire.Write(h[:])
		wire.Write(payload)
	}
	return io.NopCloser(bytes.NewReader(wire.Bytes())), nil
}
func TestDurableOutputCutFrameNeverClaimsComplete(t *testing.T) {
	f := &cutFrameFixture{newStampedFixture()}
	s := fixture(t, f)
	f.service = s
	s.Start(context.Background(), progressiveStart())
	awaitAnchor(t, s)
	f.finish(taskName(), 0)
	result := awaitTask(t, s, func(r *agent.SandboxTaskResponse) bool { return terminalTask(r.State) })
	if result.State != "unknown" || result.GetOutputStatus() != "unknown" || result.GetOutputBytes() != 0 {
		t.Fatal("short transport frame appeared complete", result)
	}
}

type earlyEOFFixture struct {
	*stampedFixture
	attaches atomic.Int32
}

func (f *earlyEOFFixture) AttachTimestamped(ctx context.Context, id string) (io.ReadCloser, error) {
	if f.attaches.Add(1) > 1 {
		return f.stampedFixture.AttachTimestamped(ctx, id)
	}
	f.stampMu.Lock()
	anchor := f.anchors[id]
	f.stampMu.Unlock()
	payload := []byte("2026-10-17T00:00:00Z " + anchor + "\n")
	var h [8]byte
	h[0] = 1
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return io.NopCloser(bytes.NewReader(append(h[:], payload...))), nil
}
func TestDurableOutputLiveCleanEOFRetrievesLaterBytesByAnchoredReconnect(t *testing.T) {
	f := &earlyEOFFixture{stampedFixture: newStampedFixture()}
	s := fixture(t, f)
	f.service = s
	s.Start(context.Background(), progressiveStart())
	awaitAnchor(t, s)
	deadline := time.Now().Add(3 * time.Second)
	for f.attaches.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if f.attaches.Load() < 2 {
		t.Fatal("live EOF was treated as completed output")
	}
	f.output(taskName(), 1, "after reconnect")
	awaitBytes(t, s, 15)
	f.finish(taskName(), 0)
	result := awaitTask(t, s, completed)
	if result.GetStdout() != "after reconnect" || result.GetOutputStatus() != "complete" {
		t.Fatal(result)
	}
	f.stampMu.Lock()
	defer f.stampMu.Unlock()
	if f.releases != 1 {
		t.Fatal("reconnect repeated command release")
	}
}

func TestDurableOutputSaturationIsExplicitAndPreviewDoesNotSaturate(t *testing.T) {
	f := newStampedFixture()
	s := fixture(t, f)
	f.service = s
	s.Start(context.Background(), progressiveStart())
	awaitAnchor(t, s)
	f.output(taskName(), 1, strings.Repeat("x", int(DurableOutputLimit)+1))
	result := awaitDiskTask(t, s, func(r *agent.SandboxTaskResponse) bool { return r.State == "failed" })
	if result.GetOutputStatus() != "limited" || result.GetOutputBytes() != DurableOutputLimit || result.GetErrorCode() != "execution_output_limit" || len(result.GetStdout()) != OutputLimit {
		t.Fatal("incorrect physical saturation", result)
	}
	info, e := os.Stat(s.outputPath(taskKey("agent-a", "native-task")))
	if e != nil || info.Size() > SharedOutputLimit || info.Mode().Perm() != 0600 {
		t.Fatal(info, e)
	}
}

func TestDurableOutputSharedQuotaCountsHeaderAndReturnsLimited(t *testing.T) {
	f := newStampedFixture()
	s := fixture(t, f)
	f.service = s
	padding, e := os.Create(s.StateRoot + "/padding.output")
	if e != nil {
		t.Fatal(e)
	}
	if e = padding.Truncate(SharedOutputLimit - 256); e != nil {
		t.Fatal(e)
	}
	padding.Close()
	r, _ := s.Start(context.Background(), progressiveStart())
	if !r.Ok {
		t.Fatal(r)
	}
	awaitAnchor(t, s)
	f.output(taskName(), 1, strings.Repeat("x", 512))
	result := awaitTask(t, s, func(r *agent.SandboxTaskResponse) bool { return r.State == "failed" })
	if result.GetOutputStatus() != "limited" || result.GetOutputBytes() <= 0 || result.GetOutputBytes() >= 256 || result.GetErrorCode() != "execution_output_limit" {
		t.Fatal(result)
	}
	s.taskMu.Lock()
	used, e := s.outputUsedLocked()
	s.taskMu.Unlock()
	if e != nil || used != SharedOutputLimit {
		t.Fatal("header bytes escaped shared quota", used, e)
	}
}

func TestDurableOutputWorkspacePagePreservesStreamOffsetsAndHidesControlMarker(t *testing.T) {
	f := newStampedFixture()
	s := fixture(t, f)
	f.service = s
	s.Start(context.Background(), progressiveStart())
	awaitAnchor(t, s)
	f.output(taskName(), 1, "A")
	f.output(taskName(), 2, "B")
	f.output(taskName(), 1, "C"+string([]byte{0xff}))
	awaitBytes(t, s, 4)
	f.finish(taskName(), 0)
	awaitTask(t, s, completed)
	request := &agent.SandboxWorkspaceRequest{ServiceToken: token, AgentId: "agent-a", Operation: "output", ArgsJson: `{"pageVersion":1,"taskId":"native-task","byteOffset":1,"maxBytes":2}`}
	page, _ := s.Workspace(context.Background(), request)
	if !page.Ok || strings.Contains(page.GetDataJson(), "TEAMIE_OUTPUT_ANCHOR") {
		t.Fatal(page)
	}
	var out struct {
		NextOffset   int64
		HasMore      bool
		OutputStatus string
		Segments     []outputSegment
	}
	if e := json.Unmarshal([]byte(page.GetDataJson()), &out); e != nil {
		t.Fatal(e)
	}
	if out.NextOffset != 3 || !out.HasMore || out.OutputStatus != "complete" || len(out.Segments) != 2 || out.Segments[0].Stream != "stderr" || out.Segments[0].ByteOffset != 1 || string(out.Segments[0].Data) != "B" || out.Segments[1].Stream != "stdout" || string(out.Segments[1].Data) != "C" {
		t.Fatal("stream reference lost", page.GetDataJson())
	}
	request.AgentId = "another-agent"
	denied, _ := s.Workspace(context.Background(), request)
	if denied.Ok {
		t.Fatal("foreign Agent read output handle")
	}
	request.ServiceToken = "invalid"
	denied, _ = s.Workspace(context.Background(), request)
	if denied.GetErrorCode() != "unauthorized" {
		t.Fatal(denied)
	}
}

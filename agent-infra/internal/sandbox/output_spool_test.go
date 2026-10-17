package sandbox

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
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

func TestDurableOutputSaturationIsExplicitAndPreviewDoesNotSaturate(t *testing.T) {
	f := newStampedFixture()
	s := fixture(t, f)
	f.service = s
	s.Start(context.Background(), progressiveStart())
	awaitAnchor(t, s)
	f.output(taskName(), 1, strings.Repeat("x", int(DurableOutputLimit)+1))
	result := awaitTask(t, s, func(r *agent.SandboxTaskResponse) bool { return r.State == "failed" })
	if result.GetOutputStatus() != "limited" || result.GetOutputBytes() != DurableOutputLimit || result.GetErrorCode() != "execution_output_limit" || len(result.GetStdout()) != OutputLimit {
		t.Fatal("incorrect physical saturation", result)
	}
	info, e := os.Stat(s.outputPath(taskKey("agent-a", "native-task")))
	if e != nil || info.Size() > SharedOutputLimit || info.Mode().Perm() != 0600 {
		t.Fatal(info, e)
	}
}

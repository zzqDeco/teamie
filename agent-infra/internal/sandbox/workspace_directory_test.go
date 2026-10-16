package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	wire "teamie/agent-protocol/kitex_gen/agent"
)

func TestWorkspaceDirectoryOpensRejectFIFOsAndSymlinks(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(workspace, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("directory", filepath.Join(workspace, "local-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "outside-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "regular"), []byte("text"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"fifo", "local-link", "outside-link", "regular"} {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				f, err := openWorkspaceDirectory(root, name)
				if f != nil {
					f.Close()
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("accepted a special file as a directory")
				}
			case <-time.After(time.Second):
				// Rescue an unsafe blocking open so even a regression does not
				// leave the test process holding a filesystem operation forever.
				rescue, _ := os.OpenFile(filepath.Join(workspace, "fifo"), os.O_RDWR|syscall.O_NONBLOCK, 0)
				if rescue != nil {
					defer rescue.Close()
				}
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("special-file open could not be rescued")
				}
				t.Fatal("opening a special file blocked")
			}
		})
	}
	if err = ensureWorkspaceParents(root, "directory/nested"); err != nil {
		t.Fatal(err)
	}
	f, err := openWorkspaceDirectory(root, "directory/nested")
	if err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat()
	f.Close()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0777 {
		t.Fatal("parent directory was not checked and chmodded through its fd", info, err)
	}
}

func TestWorkspaceDirectoryReplacementCannotBlockMutationsOrCapacityWalk(t *testing.T) {
	for _, operation := range []string{"binary", "text", "capacity", "import"} {
		t.Run(operation, func(t *testing.T) {
			s := fixture(t, &fakeEngine{})
			workspace, err := s.workspace("agent-a")
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(workspace)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			raceWorkspaceDirectory(t, workspace, func(i int) {
				name := fmt.Sprintf("changing/file-%d", i)
				switch operation {
				case "binary":
					_ = atomicBinaryImport(root, name, []byte{0, 255, 128})
				case "text":
					_ = atomicWorkspaceWrite(root, name, "text")
				case "capacity":
					_ = workspaceCapacity(root, "", 0)
				case "import":
					r := importRequest([]byte{0, 255, 128})
					r.Path, r.RequestId = name, fmt.Sprintf("race-%d", i)
					_, _ = s.ImportFile(context.Background(), r)
				}
			})
			// In particular, an adversarial Agent must not retain the global
			// file mutation lock and prevent another Agent's import.
			r := importRequest([]byte{0, 255})
			r.AgentId = "agent-b"
			out, err := s.ImportFile(context.Background(), r)
			if err != nil || !out.Ok {
				t.Fatal("directory race poisoned another Agent's import", out, err)
			}
		})
	}
}

// Every visible FIFO is a hard link to one inode, so the timeout path can
// release any blocked read-open even after the child has renamed that FIFO.
func raceWorkspaceDirectory(t *testing.T, workspace string, operation func(int)) {
	t.Helper()
	outside := t.TempDir()
	if err := os.Chmod(outside, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	fifo, changing, retired := filepath.Join(workspace, ".race-fifo"), filepath.Join(workspace, "changing"), filepath.Join(workspace, "retired")
	protected := filepath.Join(workspace, "protected")
	if err := os.Mkdir(protected, 0700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{changing, retired} {
		if err := os.Mkdir(dir, 0777); err != nil {
			t.Fatal(err)
		}
	}
	stop, changed, mutatorDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var fifos, symlinks atomic.Int64
	go func() {
		defer close(mutatorDone)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			// Keep a bounded number of retired directories. Otherwise the
			// mutator itself can grow the capacity walk faster than it scans.
			archive := filepath.Join(retired, fmt.Sprintf("directory-%d", i%8))
			_ = os.RemoveAll(archive)
			_ = os.Rename(changing, archive)
			_ = os.Remove(changing)
			if os.Link(fifo, changing) == nil {
				fifos.Add(1)
			}
			time.Sleep(100 * time.Microsecond)
			_ = os.Remove(changing)
			target := outside
			if i%2 != 0 {
				target = protected
			}
			if os.Symlink(target, changing) == nil {
				symlinks.Add(1)
			}
			time.Sleep(100 * time.Microsecond)
			_ = os.Remove(changing)
			_ = os.Mkdir(changing, 0777)
			if i == 0 {
				close(changed)
			}
			time.Sleep(100 * time.Microsecond)
		}
	}()
	<-changed
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 256; i++ {
			operation(i)
			runtime.Gosched()
		}
	}()
	blocked := false
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		blocked = true
	}
	close(stop)
	<-mutatorDone
	if blocked {
		rescue, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer rescue.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("directory race could not be unblocked")
		}
		t.Fatal("directory/FIFO replacement blocked a workspace operation")
	}
	if fifos.Load() == 0 || symlinks.Load() == 0 {
		t.Fatal("test did not exercise FIFO and symlink replacements")
	}
	entries, err := os.ReadDir(outside)
	actual, readErr := os.ReadFile(sentinel)
	info, statErr := os.Stat(outside)
	if err != nil || readErr != nil || statErr != nil || len(entries) != 1 || string(actual) != "unchanged" || info.Mode().Perm() != 0700 {
		t.Fatal("parent replacement escaped or chmodded outside the workspace", err, readErr, statErr)
	}
	// Root may resolve a local symlink despite O_NOFOLLOW. The Lstat/fd inode
	// comparison must stop a swapped link from chmodding a different directory.
	if info, err := os.Stat(protected); err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("parent replacement chmodded a different directory inode", info, err)
	}
}

func TestCanceledImportWaitingForMutationLockHasNoEffect(t *testing.T) {
	s := fixture(t, &fakeEngine{})
	r := importRequest([]byte{0, 255})
	ctx, cancel := context.WithCancel(context.Background())
	s.fileMu.Lock()
	started, done := make(chan struct{}), make(chan *wire.SandboxWorkspaceResponse, 1)
	go func() {
		close(started)
		out, _ := s.ImportFile(ctx, r)
		done <- out
	}()
	<-started
	cancel()
	s.fileMu.Unlock()
	select {
	case out := <-done:
		if out.GetErrorCode() != "execution_canceled" {
			t.Fatal("queued canceled import changed the workspace", out)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled import retained the mutation lock")
	}
	receipts, err := os.ReadDir(s.StateRoot)
	if err != nil || len(receipts) != 1 || receipts[0].Name() != "supervisor.lock" {
		t.Fatal("canceled import created a durable effect", receipts, err)
	}
	if out, err := s.ImportFile(context.Background(), r); err != nil || !out.Ok {
		t.Fatal("cancellation poisoned the stable import ID", out, err)
	}
}

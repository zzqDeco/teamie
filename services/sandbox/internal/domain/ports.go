package domain

import (
	"context"
	"io"
	"os"
)

// Engine is the primary container execution port.
type Engine interface {
	Ready(ctx context.Context, image string) error
	Cleanup(ctx context.Context) error
	Run(ctx context.Context, job Job) (Result, error)
}

// ManagedEngine separates transport observation from container lifecycle.
type ManagedEngine interface {
	Create(ctx context.Context, job Job) (string, error)
	StartContainer(ctx context.Context, id string) error
	Inspect(ctx context.Context, id string) (ContainerState, error)
	Attach(ctx context.Context, id string, logs bool) (io.ReadCloser, error)
	Kill(ctx context.Context, id string) error
	Remove(ctx context.Context, id string) error
}

// TimestampedEngine supplies daemon-origin timestamped log streams and stdin release.
type TimestampedEngine interface {
	AttachTimestamped(ctx context.Context, id string) (io.ReadCloser, error)
	ReleaseOutput(ctx context.Context, id string) error
}

// WorkspaceStorage is the outbound filesystem port implemented by infra/fs.
type WorkspaceStorage interface {
	WriteAtomicJSON(target string, r any) error
	RegularRead(root *os.Root, name string) ([]byte, error)
	ReadPageFile(root *os.Root, name string) ([]byte, error)
	OpenWorkspaceDirectory(root *os.Root, name string) (*os.File, error)
	EnsureWorkspaceParents(root *os.Root, parent string) error
	AtomicWorkspaceWrite(root *os.Root, name, content string) error
	WorkspaceCapacity(root *os.Root, name string, replacementBytes int64) bool
	WorkspaceWithinQuota(workspace string) bool
	BinaryHash(root *os.Root, name string) (string, error)
	AtomicBinaryImport(root *os.Root, name string, content []byte) error
	AcquireSupervisorLock(path string) (*os.File, error)
	ReleaseSupervisorLock(f *os.File)
	DiskAvailable(path string) bool
	OpenNonblock(root *os.Root, name string) (*os.File, error)
	OpenNonblockNoFollow(root *os.Root, name string) (*os.File, error)
	OpenNoFollowFile(path string, flag int, perm os.FileMode) (*os.File, error)
}

// DefaultWorkspaceStorage is populated by infra/fs at initialization.
var DefaultWorkspaceStorage WorkspaceStorage

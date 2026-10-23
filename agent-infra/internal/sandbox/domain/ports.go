package domain

import (
	"context"
	"io"
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

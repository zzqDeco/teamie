package sandbox

import (
	"context"
	"io"
	"time"

	"teamie/agent-infra/internal/sandbox/domain"
)

type ManagedEngine = domain.ManagedEngine
type TimestampedEngine = domain.TimestampedEngine
type ContainerState = domain.ContainerState
type outputPart = domain.OutputPart

var errContainerMissing = domain.ErrContainerMissing

func (d *Docker) AttachTimestamped(ctx context.Context, id string) (io.ReadCloser, error) {
	return d.inner().AttachTimestamped(ctx, id)
}

func (d *Docker) ReleaseOutput(ctx context.Context, id string) error {
	return d.inner().ReleaseOutput(ctx, id)
}

func (d *Docker) Create(ctx context.Context, j Job) (string, error) {
	return d.inner().Create(ctx, j)
}

func (d *Docker) StartContainer(ctx context.Context, id string) error {
	return d.inner().StartContainer(ctx, id)
}

func (d *Docker) Inspect(ctx context.Context, id string) (ContainerState, error) {
	return d.inner().Inspect(ctx, id)
}

func (d *Docker) Attach(ctx context.Context, id string, logs bool) (io.ReadCloser, error) {
	return d.inner().Attach(ctx, id, logs)
}

func (d *Docker) Kill(ctx context.Context, id string) error {
	return d.inner().Kill(ctx, id)
}

func (d *Docker) Remove(ctx context.Context, id string) error {
	return d.inner().Remove(ctx, id)
}

func flattenOutput(parts []outputPart) (string, string) {
	return domain.FlattenOutput(parts)
}

func captureFrames(r io.Reader, persist func(Result, []outputPart) error) (Result, error) {
	return domain.CaptureFrames(r, persist)
}

func operationContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

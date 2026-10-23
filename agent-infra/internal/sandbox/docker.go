// Package sandbox is the only application component allowed to contact Docker.
package sandbox

import (
	"context"
	"io"
	"net/http"

	"teamie/agent-infra/internal/sandbox/domain"
	"teamie/agent-infra/internal/sandbox/infra/docker"
)

const OutputLimit = domain.OutputLimit
const MemoryLimit = domain.MemoryLimit
const ManagedLabel = domain.ManagedLabel

type Job = domain.Job
type Result = domain.Result
type Engine = domain.Engine

type Docker struct {
	HTTP   *http.Client
	prefix string
}

func NewDocker(socket string) *Docker {
	d := docker.NewDocker(socket)
	return &Docker{HTTP: d.HTTP, prefix: "/v1.45"}
}

func (d *Docker) inner() *docker.Docker {
	return docker.NewWithClient(d.HTTP, d.prefix)
}

func (d *Docker) request(ctx context.Context, method, path string, data any) (*http.Response, error) {
	return d.inner().Request(ctx, method, path, data)
}

func (d *Docker) Ready(ctx context.Context, image string) error {
	return d.inner().Ready(ctx, image)
}

func (d *Docker) Cleanup(ctx context.Context) error {
	return d.inner().Cleanup(ctx)
}

func (d *Docker) Run(ctx context.Context, j Job) (Result, error) {
	return d.inner().Run(ctx, j)
}

func legacyContainerConfig(j Job) map[string]any {
	return docker.LegacyContainerConfig(j)
}

func containerConfig(j Job) map[string]any {
	return docker.ContainerConfig(j)
}

func demux(r io.Reader, limit int) (string, string, bool, error) {
	return domain.Demux(r, limit)
}

package sandbox

import (
	"context"
	"io"
	"net/http"
	"os"
	"time"

	rpcAdapter "teamie/services/sandbox/internal/adapter/rpc"
	"teamie/services/sandbox/internal/app"
	"teamie/services/sandbox/internal/domain"
	"teamie/services/sandbox/internal/infra/docker"
	"teamie/services/sandbox/internal/infra/fs"
)

const (
	OutputLimit        = domain.OutputLimit
	MemoryLimit        = domain.MemoryLimit
	ManagedLabel       = domain.ManagedLabel
	DurableOutputLimit = domain.DurableOutputLimit
	SharedOutputLimit  = domain.SharedOutputLimit
	outputChunkBytes   = domain.OutputChunkBytes
	OutputRetention    = domain.OutputRetention
	maxFileBytes       = domain.MaxFileBytes
	MaxPageBytes       = domain.MaxPageBytes
	MaxWorkspaceBytes  = domain.MaxWorkspaceBytes
	MaxWorkspaceFiles  = domain.MaxWorkspaceFiles
)

var (
	errContainerMissing = domain.ErrContainerMissing
	errOutputLimit      = domain.ErrOutputLimit
	errOutputExpired    = domain.ErrOutputExpired
)

type (
	Engine            = domain.Engine
	ManagedEngine     = domain.ManagedEngine
	TimestampedEngine = domain.TimestampedEngine
	Job               = domain.Job
	Result            = domain.Result
	ContainerState    = domain.ContainerState
	Receipt           = domain.Receipt
	taskReceipt       = domain.TaskReceipt
	fileReceipt       = domain.FileReceipt
	outputPart        = domain.OutputPart
	spoolRecord       = domain.SpoolRecord
	outputSegment     = domain.OutputSegment
)

type Service struct {
	*rpcAdapter.Server
}

func New(engine Engine, token, image, workspace, state string) (*Service, error) {
	appSvc, err := app.New(engine, token, image, workspace, state)
	if err != nil {
		return nil, err
	}
	return &Service{Server: rpcAdapter.New(appSvc)}, nil
}

func (s *Service) workspace(agentID string) (string, error) {
	return s.WorkspaceDir(agentID)
}

func (s *Service) receiptPath(agentID, requestID string) string {
	return s.ReceiptPath(agentID, requestID)
}

func (s *Service) taskPath(key string) string {
	return s.TaskPath(key)
}

func (s *Service) outputPath(key string) string {
	return s.OutputPath(key)
}

func (s *Service) loadSpoolLocked(key string, t *taskReceipt) error {
	return s.LoadSpoolLocked(key, t)
}

func (s *Service) finishOutputLocked(key string, t *taskReceipt) error {
	return s.FinishOutputLocked(key, t)
}

func (s *Service) saveTaskLocked(key string, t *taskReceipt) error {
	return s.SaveTaskLocked(key, t)
}

func (s *Service) loadTaskLocked(agentID, taskID string) (*taskReceipt, error) {
	return s.LoadTaskLocked(agentID, taskID)
}

func (s *Service) outputUsedLocked() (int64, error) {
	return s.OutputUsedLocked()
}

func (s *Service) retainOutputLocked(key string, t *taskReceipt, renew bool) error {
	return s.RetainOutputLocked(key, t, renew)
}

func (s *Service) appendOutputLocked(key string, t *taskReceipt, r spoolRecord) error {
	return s.AppendOutputLocked(key, t, r)
}

func (s *Service) outputTime() time.Time {
	return s.OutputTime()
}

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

func containerConfig(j Job) map[string]any {
	return docker.ContainerConfig(j)
}

func demux(r io.Reader, limit int) (string, string, bool, error) {
	return domain.Demux(r, limit)
}

func digest(v string) string {
	return domain.Digest(v)
}

func taskKey(agentID, taskID string) string {
	return domain.TaskKey(agentID, taskID)
}

func terminalTask(state string) bool {
	return domain.TerminalTask(state)
}

func encodeSpool(r spoolRecord) []byte {
	return domain.EncodeSpool(r)
}

func writeReceipt(path string, r Receipt) error {
	return fs.WriteAtomicJSON(path, r)
}

func writeAtomicJSON(path string, r any) error {
	return fs.WriteAtomicJSON(path, r)
}

func openWorkspaceDirectory(root *os.Root, name string) (*os.File, error) {
	return fs.OpenWorkspaceDirectory(root, name)
}

func ensureWorkspaceParents(root *os.Root, parent string) error {
	return fs.EnsureWorkspaceParents(root, parent)
}

func atomicWorkspaceWrite(root *os.Root, name, content string) error {
	return fs.AtomicWorkspaceWrite(root, name, content)
}

func atomicBinaryImport(root *os.Root, name string, content []byte) error {
	return fs.AtomicBinaryImport(root, name, content)
}

func workspaceCapacity(root *os.Root, name string, replacementBytes int64) bool {
	return fs.WorkspaceCapacity(root, name, replacementBytes)
}

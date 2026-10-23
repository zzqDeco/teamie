package control

import (
	"context"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/pkg/postgres"
	httpadapter "teamie/services/agent-control/internal/adapter/http"
	rpcadapter "teamie/services/agent-control/internal/adapter/rpc"
	"teamie/services/agent-control/internal/app"
	"teamie/services/agent-control/internal/domain"
	flagsinfra "teamie/services/agent-control/internal/infra/flags"
	"teamie/services/agent-control/internal/infra/pg"
	"teamie/services/agent-control/internal/infra/rpcclient"
)

var Version = "unreleased"
var SHA = "unknown"

type Agent = domain.Agent
type Changes = domain.Changes
type Error = domain.Error
type Flags = flagsinfra.Flags
type publicAgent = domain.PublicAgent

func errAPI(status int, code, message string) error {
	return domain.Err(status, code, message)
}

func fail(w http.ResponseWriter, e error) {
	httpadapter.Fail(w, e)
}

func write(w http.ResponseWriter, status int, v any) {
	httpadapter.Write(w, status, v)
}

func env(key, otherwise string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return otherwise
}

func validKey(expected, provided string) bool {
	return domain.ValidKey(expected, provided)
}

func id() string {
	return domain.NewID()
}

func normalized(ids []string) []string {
	return domain.Normalized(ids)
}

func bounded(v string, minLen, maxLen int) bool {
	return domain.Bounded(v, minLen, maxLen)
}

func same(a, b []string) bool {
	return domain.Same(a, b)
}

func intersect(selected []string, sets ...[]string) []string {
	return domain.Intersect(selected, sets...)
}

func publicProfile(a Agent) publicAgent {
	return domain.PublicProfile(a)
}

func publicProfiles(agents []Agent, workspaceID string) []publicAgent {
	return domain.PublicProfiles(agents, workspaceID)
}

type Authority interface {
	Validate(context.Context, string) (*im.User, string, error)
	Users(context.Context, []string) (map[string]*im.User, error)
	Workspaces(context.Context, string) ([]string, error)
	Participants(context.Context, string, string) ([]string, error)
	Provision(context.Context, Agent, int64, bool, bool) error
	Memberships(context.Context, Agent, []string) ([]string, error)
	Ping(context.Context) error
}

type authorityBridge struct {
	inner Authority
}

var _ domain.IMAuthority = authorityBridge{}

func (b authorityBridge) ValidateSession(ctx context.Context, token string) (*domain.UserProfile, string, error) {
	u, csrf, err := b.inner.Validate(ctx, token)
	if err != nil {
		return nil, "", err
	}
	if u == nil {
		return nil, csrf, nil
	}
	return &domain.UserProfile{
		ID:           u.Id,
		DisplayName:  u.DisplayName,
		Kind:         u.Kind,
		PlatformRole: u.PlatformRole,
	}, csrf, nil
}

func (b authorityBridge) GetUsers(ctx context.Context, ids []string) (map[string]domain.UserProfile, error) {
	raw, err := b.inner.Users(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]domain.UserProfile, len(raw))
	for k, u := range raw {
		if u != nil {
			out[k] = domain.UserProfile{
				ID:           u.Id,
				DisplayName:  u.DisplayName,
				Kind:         u.Kind,
				PlatformRole: u.PlatformRole,
			}
		}
	}
	return out, nil
}

func (b authorityBridge) GetWorkspaces(ctx context.Context, userID string) ([]string, error) {
	return b.inner.Workspaces(ctx, userID)
}
func (b authorityBridge) GetParticipants(ctx context.Context, workspaceID, requesterID string) ([]string, error) {
	return b.inner.Participants(ctx, workspaceID, requesterID)
}
func (b authorityBridge) ProvisionPrincipal(ctx context.Context, a domain.Agent, revision int64, enabled, deleted bool) error {
	return b.inner.Provision(ctx, a, revision, enabled, deleted)
}
func (b authorityBridge) SyncMemberships(ctx context.Context, a domain.Agent, ids []string) ([]string, error) {
	return b.inner.Memberships(ctx, a, ids)
}
func (b authorityBridge) Ping(ctx context.Context) error {
	return b.inner.Ping(ctx)
}

type Store struct {
	DB           *pgxpool.Pool
	IM           Authority
	Flags        Flags
	RuntimeToken string
}

func (s *Store) App() *app.Service {
	var repo *pg.Repository
	if s.DB != nil {
		repo = pg.New(postgres.New(s.DB))
	}
	var imPort domain.IMAuthority
	if s.IM != nil {
		imPort = authorityBridge{inner: s.IM}
	}
	return &app.Service{
		Tx:           repo,
		Agents:       repo,
		IM:           imPort,
		Flags:        s.Flags,
		Notifier:     rpcclient.ObservabilityNotifier{},
		RuntimeToken: s.RuntimeToken,
	}
}

func Open(ctx context.Context, url string, im Authority, cfgFlags Flags) (*Store, error) {
	db, err := postgres.Open(ctx, url)
	if err != nil {
		return nil, err
	}
	s := &Store{DB: db, IM: im, Flags: cfgFlags}
	if err = pg.Migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	return pg.Migrate(ctx, s.DB)
}
func (s *Store) Create(ctx context.Context, owner string, in Changes) (Agent, error) {
	return s.App().Create(ctx, owner, in)
}
func (s *Store) Owned(ctx context.Context, owner, id string) (Agent, error) {
	return s.App().Owned(ctx, owner, id)
}
func (s *Store) Update(ctx context.Context, owner, id string, in Changes) (Agent, error) {
	return s.App().Update(ctx, owner, id, in)
}
func (s *Store) Delete(ctx context.Context, owner, id string) error {
	return s.App().Delete(ctx, owner, id)
}
func (s *Store) list(ctx context.Context, where string, args ...any) ([]Agent, error) {
	return pg.New(postgres.New(s.DB)).ListWhere(ctx, where, args...)
}
func (s *Store) ListOwned(ctx context.Context, owner string) ([]Agent, error) {
	return s.App().ListOwned(ctx, owner)
}
func (s *Store) Discover(ctx context.Context, workspace, user string) ([]Agent, error) {
	return s.App().Discover(ctx, workspace, user)
}
func (s *Store) decorate(ctx context.Context, agents []Agent) ([]Agent, error) {
	return s.App().Decorate(ctx, agents)
}
func (s *Store) Reconcile(ctx context.Context, id string) error {
	return s.App().Reconcile(ctx, id)
}
func (s *Store) Run(ctx context.Context) {
	s.App().Run(ctx)
}

type Handler struct {
	Store  *Store
	Origin string
}

type handler func(http.ResponseWriter, *http.Request, *im.User) error

func (h *Handler) httpAdapter() *httpadapter.Adapter {
	var appSvc = (*Store)(nil)
	if h != nil {
		appSvc = h.Store
	}
	var origin string
	if h != nil {
		origin = h.Origin
	}
	a := &httpadapter.Adapter{
		Origin:  origin,
		Version: Version,
		SHA:     SHA,
	}
	if appSvc != nil {
		a.App = appSvc.App()
	}
	return a
}

func (h *Handler) auth(next handler) http.HandlerFunc {
	return h.httpAdapter().Auth(func(w http.ResponseWriter, r *http.Request, ctx context.Context, u domain.UserProfile) error {
		return next(w, r.WithContext(ctx), &im.User{
			Id:           u.ID,
			DisplayName:  u.DisplayName,
			Kind:         u.Kind,
			PlatformRole: u.PlatformRole,
		})
	})
}

func (h *Handler) one(w http.ResponseWriter, r *http.Request, a Agent, status int) error {
	return h.httpAdapter().WriteSingleProfile(w, r, a, status)
}

func (h *Handler) Routes() http.Handler {
	return h.httpAdapter().Routes()
}

type RPC struct {
	Store *Store
}

func (s *Store) definition(a Agent) *agent.AgentDefinition {
	return rpcadapter.ToDefinition(a, s.Flags.Enabled("agents.enabled"))
}

func (h *RPC) server() *rpcadapter.Server {
	return &rpcadapter.Server{App: h.Store.App()}
}

func (h *RPC) ListAgents(ctx context.Context, r *agent.AgentListRequest) (*agent.AgentListResponse, error) {
	return h.server().ListAgents(ctx, r)
}
func (h *RPC) GetAgent(ctx context.Context, r *agent.AgentRequest) (*agent.AgentResponse, error) {
	return h.server().GetAgent(ctx, r)
}
func (h *RPC) ReportRuntime(ctx context.Context, r *agent.RuntimeReport) (*agent.Ack, error) {
	return h.server().ReportRuntime(ctx, r)
}
func (h *RPC) Ping(ctx context.Context, r *agent.PingRequest) (*agent.PingResponse, error) {
	return h.server().Ping(ctx, r)
}

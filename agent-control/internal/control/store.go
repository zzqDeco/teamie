package control

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5/pgxpool"
	"teamie/agent-control/internal/control/app"
	"teamie/agent-control/internal/control/domain"
	"teamie/agent-control/internal/control/infra/pg"
	"teamie/agent-control/internal/control/infra/rpcclient"
	"teamie/backend/pkg/postgres"
)

//go:embed schema.sql
var schema string

// Store composes the PostgreSQL infrastructure adapter, IM RPC authority, and
// Agent Control application service.
type Store struct {
	DB           *pgxpool.Pool
	IM           Authority
	Flags        Flags
	RuntimeToken string
}

// App returns the Clean DDD application service bound to this Store's infrastructure.
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

func Open(ctx context.Context, url string, im Authority, flags Flags) (*Store, error) {
	db, err := postgres.Open(ctx, url)
	if err != nil {
		return nil, err
	}
	s := &Store{DB: db, IM: im, Flags: flags}
	if err = s.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	if err := postgres.Migrate(ctx, s.DB, 704201, "agent_control_schema_migrations", []postgres.Migration{
		{Version: 1, SQL: schema},
	}); err != nil {
		return domain.ErrSchemaUnavailable(err)
	}
	return nil
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

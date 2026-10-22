// Package identity wires the Identity 4-layer Clean DDD components
// (domain, app, adapter/http, adapter/rpc, infra/pg, infra/crypto) and exposes
// the service composition root.
package identity

import (
	"context"
	_ "embed"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"teamie/backend/internal/api"
	"teamie/backend/internal/config"
	"teamie/backend/internal/identity/app"
	"teamie/backend/internal/identity/domain"
	"teamie/backend/internal/identity/infra/crypto"
	"teamie/backend/internal/identity/infra/pg"
	"teamie/backend/pkg/postgres"
)

//go:embed schema.sql
var schema string

//go:embed migrations/002_principals.sql
var schemaV2 string

//go:embed migrations/003_password_resets.sql
var schemaV3 string

type flagsAdapter struct {
	flags config.Flags
}

func (f flagsAdapter) PasswordResetEnabled() bool {
	return f.flags.Load().Flags["auth.password_reset"]
}

func compatErr(err error) error {
	if err == nil {
		return nil
	}
	var de *domain.Error
	if errors.As(err, &de) {
		return api.Err(de.Status, de.Code, de.Message)
	}
	return err
}

func toAPIUser(u domain.User) api.User {
	return api.User{
		ID:           u.ID,
		Email:        u.Email,
		DisplayName:  u.DisplayName,
		Kind:         u.Kind,
		PlatformRole: u.PlatformRole,
		CreatedAt:    u.CreatedAt,
	}
}

func toDomainUser(u api.User) domain.User {
	return domain.User{
		ID:           u.ID,
		Email:        u.Email,
		DisplayName:  u.DisplayName,
		Kind:         u.Kind,
		PlatformRole: u.PlatformRole,
		CreatedAt:    u.CreatedAt,
	}
}

func toAPISession(s domain.Session) api.Session {
	return api.Session{
		User:             toAPIUser(s.User),
		CSRFToken:        s.CSRFToken,
		ServicePrincipal: s.ServicePrincipal,
	}
}

// Store composes the PostgreSQL infrastructure adapter and Identity application service.
type Store struct {
	DB                  *pgxpool.Pool
	Flags               config.Flags
	BootstrapInviteCode string
	ControlToken        string
	RuntimeToken        string
}

// App returns the Clean DDD application service bound to this Store's infrastructure.
func (s *Store) App() *app.Service {
	repo := pg.New(postgres.New(s.DB))
	return &app.Service{
		Tx:                  repo,
		Users:               repo,
		Sessions:            repo,
		Invites:             repo,
		PasswordResets:      repo,
		Principals:          repo,
		Passwords:           crypto.BcryptHasher{},
		Tokens:              crypto.TokenGen{},
		Flags:               flagsAdapter{flags: s.Flags},
		BootstrapInviteCode: s.BootstrapInviteCode,
		ControlToken:        s.ControlToken,
		RuntimeToken:        s.RuntimeToken,
	}
}

func Open(ctx context.Context, url string, flags config.Flags, code string) (*Store, error) {
	db, err := postgres.Open(ctx, url)
	if err != nil {
		return nil, err
	}
	if code == "" {
		code = DefaultBootstrapInviteCode
	}
	s := &Store{DB: db, Flags: flags, BootstrapInviteCode: code}
	if err = s.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	return postgres.Migrate(ctx, s.DB, 704101, "identity_schema_migrations", []postgres.Migration{
		{Version: 1, SQL: schema},
		{Version: 2, SQL: schemaV2},
		{Version: 3, SQL: schemaV3},
	})
}

func (s *Store) Ping(ctx context.Context) error {
	return s.App().Ping(ctx)
}

func (s *Store) newSession(ctx context.Context, tx pgx.Tx, u api.User) (api.Session, string, error) {
	ss, token, err := s.App().IssueSession(postgres.WithTx(ctx, tx), toDomainUser(u))
	return toAPISession(ss), token, compatErr(err)
}

func (s *Store) Register(ctx context.Context, email, password, name, code string) (api.Session, string, error) {
	ss, token, err := s.App().Register(ctx, email, password, name, code)
	return toAPISession(ss), token, compatErr(err)
}

func (s *Store) Login(ctx context.Context, email, password string) (api.Session, string, error) {
	ss, token, err := s.App().Login(ctx, email, password)
	return toAPISession(ss), token, compatErr(err)
}

func (s *Store) Validate(ctx context.Context, token string) (api.Session, error) {
	ss, err := s.App().Validate(ctx, token)
	return toAPISession(ss), compatErr(err)
}

func (s *Store) Users(ctx context.Context, ids []string) ([]api.User, error) {
	us, err := s.App().GetUsers(ctx, ids)
	if err != nil {
		return nil, compatErr(err)
	}
	out := make([]api.User, 0, len(us))
	for _, u := range us {
		out = append(out, toAPIUser(u))
	}
	return out, nil
}

func (s *Store) Logout(ctx context.Context, token string) error {
	return compatErr(s.App().Logout(ctx, token))
}

func (s *Store) Profile(ctx context.Context, id, name string) (api.User, error) {
	u, err := s.App().Profile(ctx, id, name)
	return toAPIUser(u), compatErr(err)
}

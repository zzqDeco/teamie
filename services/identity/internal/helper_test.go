package identity

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/pkg/httpx"
	"teamie/pkg/postgres"
	httpadapter "teamie/services/identity/internal/adapter/http"
	rpcadapter "teamie/services/identity/internal/adapter/rpc"
	"teamie/services/identity/internal/app"
	"teamie/services/identity/internal/domain"
	"teamie/services/identity/internal/infra/crypto"
	"teamie/services/identity/internal/infra/flags"
	"teamie/services/identity/internal/infra/pg"
)

const DefaultBootstrapInviteCode = domain.DefaultBootstrapInviteCode

var (
	schema   = pg.SchemaV1
	schemaV2 = pg.SchemaV2
	schemaV3 = pg.SchemaV3
)

type RegistrationInvite = domain.RegistrationInvite
type PasswordReset = domain.PasswordReset
type PasswordResetAccount = domain.PasswordResetAccount
type PasswordResetInspection = domain.PasswordResetInspection

type AdminUserPage struct {
	Users  []httpx.User `json:"users"`
	Cursor string       `json:"cursor,omitempty"`
}

func compatErr(err error) error {
	if err == nil {
		return nil
	}
	var de *domain.Error
	if errors.As(err, &de) {
		return httpx.Err(de.Status, de.Code, de.Message)
	}
	return err
}

func toAPIUser(u domain.User) httpx.User {
	return httpx.User{
		ID:           u.ID,
		Email:        u.Email,
		DisplayName:  u.DisplayName,
		Kind:         u.Kind,
		PlatformRole: u.PlatformRole,
		CreatedAt:    u.CreatedAt,
	}
}

func toDomainUser(u httpx.User) domain.User {
	return domain.User{
		ID:           u.ID,
		Email:        u.Email,
		DisplayName:  u.DisplayName,
		Kind:         u.Kind,
		PlatformRole: u.PlatformRole,
		CreatedAt:    u.CreatedAt,
	}
}

func toAPISession(s domain.Session) httpx.Session {
	return httpx.Session{
		User:             toAPIUser(s.User),
		CSRFToken:        s.CSRFToken,
		ServicePrincipal: s.ServicePrincipal,
	}
}

type Store struct {
	DB                  *pgxpool.Pool
	Flags               httpx.Flags
	BootstrapInviteCode string
	ControlToken        string
	RuntimeToken        string
}

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
		Flags:               flags.New(s.Flags),
		BootstrapInviteCode: s.BootstrapInviteCode,
		ControlToken:        s.ControlToken,
		RuntimeToken:        s.RuntimeToken,
	}
}

func Open(ctx context.Context, url string, cfgFlags httpx.Flags, code string) (*Store, error) {
	db, err := postgres.Open(ctx, url)
	if err != nil {
		return nil, err
	}
	if code == "" {
		code = DefaultBootstrapInviteCode
	}
	s := &Store{DB: db, Flags: cfgFlags, BootstrapInviteCode: code}
	if err = pg.Migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	return pg.Migrate(ctx, s.DB)
}

func (s *Store) Ping(ctx context.Context) error {
	return s.App().Ping(ctx)
}

func (s *Store) newSession(ctx context.Context, tx pgx.Tx, u httpx.User) (httpx.Session, string, error) {
	ss, token, err := s.App().IssueSession(postgres.WithTx(ctx, tx), toDomainUser(u))
	return toAPISession(ss), token, compatErr(err)
}

func (s *Store) Register(ctx context.Context, email, password, name, code string) (httpx.Session, string, error) {
	ss, token, err := s.App().Register(ctx, email, password, name, code)
	return toAPISession(ss), token, compatErr(err)
}

func (s *Store) Login(ctx context.Context, email, password string) (httpx.Session, string, error) {
	ss, token, err := s.App().Login(ctx, email, password)
	return toAPISession(ss), token, compatErr(err)
}

func (s *Store) Validate(ctx context.Context, token string) (httpx.Session, error) {
	ss, err := s.App().Validate(ctx, token)
	return toAPISession(ss), compatErr(err)
}

func (s *Store) Users(ctx context.Context, ids []string) ([]httpx.User, error) {
	us, err := s.App().GetUsers(ctx, ids)
	if err != nil {
		return nil, compatErr(err)
	}
	out := make([]httpx.User, 0, len(us))
	for _, u := range us {
		out = append(out, toAPIUser(u))
	}
	return out, nil
}

func (s *Store) Logout(ctx context.Context, token string) error {
	return compatErr(s.App().Logout(ctx, token))
}

func (s *Store) Profile(ctx context.Context, id, name string) (httpx.User, error) {
	u, err := s.App().Profile(ctx, id, name)
	return toAPIUser(u), compatErr(err)
}

func (s *Store) BootstrapRequired(ctx context.Context) (bool, error) {
	req, err := s.App().BootstrapRequired(ctx)
	return req, compatErr(err)
}

func (s *Store) CreateRegistrationInvite(ctx context.Context, user string) (RegistrationInvite, string, error) {
	inv, code, err := s.App().CreateRegistrationInvite(ctx, user)
	return inv, code, compatErr(err)
}

func (s *Store) RegistrationInvites(ctx context.Context, user string) ([]RegistrationInvite, error) {
	invs, err := s.App().RegistrationInvites(ctx, user)
	return invs, compatErr(err)
}

func (s *Store) RevokeRegistrationInvite(ctx context.Context, user, id string) error {
	return compatErr(s.App().RevokeRegistrationInvite(ctx, user, id))
}

func validatePassword(password string) error {
	return compatErr(domain.ValidatePassword(password))
}

func (s *Store) loginSession(ctx context.Context, u httpx.User, expectedHash string) (httpx.Session, string, error) {
	ss, token, err := s.App().LoginSession(ctx, toDomainUser(u), expectedHash)
	return toAPISession(ss), token, compatErr(err)
}

func (s *Store) ChangePassword(ctx context.Context, userID, sessionToken, currentPassword, newPassword string) error {
	return compatErr(s.App().ChangePassword(ctx, userID, sessionToken, currentPassword, newPassword))
}

func (s *Store) AdminUsers(ctx context.Context, adminID, sessionToken, cursor string, limit int, query string) (AdminUserPage, error) {
	page, err := s.App().AdminUsers(ctx, adminID, sessionToken, cursor, limit, query)
	if err != nil {
		return AdminUserPage{Users: []httpx.User{}}, compatErr(err)
	}
	users := make([]httpx.User, 0, len(page.Users))
	for _, u := range page.Users {
		users = append(users, toAPIUser(u))
	}
	return AdminUserPage{Users: users, Cursor: page.Cursor}, nil
}

func (s *Store) CreatePasswordResetLink(ctx context.Context, adminID, sessionToken, targetID string) (PasswordReset, string, error) {
	reset, token, err := s.App().CreatePasswordResetLink(ctx, adminID, sessionToken, targetID)
	return reset, token, compatErr(err)
}

func (s *Store) OperatorPasswordResetLink(ctx context.Context, email string) (PasswordReset, string, error) {
	reset, token, err := s.App().OperatorPasswordResetLink(ctx, email)
	return reset, token, compatErr(err)
}

func (s *Store) InspectPasswordReset(ctx context.Context, token string) (PasswordResetInspection, error) {
	res, err := s.App().InspectPasswordReset(ctx, token)
	return res, compatErr(err)
}

func (s *Store) ResetPassword(ctx context.Context, token, newPassword string) error {
	return compatErr(s.App().ResetPassword(ctx, token, newPassword))
}

func (s *Store) ProvisionPrincipal(ctx context.Context, id, name, kind string, enabled, deleted bool, revision int64) (httpx.User, error) {
	u, err := s.App().ProvisionPrincipal(ctx, id, name, kind, enabled, deleted, revision)
	return toAPIUser(u), compatErr(err)
}

func (s *Store) IssuePrincipalToken(ctx context.Context, id string) (string, time.Time, error) {
	token, expiry, err := s.App().IssuePrincipalToken(ctx, id)
	return token, expiry, compatErr(err)
}

func (s *Store) ValidatePrincipal(ctx context.Context, token, key string) (httpx.Session, error) {
	ss, err := s.App().ValidatePrincipal(ctx, token, key)
	return toAPISession(ss), compatErr(err)
}

type Handler struct {
	Store   *Store
	Origin  string
	Secure  bool
	once    sync.Once
	adapter *httpadapter.Adapter
}

func (h *Handler) Routes() http.Handler {
	h.once.Do(func() {
		h.adapter = &httpadapter.Adapter{
			App:    h.Store.App(),
			Auth:   h.Store,
			Origin: h.Origin,
			Secure: h.Secure,
		}
	})
	h.adapter.App = h.Store.App()
	h.adapter.Auth = h.Store
	h.adapter.Origin = h.Origin
	h.adapter.Secure = h.Secure
	return h.adapter.Routes()
}

type RPC struct {
	Store *Store
}

func (h *RPC) server() *rpcadapter.Server {
	return &rpcadapter.Server{App: h.Store.App()}
}

func (h *RPC) ValidateSession(ctx context.Context, r *im.SessionRequest) (*im.SessionResponse, error) {
	return h.server().ValidateSession(ctx, r)
}
func (h *RPC) GetUsers(ctx context.Context, r *im.UsersRequest) (*im.UsersResponse, error) {
	return h.server().GetUsers(ctx, r)
}
func (h *RPC) Ping(ctx context.Context, r *im.PingRequest) (*im.PingResponse, error) {
	return h.server().Ping(ctx, r)
}
func (h *RPC) ProvisionPrincipal(ctx context.Context, r *im.PrincipalControlRequest) (*im.PrincipalResponse, error) {
	return h.server().ProvisionPrincipal(ctx, r)
}
func (h *RPC) IssuePrincipalToken(ctx context.Context, r *im.PrincipalTokenRequest) (*im.PrincipalTokenResponse, error) {
	return h.server().IssuePrincipalToken(ctx, r)
}
func (h *RPC) ValidatePrincipalToken(ctx context.Context, r *im.PrincipalAuthRequest) (*im.SessionResponse, error) {
	return h.server().ValidatePrincipalToken(ctx, r)
}

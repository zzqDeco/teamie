package identity

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	"net/mail"
	"strings"
	"teamie/backend/internal/api"
	"teamie/backend/internal/config"
	"teamie/backend/internal/database"
	"time"
)

//go:embed schema.sql
var schema string

type Store struct {
	DB               *pgxpool.Pool
	Flags            config.Flags
	RegistrationCode string
}

func Open(ctx context.Context, url string, flags config.Flags, code string) (*Store, error) {
	db, err := database.Open(ctx, url)
	if err != nil {
		return nil, err
	}
	s := &Store{db, flags, code}
	if err = s.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Migrate(ctx context.Context) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(704101)"); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, schema); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Ping(ctx context.Context) error { return s.DB.Ping(ctx) }
func normalizeEmail(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	a, e := mail.ParseAddress(v)
	if e != nil || a.Address != v || len(v) > 254 {
		return "", api.Err(400, "invalid_email", "A valid email address is required")
	}
	return v, nil
}
func (s *Store) newSession(ctx context.Context, tx pgx.Tx, u api.User) (api.Session, string, error) {
	token, csrf := api.Random(32), api.Random(32)
	_, e := tx.Exec(ctx, "INSERT INTO identity_sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,$2,$3,$4)", api.Hash(token), u.ID, csrf, time.Now().Add(7*24*time.Hour))
	return api.Session{User: u, CSRFToken: csrf}, token, e
}
func (s *Store) Register(ctx context.Context, email, password, name, code string) (api.Session, string, error) {
	if !s.Flags.Load().Flags["auth.public_signup"] && (s.RegistrationCode == "" || subtle.ConstantTimeCompare([]byte(s.RegistrationCode), []byte(code)) != 1) {
		return api.Session{}, "", api.Err(403, "invite_required", "A valid registration invitation is required")
	}
	email, e := normalizeEmail(email)
	if e != nil {
		return api.Session{}, "", e
	}
	name = strings.TrimSpace(name)
	if !api.BoundedString(name, 1, 80) {
		return api.Session{}, "", api.Err(400, "invalid_name", "Display name must contain 1 to 80 characters")
	}
	if len([]rune(password)) < 12 || len(password) > 72 {
		return api.Session{}, "", api.Err(400, "invalid_password", "Password must contain at least 12 characters and at most 72 bytes")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), 12)
	if e != nil {
		return api.Session{}, "", e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return api.Session{}, "", e
	}
	defer tx.Rollback(ctx)
	u := api.User{ID: api.ID(), Email: email, DisplayName: name, Kind: "human"}
	e = tx.QueryRow(ctx, "INSERT INTO identity_users(id,email,display_name,password_hash) VALUES($1,$2,$3,$4) RETURNING created_at", u.ID, u.Email, u.DisplayName, string(hash)).Scan(&u.CreatedAt)
	if e != nil {
		var pe *pgconn.PgError
		if errors.As(e, &pe) && pe.Code == "23505" {
			e = api.Err(409, "email_in_use", "This email is already registered")
		}
		return api.Session{}, "", e
	}
	ss, token, e := s.newSession(ctx, tx, u)
	if e == nil {
		e = tx.Commit(ctx)
	}
	return ss, token, e
}
func (s *Store) Login(ctx context.Context, email, password string) (api.Session, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var u api.User
	var hash string
	e := s.DB.QueryRow(ctx, "SELECT id,email,display_name,kind,created_at,password_hash FROM identity_users WHERE email=$1", email).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Kind, &u.CreatedAt, &hash)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return api.Session{}, "", e
	}
	if errors.Is(e, pgx.ErrNoRows) {
		hash = "$2a$12$C6UzMDM.H6dfI/f/IKcEe.yr3q8mSrfHGAvQ8N13wZdG.DRZ5V6jW"
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil || u.ID == "" {
		return api.Session{}, "", api.Err(401, "invalid_credentials", "Email or password is incorrect")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return api.Session{}, "", e
	}
	defer tx.Rollback(ctx)
	ss, token, e := s.newSession(ctx, tx, u)
	if e == nil {
		e = tx.Commit(ctx)
	}
	return ss, token, e
}
func (s *Store) Validate(ctx context.Context, token string) (api.Session, error) {
	var ss api.Session
	if len(token) != 64 {
		return ss, api.Err(401, "unauthenticated", "Sign in to continue")
	}
	e := s.DB.QueryRow(ctx, `SELECT u.id,u.email,u.display_name,u.kind,u.created_at,s.csrf_token FROM identity_sessions s JOIN identity_users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now()`, api.Hash(token)).Scan(&ss.User.ID, &ss.User.Email, &ss.User.DisplayName, &ss.User.Kind, &ss.User.CreatedAt, &ss.CSRFToken)
	if errors.Is(e, pgx.ErrNoRows) {
		e = api.Err(401, "unauthenticated", "Session expired or revoked")
	}
	return ss, e
}
func (s *Store) Users(ctx context.Context, ids []string) ([]api.User, error) {
	users := []api.User{}
	if len(ids) > 1000 {
		return nil, api.Err(400, "invalid_request", "Too many users")
	}
	rows, e := s.DB.Query(ctx, "SELECT id,email,display_name,kind,created_at FROM identity_users WHERE id=ANY($1)", ids)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var u api.User
		if e = rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Kind, &u.CreatedAt); e != nil {
			return nil, e
		}
		users = append(users, u)
	}
	return users, rows.Err()
}
func (s *Store) Logout(ctx context.Context, token string) error {
	_, e := s.DB.Exec(ctx, "DELETE FROM identity_sessions WHERE token_hash=$1", api.Hash(token))
	return e
}
func (s *Store) Profile(ctx context.Context, id, name string) (api.User, error) {
	var u api.User
	name = strings.TrimSpace(name)
	if !api.BoundedString(name, 1, 80) {
		return u, api.Err(400, "invalid_name", "Display name must contain 1 to 80 characters")
	}
	e := s.DB.QueryRow(ctx, "UPDATE identity_users SET display_name=$2 WHERE id=$1 RETURNING id,email,display_name,kind,created_at", id, name).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Kind, &u.CreatedAt)
	return u, e
}

// Package chat wires the Chat 4-layer Clean DDD components
// (domain, app, adapter/http, adapter/rpc, infra/pg) and exposes the service
// composition root.
package chat

import (
	"context"
	_ "embed"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"teamie/backend/internal/api"
	"teamie/backend/internal/chat/app"
	"teamie/backend/internal/chat/domain"
	"teamie/backend/internal/chat/infra/pg"
	"teamie/backend/internal/config"
	"teamie/backend/pkg/imtext"
	"teamie/backend/pkg/postgres"
)

//go:embed schema.sql
var schema string

//go:embed migrations/002_membership_management.sql
var schemaV2 string

//go:embed migrations/003_attachments.sql
var schemaV3 string

//go:embed migrations/004_attachment_uploads.sql
var schemaV4 string

//go:embed migrations/005_mentions.sql
var schemaV5 string

type flagsAdapter struct {
	flags config.Flags
}

func (f flagsAdapter) Enabled(key string) bool {
	return f.flags.Load().Flags[key]
}

func (f flagsAdapter) Snapshot() (string, map[string]bool) {
	snap := f.flags.Load()
	return snap.Revision, snap.Flags
}

type identityAdapter struct {
	identity api.Identity
}

func (i identityAdapter) Users(ctx context.Context, ids []string) ([]domain.User, error) {
	if i.identity == nil {
		return nil, nil
	}
	us, err := i.identity.Users(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domain.User, 0, len(us))
	for _, u := range us {
		out = append(out, toDomainUser(u))
	}
	return out, nil
}

func (i identityAdapter) Ping(ctx context.Context) error {
	if i.identity == nil {
		return nil
	}
	return i.identity.Ping(ctx)
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

func toDomainMentions(ms []imtext.Mention) []domain.Mention {
	if ms == nil {
		return nil
	}
	out := make([]domain.Mention, len(ms))
	for i, m := range ms {
		out[i] = domain.Mention{UserID: m.UserID, Start: m.Start, End: m.End}
	}
	return out
}

func fromDomainMentions(ms []domain.Mention) []imtext.Mention {
	if ms == nil {
		return nil
	}
	out := make([]imtext.Mention, len(ms))
	for i, m := range ms {
		out[i] = imtext.Mention{UserID: m.UserID, Start: m.Start, End: m.End}
	}
	return out
}

func fromDomainMessage(m domain.Message) Message {
	return Message{
		ID:              m.ID,
		ConversationID:  m.ConversationID,
		SenderID:        m.SenderID,
		SenderName:      m.SenderName,
		SenderKind:      m.SenderKind,
		ClientMessageID: m.ClientMessageID,
		Seq:             m.Seq,
		Body:            m.Body,
		ReplyToID:       m.ReplyToID,
		CreatedAt:       m.CreatedAt,
		EditedAt:        m.EditedAt,
		RetractedAt:     m.RetractedAt,
		Attachments:     m.Attachments,
		Mentions:        fromDomainMentions(m.Mentions),
	}
}

func fromDomainMessages(ms []domain.Message) []Message {
	out := make([]Message, len(ms))
	for i, m := range ms {
		out[i] = fromDomainMessage(m)
	}
	return out
}

func fromDomainEvent(ev domain.Event) Event {
	var msg *Message
	if ev.Message != nil {
		m := fromDomainMessage(*ev.Message)
		msg = &m
	}
	return Event{
		ID:             ev.ID,
		Type:           ev.Type,
		ConversationID: ev.ConversationID,
		WorkspaceID:    ev.WorkspaceID,
		Message:        msg,
		Conversation:   ev.Conversation,
		ReadSeq:        ev.ReadSeq,
		CreatedAt:      ev.CreatedAt,
	}
}

// Store composes the PostgreSQL infrastructure adapter and Chat application service.
type Store struct {
	DB    *pgxpool.Pool
	Flags config.Flags
}

// App returns the Clean DDD application service bound to this Store's infrastructure.
func (s *Store) App(identity api.Identity, controlToken string) *app.Service {
	repo := pg.New(postgres.New(s.DB))
	var idDir domain.IdentityDirectory
	if identity != nil {
		idDir = identityAdapter{identity: identity}
	}
	return &app.Service{
		Tx:             repo,
		Workspaces:     repo,
		Conversations:  repo,
		Memberships:    repo,
		Messages:       repo,
		Attachments:    repo,
		Outbox:         repo,
		Identity:       idDir,
		Flags:          flagsAdapter{flags: s.Flags},
		SniffMediaType: http.DetectContentType,
		ControlToken:   controlToken,
	}
}

func Open(ctx context.Context, url string, flags config.Flags) (*Store, error) {
	db, err := postgres.Open(ctx, url)
	if err != nil {
		return nil, err
	}
	s := &Store{DB: db, Flags: flags}
	if err = s.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	return postgres.Migrate(ctx, s.DB, 704102, "chat_schema_migrations", []postgres.Migration{
		{Version: 1, SQL: schema},
		{Version: 2, SQL: schemaV2},
		{Version: 3, SQL: schemaV3},
		{Version: 4, SQL: schemaV4},
		{Version: 5, SQL: schemaV5},
	})
}

func (s *Store) Ping(ctx context.Context) error {
	return s.App(nil, "").Ping(ctx)
}

func conversationAccess(ctx context.Context, db *pgxpool.Pool, id, user string) (Conversation, error) {
	repo := pg.New(postgres.New(db))
	c, err := repo.ConversationAccess(ctx, id, user)
	return c, compatErr(err)
}

func (s *Store) Workspaces(ctx context.Context, user string) ([]Workspace, error) {
	ws, err := s.App(nil, "").ListWorkspaces(ctx, user)
	return ws, compatErr(err)
}

func (s *Store) Conversations(ctx context.Context, user, ws string) ([]Conversation, error) {
	cs, err := s.App(nil, "").ListConversations(ctx, user, ws)
	return cs, compatErr(err)
}

func (s *Store) Head(ctx context.Context, user string) (string, error) {
	h, err := s.App(nil, "").Head(ctx, user)
	return h, compatErr(err)
}

func (s *Store) Sync(ctx context.Context, user, cursor string, limit int) ([]Event, string, bool, error) {
	evs, next, more, err := s.App(nil, "").Sync(ctx, user, cursor, limit)
	if err != nil {
		return nil, "", false, compatErr(err)
	}
	out := make([]Event, len(evs))
	for i, ev := range evs {
		out[i] = fromDomainEvent(ev)
	}
	return out, next, more, nil
}

// Package pg implements the Chat outbound repository ports backed by PostgreSQL
// using the shared teamie/pkg/postgres mechanism.
package pg

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5/pgxpool"
	"teamie/pkg/postgres"
	"teamie/services/chat/internal/domain"
)

//go:embed migrations/001_chat.sql
var SchemaV1 string

//go:embed migrations/002_membership_management.sql
var SchemaV2 string

//go:embed migrations/003_attachments.sql
var SchemaV3 string

//go:embed migrations/004_attachment_uploads.sql
var SchemaV4 string

//go:embed migrations/005_mentions.sql
var SchemaV5 string

//go:embed migrations/006_conversation_mutations.sql
var SchemaV6 string

//go:embed migrations/007_reactions_and_notification_levels.sql
var SchemaV7 string

// Migrate applies all Chat PostgreSQL schema migrations under advisory lock 704102.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return postgres.Migrate(ctx, pool, 704102, "chat_schema_migrations", []postgres.Migration{
		{Version: 1, SQL: SchemaV1},
		{Version: 2, SQL: SchemaV2},
		{Version: 3, SQL: SchemaV3},
		{Version: 4, SQL: SchemaV4},
		{Version: 5, SQL: SchemaV5},
		{Version: 6, SQL: SchemaV6},
		{Version: 7, SQL: SchemaV7},
	})
}

const (
	senderClientMessageSeed int64 = 704103
	attachmentSenderSeed    int64 = 704104
	principalMembershipSeed int64 = 704106
	messageColumns                = "id,conversation_id,sender_id,sender_name,sender_kind,client_message_id,seq,body,reply_to_id,created_at,edited_at,retracted_at,attachments,mentions"
)

// Repository implements the Chat domain repository and TxManager interfaces on
// top of the shared context-aware postgres.DB handle.
type Repository struct {
	DB *postgres.DB
}

// New creates a PostgreSQL-backed Chat repository.
func New(db *postgres.DB) *Repository {
	return &Repository{DB: db}
}

func notFound(err error) error {
	if postgres.IsNoRows(err) {
		return domain.ErrNotFound()
	}
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMessage(row rowScanner) (domain.Message, error) {
	var m domain.Message
	err := row.Scan(&m.ID, &m.ConversationID, &m.SenderID, &m.SenderName, &m.SenderKind, &m.ClientMessageID, &m.Seq, &m.Body, &m.ReplyToID, &m.CreatedAt, &m.EditedAt, &m.RetractedAt, &m.Attachments, &m.Mentions)
	if m.RetractedAt != nil {
		m.Body = ""
		m.Attachments = nil
		m.Mentions = nil
	}
	return m, err
}

// --- TxManager ---

func (r *Repository) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return r.DB.WithinTx(ctx, fn)
}

func (r *Repository) LockSenderClientMessage(ctx context.Context, senderID, clientMessageID string) error {
	return r.DB.AdvisoryXactLockKey(ctx, senderID+":"+clientMessageID, senderClientMessageSeed)
}

func (r *Repository) LockAttachmentSender(ctx context.Context, userID string) error {
	return r.DB.AdvisoryXactLockKey(ctx, userID, attachmentSenderSeed)
}

func (r *Repository) LockPrincipalMembership(ctx context.Context, principalID string) error {
	return r.DB.AdvisoryXactLockKey(ctx, principalID, principalMembershipSeed)
}

func (r *Repository) Ping(ctx context.Context) error {
	return r.DB.Ping(ctx)
}

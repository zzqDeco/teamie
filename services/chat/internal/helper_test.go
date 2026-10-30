package chat

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/pkg/httpx"
	"teamie/pkg/imtext"
	"teamie/pkg/postgres"
	httpadapter "teamie/services/chat/internal/adapter/http"
	rpcadapter "teamie/services/chat/internal/adapter/rpc"
	"teamie/services/chat/internal/app"
	"teamie/services/chat/internal/domain"
	"teamie/services/chat/internal/infra/flags"
	"teamie/services/chat/internal/infra/pg"
	"teamie/services/chat/internal/infra/probe"
	redisinfra "teamie/services/chat/internal/infra/redis"
	"teamie/services/chat/internal/infra/rpcclient"
)

const HintChannel = domain.HintChannel

var (
	schema              = pg.SchemaV1
	schemaV2            = pg.SchemaV2
	schemaV3            = pg.SchemaV3
	schemaV4            = pg.SchemaV4
	schemaV5            = pg.SchemaV5
	attachmentTransfers = httpadapter.DefaultTransfers
)

type Workspace = domain.Workspace
type Conversation = domain.Conversation
type Attachment = domain.Attachment
type Member = domain.Member
type Reaction = domain.Reaction
type ReadReceipts = domain.ReadReceipts
type ReadSeqCount = domain.ReadSeqCount
type MemberReadReceipt = domain.MemberReadReceipt
type AttentionConversation = domain.AttentionConversation

type Message struct {
	ID              string           `json:"id"`
	ConversationID  string           `json:"conversationId"`
	SenderID        string           `json:"senderId"`
	SenderName      string           `json:"senderName"`
	SenderKind      string           `json:"senderKind"`
	ClientMessageID string           `json:"clientMessageId"`
	Seq             int64            `json:"seq"`
	Body            string           `json:"body"`
	ReplyToID       *string          `json:"replyToId,omitempty"`
	CreatedAt       time.Time        `json:"createdAt"`
	EditedAt        *time.Time       `json:"editedAt,omitempty"`
	RetractedAt     *time.Time       `json:"retractedAt,omitempty"`
	Attachments     []Attachment     `json:"attachments,omitempty"`
	Mentions        []imtext.Mention `json:"mentions,omitempty"`
	Reactions       []Reaction       `json:"reactions,omitempty"`
}

type Event struct {
	ID                string        `json:"id"`
	Type              string        `json:"type"`
	ConversationID    string        `json:"conversationId,omitempty"`
	WorkspaceID       string        `json:"workspaceId,omitempty"`
	Message           *Message      `json:"message,omitempty"`
	Conversation      *Conversation `json:"conversation,omitempty"`
	ReadSeq           *int64        `json:"readSeq,omitempty"`
	MessageID         string        `json:"messageId,omitempty"`
	Reactions         []Reaction    `json:"reactions,omitempty"`
	NotificationLevel *string       `json:"notificationLevel,omitempty"`
	CreatedAt         time.Time     `json:"createdAt"`
}

type WorkspaceSearchItem struct {
	Message      Message               `json:"message"`
	Conversation AttentionConversation `json:"conversation"`
}

type WorkspaceSearchPage struct {
	Items  []WorkspaceSearchItem `json:"items"`
	Cursor string                `json:"cursor,omitempty"`
}

type AttentionItem struct {
	Message       Message               `json:"message"`
	Conversation  AttentionConversation `json:"conversation"`
	WorkspaceName string                `json:"workspaceName"`
	Reasons       []string              `json:"reasons"`
	IsRead        bool                  `json:"isRead"`
}

type AttentionPage struct {
	Items                         []AttentionItem          `json:"items"`
	Cursor                        string                   `json:"cursor,omitempty"`
	UnreadCount                   int64                    `json:"unreadCount"`
	ChannelUnreadCount            int64                    `json:"channelUnreadCount"`
	AttentionUnreadByConversation []domain.AttentionUnread `json:"attentionUnreadByConversation"`
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
		Reactions:       m.Reactions,
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
		ID:                ev.ID,
		Type:              ev.Type,
		ConversationID:    ev.ConversationID,
		WorkspaceID:       ev.WorkspaceID,
		Message:           msg,
		Conversation:      ev.Conversation,
		ReadSeq:           ev.ReadSeq,
		MessageID:         ev.MessageID,
		Reactions:         ev.Reactions,
		NotificationLevel: ev.NotificationLevel,
		CreatedAt:         ev.CreatedAt,
	}
}

type Store struct {
	DB    *pgxpool.Pool
	Flags httpx.Flags
}

func (s *Store) App(identity httpx.Identity, controlToken string) *app.Service {
	repo := pg.New(postgres.New(s.DB))
	var idDir domain.IdentityDirectory
	if identity != nil {
		idDir = rpcclient.NewIdentity(identity)
	}
	return &app.Service{
		Tx:             repo,
		Mutations:      repo,
		Workspaces:     repo,
		Conversations:  repo,
		Memberships:    repo,
		Messages:       repo,
		Attachments:    repo,
		Marketplace:    repo,
		Outbox:         repo,
		Identity:       idDir,
		Flags:          flags.New(s.Flags),
		Verifier:       probe.New("https://teamie.example", false),
		SniffMediaType: http.DetectContentType,
		ControlToken:   controlToken,
		AppOrigin:      "https://teamie.example",
	}
}

func Open(ctx context.Context, url string, cfgFlags httpx.Flags) (*Store, error) {
	db, err := postgres.Open(ctx, url)
	if err != nil {
		return nil, err
	}
	s := &Store{DB: db, Flags: cfgFlags}
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

func (s *Store) CreateWorkspace(ctx context.Context, user, name string) (Workspace, Conversation, error) {
	ws, c, err := s.App(nil, "").CreateWorkspace(ctx, user, name)
	return ws, c, compatErr(err)
}

func (s *Store) Members(ctx context.Context, user, ws string) ([]Member, error) {
	ms, err := s.App(nil, "").Members(ctx, user, ws)
	return ms, compatErr(err)
}

func (s *Store) CreateInvite(ctx context.Context, user, ws string) (string, time.Time, error) {
	code, exp, err := s.App(nil, "").CreateInvite(ctx, user, ws)
	return code, exp, compatErr(err)
}

func (s *Store) Invite(ctx context.Context, code string) (Workspace, time.Time, error) {
	ws, exp, err := s.App(nil, "").Invite(ctx, code)
	return ws, exp, compatErr(err)
}

func (s *Store) AcceptInvite(ctx context.Context, user, code string) (Workspace, []Conversation, error) {
	ws, cs, err := s.App(nil, "").AcceptInvite(ctx, user, code)
	return ws, cs, compatErr(err)
}

func (s *Store) RemoveMember(ctx context.Context, user, ws, target string) error {
	return compatErr(s.App(nil, "").RemoveMember(ctx, user, ws, target))
}

func (s *Store) CreateConversation(ctx context.Context, user, ws, kind, name string, members []string) (Conversation, error) {
	c, err := s.App(nil, "").CreateConversation(ctx, user, ws, kind, name, members)
	return c, compatErr(err)
}

func (s *Store) ParticipantWorkspaces(ctx context.Context, user string) ([]string, error) {
	ids, err := s.App(nil, "").ParticipantWorkspaces(ctx, user)
	return ids, compatErr(err)
}

func (s *Store) WorkspaceParticipants(ctx context.Context, workspace, requester string) ([]string, error) {
	ids, err := s.App(nil, "").WorkspaceParticipants(ctx, workspace, requester)
	return ids, compatErr(err)
}

func (s *Store) SetPrincipalWorkspaces(ctx context.Context, principal, grantor httpx.User, requested []string, revision int64) ([]string, error) {
	ids, err := s.App(nil, "").SetPrincipalWorkspaces(ctx, toDomainUser(principal), toDomainUser(grantor), requested, revision)
	return ids, compatErr(err)
}

func (s *Store) GroupMember(ctx context.Context, actor, conversation, target string, add bool) (Conversation, error) {
	c, err := s.App(nil, "").GroupMember(ctx, actor, conversation, target, add)
	return c, compatErr(err)
}

func (s *Store) Message(ctx context.Context, user, conversation, id string) (Message, error) {
	m, err := s.App(nil, "").Message(ctx, user, conversation, id)
	return fromDomainMessage(m), compatErr(err)
}

func (s *Store) Messages(ctx context.Context, user, conversation string, before int64, limit int) ([]Message, bool, error) {
	return s.MessageHistory(ctx, user, conversation, before, 0, "", limit)
}

func (s *Store) MessageHistory(ctx context.Context, user, conversation string, before, around int64, query string, limit int) ([]Message, bool, error) {
	ms, more, err := s.App(nil, "").MessageHistory(ctx, user, conversation, before, around, query, limit)
	if err != nil {
		return nil, false, compatErr(err)
	}
	return fromDomainMessages(ms), more, nil
}

func (s *Store) FileHistory(ctx context.Context, user, conversation string, before int64, query string, limit int) ([]Message, bool, error) {
	ms, more, err := s.App(nil, "").FileHistory(ctx, user, conversation, before, query, limit)
	if err != nil {
		return nil, false, compatErr(err)
	}
	return fromDomainMessages(ms), more, nil
}

func (s *Store) Send(ctx context.Context, user httpx.User, conversation, clientID, body string, reply *string) (Message, bool, error) {
	return s.SendWithAttachments(ctx, user, conversation, clientID, body, reply, nil)
}

func (s *Store) SendWithAttachments(ctx context.Context, user httpx.User, conversation, clientID, body string, reply *string, ids []string) (Message, bool, error) {
	return s.SendAnnotated(ctx, user, conversation, clientID, body, reply, ids, nil)
}

func (s *Store) SendAnnotated(ctx context.Context, user httpx.User, conversation, clientID, body string, reply *string, ids []string, mentions []imtext.Mention) (Message, bool, error) {
	m, created, err := s.App(nil, "").SendAnnotated(ctx, toDomainUser(user), conversation, clientID, body, reply, ids, toDomainMentions(mentions))
	return fromDomainMessage(m), created, compatErr(err)
}

func (s *Store) ChangeMessage(ctx context.Context, user, conversation, id, body string, retract bool) (Message, error) {
	return s.ChangeAnnotatedMessage(ctx, user, conversation, id, body, retract, nil)
}

func (s *Store) ChangeAnnotatedMessage(ctx context.Context, user, conversation, id, body string, retract bool, mentions []imtext.Mention) (Message, error) {
	m, err := s.App(nil, "").ChangeAnnotatedMessage(ctx, user, conversation, id, body, retract, toDomainMentions(mentions))
	return fromDomainMessage(m), compatErr(err)
}

func (s *Store) Read(ctx context.Context, user, conversation string, seq int64) (int64, error) {
	n, err := s.App(nil, "").Read(ctx, user, conversation, seq)
	return n, compatErr(err)
}

func (s *Store) SearchWorkspaceMessages(ctx context.Context, user, workspaceID, query, rawCursor string, limit int) (WorkspaceSearchPage, error) {
	page, err := s.App(nil, "").SearchWorkspaceMessages(ctx, user, workspaceID, query, rawCursor, limit)
	if err != nil {
		return WorkspaceSearchPage{Items: []WorkspaceSearchItem{}}, compatErr(err)
	}
	items := make([]WorkspaceSearchItem, len(page.Items))
	for i, it := range page.Items {
		items[i] = WorkspaceSearchItem{
			Message:      fromDomainMessage(it.Message),
			Conversation: it.Conversation,
		}
	}
	return WorkspaceSearchPage{Items: items, Cursor: page.Cursor}, nil
}

func (s *Store) Attention(ctx context.Context, user, rawCursor string, limit int, unreadOnly bool) (AttentionPage, error) {
	page, err := s.App(nil, "").Attention(ctx, user, rawCursor, limit, unreadOnly)
	if err != nil {
		return AttentionPage{Items: []AttentionItem{}}, compatErr(err)
	}
	items := make([]AttentionItem, len(page.Items))
	for i, it := range page.Items {
		items[i] = AttentionItem{
			Message:       fromDomainMessage(it.Message),
			Conversation:  it.Conversation,
			WorkspaceName: it.WorkspaceName,
			Reasons:       it.Reasons,
			IsRead:        it.IsRead,
		}
	}
	return AttentionPage{
		Items:                         items,
		Cursor:                        page.Cursor,
		UnreadCount:                   page.UnreadCount,
		ChannelUnreadCount:            page.ChannelUnreadCount,
		AttentionUnreadByConversation: page.AttentionUnreadByConversation,
	}, nil
}

func acquireAttachmentTransfer() (func(), error) {
	select {
	case attachmentTransfers <- struct{}{}:
		return func() { <-attachmentTransfers }, nil
	default:
		return nil, httpx.Err(429, "attachment_busy", "File transfers are busy; please retry")
	}
}

func (s *Store) UploadAttachment(ctx context.Context, user, conversation, filename string, content []byte) (Attachment, error) {
	a, _, err := s.UploadAttachmentWithKey(ctx, user, conversation, filename, content, "")
	return a, err
}

func (s *Store) UploadAttachmentWithKey(ctx context.Context, user, conversation, filename string, content []byte, key string) (Attachment, bool, error) {
	a, first, err := s.App(nil, "").UploadAttachmentWithKey(ctx, user, conversation, filename, content, key)
	return a, first, compatErr(err)
}

func (s *Store) LookupAttachmentUpload(ctx context.Context, user, conversation, key string) (Attachment, error) {
	a, err := s.App(nil, "").LookupAttachmentUpload(ctx, user, conversation, key)
	return a, compatErr(err)
}

func (s *Store) DeletePendingAttachment(ctx context.Context, user, conversation, id string) error {
	return compatErr(s.App(nil, "").DeletePendingAttachment(ctx, user, conversation, id))
}

func (s *Store) DownloadAttachment(ctx context.Context, user, conversation, id string) (Attachment, []byte, error) {
	a, content, err := s.App(nil, "").DownloadAttachment(ctx, user, conversation, id)
	return a, content, compatErr(err)
}

func (s *Store) RunOutbox(ctx context.Context, rdb *goredis.Client) {
	redisinfra.RunOutbox(ctx, s.App(nil, ""), rdb)
}

func (s *Store) RunAttachmentCleanup(ctx context.Context) {
	redisinfra.RunAttachmentCleanup(ctx, s.App(nil, ""))
}

func (s *Store) prunePublishedOutbox(ctx context.Context) (int64, error) {
	return s.App(nil, "").PrunePublishedOutbox(ctx)
}

type outboxPublisher = redisinfra.Publisher

func (s *Store) publishOne(ctx context.Context, rdb outboxPublisher) (bool, error) {
	return s.App(nil, "").PublishOneOutbox(ctx, redisinfra.Adapter{Pub: rdb})
}

type Handler struct {
	Store    *Store
	Identity httpx.Identity
	Origin   string
}

func (h *Handler) Routes() http.Handler {
	svc := h.Store.App(h.Identity, "")
	if h.Origin != "" {
		svc.AppOrigin = h.Origin
		svc.Verifier = probe.New(h.Origin, false)
	}
	adapter := &httpadapter.Adapter{
		App:       svc,
		Auth:      h.Identity,
		Origin:    h.Origin,
		Transfers: attachmentTransfers,
	}
	return adapter.Routes()
}

type RPC struct {
	Store        *Store
	Identity     httpx.Identity
	ControlToken string
}

func (h *RPC) server() *rpcadapter.Server {
	return &rpcadapter.Server{
		App: h.Store.App(h.Identity, h.ControlToken),
	}
}

func (h *RPC) CheckAccess(ctx context.Context, r *im.AccessRequest) (*im.AccessResponse, error) {
	return h.server().CheckAccess(ctx, r)
}
func (h *RPC) CheckWorkspaceAccess(ctx context.Context, r *im.WorkspaceAccessRequest) (*im.WorkspaceAccessResponse, error) {
	return h.server().CheckWorkspaceAccess(ctx, r)
}
func (h *RPC) GetSharedWorkspaceMembers(ctx context.Context, r *im.SharedWorkspaceMembersRequest) (*im.SharedWorkspaceMembersResponse, error) {
	return h.server().GetSharedWorkspaceMembers(ctx, r)
}
func (h *RPC) Ping(ctx context.Context, r *im.PingRequest) (*im.PingResponse, error) {
	return h.server().Ping(ctx, r)
}
func (h *RPC) GetParticipantWorkspaces(ctx context.Context, r *im.ParticipantWorkspacesRequest) (*im.ParticipantWorkspacesResponse, error) {
	return h.server().GetParticipantWorkspaces(ctx, r)
}
func (h *RPC) GetWorkspaceParticipants(ctx context.Context, r *im.WorkspaceParticipantsRequest) (*im.WorkspaceParticipantsResponse, error) {
	return h.server().GetWorkspaceParticipants(ctx, r)
}
func (h *RPC) SetPrincipalWorkspaces(ctx context.Context, r *im.PrincipalWorkspacesRequest) (*im.ParticipantWorkspacesResponse, error) {
	return h.server().SetPrincipalWorkspaces(ctx, r)
}

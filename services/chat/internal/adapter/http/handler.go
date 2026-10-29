// Package http implements the inbound HTTP adapter for the Chat service,
// binding generated Thrift HTTP DTOs (teamie/contracts/gen/im/hertz_gen/im/chat) to
// the Chat application layer.
package http

import (
	"context"
	"net/http"
	"strconv"
	"time"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/cloudwego/hertz/pkg/route"
	chatgen "teamie/contracts/gen/im/hertz_gen/im/chat"
	"teamie/pkg/httpx"
	"teamie/pkg/imlimits"
	"teamie/pkg/observability"
	"teamie/services/chat/internal/app"
	"teamie/services/chat/internal/domain"
)

type actorContextKey struct{}

// WithActor attaches the authenticated domain.User to ctx.
func WithActor(ctx context.Context, u domain.User) context.Context {
	return context.WithValue(ctx, actorContextKey{}, u)
}

func actorFromContext(ctx context.Context) (domain.User, bool) {
	u, ok := ctx.Value(actorContextKey{}).(domain.User)
	return u, ok
}

type responseStatusSinkKey struct{}

func withResponseStatusSink(ctx context.Context, dst *int) context.Context {
	return context.WithValue(ctx, responseStatusSinkKey{}, dst)
}

func recordResponseStatus(ctx context.Context, status int) {
	if dst, ok := ctx.Value(responseStatusSinkKey{}).(*int); ok && dst != nil {
		*dst = status
	}
}

func responseStatusFromContext(ctx context.Context, fallback int) int {
	if dst, ok := ctx.Value(responseStatusSinkKey{}).(*int); ok && dst != nil && *dst > 0 {
		return *dst
	}
	return fallback
}

// Adapter implements the generated chatgen.ChatHTTPService contract and wires
// HTTP routes to the Chat application service.
type Adapter struct {
	App       *app.Service
	Auth      httpx.Identity
	Origin    string
	Transfers chan struct{}
}

// Compile-time verification that Adapter implements the generated Thrift HTTP service interface.
var _ chatgen.ChatHTTPService = (*Adapter)(nil)

// DefaultTransfers bounds concurrent upload/download bytea buffers under Chat's
// 144 MiB Go memory budget when Adapter.Transfers is not explicitly overridden.
var DefaultTransfers = make(chan struct{}, 2)

func (a *Adapter) acquireTransfer() (func(), error) {
	ch := a.Transfers
	if ch == nil {
		ch = DefaultTransfers
	}
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	default:
		return nil, domain.ErrAttachmentBusy()
	}
}

func formatOptTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.UTC().Format(time.RFC3339Nano)
	return &v
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toUserDTO(u domain.User) *chatgen.UserDTO {
	return &chatgen.UserDTO{
		ID:           u.ID,
		Email:        u.Email,
		DisplayName:  u.DisplayName,
		Kind:         u.Kind,
		PlatformRole: u.PlatformRole,
		CreatedAt:    u.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func toWorkspaceDTO(ws domain.Workspace) *chatgen.WorkspaceDTO {
	return &chatgen.WorkspaceDTO{
		ID:        ws.ID,
		Name:      ws.Name,
		CreatedBy: ws.CreatedBy,
		CreatedAt: ws.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func toMemberDTO(m domain.Member) *chatgen.MemberDTO {
	return &chatgen.MemberDTO{
		UserId:      m.UserID,
		DisplayName: m.DisplayName,
		Role:        m.Role,
		Kind:        m.Kind,
	}
}

func toConversationDTO(c domain.Conversation) *chatgen.ConversationDTO {
	members := c.MemberIDs
	if members == nil {
		members = []string{}
	}
	level := c.NotificationLevel
	if level == "" {
		level = "all"
	}
	return &chatgen.ConversationDTO{
		ID:                c.ID,
		WorkspaceId:       c.WorkspaceID,
		Type:              c.Type,
		Name:              c.Name,
		MemberIds:         members,
		LastSeq:           c.LastSeq,
		ReadSeq:           c.ReadSeq,
		UpdatedAt:         c.UpdatedAt.UTC().Format(time.RFC3339Nano),
		CreatedBy:         optString(c.CreatedBy),
		CanManageMembers:  c.CanManageMembers,
		NotificationLevel: &level,
	}
}

func toAttachmentDTO(a domain.Attachment) *chatgen.AttachmentDTO {
	return &chatgen.AttachmentDTO{
		ID:        a.ID,
		Filename:  a.Filename,
		MediaType: a.MediaType,
		SizeBytes: int64(a.SizeBytes),
		Sha256:    a.SHA256,
	}
}

func toMentionDTO(m domain.Mention) *chatgen.MentionDTO {
	return &chatgen.MentionDTO{
		UserId: m.UserID,
		Start:  int64(m.Start),
		End:    int64(m.End),
	}
}

func fromMentionDTOs(ms []*chatgen.MentionDTO) ([]domain.Mention, error) {
	if len(ms) == 0 {
		return nil, nil
	}
	out := make([]domain.Mention, 0, len(ms))
	for _, m := range ms {
		if m == nil {
			return nil, domain.Err(400, "invalid_mentions", "Invalid mentions")
		}
		out = append(out, domain.Mention{
			UserID: m.UserId,
			Start:  int(m.Start),
			End:    int(m.End),
		})
	}
	return out, nil
}

func reactionVersionDTO(version int64) *string {
	value := strconv.FormatInt(version, 10)
	return &value
}

func eventReactionVersionDTO(event domain.Event) *string {
	if event.Type != "reaction.updated" {
		return nil
	}
	return reactionVersionDTO(event.ReactionsVersion)
}

func toReactionDTOs(rs []domain.Reaction) []*chatgen.ReactionDTO {
	if len(rs) == 0 {
		return nil
	}
	out := make([]*chatgen.ReactionDTO, 0, len(rs))
	for _, r := range rs {
		uids := r.UserIDs
		if uids == nil {
			uids = []string{}
		}
		out = append(out, &chatgen.ReactionDTO{
			Emoji:   r.Emoji,
			Count:   r.Count,
			UserIds: uids,
		})
	}
	return out
}

func toMessageDTO(m domain.Message) *chatgen.MessageDTO {
	var attachments []*chatgen.AttachmentDTO
	if len(m.Attachments) > 0 {
		attachments = make([]*chatgen.AttachmentDTO, 0, len(m.Attachments))
		for _, att := range m.Attachments {
			attachments = append(attachments, toAttachmentDTO(att))
		}
	}
	var mentions []*chatgen.MentionDTO
	if len(m.Mentions) > 0 {
		mentions = make([]*chatgen.MentionDTO, 0, len(m.Mentions))
		for _, men := range m.Mentions {
			mentions = append(mentions, toMentionDTO(men))
		}
	}
	return &chatgen.MessageDTO{
		ID:               m.ID,
		ConversationId:   m.ConversationID,
		SenderId:         m.SenderID,
		SenderName:       m.SenderName,
		SenderKind:       m.SenderKind,
		ClientMessageId:  m.ClientMessageID,
		Seq:              m.Seq,
		Body:             m.Body,
		ReplyToId:        m.ReplyToID,
		CreatedAt:        m.CreatedAt.UTC().Format(time.RFC3339Nano),
		EditedAt:         formatOptTime(m.EditedAt),
		RetractedAt:      formatOptTime(m.RetractedAt),
		Attachments:      attachments,
		Mentions:         mentions,
		Reactions:        toReactionDTOs(m.Reactions),
		ReactionsVersion: reactionVersionDTO(m.ReactionsVersion),
	}
}

func toEventDTO(ev domain.Event) *chatgen.EventDTO {
	var msg *chatgen.MessageDTO
	if ev.Message != nil {
		msg = toMessageDTO(*ev.Message)
	}
	var conv *chatgen.ConversationDTO
	if ev.Conversation != nil {
		conv = toConversationDTO(*ev.Conversation)
	}
	reactions := toReactionDTOs(ev.Reactions)
	if ev.Type == "reaction.updated" && reactions == nil {
		reactions = []*chatgen.ReactionDTO{}
	}
	return &chatgen.EventDTO{
		ID:                ev.ID,
		Type:              ev.Type,
		ConversationId:    optString(ev.ConversationID),
		WorkspaceId:       optString(ev.WorkspaceID),
		Message:           msg,
		Conversation:      conv,
		ReadSeq:           ev.ReadSeq,
		MessageId:         optString(ev.MessageID),
		Reactions:         reactions,
		ReactionsVersion:  eventReactionVersionDTO(ev),
		NotificationLevel: ev.NotificationLevel,
		CreatedAt:         ev.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func toAttentionConversationDTO(c domain.AttentionConversation) *chatgen.AttentionConversationDTO {
	return &chatgen.AttentionConversationDTO{
		ID:          c.ID,
		WorkspaceId: c.WorkspaceID,
		Name:        c.Name,
		Type:        c.Type,
	}
}

func parseIntParam(raw, key string, fallback, min, max int64) (int64, error) {
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < min || n > max {
		return 0, domain.Err(400, "invalid_query", "Invalid "+key)
	}
	return n, nil
}

func (a *Adapter) GetBootstrap(ctx context.Context, _ *chatgen.GetBootstrapReq) (*chatgen.BootstrapResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	ws, err := a.App.ListWorkspaces(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	cs, err := a.App.ListConversations(ctx, user.ID, "")
	if err != nil {
		return nil, err
	}
	head, err := a.App.Head(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	revision := "default"
	flags := map[string]bool{}
	if a.App.Flags != nil {
		revision, flags = a.App.Flags.Snapshot()
	}
	owner := false
	wsDTOs := make([]*chatgen.WorkspaceDTO, 0, len(ws))
	for _, item := range ws {
		if item.CreatedBy == user.ID {
			owner = true
		}
		wsDTOs = append(wsDTOs, toWorkspaceDTO(item))
	}
	flags["im.channel.create"] = flags["im.channel.create"] && owner
	convDTOs := make([]*chatgen.ConversationDTO, 0, len(cs))
	for _, c := range cs {
		convDTOs = append(convDTOs, toConversationDTO(c))
	}
	return &chatgen.BootstrapResp{
		User:          toUserDTO(user),
		Workspaces:    wsDTOs,
		Conversations: convDTOs,
		Capabilities:  flags,
		FlagsRevision: revision,
		SyncCursor:    head,
		AttachmentLimits: &chatgen.AttachmentLimitsDTO{
			MaxFileBytes:       imlimits.AttachmentBytes,
			MaxFilesPerMessage: imlimits.AttachmentsPerMessage,
		},
	}, nil
}

func (a *Adapter) Sync(ctx context.Context, req *chatgen.SyncReq) (*chatgen.SyncResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	limit, err := parseIntParam(req.GetLimit(), "limit", 100, 1, 100)
	if err != nil {
		return nil, err
	}
	events, cursor, more, err := a.App.Sync(ctx, user.ID, req.GetCursor(), int(limit))
	if err != nil {
		return nil, err
	}
	out := make([]*chatgen.EventDTO, 0, len(events))
	for _, ev := range events {
		out = append(out, toEventDTO(ev))
	}
	return &chatgen.SyncResp{
		Events:  out,
		Cursor:  cursor,
		HasMore: more,
	}, nil
}

type chatResponder struct {
	httpx.Responder
	withAuth   func(next func(w http.ResponseWriter, r *http.Request, ctx context.Context, s httpx.Session) error) http.HandlerFunc
	muxHandler hertzapp.HandlerFunc
}

func (cr chatResponder) HertzHandler(_ chatgen.RouteSpec) hertzapp.HandlerFunc {
	return cr.muxHandler
}

func (chatResponder) WriteOKHTTP(w http.ResponseWriter, r *http.Request, status int, resp any) {
	httpx.WriteJSON(w, responseStatusFromContext(r.Context(), status), resp)
}

func (cr chatResponder) WrapRoute(route chatgen.RouteSpec, next http.HandlerFunc) http.HandlerFunc {
	if route.Name == "GetInvite" {
		return next
	}
	return cr.withAuth(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ httpx.Session) error {
		var statusOverride int
		next(w, r.WithContext(withResponseStatusSink(ctx, &statusOverride)))
		return nil
	})
}

// Routes mounts all Chat HTTP endpoints onto an http.ServeMux using the
// generated chatgen.RegisterChatHTTPServer stateless interface bindings and DTOs.
func (a *Adapter) Routes() http.Handler {
	m := http.NewServeMux()
	httpx.Operations(m, func(r *http.Request) error {
		return a.App.CheckDependencies(r.Context())
	})

	withAuth := func(next func(w http.ResponseWriter, r *http.Request, ctx context.Context, s httpx.Session) error) http.HandlerFunc {
		return httpx.Auth(a.Auth, a.Origin, func(w http.ResponseWriter, r *http.Request, s httpx.Session) error {
			w.Header().Set("Cache-Control", "no-store")
			ctx := WithActor(r.Context(), domain.User{
				ID:           s.User.ID,
				Email:        s.User.Email,
				DisplayName:  s.User.DisplayName,
				Kind:         s.User.Kind,
				PlatformRole: s.User.PlatformRole,
				CreatedAt:    s.User.CreatedAt,
			})
			return next(w, r, ctx, s)
		})
	}

	resp := chatResponder{
		withAuth:   withAuth,
		muxHandler: adaptor.HertzHandler(observability.HTTP(m)),
	}
	chatgen.RegisterChatHTTPServer(m, a, resp, chatgen.ChatHTTPServiceRouteHandlers{
		UploadAttachment:   withAuth(a.uploadAttachmentHTTP),
		DownloadAttachment: withAuth(a.downloadAttachmentHTTP),
	})

	return httpx.WrapHertzRouter(m, func(r *route.RouterGroup) {
		chatgen.RegisterChatHTTPServiceHertzServer(r, a, resp)
	})
}

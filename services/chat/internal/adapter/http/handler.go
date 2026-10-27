// Package http implements the inbound HTTP adapter for the Chat service,
// binding generated Thrift HTTP DTOs (teamie/contracts/gen/im/hertz_gen/im/chat) to
// the Chat application layer.
package http

import (
	"context"
	"io"
	"mime"
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

// --- chatgen.ChatHTTPService implementation ---

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

func (a *Adapter) CreateWorkspace(ctx context.Context, req *chatgen.CreateWorkspaceReq) (*chatgen.CreateWorkspaceResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	if user.Kind != "human" {
		return nil, domain.ErrHumanWorkspaceManagementRequired()
	}
	ws, c, err := a.App.CreateWorkspace(ctx, user.ID, req.Name)
	if err != nil {
		return nil, err
	}
	return &chatgen.CreateWorkspaceResp{
		Workspace:    toWorkspaceDTO(ws),
		Conversation: toConversationDTO(c),
	}, nil
}

func (a *Adapter) ListWorkspaceMembers(ctx context.Context, req *chatgen.ListWorkspaceMembersReq) (*chatgen.ListWorkspaceMembersResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	members, err := a.App.ResolvedMembers(ctx, user.ID, req.ID)
	if err != nil {
		return nil, err
	}
	out := make([]*chatgen.MemberDTO, 0, len(members))
	for _, m := range members {
		out = append(out, toMemberDTO(m))
	}
	return &chatgen.ListWorkspaceMembersResp{Members: out}, nil
}

func (a *Adapter) CreateWorkspaceInvite(ctx context.Context, req *chatgen.CreateWorkspaceInviteReq) (*chatgen.CreateWorkspaceInviteResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	code, expiry, err := a.App.CreateInvite(ctx, user.ID, req.ID)
	if err != nil {
		return nil, err
	}
	return &chatgen.CreateWorkspaceInviteResp{
		Code:      code,
		ExpiresAt: expiry.UTC().Format(time.RFC3339Nano),
		URL:       "/join/" + code,
	}, nil
}

func (a *Adapter) GetInvite(ctx context.Context, req *chatgen.GetInviteReq) (*chatgen.GetInviteResp, error) {
	ws, expiry, err := a.App.Invite(ctx, req.Code)
	if err != nil {
		return nil, err
	}
	return &chatgen.GetInviteResp{
		Workspace: &chatgen.InviteWorkspaceDTO{
			ID:   ws.ID,
			Name: ws.Name,
		},
		ExpiresAt: expiry.UTC().Format(time.RFC3339Nano),
	}, nil
}

func (a *Adapter) AcceptInvite(ctx context.Context, req *chatgen.AcceptInviteReq) (*chatgen.AcceptInviteResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	if user.Kind != "human" {
		return nil, domain.ErrHumanWorkspaceManagementRequired()
	}
	ws, cs, err := a.App.AcceptInvite(ctx, user.ID, req.Code)
	if err != nil {
		return nil, err
	}
	convs := make([]*chatgen.ConversationDTO, 0, len(cs))
	for _, c := range cs {
		convs = append(convs, toConversationDTO(c))
	}
	return &chatgen.AcceptInviteResp{
		Workspace:     toWorkspaceDTO(ws),
		Conversations: convs,
	}, nil
}

func (a *Adapter) RemoveWorkspaceMember(ctx context.Context, req *chatgen.RemoveWorkspaceMemberReq) (*chatgen.EmptyResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	if err := a.App.RemoveMember(ctx, user.ID, req.ID, req.UserId); err != nil {
		return nil, err
	}
	return &chatgen.EmptyResp{}, nil
}

func (a *Adapter) ListConversations(ctx context.Context, req *chatgen.ListConversationsReq) (*chatgen.ListConversationsResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	cs, err := a.App.ListConversations(ctx, user.ID, req.ID)
	if err != nil {
		return nil, err
	}
	convs := make([]*chatgen.ConversationDTO, 0, len(cs))
	for _, c := range cs {
		convs = append(convs, toConversationDTO(c))
	}
	return &chatgen.ListConversationsResp{Conversations: convs}, nil
}

func (a *Adapter) CreateConversation(ctx context.Context, req *chatgen.CreateConversationReq) (*chatgen.ConversationResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	c, err := a.App.CreateConversationWithKey(ctx, user.ID, req.ID, req.Type, req.GetName(), req.GetMemberIds(), req.GetIdempotencyKey())
	if err != nil {
		return nil, err
	}
	return &chatgen.ConversationResp{Conversation: toConversationDTO(c)}, nil
}

func (a *Adapter) AddConversationMember(ctx context.Context, req *chatgen.AddConversationMemberReq) (*chatgen.ConversationResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	c, err := a.App.AddConversationMemberWithKey(ctx, user.ID, req.ID, req.UserId, req.GetIdempotencyKey())
	if err != nil {
		return nil, err
	}
	return &chatgen.ConversationResp{Conversation: toConversationDTO(c)}, nil
}

func (a *Adapter) RemoveConversationMember(ctx context.Context, req *chatgen.RemoveConversationMemberReq) (*chatgen.ConversationResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	c, err := a.App.GroupMember(ctx, user.ID, req.ID, req.UserId, false)
	if err != nil {
		return nil, err
	}
	return &chatgen.ConversationResp{Conversation: toConversationDTO(c)}, nil
}

func (a *Adapter) ListMessages(ctx context.Context, req *chatgen.ListMessagesReq) (*chatgen.ListMessagesResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	before, err := parseIntParam(req.GetBeforeSeq(), "beforeSeq", 0, 0, 1<<62)
	if err != nil {
		return nil, err
	}
	limit, err := parseIntParam(req.GetLimit(), "limit", 50, 1, 100)
	if err != nil {
		return nil, err
	}
	around, err := parseIntParam(req.GetAroundSeq(), "aroundSeq", 0, 0, 1<<62)
	if err != nil {
		return nil, err
	}
	content := req.GetContent()
	if content != "" && content != "files" || content == "files" && around != 0 {
		return nil, domain.Err(400, "invalid_query", "File search cannot be combined with a context cursor")
	}
	var ms []domain.Message
	var more bool
	if content == "files" {
		ms, more, err = a.App.FileHistory(ctx, user.ID, req.ID, before, req.GetQ(), int(limit))
	} else {
		ms, more, err = a.App.MessageHistory(ctx, user.ID, req.ID, before, around, req.GetQ(), int(limit))
	}
	if err != nil {
		return nil, err
	}
	out := make([]*chatgen.MessageDTO, 0, len(ms))
	for _, m := range ms {
		out = append(out, toMessageDTO(m))
	}
	return &chatgen.ListMessagesResp{Messages: out, HasMore: more}, nil
}

func (a *Adapter) GetMessage(ctx context.Context, req *chatgen.GetMessageReq) (*chatgen.MessageResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	m, err := a.App.Message(ctx, user.ID, req.ID, req.MessageId)
	if err != nil {
		return nil, err
	}
	return &chatgen.MessageResp{Message: toMessageDTO(m)}, nil
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

func (a *Adapter) SendMessage(ctx context.Context, req *chatgen.SendMessageReq) (*chatgen.MessageResp, error) {
	msg, created, err := a.sendMessage(ctx, req)
	if err != nil {
		return nil, err
	}
	if !created {
		recordResponseStatus(ctx, http.StatusOK)
	}
	return &chatgen.MessageResp{Message: toMessageDTO(msg)}, nil
}

func (a *Adapter) sendMessage(ctx context.Context, req *chatgen.SendMessageReq) (domain.Message, bool, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return domain.Message{}, false, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	mentions, err := fromMentionDTOs(req.GetMentions())
	if err != nil {
		return domain.Message{}, false, err
	}
	return a.App.SendAnnotated(ctx, user, req.ID, req.ClientMessageId, req.Body, req.ReplyToId, req.GetAttachmentIds(), mentions)
}

func (a *Adapter) EditMessage(ctx context.Context, req *chatgen.EditMessageReq) (*chatgen.MessageResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	mentions, err := fromMentionDTOs(req.GetMentions())
	if err != nil {
		return nil, err
	}
	msg, err := a.App.ChangeAnnotatedMessage(ctx, user.ID, req.ID, req.MessageId, req.Body, false, mentions)
	if err != nil {
		return nil, err
	}
	return &chatgen.MessageResp{Message: toMessageDTO(msg)}, nil
}

func (a *Adapter) RetractMessage(ctx context.Context, req *chatgen.RetractMessageReq) (*chatgen.MessageResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	msg, err := a.App.ChangeAnnotatedMessage(ctx, user.ID, req.ID, req.MessageId, "", true, nil)
	if err != nil {
		return nil, err
	}
	return &chatgen.MessageResp{Message: toMessageDTO(msg)}, nil
}

func (a *Adapter) PutReaction(ctx context.Context, req *chatgen.PutReactionReq) (*chatgen.ReactionsResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	state, err := a.App.AddReaction(ctx, user.ID, req.ID, req.MessageId, req.Emoji)
	if err != nil {
		return nil, err
	}
	dtos := toReactionDTOs(state.Reactions)
	if dtos == nil {
		dtos = []*chatgen.ReactionDTO{}
	}
	return &chatgen.ReactionsResp{Reactions: dtos, ReactionsVersion: strconv.FormatInt(state.ReactionsVersion, 10)}, nil
}

func (a *Adapter) DeleteReaction(ctx context.Context, req *chatgen.DeleteReactionReq) (*chatgen.ReactionsResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	state, err := a.App.RemoveReaction(ctx, user.ID, req.ID, req.MessageId, req.Emoji)
	if err != nil {
		return nil, err
	}
	dtos := toReactionDTOs(state.Reactions)
	if dtos == nil {
		dtos = []*chatgen.ReactionDTO{}
	}
	return &chatgen.ReactionsResp{Reactions: dtos, ReactionsVersion: strconv.FormatInt(state.ReactionsVersion, 10)}, nil
}

func (a *Adapter) MarkRead(ctx context.Context, req *chatgen.MarkReadReq) (*chatgen.MarkReadResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	seq, err := a.App.Read(ctx, user.ID, req.ID, req.Seq)
	if err != nil {
		return nil, err
	}
	return &chatgen.MarkReadResp{ReadSeq: seq}, nil
}

func (a *Adapter) GetReadReceipts(ctx context.Context, req *chatgen.GetReadReceiptsReq) (*chatgen.ReadReceiptsResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	rr, err := a.App.ReadReceipts(ctx, user.ID, req.ID)
	if err != nil {
		return nil, err
	}
	counts := make([]*chatgen.ReadSeqCountDTO, 0, len(rr.ReadSeqCounts))
	for _, sc := range rr.ReadSeqCounts {
		counts = append(counts, &chatgen.ReadSeqCountDTO{
			ReadSeq: sc.ReadSeq,
			Count:   sc.Count,
		})
	}
	var members []*chatgen.MemberReadReceiptDTO
	if rr.Members != nil {
		members = make([]*chatgen.MemberReadReceiptDTO, 0, len(rr.Members))
		for _, m := range rr.Members {
			members = append(members, &chatgen.MemberReadReceiptDTO{
				UserId:  m.UserID,
				ReadSeq: m.ReadSeq,
			})
		}
	}
	return &chatgen.ReadReceiptsResp{
		ConversationId: rr.ConversationID,
		EligibleCount:  rr.EligibleCount,
		ReadSeqCounts:  counts,
		Members:        members,
	}, nil
}

func (a *Adapter) SetNotificationLevel(ctx context.Context, req *chatgen.SetNotificationLevelReq) (*chatgen.NotificationLevelResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	level, err := a.App.SetNotificationLevel(ctx, user.ID, req.ID, req.Level)
	if err != nil {
		return nil, err
	}
	return &chatgen.NotificationLevelResp{
		ConversationId:    req.ID,
		NotificationLevel: level,
	}, nil
}

func (a *Adapter) SearchWorkspaceMessages(ctx context.Context, req *chatgen.SearchWorkspaceMessagesReq) (*chatgen.WorkspaceSearchPageResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	limit, err := parseIntParam(req.GetLimit(), "limit", 20, 1, 50)
	if err != nil {
		return nil, err
	}
	page, err := a.App.SearchWorkspaceMessages(ctx, user.ID, req.ID, req.GetQ(), req.GetCursor(), int(limit))
	if err != nil {
		return nil, err
	}
	items := make([]*chatgen.WorkspaceSearchItemDTO, 0, len(page.Items))
	for _, it := range page.Items {
		items = append(items, &chatgen.WorkspaceSearchItemDTO{
			Message:      toMessageDTO(it.Message),
			Conversation: toAttentionConversationDTO(it.Conversation),
		})
	}
	return &chatgen.WorkspaceSearchPageResp{
		Items:  items,
		Cursor: optString(page.Cursor),
	}, nil
}

func (a *Adapter) GetAttention(ctx context.Context, req *chatgen.GetAttentionReq) (*chatgen.AttentionPageResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	limit, err := parseIntParam(req.GetLimit(), "limit", 50, 1, 100)
	if err != nil {
		return nil, err
	}
	unread := req.GetUnreadOnly()
	if unread != "" && unread != "true" && unread != "false" {
		return nil, domain.Err(400, "invalid_query", "Invalid unread filter")
	}
	page, err := a.App.Attention(ctx, user.ID, req.GetCursor(), int(limit), unread == "true")
	if err != nil {
		return nil, err
	}
	items := make([]*chatgen.AttentionItemDTO, 0, len(page.Items))
	for _, it := range page.Items {
		items = append(items, &chatgen.AttentionItemDTO{
			Message:       toMessageDTO(it.Message),
			Conversation:  toAttentionConversationDTO(it.Conversation),
			WorkspaceName: it.WorkspaceName,
			Reasons:       it.Reasons,
			IsRead:        it.IsRead,
		})
	}
	return &chatgen.AttentionPageResp{
		Items:                         items,
		Cursor:                        optString(page.Cursor),
		UnreadCount:                   page.UnreadCount,
		ChannelUnreadCount:            page.ChannelUnreadCount,
		AttentionUnreadByConversation: attentionUnreadDTOs(page.AttentionUnreadByConversation),
	}, nil
}

func attentionUnreadDTOs(items []domain.AttentionUnread) []*chatgen.AttentionUnreadDTO {
	out := make([]*chatgen.AttentionUnreadDTO, 0, len(items))
	for _, item := range items {
		out = append(out, &chatgen.AttentionUnreadDTO{ConversationId: item.ConversationID, UnreadCount: item.UnreadCount})
	}
	return out
}

func (a *Adapter) Sync(ctx context.Context, req *chatgen.SyncReq) (*chatgen.SyncResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	limit, err := parseIntParam(req.GetLimit(), "limit", 100, 1, 500)
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

func (a *Adapter) UploadAttachment(ctx context.Context, req *chatgen.UploadAttachmentReq) (*chatgen.AttachmentResp, error) {
	att, _, err := a.uploadAttachment(ctx, req)
	if err != nil {
		return nil, err
	}
	return &chatgen.AttachmentResp{Attachment: toAttachmentDTO(att)}, nil
}

func (a *Adapter) uploadAttachment(ctx context.Context, req *chatgen.UploadAttachmentReq) (domain.Attachment, bool, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return domain.Attachment{}, false, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	return a.App.UploadAttachmentWithKey(ctx, user.ID, req.ID, req.Filename, req.Content, req.GetIdempotencyKey())
}

func (a *Adapter) GetAttachmentUpload(ctx context.Context, req *chatgen.GetAttachmentUploadReq) (*chatgen.AttachmentResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	att, err := a.App.LookupAttachmentUpload(ctx, user.ID, req.ID, req.UploadKey)
	if err != nil {
		return nil, err
	}
	return &chatgen.AttachmentResp{Attachment: toAttachmentDTO(att)}, nil
}

func (a *Adapter) DeletePendingAttachment(ctx context.Context, req *chatgen.DeletePendingAttachmentReq) (*chatgen.EmptyResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	if err := a.App.DeletePendingAttachment(ctx, user.ID, req.ID, req.AttachmentId); err != nil {
		return nil, err
	}
	return &chatgen.EmptyResp{}, nil
}

func (a *Adapter) DownloadAttachment(ctx context.Context, req *chatgen.DownloadAttachmentReq) (*chatgen.DownloadAttachmentResp, error) {
	user, ok := actorFromContext(ctx)
	if !ok {
		return nil, domain.Err(401, "unauthenticated", "Sign in to continue")
	}
	att, content, err := a.App.DownloadAttachment(ctx, user.ID, req.ID, req.AttachmentId)
	if err != nil {
		return nil, err
	}
	return &chatgen.DownloadAttachmentResp{
		Attachment: toAttachmentDTO(att),
		Content:    content,
	}, nil
}

// --- HTTP Transport Wiring ---

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
		UploadAttachment: withAuth(func(w http.ResponseWriter, r *http.Request, ctx context.Context, s httpx.Session) error {
			key := r.Header.Get("Idempotency-Key")
			if key == "" {
				if a.App.Flags == nil || !a.App.Flags.Enabled("im.attachments.upload") {
					return domain.ErrFeatureDisabled()
				}
			} else if !domain.ValidUploadKey(key) {
				return domain.ErrInvalidUploadKey()
			}
			convID := r.PathValue("id")
			if _, err := a.App.ConversationAccess(ctx, convID, s.User.ID); err != nil {
				return err
			}
			release, err := a.acquireTransfer()
			if err != nil {
				return err
			}
			defer release()
			r.Body = http.MaxBytesReader(w, r.Body, imlimits.AttachmentRequestBytes)
			reader, err := r.MultipartReader()
			if err != nil {
				return httpx.Err(400, "invalid_upload", "Use multipart/form-data with one file field")
			}
			part, err := reader.NextPart()
			if err != nil {
				return httpx.Err(400, "invalid_upload", "One file is required")
			}
			defer part.Close()
			_, params, parseErr := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			filename := params["filename"]
			if parseErr != nil || part.FormName() != "file" || domain.ValidateFilename(filename) != nil {
				return httpx.Err(400, "invalid_filename", "Provide one file with a safe filename")
			}
			content, err := io.ReadAll(io.LimitReader(part, imlimits.AttachmentBytes+1))
			if err != nil || len(content) > imlimits.AttachmentBytes {
				return httpx.Err(413, "attachment_too_large", "File exceeds 4 MiB")
			}
			if _, err = reader.NextPart(); err != io.EOF {
				return httpx.Err(400, "invalid_upload", "Only one file per upload is supported")
			}
			att, first, err := a.uploadAttachment(ctx, &chatgen.UploadAttachmentReq{
				ID:             convID,
				IdempotencyKey: optString(key),
				Filename:       filename,
				Content:        content,
			})
			if err != nil {
				return err
			}
			status := http.StatusOK
			if first {
				status = http.StatusCreated
			}
			httpx.WriteJSON(w, status, &chatgen.AttachmentResp{Attachment: toAttachmentDTO(att)})
			return nil
		}),
		DownloadAttachment: withAuth(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ httpx.Session) error {
			release, err := a.acquireTransfer()
			if err != nil {
				return err
			}
			defer release()
			resp, err := a.DownloadAttachment(ctx, &chatgen.DownloadAttachmentReq{
				ID:           r.PathValue("id"),
				AttachmentId: r.PathValue("attachmentId"),
			})
			if err != nil {
				return err
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": resp.Attachment.Filename}))
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
			w.Header().Set("X-Attachment-SHA256", resp.Attachment.Sha256)
			w.Header().Set("Content-Length", strconv.Itoa(len(resp.Content)))
			if _, err = w.Write(resp.Content); err != nil {
				return nil
			}
			_ = http.NewResponseController(w).Flush()
			return nil
		}),
	})

	return httpx.WrapHertzRouter(m, func(r *route.RouterGroup) {
		chatgen.RegisterChatHTTPServiceHertzServer(r, a, resp)
	})
}

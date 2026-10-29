package http

import (
	"context"
	"io"
	"mime"
	"net/http"
	"strconv"

	chatgen "teamie/contracts/gen/im/hertz_gen/im/chat"
	"teamie/pkg/httpx"
	"teamie/pkg/imlimits"
	"teamie/services/chat/internal/domain"
)

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

func (a *Adapter) uploadAttachmentHTTP(w http.ResponseWriter, r *http.Request, ctx context.Context, s httpx.Session) error {
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
}

func (a *Adapter) downloadAttachmentHTTP(w http.ResponseWriter, r *http.Request, ctx context.Context, _ httpx.Session) error {
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
}

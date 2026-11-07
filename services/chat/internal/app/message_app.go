package app

import (
	"context"
	"slices"
	"strings"
	"time"

	"teamie/services/chat/internal/domain"
)

// Message returns a single message in an authorized conversation.
func (s *Service) Message(ctx context.Context, userID, conversationID, messageID string) (domain.Message, error) {
	var m domain.Message
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		ws, err := s.Conversations.GetConversationWorkspaceID(txCtx, conversationID)
		if err != nil {
			return err
		}
		if err = s.Workspaces.WorkspaceLock(txCtx, ws, userID, false, false); err != nil {
			return err
		}
		if err = s.Conversations.LockConversationForShare(txCtx, conversationID); err != nil {
			return err
		}
		if _, err = s.Conversations.ConversationAccess(txCtx, conversationID, userID); err != nil {
			return err
		}
		m, err = s.Messages.GetMessage(txCtx, conversationID, messageID)
		return err
	})
	return m, err
}

// MessageHistory searches or pages conversation messages.
func (s *Service) MessageHistory(ctx context.Context, userID, conversationID string, before, around int64, query string, limit int) ([]domain.Message, bool, error) {
	return s.messageHistory(ctx, userID, conversationID, before, around, query, limit, false)
}

// FileHistory lists only published files in a conversation, matched by literal filename.
func (s *Service) FileHistory(ctx context.Context, userID, conversationID string, before int64, query string, limit int) ([]domain.Message, bool, error) {
	return s.messageHistory(ctx, userID, conversationID, before, 0, query, limit, true)
}

func (s *Service) messageHistory(ctx context.Context, userID, conversationID string, before, around int64, query string, limit int, filesOnly bool) ([]domain.Message, bool, error) {
	trimmed, err := domain.ValidateHistoryQuery(before, around, query, limit)
	if err != nil {
		return nil, false, err
	}
	var out []domain.Message
	var more bool
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		ws, err := s.Conversations.GetConversationWorkspaceID(txCtx, conversationID)
		if err != nil {
			return err
		}
		if err = s.Workspaces.WorkspaceLock(txCtx, ws, userID, false, false); err != nil {
			return err
		}
		if err = s.Conversations.LockConversationForShare(txCtx, conversationID); err != nil {
			return err
		}
		if _, err = s.Conversations.ConversationAccess(txCtx, conversationID, userID); err != nil {
			return err
		}
		out, more, err = s.Messages.QueryMessageHistory(txCtx, conversationID, before, around, trimmed, limit, filesOnly)
		return err
	})
	return out, more, err
}

// SendAnnotated sends or idempotently replays a message with optional attachments and mentions.
func (s *Service) SendAnnotated(ctx context.Context, user domain.User, conversationID, clientID, body string, reply *string, ids []string, mentions []domain.Mention) (domain.Message, bool, error) {
	return s.SendMessage(ctx, user, domain.SendRequest{
		ConversationID:  conversationID,
		ClientMessageID: clientID,
		Body:            body,
		ReplyToID:       reply,
		AttachmentIDs:   ids,
		Mentions:        mentions,
	})
}

// SendMessage sends or idempotently replays an ordinary message. A forwardOf
// source creates a new message carrying an immutable snapshot of that source.
func (s *Service) SendMessage(ctx context.Context, user domain.User, req domain.SendRequest) (domain.Message, bool, error) {
	var m domain.Message
	if req.ReplyToID != nil && *req.ReplyToID == "" {
		req.ReplyToID = nil
	}
	if err := domain.ValidateForwardRequest(req); err != nil {
		return m, false, err
	}
	body, mentions, ids := req.Body, req.Mentions, req.AttachmentIDs
	conversationID, clientID, reply := req.ConversationID, req.ClientMessageID, req.ReplyToID
	if err := domain.ValidateMentions(body, mentions); err != nil {
		return m, false, err
	}
	reqMentions := domain.CanonicalMentions(mentions)
	if err := domain.ValidateAttachmentIDs(ids); err != nil {
		return m, false, err
	}
	if ids == nil {
		ids = []string{}
	}
	if !domain.BoundedString(clientID, 1, 128) {
		return m, false, domain.ErrInvalidClientMessageID()
	}
	broadcastKind := domain.BroadcastMentionKind(reqMentions)
	var hereEligible map[string]bool
	if broadcastKind == "here" {
		_, _, committedRetry, lookupErr := s.Messages.FindMessageBySenderClientID(ctx, user.ID, clientID)
		if lookupErr != nil {
			return m, false, lookupErr
		}
		if !committedRetry {
			preConv, accessErr := s.Conversations.ConversationAccess(ctx, conversationID, user.ID)
			if accessErr != nil {
				return m, false, accessErr
			}
			contentCount := len(ids)
			if req.ForwardOf != nil {
				contentCount = 1
			}
			if err := domain.ValidateMessage(body, contentCount); err != nil {
				return m, false, err
			}
			if err := s.requireFlag("im.message.mentions"); err != nil {
				return m, false, err
			}
			if err := s.requireFlag("im.mentions.broadcast"); err != nil {
				return m, false, err
			}
			if err := s.requireFlag("im.presence.publish"); err != nil {
				return m, false, err
			}
			if len(ids) > 0 {
				if err := s.requireFlag("im.attachments.upload"); err != nil {
					return m, false, err
				}
			}
			if req.ForwardOf != nil {
				if err := s.requireFlag("im.messages.forward"); err != nil {
					return m, false, err
				}
			}
			if err := s.authorizeBroadcastInTx(ctx, preConv, user.ID); err != nil {
				return m, false, err
			}
			if err := domain.ValidateMentionMembers(preConv.MemberIDs, reqMentions); err != nil {
				return m, false, err
			}
			var presenceErr error
			hereEligible, presenceErr = s.lookupHereRecipients(ctx, candidateMemberIDs(preConv.MemberIDs, user.ID))
			if presenceErr != nil {
				return m, false, presenceErr
			}
		}
	}
	created := false
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Tx.LockSenderClientMessage(txCtx, user.ID, clientID); err != nil {
			return err
		}
		var c domain.Conversation
		var source forwardSourceScope
		var err error
		if req.ForwardOf == nil {
			c, err = s.Conversations.LockConversation(txCtx, conversationID, user.ID)
		} else {
			c, source, err = s.lockForwardConversations(txCtx, user.ID, conversationID, *req.ForwardOf)
		}
		if err != nil {
			return err
		}
		existing, identity, found, err := s.Messages.FindMessageBySenderClientID(txCtx, user.ID, clientID)
		if err != nil {
			return err
		}
		if found {
			// A committed retry is answered under the current target ACL only; the
			// forward source is never rechecked, even after retraction/access loss.
			m = existing
			sameReply := (reply == nil && m.ReplyToID == nil) || (reply != nil && m.ReplyToID != nil && *reply == *m.ReplyToID)
			if m.ConversationID != conversationID || identity.BodyHash != domain.HashString(body) || !sameReply || !slices.Equal(ids, identity.AttachmentIDs) || !domain.SameMentions(reqMentions, identity.Mentions) || !domain.SameForwardSource(req.ForwardOf, identity.ForwardOf) {
				return domain.ErrIdempotencyConflict()
			}
			return nil
		}
		contentCount := len(ids)
		if req.ForwardOf != nil {
			contentCount = 1
		}
		if err = domain.ValidateMessage(body, contentCount); err != nil {
			return err
		}
		var resolvedMentions []domain.Mention
		var broadcastRecipients []string
		if len(reqMentions) > 0 {
			if err = s.requireFlag("im.message.mentions"); err != nil {
				return err
			}
			if broadcastKind != "" {
				if err = s.requireFlag("im.mentions.broadcast"); err != nil {
					return err
				}
				if broadcastKind == "here" {
					if err = s.requireFlag("im.presence.publish"); err != nil {
						return err
					}
				}
				if err = s.authorizeBroadcastInTx(txCtx, c, user.ID); err != nil {
					return err
				}
			}
			if err = domain.ValidateMentionMembers(c.MemberIDs, reqMentions); err != nil {
				return err
			}
			resolvedMentions, broadcastRecipients = resolveMentions(reqMentions, c.MemberIDs, user.ID, hereEligible)
		}
		if len(ids) > 0 {
			if err = s.requireFlag("im.attachments.upload"); err != nil {
				return err
			}
		}
		if reply != nil {
			exists, existsErr := s.Messages.MessageExistsInConversation(txCtx, *reply, conversationID)
			if existsErr != nil {
				return existsErr
			}
			if !exists {
				return domain.ErrInvalidReplyTarget()
			}
		}
		var sourceMessage domain.Message
		if req.ForwardOf != nil {
			if err = s.requireFlag("im.messages.forward"); err != nil {
				return err
			}
			if sourceMessage, err = s.authorizeForwardSource(txCtx, user.ID, c, *req.ForwardOf, source); err != nil {
				return err
			}
		}
		m = domain.Message{
			ID:              domain.NewID(),
			ConversationID:  conversationID,
			SenderID:        user.ID,
			SenderName:      user.DisplayName,
			SenderKind:      user.Kind,
			ClientMessageID: clientID,
			Body:            body,
			ReplyToID:       reply,
		}
		seq, err := s.Conversations.IncrementConversationSeq(txCtx, conversationID)
		if err != nil {
			return err
		}
		m.Seq = seq
		createdAt, err := s.Messages.InsertMessage(txCtx, m, domain.HashString(body), req.ForwardOf)
		if err != nil {
			return err
		}
		m.CreatedAt = createdAt
		if len(resolvedMentions) > 0 {
			m.Mentions = resolvedMentions
			if err = s.Messages.SetMessageInitialMentions(txCtx, m.ID, resolvedMentions, reqMentions, broadcastRecipients); err != nil {
				return err
			}
		}
		if len(ids) > 0 {
			m.Attachments, err = s.Attachments.AttachFilesToMessage(txCtx, user.ID, conversationID, m.ID, ids)
			if err != nil {
				return err
			}
			if err = s.Messages.SetMessageInitialAttachments(txCtx, m.ID, m.Attachments, ids); err != nil {
				return err
			}
		}
		if req.ForwardOf != nil {
			snapshot := domain.NewForwardedSnapshot(sourceMessage)
			if len(snapshot.Attachments) > 0 {
				snapshot.Attachments, err = s.Messages.CopyForwardAttachments(txCtx, sourceMessage.ConversationID, sourceMessage.ID, conversationID, m.ID, user.ID, snapshot.Attachments)
				if err != nil {
					return err
				}
			} else {
				snapshot.Attachments = nil
			}
			if len(snapshot.Mentions) == 0 {
				snapshot.Mentions = nil
			}
			if err = s.Messages.SetMessageForwarded(txCtx, m.ID, snapshot); err != nil {
				return err
			}
			m.Forwarded = &snapshot
		}
		if err = s.Conversations.Fanout(txCtx, c.MemberIDs, domain.Event{
			Type:           "message.created",
			WorkspaceID:    c.WorkspaceID,
			ConversationID: conversationID,
			Message:        &m,
		}); err != nil {
			return err
		}
		created = true
		return nil
	})
	return m, created, err
}

// forwardSourceScope records what the forward lock phase learned about the
// source conversation without yet authorizing it.
type forwardSourceScope struct {
	exists        bool
	sameWorkspace bool
}

// lockForwardConversations acquires the workspace share lock and both
// conversation row locks in sorted ID order (share on the source, the ordinary
// send lock on the target), then rechecks target access after the waits.
// Cross-workspace or missing sources lock only the target; such sends are
// rejected after the retry lookup.
func (s *Service) lockForwardConversations(ctx context.Context, userID, targetID string, forwardOf domain.ForwardSource) (domain.Conversation, forwardSourceScope, error) {
	var scope forwardSourceScope
	targetWorkspace, err := s.Conversations.GetConversationWorkspaceID(ctx, targetID)
	if err != nil {
		return domain.Conversation{}, scope, err
	}
	if err = s.Workspaces.WorkspaceLock(ctx, targetWorkspace, userID, false, false); err != nil {
		return domain.Conversation{}, scope, err
	}
	sourceWorkspace, err := s.Conversations.GetConversationWorkspaceID(ctx, forwardOf.ConversationID)
	switch {
	case err == nil:
		scope.exists = true
		scope.sameWorkspace = sourceWorkspace == targetWorkspace
	case domain.IsNotFound(err):
	default:
		return domain.Conversation{}, scope, err
	}
	ids := []string{targetID}
	if scope.sameWorkspace && forwardOf.ConversationID != targetID {
		ids = append(ids, forwardOf.ConversationID)
		slices.Sort(ids)
	}
	for _, id := range ids {
		if id == targetID {
			err = s.Conversations.LockConversationForUpdate(ctx, id)
		} else {
			err = s.Conversations.LockConversationForShare(ctx, id)
		}
		if err != nil {
			return domain.Conversation{}, scope, err
		}
	}
	c, err := s.Conversations.ConversationAccess(ctx, targetID, userID)
	return c, scope, err
}

// authorizeForwardSource rechecks source readability under the locks taken by
// lockForwardConversations and returns the current source message.
func (s *Service) authorizeForwardSource(ctx context.Context, userID string, target domain.Conversation, forwardOf domain.ForwardSource, scope forwardSourceScope) (domain.Message, error) {
	if !scope.exists {
		return domain.Message{}, domain.ErrNotFound()
	}
	if !scope.sameWorkspace {
		// Rejection only: an unreadable source stays an ordinary 404 and reveals
		// nothing about the other workspace.
		if _, err := s.Conversations.ConversationAccess(ctx, forwardOf.ConversationID, userID); err != nil {
			return domain.Message{}, err
		}
		return domain.Message{}, domain.ErrForwardCrossWorkspace()
	}
	if forwardOf.ConversationID != target.ID {
		if _, err := s.Conversations.ConversationAccess(ctx, forwardOf.ConversationID, userID); err != nil {
			return domain.Message{}, err
		}
	}
	source, err := s.Messages.GetMessage(ctx, forwardOf.ConversationID, forwardOf.MessageID)
	if err != nil {
		return domain.Message{}, err
	}
	if source.RetractedAt != nil {
		return domain.Message{}, domain.ErrRetractedMessageUnavailable()
	}
	return source, nil
}

// ChangeAnnotatedMessage edits or retracts an existing message.
func (s *Service) ChangeAnnotatedMessage(ctx context.Context, userID, conversationID, messageID, body string, retract bool, mentions []domain.Mention) (domain.Message, error) {
	var m domain.Message
	flag := "im.message.edit"
	if retract {
		flag = "im.message.retract"
	}
	if err := s.requireFlag(flag); err != nil {
		return m, err
	}
	var reqMentions []domain.Mention
	var broadcastKind string
	var hereEligible map[string]bool
	if !retract {
		reqMentions = domain.CanonicalMentions(mentions)
		broadcastKind = domain.BroadcastMentionKind(reqMentions)
		if broadcastKind == "here" {
			if err := domain.ValidateMentions(body, mentions); err != nil {
				return m, err
			}
			preConv, err := s.Conversations.ConversationAccess(ctx, conversationID, userID)
			if err != nil {
				return m, err
			}
			preMsg, err := s.Messages.GetMessage(ctx, conversationID, messageID)
			if err != nil {
				return m, err
			}
			if preMsg.SenderID != userID {
				return m, domain.ErrAuthorOnlyMessageChange()
			}
			contentCount := len(preMsg.Attachments)
			if preMsg.Forwarded != nil {
				contentCount = 1
			}
			if err = domain.ValidateMessage(body, contentCount); err != nil {
				return m, err
			}
			if preMsg.RetractedAt != nil {
				return m, domain.ErrMessageRetracted()
			}
			if err = s.requireFlag("im.message.mentions"); err != nil {
				return m, err
			}
			if err = s.requireFlag("im.mentions.broadcast"); err != nil {
				return m, err
			}
			if err = s.requireFlag("im.presence.publish"); err != nil {
				return m, err
			}
			if err = s.authorizeBroadcastInTx(ctx, preConv, userID); err != nil {
				return m, err
			}
			if err = domain.ValidateMentionMembers(preConv.MemberIDs, reqMentions); err != nil {
				return m, err
			}
			hereEligible, err = s.lookupHereRecipients(ctx, candidateMemberIDs(preConv.MemberIDs, userID))
			if err != nil {
				return m, err
			}
		}
	}
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		c, err := s.Conversations.LockConversation(txCtx, conversationID, userID)
		if err != nil {
			return err
		}
		m, err = s.Messages.GetMessage(txCtx, conversationID, messageID)
		if err != nil {
			return err
		}
		if m.SenderID != userID {
			return domain.ErrAuthorOnlyMessageChange()
		}
		var resolvedMentions []domain.Mention
		var broadcastRecipients []string
		if !retract {
			contentCount := len(m.Attachments)
			if m.Forwarded != nil {
				// A forward's comment may be empty; its snapshot is immutable.
				contentCount = 1
			}
			if err = domain.ValidateMessage(body, contentCount); err != nil {
				return err
			}
			if err = domain.ValidateMentions(body, mentions); err != nil {
				return err
			}
			if len(reqMentions) > 0 {
				if broadcastKind != "" {
					if err = s.requireFlag("im.message.mentions"); err != nil {
						return err
					}
					if err = s.requireFlag("im.mentions.broadcast"); err != nil {
						return err
					}
					if broadcastKind == "here" {
						if err = s.requireFlag("im.presence.publish"); err != nil {
							return err
						}
					}
					if err = s.authorizeBroadcastInTx(txCtx, c, userID); err != nil {
						return err
					}
				} else if !domain.SameMentions(reqMentions, m.Mentions) {
					if err = s.requireFlag("im.message.mentions"); err != nil {
						return err
					}
				}
				if err = domain.ValidateMentionMembers(c.MemberIDs, reqMentions); err != nil {
					return err
				}
				resolvedMentions, broadcastRecipients = resolveMentions(reqMentions, c.MemberIDs, userID, hereEligible)
			}
		}
		event := "message.updated"
		now := time.Now().UTC()
		if retract {
			if m.RetractedAt != nil {
				return nil
			}
			m.RetractedAt = &now
			m.Body = ""
			m.Attachments = nil
			m.Mentions = nil
			m.Forwarded = nil
			event = "message.retracted"
			if err = s.Messages.RetractMessageAndDeleteAttachments(txCtx, messageID, now); err != nil {
				return err
			}
			// Retraction removes every saver's saved row (no extra event; clients
			// drop saved entries on message.retracted) and unpins the message.
			if err = s.Messages.DeleteSavedForMessage(txCtx, messageID); err != nil {
				return err
			}
			if err = s.unpinInTx(txCtx, c, messageID); err != nil {
				return err
			}
			// The response and durable event must use the cleared list and the
			// revision committed by the same retraction transaction.
			m, err = s.Messages.GetMessage(txCtx, conversationID, messageID)
			if err != nil {
				return err
			}
		} else {
			if m.RetractedAt != nil {
				return domain.ErrMessageRetracted()
			}
			m.Body = body
			m.Mentions = resolvedMentions
			dbMentions := resolvedMentions
			if dbMentions == nil {
				dbMentions = []domain.Mention{}
			}
			m.EditedAt = &now
			if err = s.Messages.UpdateMessageBodyAndMentions(txCtx, messageID, body, now, dbMentions, broadcastRecipients); err != nil {
				return err
			}
		}
		return s.Conversations.Fanout(txCtx, c.MemberIDs, domain.Event{
			Type:           event,
			WorkspaceID:    c.WorkspaceID,
			ConversationID: conversationID,
			Message:        &m,
		})
	})
	return m, err
}

func candidateMemberIDs(members []string, excludeID string) []string {
	seen := make(map[string]bool, len(members))
	out := make([]string, 0, len(members))
	for _, id := range members {
		if id == "" || id == excludeID || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func (s *Service) authorizeBroadcastInTx(ctx context.Context, c domain.Conversation, actorID string) error {
	if c.Type != "channel" {
		return nil
	}
	role, found, err := s.Workspaces.GetWorkspaceMemberRole(ctx, c.WorkspaceID, actorID)
	if err != nil {
		return err
	}
	if !found || role != "owner" {
		return domain.ErrBroadcastForbidden()
	}
	return nil
}

func (s *Service) lookupHereRecipients(ctx context.Context, candidateIDs []string) (map[string]bool, error) {
	if s.Presence == nil {
		return nil, domain.ErrPresenceUnavailable()
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	states, err := s.Presence.Presence(lookupCtx, candidateIDs)
	if err != nil {
		return nil, domain.ErrPresenceUnavailable()
	}
	if s.Identity == nil {
		if len(candidateIDs) > 0 {
			return nil, domain.ErrPresenceUnavailable()
		}
		return map[string]bool{}, nil
	}
	users, activeByID, err := s.Identity.UsersWithActive(lookupCtx, candidateIDs)
	if err != nil {
		return nil, domain.ErrPresenceUnavailable()
	}
	eligible := make(map[string]bool, len(users))
	for _, u := range users {
		if !activeByID[u.ID] {
			continue
		}
		if u.Kind == "agent" {
			eligible[u.ID] = true
			continue
		}
		state := states[u.ID]
		if state == "online" || state == "away" {
			eligible[u.ID] = true
		}
	}
	return eligible, nil
}

func resolveMentions(mentions []domain.Mention, memberIDs []string, senderID string, hereEligible map[string]bool) ([]domain.Mention, []string) {
	candidates := candidateMemberIDs(memberIDs, senderID)
	out := make([]domain.Mention, len(mentions))
	broadcastRecipients := []string{}
	for i, m := range mentions {
		switch m.Kind {
		case "all":
			recipients := slices.Clone(candidates)
			if recipients == nil {
				recipients = []string{}
			}
			broadcastRecipients = recipients
			out[i] = domain.Mention{
				Kind:         "all",
				Start:        m.Start,
				End:          m.End,
				RecipientIDs: recipients,
			}
		case "here":
			recipients := make([]string, 0, len(candidates))
			for _, id := range candidates {
				if hereEligible[id] {
					recipients = append(recipients, id)
				}
			}
			broadcastRecipients = recipients
			count := len(recipients)
			out[i] = domain.Mention{
				Kind:           "here",
				Start:          m.Start,
				End:            m.End,
				RecipientCount: &count,
			}
		default:
			out[i] = domain.Mention{
				UserID: m.UserID,
				Start:  m.Start,
				End:    m.End,
			}
		}
	}
	return out, broadcastRecipients
}

// Read advances the caller's read cursor monotonically.
func (s *Service) Read(ctx context.Context, userID, conversationID string, seq int64) (int64, error) {
	state, err := s.ReadThrough(ctx, userID, conversationID, seq)
	return state.ReadSeq, err
}

// ReadThrough advances the caller's read cursor monotonically and clears the
// caller's manual unread marker when seq reaches it, even if the cursor does
// not move. Every effective change commits one caller-only read.updated event.
func (s *Service) ReadThrough(ctx context.Context, userID, conversationID string, seq int64) (domain.ReadState, error) {
	if seq < 0 {
		return domain.ReadState{}, domain.ErrInvalidReadSeq()
	}
	var result domain.ReadState
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		c, err := s.Conversations.LockConversation(txCtx, conversationID, userID)
		if err != nil {
			return err
		}
		if seq > c.LastSeq {
			seq = c.LastSeq
		}
		result = domain.ReadState{ReadSeq: c.ReadSeq, ManualUnreadSeq: c.ManualUnreadSeq}
		advanced := seq > c.ReadSeq
		clearsMarker := c.ManualUnreadSeq > 0 && seq >= c.ManualUnreadSeq
		if !advanced && !clearsMarker {
			return nil
		}
		if advanced {
			if err = s.Conversations.UpsertReadCursor(txCtx, conversationID, userID, seq); err != nil {
				return err
			}
			result.ReadSeq = seq
		}
		if clearsMarker {
			if _, err = s.Conversations.ClearManualUnread(txCtx, conversationID, userID); err != nil {
				return err
			}
			result.ManualUnreadSeq = 0
		}
		if err = s.fanoutReadState(txCtx, c, userID, result); err != nil {
			return err
		}
		if advanced && s.Flags != nil && s.Flags.Enabled("im.read_receipts.view") {
			other := make([]string, 0, len(c.MemberIDs))
			for _, id := range c.MemberIDs {
				if id != userID {
					other = append(other, id)
				}
			}
			if len(other) > 0 {
				if err = s.Conversations.EnqueueOutboxHint(txCtx, map[string]any{
					"type":           "receipts.changed",
					"conversationId": conversationID,
					"userIds":        other,
				}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return result, err
}

func (s *Service) fanoutReadState(ctx context.Context, c domain.Conversation, userID string, state domain.ReadState) error {
	readSeq, marker := state.ReadSeq, state.ManualUnreadSeq
	return s.Conversations.Fanout(ctx, []string{userID}, domain.Event{
		Type:            "read.updated",
		WorkspaceID:     c.WorkspaceID,
		ConversationID:  c.ID,
		ReadSeq:         &readSeq,
		ManualUnreadSeq: &marker,
	})
}

// ReadReceipts returns conversation read receipts for an authorized member.
func (s *Service) ReadReceipts(ctx context.Context, userID, conversationID string) (domain.ReadReceipts, error) {
	if err := s.requireFlag("im.read_receipts.view"); err != nil {
		return domain.ReadReceipts{}, err
	}
	var out domain.ReadReceipts
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		ws, err := s.Conversations.GetConversationWorkspaceID(txCtx, conversationID)
		if err != nil {
			return err
		}
		if err = s.Workspaces.WorkspaceLock(txCtx, ws, userID, false, false); err != nil {
			return err
		}
		if err = s.Conversations.LockConversationForShare(txCtx, conversationID); err != nil {
			return err
		}
		c, err := s.Conversations.ConversationAccess(txCtx, conversationID, userID)
		if err != nil {
			return err
		}
		out, err = s.Conversations.QueryReadReceipts(txCtx, c, userID)
		return err
	})
	return out, err
}

// SearchWorkspaceMessages searches unretracted messages across authorized conversations in a workspace.
func (s *Service) SearchWorkspaceMessages(ctx context.Context, userID, workspaceID, query, rawCursor string, limit int) (domain.WorkspaceSearchPage, error) {
	out := domain.WorkspaceSearchPage{Items: []domain.WorkspaceSearchItem{}}
	trimmed, cursor, err := domain.ValidateWorkspaceSearchQuery(query, rawCursor, limit)
	if err != nil {
		return out, err
	}
	var before any
	if rawCursor != "" {
		before = cursor.At
	}
	for attempt := 0; attempt < 2; attempt++ {
		candidateIDs, err := s.Messages.SearchWorkspaceCandidateIDs(ctx, userID, workspaceID, trimmed, before, cursor.ID, limit+1)
		if err != nil {
			return out, err
		}
		retry := false
		err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
			if err := s.Workspaces.WorkspaceLock(txCtx, workspaceID, userID, false, false); err != nil {
				return err
			}
			member, err := s.Workspaces.WorkspaceMemberExists(txCtx, workspaceID, userID)
			if err != nil {
				return err
			}
			if !member {
				return domain.ErrNotFound()
			}
			if len(candidateIDs) == 0 {
				return nil
			}
			conversations, err := s.Messages.LockSearchConversationsForShare(txCtx, userID, workspaceID, candidateIDs)
			if err != nil {
				return err
			}
			if len(conversations) == 0 {
				if len(candidateIDs) == limit+1 && attempt == 0 {
					retry = true
				}
				return nil
			}
			items, err := s.Messages.QueryVerifiedWorkspaceSearchItems(txCtx, userID, trimmed, before, cursor.ID, limit+1, candidateIDs, conversations)
			if err != nil {
				return err
			}
			if len(items) < len(candidateIDs) && len(candidateIDs) == limit+1 && attempt == 0 {
				retry = true
				return nil
			}
			out.Items = items
			if len(out.Items) > limit {
				out.Items = out.Items[:limit]
				last := out.Items[len(out.Items)-1].Message
				encoded, err := domain.EncodeCursor(last.CreatedAt, last.ID)
				if err != nil {
					return err
				}
				out.Cursor = encoded
			}
			return nil
		})
		if err != nil {
			return out, err
		}
		if retry {
			continue
		}
		return out, nil
	}
	return out, nil
}

// Attention computes the caller's mention/reply attention inbox projection.
func (s *Service) Attention(ctx context.Context, userID, rawCursor string, limit int, unreadOnly bool) (domain.AttentionPage, error) {
	return s.AttentionWithMessage(ctx, userID, rawCursor, limit, unreadOnly, "")
}

// AttentionWithMessage computes the caller's attention inbox projection, optionally
// restricting returned items to a single message ID while preserving global/grouped counts.
func (s *Service) AttentionWithMessage(ctx context.Context, userID, rawCursor string, limit int, unreadOnly bool, messageID string) (domain.AttentionPage, error) {
	out := domain.AttentionPage{Items: []domain.AttentionItem{}, AttentionUnreadByConversation: []domain.AttentionUnread{}}
	if messageID != "" {
		if rawCursor != "" || unreadOnly || !domain.BoundedString(messageID, 1, 128) || strings.ContainsRune(messageID, 0) {
			return out, domain.ErrInvalidAttentionQuery()
		}
	}
	if limit < 1 || limit > 100 {
		return out, domain.ErrInvalidAttentionPageSize()
	}
	cursor, err := domain.ParseAttentionCursor(rawCursor)
	if err != nil {
		return out, err
	}
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		workspaces, err := s.Messages.LockUserWorkspacesForShare(txCtx, userID)
		if err != nil {
			return err
		}
		conversations, err := s.Messages.LockUserConversationsForShare(txCtx, userID, workspaces)
		if err != nil {
			return err
		}
		if len(conversations) == 0 {
			return nil
		}
		var before any
		if rawCursor != "" {
			before = cursor.At
		}
		pageLimit := limit + 1
		if messageID != "" {
			pageLimit = 1
		}
		unread, channelUnread, items, unreadByConversation, err := s.Messages.QueryAttentionProjection(txCtx, userID, before, cursor.ID, unreadOnly, pageLimit, conversations, messageID)
		if err != nil {
			return err
		}
		out.UnreadCount = unread
		out.ChannelUnreadCount = channelUnread
		out.Items = items
		out.AttentionUnreadByConversation = unreadByConversation
		if messageID == "" && len(out.Items) > limit {
			out.Items = out.Items[:limit]
			last := out.Items[len(out.Items)-1].Message
			encoded, err := domain.EncodeCursor(last.CreatedAt, last.ID)
			if err != nil {
				return err
			}
			out.Cursor = encoded
		}
		return nil
	})
	return out, err
}

// PrunePublishedOutbox deletes a bounded batch of legacy published outbox rows.
func (s *Service) PrunePublishedOutbox(ctx context.Context) (int64, error) {
	return s.Outbox.PrunePublishedOutbox(ctx)
}

// PublishOneOutbox claims, publishes, and deletes one pending outbox hint atomically.
func (s *Service) PublishOneOutbox(ctx context.Context, pub domain.OutboxPublisher) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	sent := false
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		id, payload, found, err := s.Outbox.ClaimNextUnpublishedOutbox(txCtx)
		if err != nil || !found {
			return err
		}
		if err = pub.PublishHint(txCtx, domain.HintChannel, payload); err != nil {
			return err
		}
		if err = s.Outbox.DeleteOutboxByID(txCtx, id); err != nil {
			return err
		}
		sent = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return sent, nil
}

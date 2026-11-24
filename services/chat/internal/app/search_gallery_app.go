package app

import (
	"context"

	"teamie/services/chat/internal/domain"
)

// SearchMessages searches unretracted messages across all authorized workspaces
// (or optionally narrowed to one workspace/conversation/sender/time/files) using
// two-phase lock-minimizing share-lock verification.
func (s *Service) SearchMessages(ctx context.Context, userID, rawQ, workspaceID, conversationID, senderID, rawHasFiles, rawAfter, rawBefore, rawCursor string, limit int) (domain.GlobalSearchPage, error) {
	out := domain.GlobalSearchPage{Items: []domain.GlobalSearchItem{}}
	q, err := domain.ValidateGlobalSearchQuery(rawQ, workspaceID, conversationID, senderID, rawHasFiles, rawAfter, rawBefore, rawCursor, limit)
	if err != nil {
		return out, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		candidateIDs, err := s.Messages.SearchGlobalCandidateIDs(ctx, userID, q, q.Limit+1)
		if err != nil {
			return out, err
		}
		if len(candidateIDs) == 0 && q.ConversationID == "" && q.WorkspaceID == "" {
			return out, nil
		}
		retry := false
		err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
			var lockedConvIDs []string
			switch {
			case q.ConversationID != "":
				convWS, err := s.Conversations.GetConversationWorkspaceID(txCtx, q.ConversationID)
				if err != nil {
					return err
				}
				if q.WorkspaceID != "" && convWS != q.WorkspaceID {
					return domain.ErrNotFound()
				}
				if err := s.Workspaces.WorkspaceLock(txCtx, convWS, userID, false, false); err != nil {
					return err
				}
				if err := s.Conversations.LockConversationForShare(txCtx, q.ConversationID); err != nil {
					return err
				}
				if _, err := s.Conversations.ConversationAccess(txCtx, q.ConversationID, userID); err != nil {
					return err
				}
				if len(candidateIDs) == 0 {
					return nil
				}
				lockedConvIDs = []string{q.ConversationID}
			case q.WorkspaceID != "":
				if err := s.Workspaces.WorkspaceLock(txCtx, q.WorkspaceID, userID, false, false); err != nil {
					return err
				}
				member, err := s.Workspaces.WorkspaceMemberExists(txCtx, q.WorkspaceID, userID)
				if err != nil {
					return err
				}
				if !member {
					return domain.ErrNotFound()
				}
				if len(candidateIDs) == 0 {
					return nil
				}
				lockedConvIDs, err = s.Messages.LockSearchConversationsForShare(txCtx, userID, q.WorkspaceID, candidateIDs)
				if err != nil {
					return err
				}
			default:
				if len(candidateIDs) == 0 {
					return nil
				}
				lockedWS, err := s.Messages.LockGlobalSearchWorkspacesForShare(txCtx, userID, candidateIDs)
				if err != nil {
					return err
				}
				if len(lockedWS) == 0 {
					if len(candidateIDs) == q.Limit+1 && attempt == 0 {
						retry = true
					}
					return nil
				}
				lockedConvIDs, err = s.Messages.LockGlobalSearchConversationsForShare(txCtx, userID, lockedWS, candidateIDs)
				if err != nil {
					return err
				}
			}

			if len(lockedConvIDs) == 0 {
				if len(candidateIDs) == q.Limit+1 && attempt == 0 {
					retry = true
				}
				return nil
			}
			items, err := s.Messages.QueryVerifiedGlobalSearchItems(txCtx, userID, q, q.Limit+1, candidateIDs, lockedConvIDs)
			if err != nil {
				return err
			}
			if len(items) < len(candidateIDs) && len(candidateIDs) == q.Limit+1 && attempt == 0 {
				retry = true
				return nil
			}
			out.Items = items
			if len(out.Items) > q.Limit {
				out.Items = out.Items[:q.Limit]
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

// ListConversationAttachments returns paginated published attachments (direct
// and forwarded copies) for an authorized conversation, newest first.
func (s *Service) ListConversationAttachments(ctx context.Context, userID, conversationID, rawKind, rawQ, rawCursor string, limit int) (domain.ConversationAttachmentPage, error) {
	out := domain.ConversationAttachmentPage{Items: []domain.ConversationAttachmentItem{}}
	q, err := domain.ValidateAttachmentGalleryQuery(rawKind, rawQ, rawCursor, limit)
	if err != nil {
		return out, err
	}
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.lockConversationRead(txCtx, userID, conversationID); err != nil {
			return err
		}
		items, err := s.Attachments.ListConversationAttachments(txCtx, conversationID, q, q.Limit+1)
		if err != nil {
			return err
		}
		if len(items) > q.Limit {
			items = items[:q.Limit]
			last := items[len(items)-1]
			encoded, err := domain.EncodeAttachmentGalleryCursor(last.MessageSeq, last.Attachment.ID)
			if err != nil {
				return err
			}
			out.Cursor = encoded
		}
		out.Items = items
		return nil
	})
	return out, err
}

package app

import (
	"context"

	"teamie/services/chat/internal/domain"
)

// UploadAttachmentWithKey uploads or idempotently replays a file attachment.
func (s *Service) UploadAttachmentWithKey(ctx context.Context, userID, conversationID, filename string, content []byte, key string) (domain.Attachment, bool, error) {
	var a domain.Attachment
	if key != "" && !domain.ValidUploadKey(key) {
		return a, false, domain.ErrInvalidUploadKeyFormat()
	}
	if err := domain.ValidateFilename(filename); err != nil {
		return a, false, err
	}
	if len(content) == 0 || len(content) > domain.AttachmentBytes {
		return a, false, domain.ErrAttachmentTooLarge()
	}
	mediaType := "application/octet-stream"
	if s.SniffMediaType != nil {
		mediaType = s.SniffMediaType(content)
	}
	var thumb *domain.AttachmentThumbnail
	if s.GenerateThumbnail != nil && s.Flags != nil && s.Flags.Enabled("im.attachments.upload") {
		skipThumb := false
		if key != "" {
			if _, found, lookupErr := s.Attachments.FindAttachmentUpload(ctx, userID, key); lookupErr == nil && found {
				skipThumb = true
			}
		}
		if !skipThumb {
			if _, accessErr := s.Conversations.ConversationAccess(ctx, conversationID, userID); accessErr == nil {
				thumb = s.GenerateThumbnail(ctx, mediaType, content)
			}
		}
	}
	first := false
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.Tx.LockAttachmentSender(txCtx, userID); err != nil {
			return err
		}
		if _, err := s.Conversations.LockConversation(txCtx, conversationID, userID); err != nil {
			return err
		}
		contentHash := domain.SHA256Hex(content)
		fingerprint := domain.UploadFingerprint(conversationID, filename, contentHash)
		if key != "" {
			old, found, lookupErr := s.Attachments.FindAttachmentUpload(txCtx, userID, key)
			if lookupErr != nil {
				return lookupErr
			}
			if found {
				if old.Conversation != conversationID || old.Fingerprint != fingerprint {
					return domain.ErrUploadConflict()
				}
				if !old.Available {
					return domain.ErrAttachmentUploadUnavailable(true)
				}
				a = old.Attachment
				return nil
			}
		}
		if err := s.requireFlag("im.attachments.upload"); err != nil {
			return err
		}
		if err := s.Attachments.DeleteExpiredPendingForSender(txCtx, userID); err != nil {
			return err
		}
		pending, err := s.Attachments.CountPendingForSender(txCtx, userID)
		if err != nil {
			return err
		}
		if pending >= domain.PendingAttachmentsPerSender {
			return domain.ErrAttachmentCapacity()
		}
		a = domain.Attachment{
			ID:        domain.NewID(),
			Filename:  filename,
			MediaType: mediaType,
			SizeBytes: len(content),
			SHA256:    contentHash,
		}
		if thumb != nil {
			a.Image = &domain.AttachmentImage{Width: thumb.Width, Height: thumb.Height}
		}
		if err = s.Attachments.InsertAttachment(txCtx, a, conversationID, userID, content, thumb); err != nil {
			return err
		}
		if key != "" {
			if err = s.Attachments.InsertAttachmentUpload(txCtx, userID, key, conversationID, fingerprint, a.ID); err != nil {
				return err
			}
		}
		first = true
		return nil
	})
	return a, first, err
}

// LookupAttachmentUpload recovers a caller's own successful upload descriptor by key.
func (s *Service) LookupAttachmentUpload(ctx context.Context, userID, conversationID, key string) (domain.Attachment, error) {
	if !domain.ValidUploadKey(key) {
		return domain.Attachment{}, domain.ErrInvalidUploadKey()
	}
	var out domain.Attachment
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if _, err := s.Conversations.LockConversation(txCtx, conversationID, userID); err != nil {
			return err
		}
		old, found, err := s.Attachments.FindAttachmentUpload(txCtx, userID, key)
		if err != nil {
			return err
		}
		if !found || old.Conversation != conversationID {
			return domain.ErrAttachmentUploadNotFound()
		}
		if !old.Available {
			return domain.ErrAttachmentUploadUnavailable(false)
		}
		out = old.Attachment
		return nil
	})
	return out, err
}

// DeletePendingAttachment removes the caller's unlinked pending attachment.
func (s *Service) DeletePendingAttachment(ctx context.Context, userID, conversationID, attachmentID string) error {
	return s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if _, err := s.Conversations.LockConversation(txCtx, conversationID, userID); err != nil {
			return err
		}
		owner, message, found, err := s.Attachments.LockAttachmentForUpdate(txCtx, attachmentID, conversationID)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if owner != userID || message != nil {
			return domain.ErrPendingFileUnavailable()
		}
		return s.Attachments.DeleteAttachmentByID(txCtx, attachmentID)
	})
}

// DownloadAttachment returns authorized attachment metadata and bytes.
func (s *Service) DownloadAttachment(ctx context.Context, userID, conversationID, attachmentID string) (domain.Attachment, []byte, error) {
	var a domain.Attachment
	var content []byte
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if _, err := s.Conversations.LockConversation(txCtx, conversationID, userID); err != nil {
			return err
		}
		var getErr error
		a, content, getErr = s.Attachments.GetAuthorizedAttachmentContent(txCtx, attachmentID, conversationID, userID)
		return getErr
	})
	return a, content, err
}

// DownloadAttachmentThumbnail returns authorized thumbnail media type, bytes, and source SHA-256.
func (s *Service) DownloadAttachmentThumbnail(ctx context.Context, userID, conversationID, attachmentID string) (string, []byte, string, error) {
	var mediaType, sha256 string
	var content []byte
	err := s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := s.lockConversationRead(txCtx, userID, conversationID); err != nil {
			return err
		}
		var getErr error
		mediaType, content, sha256, getErr = s.Attachments.GetAuthorizedAttachmentThumbnail(txCtx, attachmentID, conversationID, userID)
		return getErr
	})
	return mediaType, content, sha256, err
}

// DeleteAllExpiredPendingAttachments removes expired unlinked attachments.
func (s *Service) DeleteAllExpiredPendingAttachments(ctx context.Context) error {
	return s.Attachments.DeleteAllExpiredPending(ctx)
}

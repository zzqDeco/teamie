package chat

import (
	"context"
	"time"

	"teamie/backend/internal/api"
)

// Shared upload/download admission bounds bytea buffers under Chat's 144 MiB
// Go memory budget. Hertz streams before admission instead of buffering bodies.
var attachmentTransfers = make(chan struct{}, 2)

func acquireAttachmentTransfer() (func(), error) {
	select {
	case attachmentTransfers <- struct{}{}:
		return func() { <-attachmentTransfers }, nil
	default:
		return nil, api.Err(429, "attachment_busy", "File transfers are busy; please retry")
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

func (s *Store) RunAttachmentCleanup(ctx context.Context) {
	timer := time.NewTicker(time.Hour)
	defer timer.Stop()
	for {
		cleanup, cancel := context.WithTimeout(ctx, 30*time.Second)
		_ = s.App(nil, "").DeleteAllExpiredPendingAttachments(cleanup)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func (s *Store) DeletePendingAttachment(ctx context.Context, user, conversation, id string) error {
	return compatErr(s.App(nil, "").DeletePendingAttachment(ctx, user, conversation, id))
}

func (s *Store) DownloadAttachment(ctx context.Context, user, conversation, id string) (Attachment, []byte, error) {
	a, content, err := s.App(nil, "").DownloadAttachment(ctx, user, conversation, id)
	return a, content, compatErr(err)
}

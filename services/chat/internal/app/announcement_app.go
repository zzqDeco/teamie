package app

import (
	"context"
	"strings"

	"teamie/services/chat/internal/domain"
)

const announcementPublishFlag = "im.announcements.publish"

func requirePlatformAdmin(actor domain.User) error {
	if actor.Kind != "human" || actor.PlatformRole != "admin" {
		return domain.ErrPlatformAdminRequired()
	}
	return nil
}

// ListAnnouncements returns reverse-chronological platform announcements along with
// the global latest sequence and the caller's read watermark.
func (s *Service) ListAnnouncements(ctx context.Context, userID string, beforeSeq int64, limit int) (domain.AnnouncementsPage, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	return s.Announcements.ListAnnouncements(ctx, userID, beforeSeq, limit)
}

// MarkAnnouncementsRead advances the caller's announcement read watermark monotonically,
// clamped to the current highest published announcement sequence.
func (s *Service) MarkAnnouncementsRead(ctx context.Context, userID string, seq int64) (int64, error) {
	if seq < 0 {
		return 0, domain.ErrInvalidReadSeq()
	}
	return s.Announcements.MarkAnnouncementsRead(ctx, userID, seq)
}

// CreateAnnouncement publishes a platform-wide announcement, advances the publishing
// administrator's own read cursor, and enqueues a realtime wakeup hint atomically.
func (s *Service) CreateAnnouncement(ctx context.Context, actor domain.User, rawTitle, rawBody string) (domain.Announcement, error) {
	if err := requirePlatformAdmin(actor); err != nil {
		return domain.Announcement{}, err
	}
	if err := s.requireFlag(announcementPublishFlag); err != nil {
		return domain.Announcement{}, err
	}
	title, body, err := domain.ValidateAnnouncementInput(rawTitle, rawBody)
	if err != nil {
		return domain.Announcement{}, err
	}
	authorName := strings.TrimSpace(actor.DisplayName)
	if authorName == "" {
		authorName = "Platform Admin"
	}
	var out domain.Announcement
	err = s.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		created, txErr := s.Announcements.InsertAnnouncement(txCtx, domain.Announcement{
			ID:         domain.NewID(),
			Title:      title,
			Body:       body,
			AuthorID:   actor.ID,
			AuthorName: authorName,
		})
		if txErr != nil {
			return txErr
		}
		if _, txErr = s.Announcements.MarkAnnouncementsRead(txCtx, actor.ID, created.Seq); txErr != nil {
			return txErr
		}
		if txErr = s.Conversations.EnqueueOutboxHint(txCtx, map[string]any{
			"type": "announcement.published",
		}); txErr != nil {
			return txErr
		}
		out = created
		return nil
	})
	return out, err
}

// DeleteAnnouncement removes a published platform announcement by ID.
func (s *Service) DeleteAnnouncement(ctx context.Context, actor domain.User, id string) error {
	if err := requirePlatformAdmin(actor); err != nil {
		return err
	}
	if err := s.requireFlag(announcementPublishFlag); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.ErrNotFound()
	}
	deleted, err := s.Announcements.DeleteAnnouncement(ctx, id)
	if err != nil {
		return err
	}
	if !deleted {
		return domain.ErrNotFound()
	}
	return nil
}

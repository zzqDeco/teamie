package pg

import (
	"context"

	"github.com/jackc/pgx/v5"
	"teamie/services/chat/internal/domain"
)

func (r *Repository) InsertAnnouncement(ctx context.Context, a domain.Announcement) (domain.Announcement, error) {
	err := r.DB.QueryRow(
		ctx,
		`INSERT INTO platform_announcements (id, title, body, author_id, author_name)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING seq, created_at`,
		a.ID, a.Title, a.Body, a.AuthorID, a.AuthorName,
	).Scan(&a.Seq, &a.CreatedAt)
	return a, err
}

func (r *Repository) ListAnnouncements(ctx context.Context, userID string, beforeSeq int64, limit int) (domain.AnnouncementsPage, error) {
	page := domain.AnnouncementsPage{Items: []domain.Announcement{}}
	if err := r.DB.QueryRow(
		ctx,
		`SELECT
		   COALESCE((SELECT MAX(seq) FROM platform_announcements), 0),
		   COALESCE((SELECT read_seq FROM platform_announcement_reads WHERE user_id = $1), 0)`,
		userID,
	).Scan(&page.LatestSeq, &page.ReadSeq); err != nil {
		return page, err
	}

	var (
		rows pgx.Rows
		err  error
	)
	if beforeSeq > 0 {
		rows, err = r.DB.Query(
			ctx,
			`SELECT id, seq, title, body, author_id, author_name, created_at
			 FROM platform_announcements
			 WHERE seq < $1
			 ORDER BY seq DESC
			 LIMIT $2`,
			beforeSeq, limit+1,
		)
	} else {
		rows, err = r.DB.Query(
			ctx,
			`SELECT id, seq, title, body, author_id, author_name, created_at
			 FROM platform_announcements
			 ORDER BY seq DESC
			 LIMIT $1`,
			limit+1,
		)
	}
	if err != nil {
		return page, err
	}
	defer rows.Close()

	for rows.Next() {
		var a domain.Announcement
		if err = rows.Scan(&a.ID, &a.Seq, &a.Title, &a.Body, &a.AuthorID, &a.AuthorName, &a.CreatedAt); err != nil {
			return page, err
		}
		page.Items = append(page.Items, a)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		page.HasMore = true
		page.Items = page.Items[:limit]
	}
	return page, nil
}

func (r *Repository) MarkAnnouncementsRead(ctx context.Context, userID string, seq int64) (int64, error) {
	var readSeq int64
	err := r.DB.QueryRow(
		ctx,
		`WITH clamped AS (
		   SELECT LEAST($2::bigint, COALESCE((SELECT MAX(seq) FROM platform_announcements), 0)) AS target_seq
		 )
		 INSERT INTO platform_announcement_reads (user_id, read_seq, updated_at)
		 SELECT $1, target_seq, now() FROM clamped
		 ON CONFLICT (user_id) DO UPDATE
		   SET read_seq = GREATEST(platform_announcement_reads.read_seq, EXCLUDED.read_seq),
		       updated_at = now()
		 RETURNING read_seq`,
		userID, seq,
	).Scan(&readSeq)
	return readSeq, err
}

func (r *Repository) DeleteAnnouncement(ctx context.Context, id string) (bool, error) {
	tag, err := r.DB.Exec(ctx, `DELETE FROM platform_announcements WHERE id = $1`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

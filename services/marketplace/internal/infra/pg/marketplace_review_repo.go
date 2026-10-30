package pg

import (
	"context"
	"encoding/json"

	"teamie/services/marketplace/internal/domain"
)

func (r *Repository) InsertMarketplaceAppReview(ctx context.Context, review domain.MarketplaceAppReview, snapshot any) error {
	rawSnap := []byte(`{}`)
	if snapshot != nil {
		b, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		rawSnap = b
	}
	_, err := r.DB.Conn(ctx).Exec(
		ctx,
		`INSERT INTO marketplace_app_reviews (id, app_id, revision, action, actor_id, note, web_url, mcp_url, snapshot, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		review.ID,
		review.AppID,
		review.Revision,
		review.Action,
		review.ActorID,
		review.Note,
		review.WebURL,
		review.MCPURL,
		rawSnap,
		review.CreatedAt,
	)
	return err
}

func (r *Repository) ListMarketplaceAppReviews(ctx context.Context, appID string) ([]domain.MarketplaceAppReview, error) {
	rows, err := r.DB.Conn(ctx).Query(
		ctx,
		`SELECT id, app_id, revision, action, actor_id, note, web_url, mcp_url, created_at
		 FROM marketplace_app_reviews
		 WHERE app_id=$1
		 ORDER BY created_at DESC, id DESC
		 LIMIT 50`,
		appID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.MarketplaceAppReview{}
	for rows.Next() {
		var rev domain.MarketplaceAppReview
		if err := rows.Scan(
			&rev.ID,
			&rev.AppID,
			&rev.Revision,
			&rev.Action,
			&rev.ActorID,
			&rev.Note,
			&rev.WebURL,
			&rev.MCPURL,
			&rev.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, rev)
	}
	return out, rows.Err()
}

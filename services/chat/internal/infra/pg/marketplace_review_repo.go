package pg

import (
	"context"
	"encoding/json"
	"time"

	"teamie/pkg/postgres"
	"teamie/services/chat/internal/domain"
)

func (r *Repository) ListDeveloperMarketplaceApps(ctx context.Context, ownerID string) ([]domain.MarketplaceApp, error) {
	rows, err := r.DB.Conn(ctx).Query(
		ctx,
		"SELECT "+marketplaceAppSafeColumns+" FROM marketplace_apps WHERE owner_id=$1 ORDER BY created_at DESC,id DESC",
		ownerID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.MarketplaceApp{}
	for rows.Next() {
		a, scanErr := scanMarketplaceAppSafe(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) CountDeveloperMarketplaceApps(ctx context.Context, ownerID string) (int, error) {
	var count int
	err := r.DB.Conn(ctx).QueryRow(
		ctx,
		"SELECT count(*) FROM marketplace_apps WHERE owner_id=$1",
		ownerID,
	).Scan(&count)
	return count, err
}

func (r *Repository) SaveMarketplaceVerificationReport(
	ctx context.Context,
	id string,
	verifiedAt time.Time,
	report domain.MarketplaceEndpointVerification,
) (domain.MarketplaceApp, error) {
	raw, err := json.Marshal(report)
	if err != nil {
		return domain.MarketplaceApp{}, err
	}
	row := r.DB.Conn(ctx).QueryRow(
		ctx,
		"UPDATE marketplace_apps SET last_verified_at=$2,verification_report=$3,updated_at=now() WHERE id=$1 RETURNING "+marketplaceAppSafeColumns,
		id, verifiedAt, raw,
	)
	updated, scanErr := scanMarketplaceAppSafe(row)
	if postgres.IsNoRows(scanErr) {
		return updated, domain.ErrMarketplaceAppUnavailable()
	}
	return updated, scanErr
}

func (r *Repository) InsertMarketplaceAppReview(ctx context.Context, review domain.MarketplaceAppReview, snapshot any) error {
	snapBytes := []byte("{}")
	if snapshot != nil {
		if encoded, err := json.Marshal(snapshot); err == nil {
			snapBytes = encoded
		}
	}
	_, err := r.DB.Conn(ctx).Exec(
		ctx,
		`INSERT INTO marketplace_app_reviews(id,app_id,revision,action,actor_id,note,web_url,mcp_url,snapshot) `+
			`VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		review.ID, review.AppID, review.Revision, review.Action, review.ActorID,
		review.Note, review.WebURL, review.MCPURL, snapBytes,
	)
	return err
}

func (r *Repository) ListMarketplaceAppReviews(ctx context.Context, appID string) ([]domain.MarketplaceAppReview, error) {
	rows, err := r.DB.Conn(ctx).Query(
		ctx,
		`SELECT id,app_id,revision,action,actor_id,note,web_url,mcp_url,created_at `+
			`FROM marketplace_app_reviews WHERE app_id=$1 ORDER BY created_at DESC,id DESC LIMIT 50`,
		appID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.MarketplaceAppReview{}
	for rows.Next() {
		var rev domain.MarketplaceAppReview
		if scanErr := rows.Scan(
			&rev.ID, &rev.AppID, &rev.Revision, &rev.Action, &rev.ActorID,
			&rev.Note, &rev.WebURL, &rev.MCPURL, &rev.CreatedAt,
		); scanErr != nil {
			return nil, scanErr
		}
		out = append(out, rev)
	}
	return out, rows.Err()
}

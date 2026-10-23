package pg

import (
	"context"

	"teamie/pkg/postgres"
)

func (r *Repository) PrunePublishedOutbox(ctx context.Context) (int64, error) {
	tag, err := r.DB.Conn(ctx).Exec(ctx, `WITH published AS (
		SELECT id FROM outbox WHERE published_at IS NOT NULL ORDER BY id
		FOR UPDATE SKIP LOCKED LIMIT 1000
	) DELETE FROM outbox o USING published p WHERE o.id=p.id AND o.published_at IS NOT NULL`)
	return tag.RowsAffected(), err
}

func (r *Repository) ClaimNextUnpublishedOutbox(ctx context.Context) (int64, []byte, bool, error) {
	var id int64
	var payload []byte
	err := r.DB.Conn(ctx).QueryRow(ctx, "SELECT id,payload FROM outbox WHERE published_at IS NULL ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1").Scan(&id, &payload)
	if postgres.IsNoRows(err) {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, err
	}
	return id, payload, true, nil
}

func (r *Repository) DeleteOutboxByID(ctx context.Context, id int64) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, "DELETE FROM outbox WHERE id=$1", id)
	return err
}

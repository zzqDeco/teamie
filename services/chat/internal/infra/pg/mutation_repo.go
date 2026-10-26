package pg

import (
	"context"
	"teamie/pkg/postgres"
	"teamie/services/chat/internal/domain"
)

func (r *Repository) LockMutation(ctx context.Context, actor, key string) (domain.ConversationMutation, bool, error) {
	if err := r.DB.AdvisoryXactLockKey(ctx, actor+":"+key, 704107); err != nil {
		return domain.ConversationMutation{}, false, err
	}
	var receipt domain.ConversationMutation
	err := r.DB.Conn(ctx).QueryRow(ctx, `SELECT request_hash,conversation_id FROM conversation_mutations WHERE actor_id=$1 AND request_key=$2`, actor, key).Scan(&receipt.RequestHash, &receipt.ConversationID)
	if postgres.IsNoRows(err) {
		return receipt, false, nil
	}
	return receipt, err == nil, err
}
func (r *Repository) SaveMutation(ctx context.Context, actor, key string, receipt domain.ConversationMutation) error {
	_, err := r.DB.Conn(ctx).Exec(ctx, `INSERT INTO conversation_mutations(actor_id,request_key,request_hash,conversation_id) VALUES($1,$2,$3,$4)`, actor, key, receipt.RequestHash, receipt.ConversationID)
	return err
}

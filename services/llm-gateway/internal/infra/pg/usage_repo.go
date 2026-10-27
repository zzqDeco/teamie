package pg

import (
	"context"

	"teamie/services/llm-gateway/internal/domain"
)

// Record inserts a model usage audit record via the context-aware connection/tx.
func (s *PGStore) Record(ctx context.Context, u domain.Usage) error {
	_, err := s.DB.Conn(ctx).Exec(ctx, `INSERT INTO gateway_usage(owner_id,agent_id,session_id,run_id,model_id,status,prompt_tokens,completion_tokens,latency_ms)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9);`, u.OwnerID, u.AgentID, u.SessionID, u.RunID, u.ModelID, u.Status, u.PromptTokens, u.CompletionTokens, u.LatencyMS)
	return err
}

// Prune deletes old or excess usage audit rows.
func (s *PGStore) Prune(ctx context.Context) error {
	_, err := s.DB.Conn(ctx).Exec(ctx, `DELETE FROM gateway_usage WHERE id IN(SELECT id FROM gateway_usage WHERE created_at<now()-interval '30 days' OR id <= (SELECT coalesce(max(id),0)-100000 FROM gateway_usage) ORDER BY id LIMIT 10000)`)
	return err
}

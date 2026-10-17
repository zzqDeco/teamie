package engine

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// These tools own their cached observation boundary and must recheck current
// source ACLs even when replaying. Generic native receipts cannot bypass that.
func privateReadTool(name string) bool {
	return len(name) >= 3 && name[:3] == "im_" || name == "context_read" || name == "context_search" || name == "workspace_read_page"
}

// The task row, not model-authored text or the most recent conversation,
// identifies the logical parent. All lookups stay inside Runtime's database.
func taskInputOrigin(ctx context.Context, tx pgx.Tx, agentID, taskID string) (string, []InputSource, bool, error) {
	var parent string
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT COALESCE(t.run_id::text,''),COALESCE(r.input_json,'[]'::jsonb)
FROM runtime_tasks t LEFT JOIN runtime_runs r ON r.id=t.run_id AND r.agent_id=t.agent_id
WHERE t.agent_id=$1 AND t.id=$2`, agentID, taskID).Scan(&parent, &raw)
	if err != nil {
		return "", nil, false, err
	}
	var inputs []Input
	if err = json.Unmarshal(raw, &inputs); err != nil {
		return "", nil, false, err
	}
	refs, overflow := inputSources(inputs)
	return parent, refs, overflow, nil
}

package engine

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/eino/components/tool"
)

const currentToolProfile = "teamie-tools-v035"
const v034ToolProfile = "teamie-tools-v034"

// v0.3.4 wrote plain schema.Message receipts. Native receipts are serialized
// with a registered type and must never select an older execution profile.
func classicModelReceipt(raw []byte) bool {
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil || value["$type"] != nil || string(value["role"]) != `"assistant"` {
		return false
	}
	return isLegacyMessage(raw)
}

// Keep the entire started Run's tool profile, including summary token counting
// and actual execution. Appending native receipts after old ones must not
// change that choice on another retry. No stored hash or receipt is rewritten.
func (s *Store) runToolProfile(ctx context.Context, runID string) (string, error) {
	rows, err := s.DB.Query(ctx, `SELECT response FROM runtime_model_calls WHERE run_id=$1 ORDER BY step`, runID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return "", err
		}
		if classicModelReceipt(raw) {
			return v034ToolProfile, nil
		}
	}
	return currentToolProfile, rows.Err()
}

func toolsForRun(ctx context.Context, available []tool.BaseTool, run *Run) ([]tool.BaseTool, error) {
	if run.toolProfile != v034ToolProfile {
		return available, nil
	}
	previous := make([]tool.BaseTool, 0, len(available))
	for _, item := range available {
		info, err := item.Info(ctx)
		if err != nil {
			return nil, err
		}
		if info.Name != "im_get_attachment" {
			previous = append(previous, item)
		}
	}
	return previous, nil
}

func resumeCompatibleForRun(metadata *checkpointMetadata, revision int64, run *Run) bool {
	if !resumeCompatible(metadata, revision, run.ID) {
		return false
	}
	// Existing native-only v0.3.5 checkpoints used the current tool profile.
	// A classic Run cannot load an unlabelled native checkpoint: v0.3.5 may
	// have built it with five tools or already changed summary provenance.
	return metadata.ToolProfile == run.toolProfile || metadata.ToolProfile == "" && run.toolProfile == currentToolProfile
}

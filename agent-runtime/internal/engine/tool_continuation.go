package engine

import (
	"context"
	"encoding/json"
	"errors"
)

// A native filesystem tool cannot represent pagination in its file-content
// type. Return a real tool observation so the model can take the advertised
// continuation, without forging partial file content or ending the whole Run.
func nativePageContinuation(ctx context.Context, err error) (string, bool) {
	run, runErr := CurrentRun(ctx)
	if runErr != nil || contextProfile(run) != contextProfileV1 {
		return "", false
	}
	var page *workspacePageRequiredError
	if !errors.As(err, &page) {
		return "", false
	}
	wire, marshalErr := json.Marshal(struct {
		OK        bool           `json:"ok"`
		Error     string         `json:"error"`
		Operation string         `json:"operation"`
		Tool      string         `json:"tool"`
		Resume    map[string]any `json:"resume"`
	}{Error: "workspace_page_required", Operation: page.Operation, Tool: "workspace_read_page", Resume: page.Resume})
	return string(wire), marshalErr == nil
}

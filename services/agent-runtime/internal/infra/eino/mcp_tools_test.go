package eino

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/agent/kitex_gen/agent/modelgatewayservice"
	"teamie/pkg/imtext"
	"teamie/services/agent-runtime/internal/domain"
)

type mcpFakeExecTag struct {
	rows int64
}

func (t mcpFakeExecTag) RowsAffected() int64 { return t.rows }

type mcpFakeRowScanner struct {
	scanFn func(dest ...any) error
}

func (r mcpFakeRowScanner) Scan(dest ...any) error { return r.scanFn(dest...) }

type mcpFakeToolReceipt struct {
	name   string
	hash   string
	result []byte
}

type mcpFakeModelReceipt struct {
	hash     string
	response []byte
}

type mcpInMemoryRepo struct {
	domain.RuntimeRepository
	mu           sync.Mutex
	seq          int64
	cursor       string
	observations map[string][]byte
	toolCalls    map[string]*mcpFakeToolReceipt
	modelCalls   map[string]*mcpFakeModelReceipt
	checkpoints  map[string]struct {
		payload  []byte
		metadata []byte
	}
	pending  [][]byte
	accepted map[string]struct {
		raw   []byte
		runID string
	}
	runs map[string]struct {
		key            string
		inputsJSON     []byte
		contextProfile string
		contextBudget  json.RawMessage
	}
	runByKey map[string]string
}

func newMCPInMemoryStore() *Store {
	return &Store{
		DB: &mcpInMemoryRepo{
			seq:          100,
			cursor:       "0",
			observations: map[string][]byte{},
			toolCalls:    map[string]*mcpFakeToolReceipt{},
			modelCalls:   map[string]*mcpFakeModelReceipt{},
			checkpoints: map[string]struct {
				payload  []byte
				metadata []byte
			}{},
			accepted: map[string]struct {
				raw   []byte
				runID string
			}{},
			runs: map[string]struct {
				key            string
				inputsJSON     []byte
				contextProfile string
				contextBudget  json.RawMessage
			}{},
			runByKey: map[string]string{},
		},
	}
}

func (r *mcpInMemoryRepo) AcquireLease(_ context.Context, agentID, owner string) (*Lease, error) {
	return &Lease{AgentID: agentID, Owner: owner, Fence: 1}, nil
}

func (r *mcpInMemoryRepo) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

func (r *mcpInMemoryRepo) CheckLease(_ context.Context, _ *Lease) error {
	return nil
}

func (r *mcpInMemoryRepo) CheckLeaseForUpdate(_ context.Context, _ *Lease) error {
	return nil
}

func (r *mcpInMemoryRepo) GetCursor(_ context.Context, _ *Lease) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cursor == "" {
		return "0", nil
	}
	return r.cursor, nil
}

func (r *mcpInMemoryRepo) GetInboxCursor(ctx context.Context, l *Lease) (string, error) {
	return r.GetCursor(ctx, l)
}

func (r *mcpInMemoryRepo) IngestInputs(_ context.Context, _ *Lease, cursor string, inputs []domain.Input) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cursor != "" {
		r.cursor = cursor
	}
	for _, in := range inputs {
		raw, _ := json.Marshal(in)
		r.pending = append(r.pending, raw)
	}
	return nil
}

func (r *mcpInMemoryRepo) GetCheckpoint(_ context.Context, id string) ([]byte, []byte, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.checkpoints[id]
	if !ok {
		return nil, nil, false, nil
	}
	return append([]byte(nil), item.payload...), append([]byte(nil), item.metadata...), true, nil
}

func (r *mcpInMemoryRepo) SetCheckpoint(_ context.Context, _ *Lease, id string, payload, metadata []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkpoints[id] = struct {
		payload  []byte
		metadata []byte
	}{
		payload:  append([]byte(nil), payload...),
		metadata: append([]byte(nil), metadata...),
	}
	return nil
}

func (r *mcpInMemoryRepo) DeleteCheckpoint(_ context.Context, _ *Lease, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.checkpoints, id)
	return nil
}

func (r *mcpInMemoryRepo) LoadSessionEventPayloads(
	_ context.Context,
	_, _, _ string,
	_, _ int64,
	_ string,
	_ []string,
	_ int,
	_ bool,
) ([][]byte, bool, error) {
	return nil, false, nil
}

func (r *mcpInMemoryRepo) GetCachedModelCall(_ context.Context, runID string, step int, hash string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.modelCalls[fmt.Sprintf("%s/%d", runID, step)]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	if item.hash != hash {
		return nil, ErrReplayConflict
	}
	return append([]byte(nil), item.response...), nil
}

func (r *mcpInMemoryRepo) GetModelStepAndRecovery(_ context.Context, runID string, step int) ([]byte, []byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.modelCalls[fmt.Sprintf("%s/%d", runID, step)]
	if !ok {
		return nil, nil, nil
	}
	return append([]byte(nil), item.response...), nil, nil
}

func (r *mcpInMemoryRepo) SaveModelCall(_ context.Context, _ *Lease, runID string, step int, hash string, b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := fmt.Sprintf("%s/%d", runID, step)
	if existing, ok := r.modelCalls[k]; ok {
		if existing.hash != hash {
			return ErrReplayConflict
		}
		return nil
	}
	r.modelCalls[k] = &mcpFakeModelReceipt{hash: hash, response: append([]byte(nil), b...)}
	return nil
}

func (r *mcpInMemoryRepo) GetContextRecovery(_ context.Context, _ string, _ int) (string, string, string, bool, int, []byte, error) {
	return "", "", "", false, 0, nil, pgx.ErrNoRows
}

func (r *mcpInMemoryRepo) ListPendingInputs(_ context.Context, _ string) ([][]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]byte, len(r.pending))
	copy(out, r.pending)
	return out, nil
}

func (r *mcpInMemoryRepo) StartOrResumeRun(
	_ context.Context,
	_ *Lease,
	firstEventID, newRunID, _ string,
	prepareNew func() (key string, inputsJSON []byte, canonProfile string, canonBudget json.RawMessage, inputIDs []string, err error),
) (string, []byte, int64, int64, string, json.RawMessage, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existingID, ok := r.runByKey[firstEventID]; ok {
		entry := r.runs[existingID]
		return existingID, entry.inputsJSON, 0, 1, entry.contextProfile, entry.contextBudget, true, nil
	}
	key, inputsJSON, canonProfile, canonBudget, _, err := prepareNew()
	if err != nil {
		return "", nil, 0, 0, "", nil, false, err
	}
	r.runByKey[firstEventID] = newRunID
	r.runs[newRunID] = struct {
		key            string
		inputsJSON     []byte
		contextProfile string
		contextBudget  json.RawMessage
	}{
		key:            key,
		inputsJSON:     inputsJSON,
		contextProfile: canonProfile,
		contextBudget:  canonBudget,
	}
	var inputs []domain.Input
	_ = json.Unmarshal(inputsJSON, &inputs)
	for _, in := range inputs {
		raw, _ := json.Marshal(in)
		r.accepted[in.EventID] = struct {
			raw   []byte
			runID string
		}{raw: raw, runID: newRunID}
	}
	return newRunID, inputsJSON, 0, 1, canonProfile, canonBudget, false, nil
}

func (r *mcpInMemoryRepo) FinishRun(_ context.Context, _ *Lease, runID, state, _ string, _ bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.runs[runID]; ok && state == "completed" {
		var inputs []domain.Input
		_ = json.Unmarshal(entry.inputsJSON, &inputs)
		consumed := map[string]bool{}
		for _, in := range inputs {
			consumed[in.EventID] = true
		}
		filtered := make([][]byte, 0, len(r.pending))
		for _, raw := range r.pending {
			var in domain.Input
			if json.Unmarshal(raw, &in) == nil && !consumed[in.EventID] {
				filtered = append(filtered, raw)
			}
		}
		r.pending = filtered
	}
	return nil
}

func (r *mcpInMemoryRepo) ReadObservation(_ context.Context, runID, callID, key string) ([]byte, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	val, ok := r.observations[runID+"/"+callID+"/"+key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), val...), true, nil
}

func (r *mcpInMemoryRepo) SaveObservation(_ context.Context, _ *Lease, runID, callID, key string, payload []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observations[runID+"/"+callID+"/"+key] = append([]byte(nil), payload...)
	return nil
}

func (r *mcpInMemoryRepo) SaveRunningRunObservation(ctx context.Context, l *Lease, runID, _, _, call, key string, response []byte) error {
	return r.SaveObservation(ctx, l, runID, call, key, response)
}

func (r *mcpInMemoryRepo) GetToolCallObservationWithMeta(_ context.Context, runID, call, obsKey string) (string, string, []byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tc, ok := r.toolCalls[runID+"/"+call]
	if !ok {
		return "", "", nil, pgx.ErrNoRows
	}
	var obs []byte
	if val, found := r.observations[runID+"/"+call+"/"+obsKey]; found {
		obs = append([]byte(nil), val...)
	}
	return tc.hash, tc.name, obs, nil
}

func (r *mcpInMemoryRepo) CheckRunAttemptRunning(_ context.Context, runID, _, _ string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.runs[runID]
	return ok, nil
}

func (r *mcpInMemoryRepo) PutContextArtifact(_ context.Context, _ *Lease, _ domain.ContextArtifactWrite, _ func(oldSources []byte) ([]byte, error)) error {
	return nil
}

func (r *mcpInMemoryRepo) LoadVisibleContextArtifact(_ context.Context, _, _, _ string, _, _ int64, _, _ string) (*domain.LoadedContextArtifactRow, bool, error) {
	return nil, false, nil
}

func (r *mcpInMemoryRepo) GetRunContextBudget(_ context.Context, runID string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.runs[runID]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	return append([]byte(nil), entry.contextBudget...), nil
}

func (r *mcpInMemoryRepo) ListIMToolCallResults(_ context.Context, runID string) ([]domain.ToolCallResultRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prefix := runID + "/"
	out := []domain.ToolCallResultRecord{}
	for k, tc := range r.toolCalls {
		if strings.HasPrefix(k, prefix) && len(tc.result) > 0 {
			out = append(out, domain.ToolCallResultRecord{
				CallID: strings.TrimPrefix(k, prefix),
				Name:   tc.name,
				Result: append([]byte(nil), tc.result...),
			})
		}
	}
	return out, nil
}

func (r *mcpInMemoryRepo) ListAcceptedToolCallCandidates(_ context.Context, _, callID string, names []string, runID string, _ int64, _ string) ([]domain.AcceptedToolCallCandidate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tc, ok := r.toolCalls[runID+"/"+callID]
	if !ok || len(tc.result) == 0 || !slices.Contains(names, tc.name) {
		return nil, nil
	}
	return []domain.AcceptedToolCallCandidate{{
		RunID: runID,
		Name:  tc.name,
		Raw:   append([]byte(nil), tc.result...),
	}}, nil
}

func (r *mcpInMemoryRepo) ListMatchingToolCallReceipts(_ context.Context, _, callID string, names, hashes []string, runID string, _ int64, _ bool) ([]domain.AcceptedToolCallCandidate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tc, ok := r.toolCalls[runID+"/"+callID]
	if !ok || len(tc.result) == 0 || !slices.Contains(names, tc.name) || (len(hashes) > 0 && !slices.Contains(hashes, tc.hash)) {
		return nil, nil
	}
	return []domain.AcceptedToolCallCandidate{{
		RunID: runID,
		Name:  tc.name,
		Raw:   append([]byte(nil), tc.result...),
	}}, nil
}

func (r *mcpInMemoryRepo) GetObservation(_ context.Context, runID, callID, key string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	val, ok := r.observations[runID+"/"+callID+"/"+key]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	return append([]byte(nil), val...), nil
}

func (r *mcpInMemoryRepo) ListModelCallResponses(_ context.Context, runID string) ([][]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prefix := runID + "/"
	out := [][]byte{}
	for k, item := range r.modelCalls {
		if strings.HasPrefix(k, prefix) {
			out = append(out, append([]byte(nil), item.response...))
		}
	}
	return out, nil
}

func (r *mcpInMemoryRepo) ListOtherAttemptMessageEvents(_ context.Context, _, _ string) ([]domain.AttemptEventPayload, error) {
	return nil, nil
}

func (r *mcpInMemoryRepo) RecordToolIntent(_ context.Context, _ *Lease, runID, id, name, hash string) ([]byte, error) {
	b, _, err := r.RecordExternalToolIntent(context.Background(), nil, runID, id, name, hash)
	return b, err
}

func (r *mcpInMemoryRepo) RecordExternalToolIntent(_ context.Context, _ *Lease, runID, id, name, hash string) ([]byte, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := runID + "/" + id
	if existing, ok := r.toolCalls[k]; ok {
		if existing.name != name || existing.hash != hash {
			return nil, false, ErrReplayConflict
		}
		return append([]byte(nil), existing.result...), false, nil
	}
	r.toolCalls[k] = &mcpFakeToolReceipt{name: name, hash: hash}
	return nil, true, nil
}

func (r *mcpInMemoryRepo) RecordToolResult(_ context.Context, _ *Lease, runID, id string, b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := runID + "/" + id
	if existing, ok := r.toolCalls[k]; ok {
		existing.result = append([]byte(nil), b...)
	}
	return nil
}

func (r *mcpInMemoryRepo) WithinFencedTx(ctx context.Context, _ *Lease, fn func(txCtx context.Context) error) error {
	return fn(ctx)
}

func (r *mcpInMemoryRepo) QueryRow(_ context.Context, sql string, args ...any) domain.RowScanner {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.Contains(sql, "SELECT count(*) FROM runtime_inputs") {
		if strings.Contains(sql, "im.unread_digest") {
			bodyStr, _ := args[1].(string)
			matches := 0
			for _, raw := range r.pending {
				var in domain.Input
				if json.Unmarshal(raw, &in) == nil && in.Kind == "im.unread_digest" && in.Body == bodyStr {
					matches++
				}
			}
			return mcpFakeRowScanner{scanFn: func(dest ...any) error {
				if p, ok := dest[0].(*int); ok {
					*p = matches
				}
				return nil
			}}
		}
		convID, _ := args[1].(string)
		bodyStr, _ := args[2].(string)
		matches := 0
		for _, raw := range r.pending {
			var in domain.Input
			if json.Unmarshal(raw, &in) == nil && in.Kind == "app.notification" && in.ConversationID == convID && in.Body == bodyStr {
				matches++
			}
		}
		return mcpFakeRowScanner{scanFn: func(dest ...any) error {
			if p, ok := dest[0].(*int); ok {
				*p = matches
			}
			return nil
		}}
	}
	if strings.Contains(sql, "runtime_notification_input_seq") {
		r.seq++
		seqStr := fmt.Sprintf("9%019d", r.seq)
		return mcpFakeRowScanner{scanFn: func(dest ...any) error {
			if p, ok := dest[0].(*string); ok {
				*p = seqStr
			}
			return nil
		}}
	}
	return mcpFakeRowScanner{scanFn: func(dest ...any) error { return nil }}
}

func (r *mcpInMemoryRepo) Exec(_ context.Context, sql string, args ...any) (domain.ExecTag, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.Contains(sql, "INSERT INTO runtime_inputs") && len(args) >= 3 {
		if raw, ok := args[2].([]byte); ok {
			r.pending = append(r.pending, append([]byte(nil), raw...))
			return mcpFakeExecTag{rows: 1}, nil
		}
	}
	return mcpFakeExecTag{rows: 1}, nil
}

func (r *mcpInMemoryRepo) AppendSessionEvents(_ context.Context, _ *Lease, _, _, _ string, _ []domain.SessionEventWrite) (bool, error) {
	return false, nil
}

func (r *mcpInMemoryRepo) GetAcceptedInputPayload(_ context.Context, _, eventID, _ string, _ int64) ([]byte, string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.accepted[eventID]
	if !ok {
		return nil, "", false, nil
	}
	return append([]byte(nil), item.raw...), item.runID, true, nil
}

func (r *mcpInMemoryRepo) ResolveRunSources(_ context.Context, _, runID string) ([]domain.InputSource, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.runs[runID]
	if !ok {
		return nil, domain.ErrContextUnavailable
	}
	var inputs []domain.Input
	if err := json.Unmarshal(entry.inputsJSON, &inputs); err != nil {
		return nil, domain.ErrContextUnavailable
	}
	refs, _ := domain.ExtractInputSources(inputs)
	return domain.CanonicalContextSources(refs)
}

func signTestMarketplaceToken(secret string, claims map[string]any) string {
	headerBytes, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	claimsBytes, _ := json.Marshal(claims)
	encodedHeader := base64.RawURLEncoding.EncodeToString(headerBytes)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claimsBytes)
	signingInput := encodedHeader + "." + encodedClaims
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(signingInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signingInput + "." + sig
}

func verifyTestMarketplaceToken(token, secret string) (map[string]any, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, false
	}
	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(signingInput))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[2]), []byte(expectedSig)) {
		return nil, false
	}
	rawClaims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var claims map[string]any
	if err := json.Unmarshal(rawClaims, &claims); err != nil {
		return nil, false
	}
	return claims, true
}

func TestMarketplaceMCPToolsDiscoveryExecutionReplayAndNotificationWakeup(t *testing.T) {
	ctx := context.Background()
	var s *Store
	if os.Getenv("TEAMIE_RUNTIME_TEST_DATABASE_URL") != "" {
		s = testStore(t)
	} else {
		s = newMCPInMemoryStore()
	}
	agentID := uuid.NewString()
	ownerID := uuid.NewString()
	wsID := uuid.NewString()
	groupConvID := uuid.NewString()
	channelConvID := uuid.NewString()
	appID := uuid.NewString()
	appSecret := "tps_test_secret_for_mcp_verification"

	var (
		mcpAuthMu       sync.Mutex
		lastAuthClaims  map[string]any
		toolCallCounter atomic.Int32
		appInstalled    atomic.Bool
	)
	appInstalled.Store(true)

	mcpSrv := server.NewMCPServer("Board Room MCP", "1.0.0", server.WithToolCapabilities(true))
	mcpSrv.AddTool(
		mcp.NewTool(
			"submit_turn_action",
			mcp.WithDescription("Submit a structured action for the current turn in the room"),
			mcp.WithString("action", mcp.Required(), mcp.Description("Action identifier")),
			mcp.WithNumber("seat", mcp.Description("Optional target seat number")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			authHeader := req.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				return mcp.NewToolResultError("missing bearer token"), nil
			}
			claims, ok := verifyTestMarketplaceToken(strings.TrimPrefix(authHeader, "Bearer "), appSecret)
			if !ok {
				return mcp.NewToolResultError("invalid context token signature"), nil
			}
			mcpAuthMu.Lock()
			lastAuthClaims = claims
			mcpAuthMu.Unlock()

			count := toolCallCounter.Add(1)
			args := req.GetArguments()
			action, _ := args["action"].(string)
			payload, _ := json.Marshal(map[string]any{
				"accepted":       true,
				"action":         action,
				"callCount":      count,
				"actorId":        claims["userId"],
				"conversationId": claims["conversationId"],
			})
			return mcp.NewToolResultText(string(payload)), nil
		},
	)

	streamableHTTP := server.NewStreamableHTTPServer(mcpSrv)
	mcpHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		claims, ok := verifyTestMarketplaceToken(strings.TrimPrefix(authHeader, "Bearer "), appSecret)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mcpAuthMu.Lock()
		lastAuthClaims = claims
		mcpAuthMu.Unlock()
		streamableHTTP.ServeHTTP(w, r)
	}))
	defer mcpHTTP.Close()

	imHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/bootstrap":
			_ = json.NewEncoder(w).Encode(Bootstrap{
				Workspaces: []Workspace{{ID: wsID, Name: "Team"}},
				Conversations: []Conversation{
					{ID: groupConvID, WorkspaceID: wsID, Type: "group", Name: "Game Group"},
					{ID: channelConvID, WorkspaceID: wsID, Type: "channel", Name: "general"},
				},
				SyncCursor: "1",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/conversations/"+channelConvID+"/apps":
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"code": "marketplace_group_only", "message": "Marketplace apps are only available in group conversations"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/conversations/"+groupConvID+"/apps":
			apps := []imMarketplaceApp{}
			if appInstalled.Load() {
				apps = append(apps, imMarketplaceApp{
					ID:          appID,
					Slug:        "board-room",
					Name:        "Board Room",
					Description: "Turn-based table app",
					WebURL:      "https://board.example.com/ui",
					MCPURL:      mcpHTTP.URL + "/mcp",
					Enabled:     true,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"apps": apps})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/conversations/"+groupConvID+"/apps/"+appID+"/session":
			if !appInstalled.Load() {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "marketplace_app_not_installed"}})
				return
			}
			now := time.Now().UTC()
			exp := now.Add(15 * time.Minute)
			token := signTestMarketplaceToken(appSecret, map[string]any{
				"iss":            "teamie",
				"aud":            appID,
				"sub":            agentID,
				"appId":          appID,
				"slug":           "board-room",
				"workspaceId":    wsID,
				"conversationId": groupConvID,
				"userId":         agentID,
				"displayName":    "PlayerBot",
				"iat":            now.Unix(),
				"exp":            exp.Unix(),
			})
			_ = json.NewEncoder(w).Encode(imConversationAppSession{
				App: imMarketplaceApp{
					ID:          appID,
					Slug:        "board-room",
					Name:        "Board Room",
					Description: "Turn-based table app",
					WebURL:      "https://board.example.com/ui",
					MCPURL:      mcpHTTP.URL + "/mcp",
					Enabled:     true,
				},
				Token:     token,
				ExpiresAt: exp.Format(time.RFC3339),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer imHTTP.Close()

	flagsFile := filepath.Join(t.TempDir(), "flags.json")
	rawFlags, _ := json.Marshal(map[string]any{
		"flags": map[string]bool{
			"agents.runtime.v3":      true,
			"agents.context.v1":      true,
			"im.marketplace.enabled": true,
		},
	})
	if err := os.WriteFile(flagsFile, rawFlags, 0o600); err != nil {
		t.Fatal(err)
	}

	lease, err := s.Acquire(ctx, agentID, "mcp-worker")
	if err != nil {
		t.Fatal(err)
	}
	imc := &IM{
		BaseURL:    imHTTP.URL,
		ServiceKey: "fixture-im-secret",
		AgentID:    agentID,
		token:      "agent-principal-token",
		expiry:     time.Now().Add(time.Hour),
	}
	toolBuilder := &Tools{
		Store:     s,
		Lease:     lease,
		IM:        imc,
		OwnerID:   ownerID,
		AgentID:   agentID,
		FlagsFile: flagsFile,
	}

	// 1. Channel conversation must NOT expose Marketplace MCP tools (403 marketplace_group_only skipped cleanly).
	chanInput := Input{
		EventID:        "1",
		WorkspaceID:    wsID,
		ConversationID: channelConvID,
		MessageID:      uuid.NewString(),
		SenderID:       ownerID,
		SenderKind:     "human",
		Body:           "Hello in channel",
	}
	if err := s.Ingest(ctx, lease, "1", []Input{chanInput}); err != nil {
		t.Fatal(err)
	}
	chanRun, err := s.StartRunWithContextPolicy(ctx, lease, []Input{chanInput}, contextProfileV1, archiveContextBudget(t))
	if err != nil {
		t.Fatal(err)
	}
	chanMCPTools, err := toolBuilder.BuildMCPTools(WithRun(ctx, chanRun), chanRun)
	if err != nil {
		t.Fatalf("unexpected error building MCP tools for channel conversation: %v", err)
	}
	if len(chanMCPTools) != 0 {
		t.Fatalf("expected 0 MCP tools in channel conversation, got %d", len(chanMCPTools))
	}
	if err := s.FinishRun(ctx, lease, chanRun, "completed", "", false); err != nil {
		t.Fatal(err)
	}

	// 2. Group conversation discovers tools via eino-ext/components/tool/mcp and executes with symmetric Context Token.
	groupInput := Input{
		EventID:        "2",
		WorkspaceID:    wsID,
		ConversationID: groupConvID,
		MessageID:      uuid.NewString(),
		SenderID:       ownerID,
		SenderKind:     "human",
		Body:           "Your turn in the group room",
	}
	if err := s.Ingest(ctx, lease, "2", []Input{groupInput}); err != nil {
		t.Fatal(err)
	}
	groupRun, err := s.StartRunWithContextPolicy(ctx, lease, []Input{groupInput}, contextProfileV1, archiveContextBudget(t))
	if err != nil {
		t.Fatal(err)
	}
	runCtx := WithRun(ctx, groupRun)
	groupMCPTools, err := toolBuilder.BuildMCPTools(runCtx, groupRun)
	if err != nil {
		t.Fatalf("BuildMCPTools for group conversation failed: %v", err)
	}
	if len(groupMCPTools) != 1 {
		t.Fatalf("expected 1 MCP tool, got %d", len(groupMCPTools))
	}
	info1, err := groupMCPTools[0].Info(runCtx)
	if err != nil {
		t.Fatal(err)
	}
	if info1.Name != "mcp__board_room__submit_turn_action" {
		t.Fatalf("unexpected exposed tool name: %s", info1.Name)
	}
	if !strings.Contains(info1.Desc, "Board Room") || !strings.Contains(info1.Desc, groupConvID) {
		t.Fatalf("expected tool description to include app name and group conversationId, got %s", info1.Desc)
	}

	node, err := compose.NewToolNode(runCtx, &compose.ToolsNodeConfig{
		Tools:               groupMCPTools,
		ExecuteSequentially: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	callOut1, err := invokeContextTool(node, runCtx, "mcp-call-1", "mcp__board_room__submit_turn_action", map[string]any{
		"action": "check_status",
		"seat":   3,
	})
	if err != nil {
		t.Fatalf("MCP tool invocation failed: %v", err)
	}
	if !strings.Contains(callOut1, `"ok":true`) || !strings.Contains(callOut1, "check_status") {
		t.Fatalf("unexpected MCP tool output: %s", callOut1)
	}
	if toolCallCounter.Load() != 1 {
		t.Fatalf("expected 1 remote MCP tool call, got %d", toolCallCounter.Load())
	}

	mcpAuthMu.Lock()
	claims := lastAuthClaims
	mcpAuthMu.Unlock()
	if claims == nil || claims["userId"] != agentID || claims["conversationId"] != groupConvID || claims["workspaceId"] != wsID {
		t.Fatalf("unexpected Context Token claims received by MCP server: %+v", claims)
	}
	if _, hasKind := claims["kind"]; hasKind {
		t.Fatalf("Context Token must not contain participant kind: %+v", claims)
	}
	if _, hasParticipantKind := claims["participantKind"]; hasParticipantKind {
		t.Fatalf("Context Token must not contain participantKind: %+v", claims)
	}

	// 3. Crash and replay of the same logical Run restores pinned ToolInfo and replays committed receipt without calling MCP server.
	restartedRun, err := s.StartRunWithContextPolicy(ctx, lease, []Input{groupInput}, contextProfileV1, archiveContextBudget(t))
	if err != nil || restartedRun.ID != groupRun.ID || restartedRun.Attempt == groupRun.Attempt {
		t.Fatalf("expected restarted attempt on same Run, got err=%v", err)
	}
	restartCtx := WithRun(ctx, restartedRun)
	replayedMCPTools, err := toolBuilder.BuildMCPTools(restartCtx, restartedRun)
	if err != nil || len(replayedMCPTools) != 1 {
		t.Fatalf("expected pinned MCP tool on replay, got len=%d err=%v", len(replayedMCPTools), err)
	}
	info2, err := replayedMCPTools[0].Info(restartCtx)
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := json.Marshal(info1)
	b2, _ := json.Marshal(info2)
	if string(b1) != string(b2) {
		t.Fatalf("pinned ToolInfo changed across replay:\nbefore=%s\nafter=%s", string(b1), string(b2))
	}
	replayNode, err := compose.NewToolNode(restartCtx, &compose.ToolsNodeConfig{
		Tools:               replayedMCPTools,
		ExecuteSequentially: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	callOut2, err := invokeContextTool(replayNode, restartCtx, "mcp-call-1", "mcp__board_room__submit_turn_action", map[string]any{
		"action": "check_status",
		"seat":   3,
	})
	if err != nil {
		t.Fatalf("replayed MCP tool invocation failed: %v", err)
	}
	if callOut2 != callOut1 {
		t.Fatalf("replay output mismatch:\ngot=%s\nwant=%s", callOut2, callOut1)
	}
	if toolCallCounter.Load() != 1 {
		t.Fatalf("replay must not re-invoke remote MCP server; toolCallCounter=%d", toolCallCounter.Load())
	}
	if err := s.FinishRun(ctx, lease, restartedRun, "completed", "", false); err != nil {
		t.Fatal(err)
	}

	// 4. Live MCP Notification Watcher receives server-pushed notification and persists TEAMIE_APP_NOTIFICATION input.
	watcher := newMCPNotificationWatcher(s, lease, imc, flagsFile)
	defer watcher.closeAll()
	watcher.sync(ctx)

	// Send an MCP notification from the MCP server to all connected clients.
	mcpSrv.SendNotificationToAllClients("notifications/room_state_updated", map[string]any{
		"phase":      "action_phase",
		"activeSeat": 2,
	})

	select {
	case <-watcher.wake():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for MCP notification wakeup signal")
	}

	pending, err := s.Pending(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending app.notification input, got %d", len(pending))
	}
	notifIn := pending[0]
	if notifIn.Kind != "app.notification" || notifIn.WorkspaceID != wsID || notifIn.ConversationID != groupConvID {
		t.Fatalf("unexpected app.notification input: %+v", notifIn)
	}
	msgs := inputMessages([]Input{notifIn})
	if len(msgs) != 1 || !strings.HasPrefix(agenticText(msgs[0]), "TEAMIE_APP_NOTIFICATION ") {
		t.Fatalf("expected TEAMIE_APP_NOTIFICATION prefix, got %q", agenticText(msgs[0]))
	}

	engine := &Engine{Store: s, Config: Config{FlagsFile: flagsFile}}
	if err := engine.authorizeAppNotificationInput(ctx, imc, lease, notifIn); err != nil {
		t.Fatalf("expected authorizeAppNotificationInput to succeed when app is installed, got %v", err)
	}

	// Verify ContextArchive provenance resolution for TEAMIE_APP_NOTIFICATION
	notifRun, err := s.StartRunWithContextPolicy(ctx, lease, []Input{notifIn}, contextProfileV1, archiveContextBudget(t))
	if err != nil {
		t.Fatal(err)
	}
	notifCtx := WithRun(ctx, notifRun)
	session := &SessionStore[*schema.AgenticMessage]{Store: s, Lease: lease}
	if err := session.AppendEvents(notifCtx, "agent:"+agentID, []*adk.SessionEvent[*schema.AgenticMessage]{{EventID: "app-notif-msg", Message: msgs[0]}}); err != nil {
		t.Fatal(err)
	}
	archive := &ContextArchive{Store: s, Lease: lease, IM: imc}
	sources, matched, err := archive.inputMessageSources(notifCtx, notifRun, msgs[0])
	if err != nil || !matched || len(sources) != 1 || sources[0].WorkspaceID != wsID || sources[0].ConversationID != groupConvID {
		t.Fatalf("expected TEAMIE_APP_NOTIFICATION provenance to retain group scope, got matched=%v sources=%+v err=%v", matched, sources, err)
	}
	if err := s.FinishRun(ctx, lease, notifRun, "completed", "", false); err != nil {
		t.Fatal(err)
	}

	// Uninstalling the app must cause authorizeAppNotificationInput and replayed MCP tool receipts to fail closed with errContextUnavailable.
	appInstalled.Store(false)
	if err := engine.authorizeAppNotificationInput(ctx, imc, lease, notifIn); !errors.Is(err, errContextUnavailable) {
		t.Fatalf("expected errContextUnavailable after app uninstall, got %v", err)
	}
	if _, err := invokeContextTool(replayNode, restartCtx, "mcp-call-1", "mcp__board_room__submit_turn_action", map[string]any{
		"action": "check_status",
		"seat":   3,
	}); !errors.Is(err, errContextUnavailable) {
		t.Fatalf("expected replayed MCP tool receipt to fail closed with errContextUnavailable after app uninstall, got %v", err)
	}
}

func TestConversationPresenceUnreadDigestAndProactiveList(t *testing.T) {
	ctx := context.Background()
	var s *Store
	if os.Getenv("TEAMIE_RUNTIME_TEST_DATABASE_URL") != "" {
		s = testStore(t)
	} else {
		s = newMCPInMemoryStore()
	}

	agentID := uuid.NewString()
	ownerID := uuid.NewString()
	wsID := uuid.NewString()
	groupConvID := uuid.NewString()
	channelConvID := uuid.NewString()
	directConvID := uuid.NewString()

	type syncEvent struct {
		ID             string   `json:"id"`
		Type           string   `json:"type"`
		WorkspaceID    string   `json:"workspaceId"`
		ConversationID string   `json:"conversationId"`
		Message        *Message `json:"message"`
	}
	var (
		imMu          sync.Mutex
		syncEvents    []syncEvent
		syncCursor    = "0"
		readSeqByConv = map[string]int64{}
		memberInGroup = true
	)

	imHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/bootstrap":
			imMu.Lock()
			convs := []Conversation{
				{ID: channelConvID, WorkspaceID: wsID, Type: "channel", Name: "general", MemberIDs: []string{ownerID, agentID}},
				{ID: directConvID, WorkspaceID: wsID, Type: "direct", Name: "Direct Alice", MemberIDs: []string{ownerID, agentID}},
			}
			if memberInGroup {
				convs = append(convs, Conversation{ID: groupConvID, WorkspaceID: wsID, Type: "group", Name: "Strategy Room", MemberIDs: []string{ownerID, agentID}})
			}
			cur := syncCursor
			imMu.Unlock()
			_ = json.NewEncoder(w).Encode(Bootstrap{
				Workspaces:    []Workspace{{ID: wsID, Name: "Team"}},
				Conversations: convs,
				SyncCursor:    cur,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sync":
			imMu.Lock()
			events := append([]syncEvent(nil), syncEvents...)
			syncEvents = nil
			cur := syncCursor
			imMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"events":  events,
				"cursor":  cur,
				"hasMore": false,
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/read"):
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/conversations/"), "/")
			if len(parts) == 2 && parts[1] == "read" {
				var body struct {
					Seq int64 `json:"seq"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				imMu.Lock()
				if body.Seq > readSeqByConv[parts[0]] {
					readSeqByConv[parts[0]] = body.Seq
				}
				imMu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer imHTTP.Close()

	flagsFile := filepath.Join(t.TempDir(), "flags.json")
	rawFlags, _ := json.Marshal(map[string]any{
		"flags": map[string]bool{
			"agents.runtime.v3":      true,
			"agents.context.v1":      true,
			"im.marketplace.enabled": true,
		},
	})
	if err := os.WriteFile(flagsFile, rawFlags, 0o600); err != nil {
		t.Fatal(err)
	}

	lease, err := s.Acquire(ctx, agentID, "presence-worker")
	if err != nil {
		t.Fatal(err)
	}
	imc := &IM{
		BaseURL:    imHTTP.URL,
		ServiceKey: "fixture-im-secret",
		AgentID:    agentID,
		token:      "agent-principal-token",
		expiry:     time.Now().Add(time.Hour),
	}
	digestInterval := 100 * time.Millisecond
	presence := newConversationPresenceState(digestInterval, nil)
	engine := &Engine{Store: s, Config: Config{FlagsFile: flagsFile}, UnreadDigestInterval: digestInterval}
	reads := newReadReceiptSender()

	// Batch 1: messages arrive across channel, group, and direct while activeConversationId == "".
	imMu.Lock()
	syncCursor = "4"
	syncEvents = []syncEvent{
		{
			ID:             "1",
			Type:           "message.created",
			WorkspaceID:    wsID,
			ConversationID: channelConvID,
			Message:        &Message{ID: uuid.NewString(), ConversationID: channelConvID, SenderID: ownerID, SenderKind: "human", Body: "Channel update 1", Seq: 10},
		},
		{
			ID:             "2",
			Type:           "message.created",
			WorkspaceID:    wsID,
			ConversationID: channelConvID,
			Message: &Message{
				ID:             uuid.NewString(),
				ConversationID: channelConvID,
				SenderID:       ownerID,
				SenderKind:     "human",
				Body:           "@Bot please review later",
				Seq:            11,
				Mentions:       []imtext.Mention{{UserID: agentID, Start: 0, End: 4}},
			},
		},
		{
			ID:             "3",
			Type:           "message.created",
			WorkspaceID:    wsID,
			ConversationID: groupConvID,
			Message:        &Message{ID: uuid.NewString(), ConversationID: groupConvID, SenderID: ownerID, SenderKind: "human", Body: "Room round started", Seq: 5},
		},
		{
			ID:             "4",
			Type:           "message.created",
			WorkspaceID:    wsID,
			ConversationID: directConvID,
			Message:        &Message{ID: uuid.NewString(), ConversationID: directConvID, SenderID: ownerID, SenderKind: "human", Body: "Ping in DM", Seq: 3},
		},
	}
	imMu.Unlock()

	ingested, err := engine.ingestWithPresence(ctx, imc, lease, presence)
	if err != nil {
		t.Fatalf("ingestWithPresence failed: %v", err)
	}
	if len(ingested) != 0 {
		t.Fatalf("expected 0 immediate TEAMIE_NEW_MESSAGE inputs for non-active conversations, got %d", len(ingested))
	}
	reads.recordInputs(ingested)
	if target, _ := reads.take(time.Now().Add(time.Hour)); target != nil {
		t.Fatalf("non-active conversations must not advance IM read receipts, got %+v", target)
	}

	// Verify unread state was durably checkpointed before cursor advance so worker restart never loses buffered counts.
	restored := newConversationPresenceState(digestInterval, nil)
	if err := restored.Restore(ctx, s, lease); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
	chanEnriched := restored.EnrichConversation(Conversation{ID: channelConvID, WorkspaceID: wsID, Type: "channel", Name: "general"})
	if chanEnriched.UnreadCount != 2 || chanEnriched.MentionCount != 1 || chanEnriched.LastSeq != 11 || chanEnriched.Active {
		t.Fatalf("unexpected restored channel unread state: %+v", chanEnriched)
	}

	// Proactive confirmation tool: im_list_conversations immediately surfaces unreadCount/mentionCount/lastSeq before the 1-minute digest fires.
	seedInput := Input{EventID: "100", WorkspaceID: wsID, ConversationID: channelConvID, MessageID: uuid.NewString(), SenderID: ownerID, SenderKind: "human", Body: "seed"}
	listRun, err := s.StartRunWithContextPolicy(ctx, lease, []Input{seedInput}, contextProfileV1, archiveContextBudget(t))
	if err != nil {
		t.Fatal(err)
	}
	listTools, err := (&Tools{Store: s, Lease: lease, IM: imc, Presence: presence}).BuildIM()
	if err != nil {
		t.Fatal(err)
	}
	listNode, err := compose.NewToolNode(WithRun(ctx, listRun), &compose.ToolsNodeConfig{Tools: listTools, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	listOut, err := invokeContextTool(listNode, WithRun(ctx, listRun), "proactive-list-1", "im_list_conversations", map[string]any{})
	if err != nil {
		t.Fatalf("im_list_conversations failed: %v", err)
	}
	if !strings.Contains(listOut, `"unreadCount":2`) || !strings.Contains(listOut, `"mentionCount":1`) || !strings.Contains(listOut, `"lastSeq":11`) {
		t.Fatalf("expected im_list_conversations to report live unread/mention counts, got %s", listOut)
	}
	_ = s.FinishRun(ctx, lease, listRun, "completed", "", false)

	// Exercise the boundary using the buffered event's deadline. The database
	// and tool checks above may take longer than the 100ms fixture interval.
	presence.mu.Lock()
	digestDueAt := presence.firstDirtyAt.Add(digestInterval)
	presence.mu.Unlock()
	flushedEarly, err := presence.FlushDueDigest(ctx, s, lease, digestDueAt.Add(-time.Nanosecond))
	if err != nil || flushedEarly {
		t.Fatalf("expected FlushDueDigest before interval to emit nothing, got flushed=%v err=%v", flushedEarly, err)
	}

	// After digestInterval elapses, FlushDueDigest emits exactly 1 coalesced TEAMIE_UNREAD_DIGEST input.
	flushed, err := presence.FlushDueDigest(ctx, s, lease, digestDueAt)
	if err != nil || !flushed {
		t.Fatalf("expected FlushDueDigest after interval to emit 1 digest, got flushed=%v err=%v", flushed, err)
	}
	// Calling FlushDueDigest again without new messages must NOT emit duplicate digests.
	flushedDup, err := presence.FlushDueDigest(ctx, s, lease, digestDueAt.Add(digestInterval))
	if err != nil || flushedDup {
		t.Fatalf("expected no duplicate digest without new unread messages, got flushed=%v err=%v", flushedDup, err)
	}

	pending, err := s.Pending(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending im.unread_digest input, got %d", len(pending))
	}
	digestIn := pending[0]
	if digestIn.Kind != "im.unread_digest" || len(digestIn.SourceRefs) != 3 {
		t.Fatalf("unexpected digest input: %+v", digestIn)
	}
	digestMsgs := inputMessages([]Input{digestIn})
	if len(digestMsgs) != 1 || !strings.HasPrefix(agenticText(digestMsgs[0]), "TEAMIE_UNREAD_DIGEST ") {
		t.Fatalf("expected TEAMIE_UNREAD_DIGEST prefix, got %q", agenticText(digestMsgs[0]))
	}
	if err := engine.authorizeUnreadDigestInput(ctx, imc, lease, digestIn); err != nil {
		t.Fatalf("expected authorizeUnreadDigestInput to succeed, got %v", err)
	}

	digestRun, err := s.StartRunWithContextPolicy(ctx, lease, []Input{digestIn}, contextProfileV1, archiveContextBudget(t))
	if err != nil {
		t.Fatal(err)
	}
	archive := &ContextArchive{Store: s, Lease: lease, IM: imc}
	sources, matched, err := archive.inputMessageSources(WithRun(ctx, digestRun), digestRun, digestMsgs[0])
	if err != nil || !matched || len(sources) != 3 {
		t.Fatalf("expected TEAMIE_UNREAD_DIGEST provenance to retain 3 conversations, got matched=%v len=%d err=%v", matched, len(sources), err)
	}
	_ = s.FinishRun(ctx, lease, digestRun, "completed", "", false)

	// Revoking membership from one of the referenced conversations causes authorizeUnreadDigestInput to fail closed.
	imMu.Lock()
	memberInGroup = false
	imMu.Unlock()
	if err := engine.authorizeUnreadDigestInput(ctx, imc, lease, digestIn); !errors.Is(err, errContextUnavailable) {
		t.Fatalf("expected errContextUnavailable after group membership removal, got %v", err)
	}
	imMu.Lock()
	memberInGroup = true
	imMu.Unlock()

	// Entering channelConvID delivers subsequent channel messages immediately as TEAMIE_NEW_MESSAGE while buffering group messages.
	presence.Enter(wsID, channelConvID, "channel")
	imMu.Lock()
	syncCursor = "6"
	syncEvents = []syncEvent{
		{
			ID:             "5",
			Type:           "message.created",
			WorkspaceID:    wsID,
			ConversationID: channelConvID,
			Message:        &Message{ID: uuid.NewString(), ConversationID: channelConvID, SenderID: ownerID, SenderKind: "human", Body: "Live reply in active channel", Seq: 12},
		},
		{
			ID:             "6",
			Type:           "message.created",
			WorkspaceID:    wsID,
			ConversationID: groupConvID,
			Message:        &Message{ID: uuid.NewString(), ConversationID: groupConvID, SenderID: ownerID, SenderKind: "human", Body: "Another room turn", Seq: 6},
		},
	}
	imMu.Unlock()

	activeIngested, err := engine.ingestWithPresence(ctx, imc, lease, presence)
	if err != nil {
		t.Fatalf("ingestWithPresence with active channel failed: %v", err)
	}
	if len(activeIngested) != 1 || activeIngested[0].ConversationID != channelConvID || activeIngested[0].Seq != 12 {
		t.Fatalf("expected only active channel message to ingest immediately, got %+v", activeIngested)
	}
	groupEnriched := presence.EnrichConversation(Conversation{ID: groupConvID, WorkspaceID: wsID, Type: "group", Name: "Strategy Room"})
	if groupEnriched.UnreadCount != 2 || groupEnriched.LastSeq != 6 {
		t.Fatalf("expected non-active group unread count to increment to 2, got %+v", groupEnriched)
	}
}

type dynamicTurnGatewayFixture struct {
	modelgatewayservice.Client
	calls     atomic.Int32
	lastError atomic.Value
	stepsFn   func(step int32) (*schema.AgenticMessage, error)
}

func (g *dynamicTurnGatewayFixture) Chat(_ context.Context, _ *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	step := g.calls.Add(1)
	msg, err := g.stepsFn(step)
	if err != nil {
		g.lastError.Store(err.Error())
		return nil, err
	}
	raw, err := (&schema.HumanReadableSerializer{}).Marshal(msg)
	if err != nil {
		return nil, err
	}
	val := string(raw)
	return &wire.GatewayChatResponse{Ok: true, ResponseJson: &val}, nil
}

type stepToolRecorderModel struct {
	inner     *GatewayModel
	mu        sync.Mutex
	stepTools [][]string
	requests  [][]byte
}

func (m *stepToolRecorderModel) capture(ctx context.Context, msgs []*schema.AgenticMessage, stream bool, options []model.Option) {
	r, _ := CurrentRun(ctx)
	opts := model.GetCommonOptions(nil, options...)
	_ = m.inner.capRunOutput(r, opts)
	names := make([]string, 0, len(opts.Tools))
	for _, info := range opts.Tools {
		if info != nil {
			names = append(names, info.Name)
		}
	}
	raw, _ := agenticRequestForRun(r, msgs, opts, stream)
	m.mu.Lock()
	m.stepTools = append(m.stepTools, names)
	m.requests = append(m.requests, raw)
	m.mu.Unlock()
}

func (m *stepToolRecorderModel) Generate(ctx context.Context, msgs []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	m.capture(ctx, msgs, false, opts)
	return m.inner.Generate(ctx, msgs, opts...)
}

func (m *stepToolRecorderModel) Stream(ctx context.Context, msgs []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	m.capture(ctx, msgs, true, opts)
	return m.inner.Stream(ctx, msgs, opts...)
}

type mcpStreamReadyWriter struct {
	http.ResponseWriter
	ready chan<- struct{}
}

func (w *mcpStreamReadyWriter) Flush() {
	w.ResponseWriter.(http.Flusher).Flush()
	select {
	case w.ready <- struct{}{}:
	default:
	}
}

func TestMidTurnDynamicMCPToolMountingSendGateAndSingleGroupWatcher(t *testing.T) {
	ctx := context.Background()
	var s *Store
	if os.Getenv("TEAMIE_RUNTIME_TEST_DATABASE_URL") != "" {
		s = testStore(t)
	} else {
		s = newMCPInMemoryStore()
	}

	agentID := uuid.NewString()
	ownerID := uuid.NewString()
	wsID := uuid.NewString()
	groupAConvID := uuid.NewString()
	groupBConvID := uuid.NewString()
	channelConvID := uuid.NewString()
	groupAMsgID := uuid.NewString()
	channelMsgID := uuid.NewString()

	appAID := uuid.NewString()
	appBID := uuid.NewString()
	appSecret := "tps_dynamic_mid_turn_secret"

	var (
		groupAToolCalls atomic.Int32
		groupBToolCalls atomic.Int32
		sentMessages    atomic.Int32
		readMu          sync.Mutex
		readSeqByConv   = map[string]int64{}
	)

	makeMCPServer := func(name, toolName string, counter *atomic.Int32) (*server.MCPServer, *httptest.Server, <-chan struct{}) {
		srv := server.NewMCPServer(name, "1.0.0", server.WithToolCapabilities(true))
		srv.AddTool(
			mcp.NewTool(
				toolName,
				mcp.WithDescription("Execute action in "+name),
				mcp.WithString("action", mcp.Required()),
			),
			func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				authHeader := req.Header.Get("Authorization")
				claims, ok := verifyTestMarketplaceToken(strings.TrimPrefix(authHeader, "Bearer "), appSecret)
				if !ok {
					return mcp.NewToolResultError("invalid token"), nil
				}
				n := counter.Add(1)
				args := req.GetArguments()
				out, _ := json.Marshal(map[string]any{
					"accepted":       true,
					"action":         args["action"],
					"count":          n,
					"conversationId": claims["conversationId"],
				})
				return mcp.NewToolResultText(string(out)), nil
			},
		)
		streamHTTP := server.NewStreamableHTTPServer(srv)
		ready := make(chan struct{}, 1)
		httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if _, ok := verifyTestMarketplaceToken(strings.TrimPrefix(authHeader, "Bearer "), appSecret); !ok {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Method == http.MethodGet {
				// The server registers its notification session before flushing
				// the SSE headers. Initialize returning is not this readiness gate.
				w = &mcpStreamReadyWriter{ResponseWriter: w, ready: ready}
			}
			streamHTTP.ServeHTTP(w, r)
		}))
		return srv, httpSrv, ready
	}

	mcpSrvA, mcpHTTPA, groupAStreamReady := makeMCPServer("Board Room", "submit_turn_action", &groupAToolCalls)
	defer mcpHTTPA.Close()
	mcpSrvB, mcpHTTPB, _ := makeMCPServer("Dice Table", "roll_dice", &groupBToolCalls)
	defer mcpHTTPB.Close()

	imHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/bootstrap":
			_ = json.NewEncoder(w).Encode(Bootstrap{
				Workspaces: []Workspace{{ID: wsID, Name: "Team"}},
				Conversations: []Conversation{
					{ID: groupAConvID, WorkspaceID: wsID, Type: "group", Name: "Group A", MemberIDs: []string{ownerID, agentID}},
					{ID: groupBConvID, WorkspaceID: wsID, Type: "group", Name: "Group B", MemberIDs: []string{ownerID, agentID}},
					{ID: channelConvID, WorkspaceID: wsID, Type: "channel", Name: "general", MemberIDs: []string{ownerID, agentID}},
				},
				SyncCursor: "10",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/conversations/"+groupAConvID+"/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messages": []Message{
					{ID: groupAMsgID, ConversationID: groupAConvID, SenderID: ownerID, SenderKind: "human", Body: "Please take seat 1", Seq: 7},
				},
				"hasMore": false,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/conversations/"+groupAConvID+"/messages/"+groupAMsgID:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": Message{ID: groupAMsgID, ConversationID: groupAConvID, SenderID: ownerID, SenderKind: "human", Body: "Please take seat 1", Seq: 7},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/conversations/"+channelConvID+"/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messages": []Message{
					{ID: channelMsgID, ConversationID: channelConvID, SenderID: ownerID, SenderKind: "human", Body: "Status check in general", Seq: 15},
				},
				"hasMore": false,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/conversations/"+channelConvID+"/messages/"+channelMsgID:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": Message{ID: channelMsgID, ConversationID: channelConvID, SenderID: ownerID, SenderKind: "human", Body: "Status check in general", Seq: 15},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/read"):
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/conversations/"), "/")
			var body struct {
				Seq int64 `json:"seq"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			readMu.Lock()
			if len(parts) == 2 && body.Seq > readSeqByConv[parts[0]] {
				readSeqByConv[parts[0]] = body.Seq
			}
			readMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/conversations/"+groupAConvID+"/messages":
			var body struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			n := sentMessages.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": Message{
					ID:             uuid.NewString(),
					ConversationID: groupAConvID,
					SenderID:       agentID,
					SenderKind:     "agent",
					Body:           body.Body,
					Seq:            int64(20 + n),
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/conversations/"+groupAConvID+"/apps":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"apps": []imMarketplaceApp{{
					ID:      appAID,
					Slug:    "board-room",
					Name:    "Board Room",
					MCPURL:  mcpHTTPA.URL + "/mcp",
					Enabled: true,
				}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/conversations/"+groupBConvID+"/apps":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"apps": []imMarketplaceApp{{
					ID:      appBID,
					Slug:    "dice-table",
					Name:    "Dice Table",
					MCPURL:  mcpHTTPB.URL + "/mcp",
					Enabled: true,
				}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/conversations/"+groupAConvID+"/apps/"+appAID+"/session":
			now := time.Now().UTC()
			exp := now.Add(15 * time.Minute)
			tok := signTestMarketplaceToken(appSecret, map[string]any{
				"iss":            "teamie",
				"aud":            appAID,
				"sub":            agentID,
				"appId":          appAID,
				"slug":           "board-room",
				"workspaceId":    wsID,
				"conversationId": groupAConvID,
				"userId":         agentID,
				"displayName":    "AgentPlayer",
				"iat":            now.Unix(),
				"exp":            exp.Unix(),
			})
			_ = json.NewEncoder(w).Encode(imConversationAppSession{
				App:       imMarketplaceApp{ID: appAID, Slug: "board-room", Name: "Board Room", MCPURL: mcpHTTPA.URL + "/mcp", Enabled: true},
				Token:     tok,
				ExpiresAt: exp.Format(time.RFC3339),
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/conversations/"+groupBConvID+"/apps/"+appBID+"/session":
			now := time.Now().UTC()
			exp := now.Add(15 * time.Minute)
			tok := signTestMarketplaceToken(appSecret, map[string]any{
				"iss":            "teamie",
				"aud":            appBID,
				"sub":            agentID,
				"appId":          appBID,
				"slug":           "dice-table",
				"workspaceId":    wsID,
				"conversationId": groupBConvID,
				"userId":         agentID,
				"displayName":    "AgentPlayer",
				"iat":            now.Unix(),
				"exp":            exp.Unix(),
			})
			_ = json.NewEncoder(w).Encode(imConversationAppSession{
				App:       imMarketplaceApp{ID: appBID, Slug: "dice-table", Name: "Dice Table", MCPURL: mcpHTTPB.URL + "/mcp", Enabled: true},
				Token:     tok,
				ExpiresAt: exp.Format(time.RFC3339),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer imHTTP.Close()

	flagsFile := filepath.Join(t.TempDir(), "flags.json")
	rawFlags, _ := json.Marshal(map[string]any{
		"flags": map[string]bool{
			"agents.runtime.v3":      true,
			"agents.context.v1":      true,
			"im.marketplace.enabled": true,
		},
	})
	if err := os.WriteFile(flagsFile, rawFlags, 0o600); err != nil {
		t.Fatal(err)
	}

	lease, err := s.Acquire(ctx, agentID, "dynamic-turn-worker")
	if err != nil {
		t.Fatal(err)
	}
	imc := &IM{
		BaseURL:    imHTTP.URL,
		ServiceKey: "fixture-im-secret",
		AgentID:    agentID,
		token:      "agent-principal-token",
		expiry:     time.Now().Add(time.Hour),
	}

	mcpWatcher := newMCPNotificationWatcher(s, lease, imc, flagsFile)
	defer mcpWatcher.closeAll()
	presence := newConversationPresenceState(time.Minute, mcpWatcher.triggerWake)
	mcpWatcher.setPresence(presence)

	// Before entering any group, mcpWatcher must maintain 0 active MCP streams even though the Agent belongs to 2 groups with apps.
	mcpWatcher.sync(ctx)
	if got := mcpWatcher.activeSubscriptionCount(); got != 0 {
		t.Fatalf("expected 0 active MCP subscriptions when activeConversationId is empty, got %d", got)
	}

	digestBodyBytes, _ := json.Marshal(UnreadDigestBody{
		ActiveConversationID: "",
		UnreadConversations: []UnreadConversationSummary{
			{WorkspaceID: wsID, ConversationID: groupAConvID, Name: "Group A", Type: "group", UnreadCount: 1, LastSeq: 7},
		},
	})
	var digestEventID string
	if err := s.DB.QueryRow(ctx, `SELECT (90000000000000000000::numeric+nextval('runtime_notification_input_seq'))::text`).Scan(&digestEventID); err != nil {
		t.Fatal(err)
	}
	turnInput := Input{
		EventID:        digestEventID,
		Kind:           "im.unread_digest",
		NotificationID: "unread-digest:turn-1",
		Body:           string(digestBodyBytes),
		CreatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		SourceRefs:     []InputSource{{WorkspaceID: wsID, ConversationID: groupAConvID}},
	}
	if err := s.Ingest(ctx, lease, "10", []Input{turnInput}); err != nil {
		t.Fatal(err)
	}

	sendBeforeEnterArgs, _ := json.Marshal(map[string]any{"conversationId": groupAConvID, "body": "premature message"})
	enterGroupAArgs, _ := json.Marshal(map[string]any{"conversationId": groupAConvID})
	mcpCallArgs, _ := json.Marshal(map[string]any{"action": "take_seat_1"})
	sendInGroupAArgs, _ := json.Marshal(map[string]any{"conversationId": groupAConvID, "body": "Ready at seat 1"})
	enterChannelArgs, _ := json.Marshal(map[string]any{"conversationId": channelConvID})

	gatewayFixture := &dynamicTurnGatewayFixture{
		stepsFn: func(step int32) (*schema.AgenticMessage, error) {
			switch step {
			case 1:
				// Step 1: attempt im_send_message before entering groupAConvID (must return not_in_conversation).
				return &schema.AgenticMessage{
					Role: schema.AgenticRoleTypeAssistant,
					ContentBlocks: []*schema.ContentBlock{
						schema.NewContentBlock(&schema.FunctionToolCall{CallID: "call-premature-send", Name: "im_send_message", Arguments: string(sendBeforeEnterArgs)}),
					},
				}, nil
			case 2:
				// Step 2: enter groupAConvID -> dynamically mounts Group A MCP tools for Step 3!
				return &schema.AgenticMessage{
					Role: schema.AgenticRoleTypeAssistant,
					ContentBlocks: []*schema.ContentBlock{
						schema.NewContentBlock(&schema.FunctionToolCall{CallID: "call-enter-group-a", Name: "im_enter_conversation", Arguments: string(enterGroupAArgs)}),
					},
				}, nil
			case 3:
				// Verify mcpWatcher wakes and subscribes ONLY to Group A (not Group B) while in Group A.
				mcpWatcher.sync(ctx)
				if got := mcpWatcher.activeSubscriptionCount(); got != 1 {
					return nil, fmt.Errorf("expected exactly 1 active MCP subscription (Group A only), got %d", got)
				}
				select {
				case <-groupAStreamReady:
				case <-time.After(5 * time.Second):
					return nil, fmt.Errorf("Group A notification stream did not become ready")
				}
				// Push a notification on Group B (must NOT be received) and Group A (must be received).
				mcpSrvB.SendNotificationToAllClients("notifications/dice_rolled", map[string]any{"roll": 6})
				mcpSrvA.SendNotificationToAllClients("notifications/seat_assigned", map[string]any{"seat": 1})
				// Do not leave A until the notification is durably accepted there.
				deadline := time.NewTimer(5 * time.Second)
				defer deadline.Stop()
				for {
					pending, err := s.Pending(ctx, lease)
					if err != nil {
						return nil, err
					}
					persisted := false
					for _, in := range pending {
						persisted = persisted || in.Kind == "app.notification" && in.ConversationID == groupAConvID && strings.Contains(in.Body, "notifications/seat_assigned")
					}
					if persisted {
						break
					}
					select {
					case <-mcpWatcher.wake():
					case <-deadline.C:
						return nil, fmt.Errorf("Group A notification was not persisted before switching away")
					}
				}

				// Step 3 (same Turn!): invoke newly mounted mcp__board_room__submit_turn_action AND im_send_message.
				return &schema.AgenticMessage{
					Role: schema.AgenticRoleTypeAssistant,
					ContentBlocks: []*schema.ContentBlock{
						schema.NewContentBlock(&schema.FunctionToolCall{CallID: "call-mcp-action", Name: "mcp__board_room__submit_turn_action", Arguments: string(mcpCallArgs)}),
						schema.NewContentBlock(&schema.FunctionToolCall{CallID: "call-send-group-a", Name: "im_send_message", Arguments: string(sendInGroupAArgs)}),
					},
				}, nil
			case 4:
				// Step 4: switch from Group A to channelConvID -> unmounts Group A MCP tools and closes stream!
				return &schema.AgenticMessage{
					Role: schema.AgenticRoleTypeAssistant,
					ContentBlocks: []*schema.ContentBlock{
						schema.NewContentBlock(&schema.FunctionToolCall{CallID: "call-enter-channel", Name: "im_enter_conversation", Arguments: string(enterChannelArgs)}),
					},
				}, nil
			case 5:
				mcpWatcher.sync(ctx)
				if got := mcpWatcher.activeSubscriptionCount(); got != 0 {
					return nil, fmt.Errorf("expected 0 active MCP subscriptions after switching to channel, got %d", got)
				}
				// Step 5: leave conversation completely.
				return &schema.AgenticMessage{
					Role: schema.AgenticRoleTypeAssistant,
					ContentBlocks: []*schema.ContentBlock{
						schema.NewContentBlock(&schema.FunctionToolCall{CallID: "call-leave", Name: "im_leave_conversation", Arguments: `{}`}),
					},
				}, nil
			case 6:
				return assistantText("Turn completed cleanly."), nil
			default:
				return nil, fmt.Errorf("unexpected provider call at step %d", step)
			}
		},
	}

	gatewayModel := &GatewayModel{Store: s, Lease: lease, Client: gatewayFixture, AgentID: agentID, OwnerID: ownerID}
	recorder := &stepToolRecorderModel{inner: gatewayModel}
	session := &SessionStore[*schema.AgenticMessage]{Store: s, Lease: lease}
	budgetRaw := archiveContextBudget(t)
	budget, err := ParseContextBudgetSnapshot(budgetRaw)
	if err != nil {
		t.Fatal(err)
	}

	var firstRunID string
	for attempt := 0; attempt < 2; attempt++ {
		run, err := s.StartRunWithContextPolicy(ctx, lease, []Input{turnInput}, contextProfileV1, budgetRaw)
		if err != nil {
			t.Fatal(err)
		}
		if attempt == 0 {
			firstRunID = run.ID
		} else if run.ID != firstRunID {
			t.Fatalf("expected replay to resume same Run %s, got %s", firstRunID, run.ID)
		}
		runCtx := WithRun(ctx, run)

		toolBuilder := &Tools{
			Store:     s,
			Lease:     lease,
			IM:        imc,
			OwnerID:   ownerID,
			AgentID:   agentID,
			FlagsFile: flagsFile,
			Presence:  presence,
		}
		presenceActive, err := toolBuilder.initRunPresence(runCtx, run)
		if err != nil || !presenceActive {
			t.Fatalf("expected initRunPresence=true, got active=%v err=%v", presenceActive, err)
		}
		imTools, err := toolBuilder.BuildIM()
		if err != nil {
			t.Fatal(err)
		}
		presenceTools, err := toolBuilder.BuildPresenceTools()
		if err != nil {
			t.Fatal(err)
		}
		activeTools := append([]tool.BaseTool{}, imTools...)
		activeTools = append(activeTools, presenceTools...)

		archive := &ContextArchive{Store: s, Lease: lease, IM: imc}
		pipeline, err := NewContextPipeline(runCtx, ContextPipelineConfig{
			Model:             recorder,
			Budget:            budget,
			Backend:           archive,
			ArchivePath:       archive.Path,
			ArchiveToolPath:   archive.ArchiveToolPath,
			ArchiveTranscript: archive.ArchiveTranscript,
		})
		if err != nil {
			t.Fatal(err)
		}
		agent, err := adk.NewTypedChatModelAgent(runCtx, &adk.TypedChatModelAgentConfig[*schema.AgenticMessage]{
			Name:        "teamie_agent",
			Description: "Dynamic presence & MCP test agent",
			Instruction: contextInstruction(&wire.AgentDefinition{Id: agentID, OwnerId: ownerID, Name: "Player"}, budget) + presenceAndMarketplaceGuidance,
			Model:       recorder,
			ToolsConfig: adk.ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools:               activeTools,
					ExecuteSequentially: true,
					UnknownToolsHandler: toolBuilder.HandleUnknownMCPTool,
				},
			},
			MaxIterations:    10,
			Handlers:         []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{toolBuilder.NewMCPDynamicToolMiddleware(), pipeline, &nativeToolReceipts{Store: s, Lease: lease, IM: imc}},
			ModelRetryConfig: pipeline.RetryConfig(),
		})
		if err != nil {
			t.Fatal(err)
		}
		runner := adk.NewTypedRunner(adk.TypedRunnerConfig[*schema.AgenticMessage]{
			Agent:        agent,
			SessionID:    "agent:" + agentID,
			SessionStore: session,
		})
		events := runner.Run(runCtx, inputMessages(run.Inputs))
		for {
			ev, ok := events.Next()
			if !ok {
				break
			}
			if ev != nil && ev.Err != nil {
				t.Fatalf("attempt %d failed with error: %v (fixture: %v)", attempt, ev.Err, gatewayFixture.lastError.Load())
			}
		}
	}

	// Verify side effects happened ONCE across both attempts (live + crash replay).
	if got := gatewayFixture.calls.Load(); got != 6 {
		t.Fatalf("expected 6 provider calls across live + replay, got %d", got)
	}
	if got := groupAToolCalls.Load(); got != 1 {
		t.Fatalf("expected 1 remote Group A MCP tool call across live + replay, got %d", got)
	}
	if got := groupBToolCalls.Load(); got != 0 {
		t.Fatalf("expected 0 Group B MCP tool calls, got %d", got)
	}
	if got := sentMessages.Load(); got != 1 {
		t.Fatalf("expected 1 IM message sent (premature send blocked by not_in_conversation), got %d", got)
	}

	readMu.Lock()
	groupAReadSeq := readSeqByConv[groupAConvID]
	channelReadSeq := readSeqByConv[channelConvID]
	readMu.Unlock()
	if groupAReadSeq != 7 || channelReadSeq != 15 {
		t.Fatalf("expected im_enter_conversation to advance read cursors (groupA=7, channel=15), got groupA=%d channel=%d", groupAReadSeq, channelReadSeq)
	}
	if got := presence.ActiveConversationID(); got != "" {
		t.Fatalf("expected activeConversationId to be empty after im_leave_conversation, got %q", got)
	}

	// Verify dynamic mid-Turn tool schemas at every step AND byte-exact request hash replay!
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.stepTools) != 12 || len(recorder.requests) != 12 {
		t.Fatalf("expected 12 recorded model steps (6 live + 6 replay), got tools=%d requests=%d", len(recorder.stepTools), len(recorder.requests))
	}
	hasMCP := func(tools []string, target string) bool {
		return slices.Contains(tools, target)
	}
	anyMCP := func(tools []string) bool {
		for _, name := range tools {
			if strings.HasPrefix(name, "mcp__") {
				return true
			}
		}
		return false
	}

	// Steps 0 & 1 (before entering Group A): 0 MCP tools, presence tools present.
	for _, idx := range []int{0, 1} {
		if anyMCP(recorder.stepTools[idx]) {
			t.Fatalf("step %d must not expose MCP tools before entering group: %v", idx+1, recorder.stepTools[idx])
		}
		if !slices.Contains(recorder.stepTools[idx], "im_enter_conversation") || !slices.Contains(recorder.stepTools[idx], "im_leave_conversation") {
			t.Fatalf("step %d missing presence tools: %v", idx+1, recorder.stepTools[idx])
		}
	}
	// Steps 2 & 3 (while inside Group A): Group A's MCP tool is mounted, Group B's MCP tool is NOT!
	for _, idx := range []int{2, 3} {
		if !hasMCP(recorder.stepTools[idx], "mcp__board_room__submit_turn_action") {
			t.Fatalf("step %d expected mcp__board_room__submit_turn_action to be dynamically mounted, got %v", idx+1, recorder.stepTools[idx])
		}
		if hasMCP(recorder.stepTools[idx], "mcp__dice_table__roll_dice") {
			t.Fatalf("step %d must not mount non-active Group B MCP tools: %v", idx+1, recorder.stepTools[idx])
		}
	}
	// Steps 4 & 5 (after switching to channelConvID and after leaving): 0 MCP tools!
	for _, idx := range []int{4, 5} {
		if anyMCP(recorder.stepTools[idx]) {
			t.Fatalf("step %d expected all MCP tools to be unmounted after leaving Group A, got %v", idx+1, recorder.stepTools[idx])
		}
	}
	// Verify every step's modelRequestHash on replay matched Attempt 1 identically!
	for step := 0; step < 6; step++ {
		h1 := modelRequestHash(recorder.requests[step])
		h2 := modelRequestHash(recorder.requests[step+6])
		if h1 != h2 {
			t.Fatalf("step %d modelRequestHash diverged on replay: live=%s replay=%s", step+1, h1, h2)
		}
	}

	// Verify Group A's pushed notification arrived while Group B's pushed notification was never subscribed.
	select {
	case <-mcpWatcher.wake():
	case <-time.After(2 * time.Second):
	}
	pendingInputs, err := s.Pending(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	foundGroupANotif := false
	for _, in := range pendingInputs {
		if in.Kind == "app.notification" {
			if in.ConversationID == groupBConvID {
				t.Fatalf("received unexpected notification from non-active Group B: %+v", in)
			}
			if in.ConversationID == groupAConvID && strings.Contains(in.Body, "seat_assigned") {
				foundGroupANotif = true
			}
		}
	}
	if !foundGroupANotif {
		t.Fatalf("expected Group A notification to be persisted while Group A was active, pending=%+v", pendingInputs)
	}
}

func TestMCPTransportSSRFProtection(t *testing.T) {
	prev := mcpForceProductionIPPolicy.Load()
	mcpForceProductionIPPolicy.Store(true)
	t.Cleanup(func() {
		mcpForceProductionIPPolicy.Store(prev)
	})

	blockedURLs := []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://100.100.100.200/latest/meta-data/",
		"http://10.0.0.1:8080/mcp",
		"http://172.16.5.4:8080/mcp",
		"http://192.168.1.10:8080/mcp",
		"http://127.0.0.1:8080/mcp",
		"http://[::1]:8080/mcp",
		"http://0.0.0.0:8080/mcp",
		"http://100.64.0.1:8080/mcp",
		"file:///etc/passwd",
		"gopher://example.com/mcp",
		"https://user:secret@93.184.216.34/mcp",
	}
	for _, rawURL := range blockedURLs {
		if _, err := newMCPHTTPClient(context.Background(), rawURL, nil); err == nil {
			t.Fatalf("expected SSRF validation to reject %q, but it succeeded", rawURL)
		}
	}

	publicIP := netip.MustParseAddr("93.184.216.34")
	if !isAllowedMCPTargetIP(publicIP) {
		t.Fatalf("expected public unicast IP 93.184.216.34 to be allowed")
	}
	if err := validateMCPTargetURL("https://93.184.216.34/mcp"); err != nil {
		t.Fatalf("expected https://93.184.216.34/mcp to pass URL validation, got %v", err)
	}
}

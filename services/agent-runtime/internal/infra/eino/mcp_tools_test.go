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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
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

type mcpInMemoryRepo struct {
	domain.RuntimeRepository
	mu           sync.Mutex
	seq          int64
	observations map[string][]byte
	toolCalls    map[string]*mcpFakeToolReceipt
	pending      [][]byte
	accepted     map[string]struct {
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
			observations: map[string][]byte{},
			toolCalls:    map[string]*mcpFakeToolReceipt{},
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

func (r *mcpInMemoryRepo) IngestInputs(_ context.Context, _ *Lease, _ string, inputs []domain.Input) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, in := range inputs {
		raw, _ := json.Marshal(in)
		r.pending = append(r.pending, raw)
	}
	return nil
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

func (r *mcpInMemoryRepo) GetObservation(_ context.Context, runID, callID, key string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	val, ok := r.observations[runID+"/"+callID+"/"+key]
	if !ok {
		return nil, errors.New("no rows in result set")
	}
	return append([]byte(nil), val...), nil
}

func (r *mcpInMemoryRepo) ListModelCallResponses(_ context.Context, _ string) ([][]byte, error) {
	return nil, nil
}

func (r *mcpInMemoryRepo) ListOtherAttemptMessageEvents(_ context.Context, _, _ string) ([]domain.AttemptEventPayload, error) {
	return nil, nil
}

func (r *mcpInMemoryRepo) RecordToolIntent(_ context.Context, _ *Lease, runID, id, name, hash string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := runID + "/" + id
	if existing, ok := r.toolCalls[k]; ok {
		if existing.name != name || existing.hash != hash {
			return nil, ErrReplayConflict
		}
		return append([]byte(nil), existing.result...), nil
	}
	r.toolCalls[k] = &mcpFakeToolReceipt{name: name, hash: hash}
	return nil, nil
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
		seqStr := fmt.Sprintf("90000000000000000%d", r.seq)
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
	if err != nil || !matched || len(sources) != 1 || sources[0].WorkspaceID != wsID {
		t.Fatalf("expected TEAMIE_APP_NOTIFICATION provenance to resolve to workspace source, got matched=%v sources=%+v err=%v", matched, sources, err)
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

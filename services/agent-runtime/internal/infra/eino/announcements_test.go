package eino

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/kitex/client/callopt"
	"github.com/google/uuid"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/services/agent-runtime/internal/domain"
)

type announcementTestGateway struct {
	*fixtureGateway
}

func (g *announcementTestGateway) Chat(_ context.Context, r *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	g.requests = append(g.requests, []byte(r.RequestJson))
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": chatMessage{Role: "assistant", Content: "已阅平台公告。"}, "finish_reason": "stop"}},
		"usage":   map[string]int{"prompt_tokens": 20, "completion_tokens": 5, "total_tokens": 25},
	})
	raw := string(b)
	return &wire.GatewayChatResponse{Ok: true, ResponseJson: modelFixtureResponse(raw), ModelConfigId: g.configIDLocked()}, nil
}

type mockAnnouncementIM struct {
	mu           sync.Mutex
	workspaceID  string
	convID       string
	nextSeq      int64
	items        []imAnnouncement
	readSeq      int64
	failReadPost bool
	readPosts    int
}

func (m *mockAnnouncementIM) add(title, body string) imAnnouncement {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextSeq++
	ann := imAnnouncement{
		ID:         uuid.NewString(),
		Seq:        m.nextSeq,
		Title:      title,
		Body:       body,
		AuthorID:   uuid.NewString(),
		AuthorName: "Platform Admin",
		CreatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}
	m.items = append([]imAnnouncement{ann}, m.items...)
	return ann
}

func (m *mockAnnouncementIM) remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.items[:0]
	for _, it := range m.items {
		if it.ID != id {
			kept = append(kept, it)
		}
	}
	m.items = kept
}

func (m *mockAnnouncementIM) setWorkspace(wsID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workspaceID = wsID
}

func (m *mockAnnouncementIM) currentReadSeq() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.readSeq
}

func (m *mockAnnouncementIM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	m.mu.Lock()
	defer m.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sync":
		_ = json.NewEncoder(w).Encode(map[string]any{"events": []any{}, "cursor": "0", "hasMore": false})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/bootstrap":
		var workspaces []Workspace
		var convs []Conversation
		if m.workspaceID != "" {
			workspaces = []Workspace{{ID: m.workspaceID, Name: "Core"}}
			if m.convID != "" {
				convs = []Conversation{{ID: m.convID, WorkspaceID: m.workspaceID, Name: "general", Type: "channel"}}
			}
		}
		_ = json.NewEncoder(w).Encode(Bootstrap{
			Workspaces:    workspaces,
			Conversations: convs,
			SyncCursor:    "0",
		})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/announcements":
		beforeSeq, _ := strconv.ParseInt(r.URL.Query().Get("beforeSeq"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 20
		}
		latestSeq := int64(0)
		for _, it := range m.items {
			if it.Seq > latestSeq {
				latestSeq = it.Seq
			}
		}
		filtered := make([]imAnnouncement, 0, limit+1)
		for _, it := range m.items {
			if beforeSeq == 0 || it.Seq < beforeSeq {
				filtered = append(filtered, it)
				if len(filtered) > limit {
					break
				}
			}
		}
		hasMore := len(filtered) > limit
		if hasMore {
			filtered = filtered[:limit]
		}
		_ = json.NewEncoder(w).Encode(imAnnouncementPage{
			Items:     filtered,
			ReadSeq:   m.readSeq,
			LatestSeq: latestSeq,
			HasMore:   hasMore,
		})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/announcements/read":
		if m.failReadPost {
			m.failReadPost = false
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"im_unavailable"}}`))
			return
		}
		var req struct {
			Seq int64 `json:"seq"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Seq <= 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		m.readPosts++
		if req.Seq > m.readSeq {
			m.readSeq = req.Seq
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"readSeq": m.readSeq})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestPlatformAnnouncementIngestReplayProvenanceAndTurnLoop(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	agentID := uuid.NewString()
	wsID := uuid.NewString()
	convID := uuid.NewString()
	lease, err := s.Acquire(ctx, agentID, "announcement-test")
	if err != nil {
		t.Fatal(err)
	}

	imMock := &mockAnnouncementIM{workspaceID: wsID, convID: convID}
	firstOld := imMock.add("v0.4.0 历史公告", "旧版本公告")
	latestInit := imMock.add("v0.4.6 平台更新", "新增群聊应用与平台公告能力。")

	srv := httptest.NewServer(imMock)
	defer srv.Close()

	imc := &IM{
		BaseURL:    srv.URL,
		ServiceKey: "fixture-im-secret",
		AgentID:    agentID,
		Identity:   &fixtureIdentity{},
	}
	eng := &Engine{Store: s}

	// 1. Brand-new Agent (readSeq == 0) receives only the latest announcement (seq=2), not obsolete seq=1.
	ingested, err := eng.ingestAnnouncements(ctx, imc, lease)
	if err != nil {
		t.Fatalf("ingestAnnouncements initial: %v", err)
	}
	if len(ingested) != 1 || ingested[0].Seq != latestInit.Seq || strings.Contains(ingested[0].Body, firstOld.Title) {
		t.Fatalf("expected only latest announcement seq=%d, got %+v", latestInit.Seq, ingested)
	}
	if imMock.currentReadSeq() != latestInit.Seq {
		t.Fatalf("expected readSeq=%d, got %d", latestInit.Seq, imMock.currentReadSeq())
	}

	pending, err := s.Pending(ctx, lease)
	if err != nil || len(pending) != 1 {
		t.Fatalf("expected 1 pending announcement input, got len=%d err=%v", len(pending), err)
	}
	msgs := inputMessages(pending)
	if len(msgs) != 1 || !strings.HasPrefix(agenticText(msgs[0]), "TEAMIE_PLATFORM_ANNOUNCEMENT ") {
		t.Fatalf("expected TEAMIE_PLATFORM_ANNOUNCEMENT prefix, got %q", agenticText(msgs[0]))
	}
	if domain.RelevantPreemptInput(toDomainInput(pending[0]), nil, nil) {
		t.Fatal("platform.announcement must never preempt an in-flight turn")
	}

	// Verify ContextArchive provenance resolution and workspace ACL enforcement.
	budgetBytes, err := newRunContextBudget(16384, true, true, true, true, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	annRun, err := s.StartRunWithContextPolicy(ctx, lease, pending, contextProfileV1, budgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.configureRun(ctx, lease, annRun, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	archive := &ContextArchive{Store: s, Lease: lease, IM: imc}
	sources, matched, err := archive.inputMessageSources(WithRun(ctx, annRun), annRun, msgs[0])
	if err != nil || !matched || len(sources) != 1 ||
		sources[0].WorkspaceID != domain.PlatformAnnouncementWorkspace ||
		sources[0].ConversationID != domain.PlatformAnnouncementConversation ||
		sources[0].MessageID != latestInit.ID ||
		sources[0].EventID != "announcement:"+strconv.FormatInt(latestInit.Seq, 10) {
		t.Fatalf("expected platform announcement provenance for %s, got matched=%v sources=%+v err=%v", latestInit.ID, matched, sources, err)
	}
	if err = archive.authorizeSources(ctx, sources); err != nil {
		t.Fatalf("expected live platform announcement provenance to authorize, got %v", err)
	}
	if err = s.FinishRun(ctx, lease, annRun, "completed", "", false); err != nil {
		t.Fatal(err)
	}

	// 2. Crash/failure before POST /api/v1/announcements/read must not duplicate runtime_inputs on replay.
	third := imMock.add("行为同步通知", "大家可以在休息群自由聊天。")
	imMock.mu.Lock()
	imMock.failReadPost = true
	imMock.mu.Unlock()

	if _, err = eng.ingestAnnouncements(ctx, imc, lease); err == nil {
		t.Fatal("expected transient error when POST /api/v1/announcements/read returns 503")
	}
	if imMock.currentReadSeq() != latestInit.Seq {
		t.Fatalf("readSeq should remain %d before retry, got %d", latestInit.Seq, imMock.currentReadSeq())
	}
	replayed, err := eng.ingestAnnouncements(ctx, imc, lease)
	if err != nil {
		t.Fatalf("retry ingestAnnouncements: %v", err)
	}
	if len(replayed) != 0 {
		t.Fatalf("expected 0 duplicate inserts on replay, got %d", len(replayed))
	}
	if imMock.currentReadSeq() != third.Seq {
		t.Fatalf("expected readSeq=%d after retry, got %d", third.Seq, imMock.currentReadSeq())
	}

	var thirdCount int
	if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM runtime_inputs WHERE agent_id=$1 AND notification_id=$2`, agentID, "announcement:"+third.ID).Scan(&thirdCount); err != nil {
		t.Fatal(err)
	}
	if thirdCount != 1 {
		t.Fatalf("expected exactly 1 durable row for announcement %s, got %d", third.ID, thirdCount)
	}
	// Mark third completed so step 3 starts clean.
	pendingThird, err := s.Pending(ctx, lease)
	if err != nil || len(pendingThird) != 1 {
		t.Fatalf("expected 1 pending input for third announcement, got %d err=%v", len(pendingThird), err)
	}
	run3, err := s.StartRunWithContextPolicy(ctx, lease, pendingThird, contextProfileV1, budgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, lease, run3, "completed", "", false); err != nil {
		t.Fatal(err)
	}

	// 3. Mistaken announcement deleted by admin before turn validation is skipped cleanly.
	mistaken := imMock.add("误发测试公告", "请忽略")
	if _, err = eng.ingestAnnouncements(ctx, imc, lease); err != nil {
		t.Fatal(err)
	}
	imMock.remove(mistaken.ID)
	pendingMistaken, err := s.Pending(ctx, lease)
	if err != nil || len(pendingMistaken) != 1 {
		t.Fatalf("expected 1 pending mistaken input, got %d err=%v", len(pendingMistaken), err)
	}
	if authErr := eng.authorizeAnnouncementInput(ctx, imc, lease, pendingMistaken[0]); !errors.Is(authErr, errContextUnavailable) {
		t.Fatalf("expected errContextUnavailable for deleted announcement, got %v", authErr)
	}
	if err = s.Skip(ctx, lease, pendingMistaken[0].EventID); err != nil {
		t.Fatal(err)
	}

	// Platform-wide provenance stays valid across workspace changes and works for zero-workspace Agents,
	// while admin deletion revokes both turn-input and ContextArchive provenance authorization.
	imMock.setWorkspace("")
	live := imMock.add("跨工作区平台通知", "即使尚未加入任何工作区也能接收并保留上下文血缘。")
	ingestedZeroWS, err := eng.ingestAnnouncements(ctx, imc, lease)
	if err != nil || len(ingestedZeroWS) != 1 || ingestedZeroWS[0].Seq != live.Seq {
		t.Fatalf("expected zero-workspace Agent to ingest announcement seq=%d, got %+v err=%v", live.Seq, ingestedZeroWS, err)
	}
	pendingLive, err := s.Pending(ctx, lease)
	if err != nil || len(pendingLive) != 1 || pendingLive[0].Seq != live.Seq {
		t.Fatalf("expected pending live announcement, got %+v err=%v", pendingLive, err)
	}
	if authErr := eng.authorizeAnnouncementInput(ctx, imc, lease, pendingLive[0]); authErr != nil {
		t.Fatalf("expected platform announcement to remain authorized with zero workspaces, got %v", authErr)
	}
	if authErr := archive.authorizeSources(ctx, pendingLive[0].SourceRefs); authErr != nil {
		t.Fatalf("expected ContextArchive provenance to remain authorized with zero workspaces, got %v", authErr)
	}
	imMock.remove(live.ID)
	if authErr := archive.authorizeSources(ctx, pendingLive[0].SourceRefs); !errors.Is(authErr, errContextUnavailable) {
		t.Fatalf("expected ContextArchive provenance errContextUnavailable after announcement deletion, got %v", authErr)
	}
	if err = s.Skip(ctx, lease, pendingLive[0].EventID); err != nil {
		t.Fatal(err)
	}
	imMock.setWorkspace(wsID)

	// 3b. Multi-page catch-up (>20 unread announcements) fetches all unread pages in ascending order before advancing readSeq.
	const burstUnread = 45
	var expectedSeqs []int64
	for i := 0; i < burstUnread; i++ {
		item := imMock.add("离线积压公告 #"+strconv.Itoa(i+1), "第 "+strconv.Itoa(i+1)+" 条积压公告")
		expectedSeqs = append(expectedSeqs, item.Seq)
	}
	catchUp, err := eng.ingestAnnouncements(ctx, imc, lease)
	if err != nil {
		t.Fatalf("multi-page ingestAnnouncements: %v", err)
	}
	if len(catchUp) != burstUnread {
		t.Fatalf("expected all %d unread announcements across pages, got %d", burstUnread, len(catchUp))
	}
	for i, in := range catchUp {
		if in.Seq != expectedSeqs[i] {
			t.Fatalf("expected ascending seq[%d]=%d, got %d", i, expectedSeqs[i], in.Seq)
		}
	}
	if imMock.currentReadSeq() != expectedSeqs[len(expectedSeqs)-1] {
		t.Fatalf("expected readSeq=%d after multi-page catch-up, got %d", expectedSeqs[len(expectedSeqs)-1], imMock.currentReadSeq())
	}
	var drained []Input
	for {
		batch, pErr := s.Pending(ctx, lease)
		if pErr != nil {
			t.Fatal(pErr)
		}
		if len(batch) == 0 {
			break
		}
		for _, in := range batch {
			drained = append(drained, in)
			if err = s.Skip(ctx, lease, in.EventID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(drained) != burstUnread {
		t.Fatalf("expected %d total drained catch-up inputs, got %d", burstUnread, len(drained))
	}
	for i, in := range drained {
		if in.Seq != expectedSeqs[i] {
			t.Fatalf("expected drained seq[%d]=%d, got %d", i, expectedSeqs[i], in.Seq)
		}
	}

	// 4. End-to-end worker TurnLoop processes live announcement without defensive prompt bloat.
	flagsPath := filepath.Join(t.TempDir(), "flags.json")
	if err = os.WriteFile(flagsPath, []byte(`{"flags":{"agents.runtime.v3":true,"agents.context.v1":true,"im.dm.create":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	agentID2 := uuid.NewString()
	lease2, err := s.Acquire(ctx, agentID2, "announcement-worker-test")
	if err != nil {
		t.Fatal(err)
	}
	imMock2 := &mockAnnouncementIM{workspaceID: wsID, convID: convID}
	srv2 := httptest.NewServer(imMock2)
	defer srv2.Close()

	baseGW := &fixtureGateway{ready: true}
	gw := &announcementTestGateway{fixtureGateway: baseGW}
	def := &wire.AgentDefinition{
		Id:           agentID2,
		OwnerId:      uuid.NewString(),
		Name:         "Nova",
		Description:  "Autonomous engineer",
		WorkspaceIds: []string{wsID},
		Enabled:      true,
		Revision:     1,
	}
	cp := &fixtureControl{}
	workerEng := &Engine{
		Store:                    s,
		Config:                   Config{IMURL: srv2.URL, IMToken: "fixture-im-secret", ControlToken: "fixture-cp-secret", GatewayToken: "fixture-gw-secret", SandboxToken: "fixture-sb-secret", FlagsFile: flagsPath},
		Control:                  cp,
		Gateway:                  gw,
		Sandbox:                  &fixtureSandbox{},
		Identity:                 &fixtureIdentity{},
		AnnouncementPollInterval: 25 * time.Millisecond,
		owner:                    "announcement-worker-test",
	}

	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	done := make(chan struct{})
	go func() {
		defer close(done)
		workerEng.work(workCtx, def, lease2)
	}()

	published := imMock2.add("v0.4.6 正式上线", "支持平台公告弹窗与模型网关平滑错峰。")
	var finalSys, finalUser string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		baseGW.mu.Lock()
		reqs := append([][]byte(nil), baseGW.requests...)
		baseGW.mu.Unlock()
		for _, rawReq := range reqs {
			var parsed struct {
				Messages []chatMessage `json:"messages"`
			}
			if decodeFixtureRequest(string(rawReq), &parsed) == nil {
				for _, m := range parsed.Messages {
					if m.Role == "system" && finalSys == "" {
						finalSys = m.Content
					}
					if m.Role == "user" && strings.HasPrefix(m.Content, "TEAMIE_PLATFORM_ANNOUNCEMENT ") && strings.Contains(m.Content, published.Title) {
						finalUser = m.Content
					}
				}
			}
		}
		if finalUser != "" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancelWork()
	<-done

	if !strings.Contains(finalUser, "TEAMIE_PLATFORM_ANNOUNCEMENT ") || !strings.Contains(finalUser, published.Title) {
		var runState, runErr, inState string
		_ = s.DB.QueryRow(ctx, `SELECT coalesce(state,''), coalesce(last_error,'') FROM runtime_runs WHERE agent_id=$1 ORDER BY started_at DESC LIMIT 1`, agentID2).Scan(&runState, &runErr)
		_ = s.DB.QueryRow(ctx, `SELECT coalesce(state,'') FROM runtime_inputs WHERE agent_id=$1 ORDER BY event_id DESC LIMIT 1`, agentID2).Scan(&inState)
		t.Fatalf("expected worker TurnLoop to deliver TEAMIE_PLATFORM_ANNOUNCEMENT with %q, got %q (inputState=%q runState=%q runErr=%q cpStates=%v)", published.Title, finalUser, inState, runState, runErr, cp.states)
	}
	if strings.Contains(finalSys, "TEAMIE_PLATFORM_ANNOUNCEMENT") {
		t.Fatalf("expected zero defensive announcement prompt bloat in system prompt, got %q", finalSys)
	}
}

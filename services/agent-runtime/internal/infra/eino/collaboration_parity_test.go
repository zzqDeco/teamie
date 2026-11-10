package eino

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/services/agent-runtime/internal/domain"
)

func TestNonWorkSyncEventsAdvanceCursorWithoutWakeupOrReadMutation(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	e, f, gw, _ := fixtureEngine(t, s)

	f.add("silent initial message", "")
	cancel, done := startFixtureWorker(t, e, f)
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE state='completed'`) == 1 &&
			receivedSeq(f, f.conversation) == 1
	})

	gw.mu.Lock()
	initialModelCalls := gw.calls
	gw.mu.Unlock()
	f.mu.Lock()
	initialReadPosts := f.readCalls
	f.mu.Unlock()
	if initialModelCalls != 1 || initialReadPosts != 1 {
		t.Fatalf("unexpected baseline: modelCalls=%d readPosts=%d", initialModelCalls, initialReadPosts)
	}

	// Emit pin.updated (2), saved.updated (3), and manual-unread read.updated (4).
	f.mu.Lock()
	var msgID string
	for id := range f.messages {
		msgID = id
	}
	f.events = append(f.events,
		map[string]any{
			"id":             "2",
			"type":           "pin.updated",
			"workspaceId":    f.workspace,
			"conversationId": f.conversation,
			"pin":            map[string]any{"conversationId": f.conversation, "messageId": msgID, "pinned": true},
		},
		map[string]any{
			"id":             "3",
			"type":           "saved.updated",
			"workspaceId":    f.workspace,
			"conversationId": f.conversation,
			"saved":          map[string]any{"conversationId": f.conversation, "messageId": msgID, "saved": true},
		},
		map[string]any{
			"id":             "4",
			"type":           "read.updated",
			"workspaceId":    f.workspace,
			"conversationId": f.conversation,
			"read":           map[string]any{"conversationId": f.conversation, "readSeq": 1, "manualUnreadSeq": 1},
		},
	)
	f.mu.Unlock()

	waitFor(t, func() bool {
		var cur string
		if err := s.DB.QueryRow(ctx, `SELECT cursor FROM runtime_agents WHERE agent_id=$1`, f.agent).Scan(&cur); err != nil {
			return false
		}
		return cur == "4"
	})
	// Allow one beat beyond readReceiptSender's interval to confirm no spurious read or run fires.
	time.Sleep(150 * time.Millisecond)

	cancel()
	<-done

	if gotInputs := count(t, s, `SELECT count(*) FROM runtime_inputs WHERE agent_id=$1`, f.agent); gotInputs != 1 {
		t.Fatalf("non-work sync events created runtime_inputs: got %d, want 1", gotInputs)
	}
	if gotRuns := count(t, s, `SELECT count(*) FROM runtime_runs WHERE agent_id=$1`, f.agent); gotRuns != 1 {
		t.Fatalf("non-work sync events woke Agent into extra runs: got %d, want 1", gotRuns)
	}
	gw.mu.Lock()
	afterModelCalls := gw.calls
	gw.mu.Unlock()
	if afterModelCalls != initialModelCalls {
		t.Fatalf("non-work sync events triggered model call: got %d, want %d", afterModelCalls, initialModelCalls)
	}
	f.mu.Lock()
	afterReadPosts := f.readCalls
	f.mu.Unlock()
	if afterReadPosts != initialReadPosts || receivedSeq(f, f.conversation) != 1 {
		t.Fatalf("manual unread or non-work sync events altered IM read confirmations: posts=%d seq=%d", afterReadPosts, receivedSeq(f, f.conversation))
	}
}

func TestForwardedMessagesPreserveSnapshotEnforceTargetProvenanceAndDownloadCopiedAttachment(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	agentID := uuid.NewString()
	targetWS := uuid.NewString()
	activeConv := uuid.NewString()
	targetConv := uuid.NewString()
	forbiddenSourceConv := uuid.NewString()
	forbiddenSourceMsg := uuid.NewString()
	origSenderID := uuid.NewString()

	copiedBytes := []byte("fake-png-binary-payload-for-copied-forward-attachment")
	copiedSum := sha256.Sum256(copiedBytes)
	copiedAtt := Attachment{
		ID:        uuid.NewString(),
		Filename:  "architecture.png",
		MediaType: "image/png",
		SizeBytes: len(copiedBytes),
		SHA256:    hex.EncodeToString(copiedSum[:]),
		Image:     &AttachmentImage{Width: 640, Height: 480},
	}
	directBytes := []byte("direct-image-bytes")
	directSum := sha256.Sum256(directBytes)
	directAtt := Attachment{
		ID:        uuid.NewString(),
		Filename:  "thumb.webp",
		MediaType: "image/webp",
		SizeBytes: len(directBytes),
		SHA256:    hex.EncodeToString(directSum[:]),
		Image:     &AttachmentImage{Width: 320, Height: 240},
	}

	forwardMsg1 := Message{
		ID:             uuid.NewString(),
		ConversationID: targetConv,
		SenderID:       uuid.NewString(),
		SenderName:     "Bob",
		SenderKind:     "human",
		Body:           "", // empty-comment forward
		Attachments:    []Attachment{directAtt},
		Forwarded: &ForwardedMessage{
			SourceConversationID: forbiddenSourceConv,
			SourceMessageID:      forbiddenSourceMsg,
			SenderID:             origSenderID,
			SenderName:           "Carol",
			SenderKind:           "human",
			CreatedAt:            "2026-11-07T01:00:00Z",
			Body:                 "@Agent @all secret roadmap inside forwarded snapshot",
			Mentions: []MentionSpan{
				{UserID: agentID, Start: 0, End: 6},
				{Kind: "all", Start: 7, End: 11},
			},
			Attachments: []Attachment{copiedAtt},
		},
		Seq:       1,
		CreatedAt: "2026-11-07T01:05:00Z",
	}
	forwardMsg2 := Message{
		ID:             uuid.NewString(),
		ConversationID: targetConv,
		SenderID:       forwardMsg1.SenderID,
		SenderName:     "Bob",
		SenderKind:     "human",
		Body:           "@Agent please review",
		Mentions:       []MentionSpan{{UserID: agentID, Start: 0, End: 6}},
		Forwarded:      forwardMsg1.Forwarded,
		Seq:            2,
		CreatedAt:      "2026-11-07T01:06:00Z",
	}

	var mu sync.Mutex
	var events []map[string]any
	var forbiddenHits atomic.Int32
	messagesByID := map[string]Message{
		forwardMsg1.ID: forwardMsg1,
		forwardMsg2.ID: forwardMsg2,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) || r.Header.Get("X-Teamie-Service-Key") != "fixture-im-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.Contains(r.URL.Path, forbiddenSourceConv) {
			forbiddenHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"no source acl"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/bootstrap":
			_ = json.NewEncoder(w).Encode(Bootstrap{
				Conversations: []Conversation{
					{ID: activeConv, WorkspaceID: targetWS, Type: "group", Name: "Active"},
					{ID: targetConv, WorkspaceID: targetWS, Type: "group", Name: "Target"},
				},
				SyncCursor: "0",
			})
		case r.URL.Path == "/api/v1/sync":
			mu.Lock()
			var cur int
			_, _ = fmt.Sscan(r.URL.Query().Get("cursor"), &cur)
			batch := append([]map[string]any(nil), events[cur:]...)
			next := fmt.Sprint(len(events))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"events": batch, "cursor": next, "hasMore": false})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/conversations/"+targetConv+"/messages/"+forwardMsg1.ID:
			_ = json.NewEncoder(w).Encode(map[string]any{"message": forwardMsg1})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/conversations/"+targetConv+"/messages/"+forwardMsg2.ID:
			_ = json.NewEncoder(w).Encode(map[string]any{"message": forwardMsg2})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/conversations/"+targetConv+"/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{"messages": []Message{forwardMsg2, forwardMsg1}, "hasMore": false})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/conversations/"+targetConv+"/attachments/"+copiedAtt.ID+"/content":
			w.Header().Set("Content-Type", copiedAtt.MediaType)
			_, _ = w.Write(copiedBytes)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/conversations/"+targetConv+"/attachments/"+directAtt.ID+"/content":
			w.Header().Set("Content-Type", directAtt.MediaType)
			_, _ = w.Write(directBytes)
		case strings.HasSuffix(r.URL.Path, "/read") && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{"readSeq": 2})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = messagesByID
		}
	}))
	defer server.Close()

	sandbox := &attachmentSandboxFixture{}
	flags := filepath.Join(t.TempDir(), "flags.json")
	if err := os.WriteFile(flags, []byte(`{"revision":"collab-forward","flags":{"agents.runtime.v3":true,"agents.tasks.execute":true,"agents.context.v1":true,"agents.mentions.v1":true,"agents.files.v1":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	baseGW := &fixtureGateway{ready: true}
	e := &Engine{
		Store: s,
		Config: Config{
			IMURL:        server.URL,
			IMToken:      "fixture-im-secret",
			ControlToken: "fixture-cp-secret",
			GatewayToken: "fixture-gw-secret",
			SandboxToken: "fixture-sb-secret",
			FlagsFile:    flags,
		},
		Control:  &fixtureControl{},
		Gateway:  baseGW,
		Sandbox:  sandbox,
		Identity: &fixtureIdentity{},
		owner:    uuid.NewString(),
	}

	// Part 1: Verify non-active unread tracking ignores Forwarded.Mentions and counts top-level Mentions.
	unreadAgentID := agentID
	unreadLease, err := s.Acquire(ctx, unreadAgentID, "unread-check")
	if err != nil {
		t.Fatal(err)
	}
	imc := &IM{BaseURL: server.URL, AgentID: unreadAgentID, Identity: e.Identity, ServiceKey: e.Config.IMToken}
	presence := newConversationPresenceState(time.Minute, nil)
	presence.Enter(targetWS, activeConv, "group")

	mu.Lock()
	events = append(events, map[string]any{
		"id":             "1",
		"type":           "message.created",
		"workspaceId":    targetWS,
		"conversationId": targetConv,
		"message":        forwardMsg1,
	})
	mu.Unlock()
	if committed, err := e.ingestWithPresenceAndAuth(ctx, imc, unreadLease, presence, nil); err != nil || len(committed) != 0 {
		t.Fatalf("non-active forward ingest: committed=%d err=%v", len(committed), err)
	}
	enriched1 := presence.EnrichConversation(Conversation{ID: targetConv, WorkspaceID: targetWS})
	if enriched1.UnreadCount != 1 || enriched1.MentionCount != 0 {
		t.Fatalf("display-only Forwarded.Mentions counted as mention: unread=%d mention=%d", enriched1.UnreadCount, enriched1.MentionCount)
	}

	mu.Lock()
	events = append(events, map[string]any{
		"id":             "2",
		"type":           "message.created",
		"workspaceId":    targetWS,
		"conversationId": targetConv,
		"message":        forwardMsg2,
	})
	mu.Unlock()
	if _, err := e.ingestWithPresenceAndAuth(ctx, imc, unreadLease, presence, nil); err != nil {
		t.Fatal(err)
	}
	enriched2 := presence.EnrichConversation(Conversation{ID: targetConv, WorkspaceID: targetWS})
	if enriched2.UnreadCount != 2 || enriched2.MentionCount != 1 {
		t.Fatalf("top-level mention on forward miscounted: unread=%d mention=%d", enriched2.UnreadCount, enriched2.MentionCount)
	}
	s.Release(ctx, unreadLease)

	// Part 2: Full worker TurnLoop in targetConv with empty-comment forward, tool reads, copied attachment import, and provenance checks.
	turnAgentID := uuid.NewString()
	mu.Lock()
	events = []map[string]any{{
		"id":             "1",
		"type":           "message.created",
		"workspaceId":    targetWS,
		"conversationId": targetConv,
		"message":        forwardMsg1,
	}}
	mu.Unlock()

	gw := &toolPathGateway{fixtureGateway: baseGW}
	gw.steps = []func(toolPathRequest) (chatMessage, error){
		func(req toolPathRequest) (chatMessage, error) {
			var delivered Input
			for _, m := range req.Messages {
				if strings.HasPrefix(m.Content, "TEAMIE_NEW_MESSAGE ") {
					if err := json.Unmarshal([]byte(strings.TrimPrefix(m.Content, "TEAMIE_NEW_MESSAGE ")), &delivered); err != nil {
						return chatMessage{}, err
					}
				}
			}
			if delivered.MessageID != forwardMsg1.ID || delivered.Body != "" || delivered.Forwarded == nil {
				return chatMessage{}, fmt.Errorf("empty-comment forward envelope missing Forwarded: %+v", delivered)
			}
			if !domain.ForwardedEqual(delivered.Forwarded, forwardMsg1.Forwarded) {
				return chatMessage{}, fmt.Errorf("delivered Forwarded snapshot mutated: got %+v", delivered.Forwarded)
			}
			if len(delivered.Attachments) != 1 || delivered.Attachments[0].Image == nil || *delivered.Attachments[0].Image != (AttachmentImage{Width: 320, Height: 240}) {
				return chatMessage{}, fmt.Errorf("delivered direct attachment image metadata lost: %+v", delivered.Attachments)
			}
			return toolPathCall("im_get_message", "read-fwd-msg", map[string]any{
				"conversationId": targetConv,
				"messageId":      forwardMsg1.ID,
			})
		},
		func(req toolPathRequest) (chatMessage, error) {
			obs, err := toolPathLast(req, "read-fwd-msg")
			if err != nil || !obs.OK {
				return chatMessage{}, fmt.Errorf("im_get_message failed: %+v err=%v", obs, err)
			}
			var got Message
			if err := json.Unmarshal(obs.Data, &got); err != nil {
				return chatMessage{}, err
			}
			if !domain.ForwardedEqual(got.Forwarded, forwardMsg1.Forwarded) || !domain.AttachmentsEqual(got.Attachments, forwardMsg1.Attachments) {
				return chatMessage{}, fmt.Errorf("im_get_message lost forwarded/image metadata: %+v", got)
			}
			return toolPathCall("im_list_messages", "list-fwd-msg", map[string]any{
				"conversationId": targetConv,
				"query":          "roadmap",
			})
		},
		func(req toolPathRequest) (chatMessage, error) {
			obs, err := toolPathLast(req, "list-fwd-msg")
			if err != nil || !obs.OK {
				return chatMessage{}, fmt.Errorf("im_list_messages failed: %+v err=%v", obs, err)
			}
			var page toolPathPage
			if err := json.Unmarshal(obs.Data, &page); err != nil || len(page.Messages) != 2 {
				return chatMessage{}, fmt.Errorf("im_list_messages query on Forwarded.Body failed: %+v err=%v", page, err)
			}
			if !domain.ForwardedEqual(page.Messages[1].Forwarded, forwardMsg1.Forwarded) {
				return chatMessage{}, fmt.Errorf("im_list_messages dropped Forwarded metadata: %+v", page.Messages[1])
			}
			return toolPathCall("im_get_attachment", "dl-copied-att", getAttachmentArgs{
				ConversationID: targetConv,
				MessageID:      forwardMsg1.ID,
				AttachmentID:   copiedAtt.ID,
				Path:           "/workspace/architecture.png",
			})
		},
		func(req toolPathRequest) (chatMessage, error) {
			obs, err := toolPathLast(req, "dl-copied-att")
			if err != nil || !obs.OK {
				return chatMessage{}, fmt.Errorf("im_get_attachment on copied forward attachment failed: %+v err=%v", obs, err)
			}
			return chatMessage{Role: "assistant", Content: "PRIVATE_FORWARD_COMPLETE"}, nil
		},
	}
	e.Gateway = gw

	turnLease, err := s.Acquire(ctx, turnAgentID, e.owner)
	if err != nil {
		t.Fatal(err)
	}
	wCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.work(wCtx, &wire.AgentDefinition{Id: turnAgentID, OwnerId: "owner-1", Name: "Agent", Enabled: true, Revision: 1}, turnLease)
		s.Release(ctx, turnLease)
	}()
	waitFor(t, func() bool {
		return count(t, s, `SELECT count(*) FROM runtime_runs WHERE agent_id=$1 AND state IN ('completed','failed')`, turnAgentID) == 1
	})
	cancel()
	<-done

	gw.pathMu.Lock()
	if gw.failure != nil {
		gw.pathMu.Unlock()
		t.Fatal(gw.failure)
	}
	gw.pathMu.Unlock()
	if count(t, s, `SELECT count(*) FROM runtime_runs WHERE agent_id=$1 AND state='completed'`, turnAgentID) != 1 {
		t.Fatal("forward Run did not complete cleanly")
	}

	sandbox.mu.Lock()
	if len(sandbox.requests) != 1 || !bytes.Equal(sandbox.requests[0].Content, copiedBytes) {
		sandbox.mu.Unlock()
		t.Fatalf("copied forward attachment was not imported verbatim via /content: requests=%d", len(sandbox.requests))
	}
	sandbox.mu.Unlock()

	var runID string
	if err := s.DB.QueryRow(ctx, `SELECT id FROM runtime_runs WHERE agent_id=$1`, turnAgentID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	sources, err := s.DB.ResolveRunSources(ctx, turnAgentID, runID)
	if err != nil || len(sources) != 1 {
		t.Fatalf("ResolveRunSources = %+v, err=%v", sources, err)
	}
	src := sources[0]
	if src.ConversationID != targetConv || src.MessageID != forwardMsg1.ID || src.SenderID != forwardMsg1.SenderID {
		t.Fatalf("unexpected Run source provenance: %+v", src)
	}
	if src.ConversationID == forbiddenSourceConv || src.MessageID == forbiddenSourceMsg || src.SenderID == origSenderID {
		t.Fatalf("untrusted forward snapshot leaked into Run provenance: %+v", src)
	}
	if !slices.Contains(src.AttachmentIDs, directAtt.ID) || !slices.Contains(src.AttachmentIDs, copiedAtt.ID) {
		t.Fatalf("Run source missing target/copied attachment IDs: %+v", src.AttachmentIDs)
	}

	// Verify ContextArchive ACL re-verification checks ONLY targetConv and never touches forbiddenSourceConv.
	checkLease, err := s.Acquire(ctx, turnAgentID, "archive-verify")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release(ctx, checkLease)
	archive := &ContextArchive{
		Store: s,
		Lease: checkLease,
		IM:    &IM{BaseURL: server.URL, AgentID: turnAgentID, Identity: e.Identity, ServiceKey: e.Config.IMToken},
	}
	if err := archive.authorizeSourcesWithTombstones(ctx, sources, nil); err != nil {
		t.Fatalf("ContextArchive source verification failed on forwarded message: %v", err)
	}
	if forbiddenHits.Load() != 0 {
		t.Fatalf("Runtime probed untrusted sourceConversationId %d times", forbiddenHits.Load())
	}
}

func TestBroadcastMentionsAllAndHereAddressingRetryAndUnreadDigest(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	agentID := uuid.NewString()
	otherUser := uuid.NewString()
	wsID := uuid.NewString()
	convActive := uuid.NewString()
	convAll := uuid.NewString()
	convHere := uuid.NewString()

	one := 1
	two := 2
	msgAllExcluded := Message{
		ID:             uuid.NewString(),
		ConversationID: convAll,
		SenderID:       otherUser,
		SenderName:     "Alice",
		SenderKind:     "human",
		Body:           "@all standup",
		Mentions:       []MentionSpan{{Kind: "all", Start: 0, End: 4, RecipientIDs: []string{otherUser}}},
		Seq:            1,
		CreatedAt:      "2026-11-07T02:00:00Z",
	}
	msgAllIncluded := Message{
		ID:             uuid.NewString(),
		ConversationID: convAll,
		SenderID:       otherUser,
		SenderName:     "Alice",
		SenderKind:     "human",
		Body:           "@all deploy check",
		Mentions:       []MentionSpan{{Kind: "all", Start: 0, End: 4, RecipientIDs: []string{otherUser, agentID}}},
		Seq:            2,
		CreatedAt:      "2026-11-07T02:01:00Z",
	}
	msgPlainBeforeHere := Message{
		ID:             uuid.NewString(),
		ConversationID: convHere,
		SenderID:       otherUser,
		SenderName:     "Bob",
		SenderKind:     "human",
		Body:           "heads up",
		Seq:            1,
		CreatedAt:      "2026-11-07T02:02:00Z",
	}
	// Include URL-sensitive characters to prove url.QueryEscape is used on ?messageId=.
	msgHereNonActive := Message{
		ID:             "msg/here?space=1&x=2",
		ConversationID: convHere,
		SenderID:       otherUser,
		SenderName:     "Bob",
		SenderKind:     "human",
		Body:           "@here incident",
		Mentions:       []MentionSpan{{Kind: "here", Start: 0, End: 5, RecipientCount: &two}},
		Seq:            2,
		CreatedAt:      "2026-11-07T02:03:00Z",
	}
	msgHereReplyOnly := Message{
		ID:             uuid.NewString(),
		ConversationID: convActive,
		SenderID:       otherUser,
		SenderName:     "Carol",
		SenderKind:     "human",
		Body:           "@here reply-only",
		Mentions:       []MentionSpan{{Kind: "here", Start: 0, End: 5, RecipientCount: &one}},
		Seq:            1,
		CreatedAt:      "2026-11-07T02:04:00Z",
	}
	msgHereAddressed := Message{
		ID:             uuid.NewString(),
		ConversationID: convActive,
		SenderID:       otherUser,
		SenderName:     "Carol",
		SenderKind:     "human",
		Body:           "@here paged",
		Mentions:       []MentionSpan{{Kind: "here", Start: 0, End: 5, RecipientCount: &one}},
		Seq:            2,
		CreatedAt:      "2026-11-07T02:05:00Z",
	}
	msgHereNotFound := Message{
		ID:             uuid.NewString(),
		ConversationID: convActive,
		SenderID:       otherUser,
		SenderName:     "Carol",
		SenderKind:     "human",
		Body:           "@here revoked",
		Mentions:       []MentionSpan{{Kind: "here", Start: 0, End: 5, RecipientCount: &one}},
		Seq:            3,
		CreatedAt:      "2026-11-07T02:06:00Z",
	}
	msgHereAuthBlocked := Message{
		ID:             uuid.NewString(),
		ConversationID: convActive,
		SenderID:       otherUser,
		SenderName:     "Carol",
		SenderKind:     "human",
		Body:           "@here disabled agent",
		Mentions:       []MentionSpan{{Kind: "here", Start: 0, End: 5, RecipientCount: &one}},
		Seq:            4,
		CreatedAt:      "2026-11-07T02:07:00Z",
	}

	var mu sync.Mutex
	var events []map[string]any
	failNextAttention503 := false
	var attentionCalls int
	var rawQueries []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) || r.Header.Get("X-Teamie-Service-Key") != "fixture-im-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/bootstrap":
			_ = json.NewEncoder(w).Encode(Bootstrap{
				Conversations: []Conversation{
					{ID: convActive, WorkspaceID: wsID, Type: "group", Name: "Active"},
					{ID: convAll, WorkspaceID: wsID, Type: "group", Name: "AllRoom"},
					{ID: convHere, WorkspaceID: wsID, Type: "group", Name: "HereRoom"},
				},
				SyncCursor: "0",
			})
		case "/api/v1/sync":
			mu.Lock()
			var cur int
			_, _ = fmt.Sscan(r.URL.Query().Get("cursor"), &cur)
			batch := append([]map[string]any(nil), events[cur:]...)
			next := fmt.Sprint(len(events))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"events": batch, "cursor": next, "hasMore": false})
		case "/api/v1/attention":
			mu.Lock()
			attentionCalls++
			rawQueries = append(rawQueries, r.URL.RawQuery)
			shouldFail := failNextAttention503
			if failNextAttention503 {
				failNextAttention503 = false
			}
			mu.Unlock()
			if shouldFail {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"retry"}}`))
				return
			}
			msgID := r.URL.Query().Get("messageId")
			switch msgID {
			case msgHereNonActive.ID, msgHereAddressed.ID:
				_ = json.NewEncoder(w).Encode(map[string]any{
					"items": []map[string]any{
						{"message": map[string]any{"id": "unrelated-id"}, "reasons": []string{"mention"}},
						{"message": map[string]any{"id": msgID}, "reasons": []string{"reply", "mention"}},
					},
				})
			case msgHereReplyOnly.ID:
				_ = json.NewEncoder(w).Encode(map[string]any{
					"items": []map[string]any{
						{"message": map[string]any{"id": msgID}, "reasons": []string{"reply"}},
					},
				})
			case msgHereNotFound.ID:
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"gone"}}`))
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	e := &Engine{
		Store:    s,
		Config:   Config{IMURL: server.URL, IMToken: "fixture-im-secret"},
		Identity: &fixtureIdentity{},
		owner:    uuid.NewString(),
	}
	lease, err := s.Acquire(ctx, agentID, e.owner)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release(ctx, lease)
	imc := &IM{BaseURL: server.URL, AgentID: agentID, Identity: e.Identity, ServiceKey: e.Config.IMToken}
	t0 := time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC)
	presence := newConversationPresenceState(time.Minute, nil)
	presence.Enter(wsID, convActive, "group")

	// 1. @all in non-active conversation: excluded vs included recipientIds.
	mu.Lock()
	events = append(events,
		map[string]any{"id": "1", "type": "message.created", "workspaceId": wsID, "conversationId": convAll, "message": msgAllExcluded},
		map[string]any{"id": "2", "type": "message.created", "workspaceId": wsID, "conversationId": convAll, "message": msgAllIncluded},
	)
	mu.Unlock()
	if _, err := e.ingestWithPresenceAndAuth(ctx, imc, lease, presence, nil); err != nil {
		t.Fatal(err)
	}
	allSummary := presence.EnrichConversation(Conversation{ID: convAll, WorkspaceID: wsID})
	if allSummary.UnreadCount != 2 || allSummary.MentionCount != 1 {
		t.Fatalf("@all unread tracking: got unread=%d mention=%d, want unread=2 mention=1", allSummary.UnreadCount, allSummary.MentionCount)
	}

	// 2. @here in non-active conversation with an earlier plain message in the same sync page:
	// First attempt returns 503 -> cursor must stay at "2" AND presence must NOT record msgPlainBeforeHere yet!
	mu.Lock()
	failNextAttention503 = true
	events = append(events,
		map[string]any{"id": "3", "type": "message.created", "workspaceId": wsID, "conversationId": convHere, "message": msgPlainBeforeHere},
		map[string]any{"id": "4", "type": "message.created", "workspaceId": wsID, "conversationId": convHere, "message": msgHereNonActive},
	)
	mu.Unlock()
	if _, err := e.ingestWithPresenceAndAuth(ctx, imc, lease, presence, nil); err == nil {
		t.Fatal("expected @here 503 lookup failure to abort ingestion before cursor advance")
	}
	if cur, err := s.Cursor(ctx, lease); err != nil || cur != "2" {
		t.Fatalf("cursor advanced despite @here lookup failure: cur=%s err=%v", cur, err)
	}
	if hereBeforeRetry := presence.EnrichConversation(Conversation{ID: convHere, WorkspaceID: wsID}); hereBeforeRetry.UnreadCount != 0 || hereBeforeRetry.MentionCount != 0 {
		t.Fatalf("failed @here batch mutated in-memory unread counts before retry: %+v", hereBeforeRetry)
	}

	// Retry succeeds -> cursor advances to "4", unread=2 (no double count!), mention=1, URL query properly escaped.
	if _, err := e.ingestWithPresenceAndAuth(ctx, imc, lease, presence, nil); err != nil {
		t.Fatalf("@here retry failed: %v", err)
	}
	if cur, err := s.Cursor(ctx, lease); err != nil || cur != "4" {
		t.Fatalf("cursor after @here retry = %s, want 4 (err=%v)", cur, err)
	}
	hereSummary := presence.EnrichConversation(Conversation{ID: convHere, WorkspaceID: wsID})
	if hereSummary.UnreadCount != 2 || hereSummary.MentionCount != 1 {
		t.Fatalf("@here unread tracking after retry: got unread=%d mention=%d, want unread=2 mention=1", hereSummary.UnreadCount, hereSummary.MentionCount)
	}
	mu.Lock()
	gotRawQuery := rawQueries[0]
	mu.Unlock()
	if gotRawQuery != "messageId=msg%2Fhere%3Fspace%3D1%26x%3D2" {
		t.Fatalf("attention query not URL-encoded: got %q", gotRawQuery)
	}

	// Flush unread digest and verify MentionCount == 1 for both @all and @here rooms.
	flushed, err := presence.FlushDueDigest(ctx, s, lease, time.Now().UTC().Add(2*time.Minute))
	if err != nil || !flushed {
		t.Fatalf("FlushDueDigest: flushed=%v err=%v", flushed, err)
	}
	pendingDigest, err := s.Pending(ctx, lease)
	if err != nil || len(pendingDigest) != 1 || pendingDigest[0].Kind != "im.unread_digest" {
		t.Fatalf("expected 1 pending unread digest, got %+v err=%v", pendingDigest, err)
	}
	var digestBody UnreadDigestBody
	if err := json.Unmarshal([]byte(pendingDigest[0].Body), &digestBody); err != nil || len(digestBody.UnreadConversations) != 2 {
		t.Fatalf("invalid UnreadDigestBody: %+v err=%v", digestBody, err)
	}
	for _, item := range digestBody.UnreadConversations {
		if item.UnreadCount != 2 || item.MentionCount != 1 {
			t.Fatalf("digest item %s wrong counts: %+v", item.ConversationID, item)
		}
	}
	_ = t0

	// 3. Active conversation @here: reply-only (false), mention (true), 404 access loss (false, advances cursor).
	mu.Lock()
	events = append(events,
		map[string]any{"id": "5", "type": "message.created", "workspaceId": wsID, "conversationId": convActive, "message": msgHereReplyOnly},
		map[string]any{"id": "6", "type": "message.created", "workspaceId": wsID, "conversationId": convActive, "message": msgHereAddressed},
		map[string]any{"id": "7", "type": "message.created", "workspaceId": wsID, "conversationId": convActive, "message": msgHereNotFound},
	)
	mu.Unlock()
	committed, err := e.ingestWithPresenceAndAuth(ctx, imc, lease, presence, func(context.Context) error { return nil })
	if err != nil || len(committed) != 3 {
		t.Fatalf("active @here ingest: committed=%d err=%v", len(committed), err)
	}
	if committed[0].BroadcastAddressed || committed[0].AddressedTo(agentID) {
		t.Fatalf("reply-only attention item falsely marked BroadcastAddressed: %+v", committed[0])
	}
	if !committed[1].BroadcastAddressed || !committed[1].AddressedTo(agentID) {
		t.Fatalf("mention attention item did not mark BroadcastAddressed: %+v", committed[1])
	}
	if committed[2].BroadcastAddressed || committed[2].AddressedTo(agentID) {
		t.Fatalf("404 attention lookup falsely marked BroadcastAddressed: %+v", committed[2])
	}
	raw0, _ := json.Marshal(committed[0])
	raw1, _ := json.Marshal(committed[1])
	if bytes.Contains(raw0, []byte("broadcastAddressed")) || !bytes.Contains(raw1, []byte(`"broadcastAddressed":true`)) {
		t.Fatalf("unexpected broadcastAddressed JSON serialization: raw0=%s raw1=%s", raw0, raw1)
	}

	// 4. Worker authorize failure aborts BEFORE calling /api/v1/attention or advancing cursor.
	mu.Lock()
	callsBeforeAuthFail := attentionCalls
	events = append(events,
		map[string]any{"id": "8", "type": "message.created", "workspaceId": wsID, "conversationId": convActive, "message": msgHereAuthBlocked},
	)
	mu.Unlock()
	authErr := errors.New("principal_disabled")
	if _, err := e.ingestWithPresenceAndAuth(ctx, imc, lease, presence, func(context.Context) error { return authErr }); !errors.Is(err, authErr) {
		t.Fatalf("expected authorize failure %v, got %v", authErr, err)
	}
	mu.Lock()
	callsAfterAuthFail := attentionCalls
	mu.Unlock()
	if callsAfterAuthFail != callsBeforeAuthFail {
		t.Fatalf("unauthorized worker still called /api/v1/attention: before=%d after=%d", callsBeforeAuthFail, callsAfterAuthFail)
	}
	if cur, err := s.Cursor(ctx, lease); err != nil || cur != "7" {
		t.Fatalf("cursor advanced despite authorize failure: cur=%s err=%v", cur, err)
	}
}

func TestExistingRunsKeepByteExactPromptsToolManifestsAndRequestHashes(t *testing.T) {
	ctx := context.Background()
	s, lease, run := contextModelRun(t)
	ctx = WithRun(ctx, run)

	// 1.Runtime profile, version, and schema invariants remain untouched.
	if domain.NativeRuntimeVersion != "eino-v0.10.0-alpha.35/teamie-v4-agentic" || domain.CurrentToolProfile != "teamie-tools-v035" {
		t.Fatalf("runtime version or tool profile changed: %s / %s", domain.NativeRuntimeVersion, domain.CurrentToolProfile)
	}
	var maxMigration int
	var writerSchema string
	if err := s.DB.QueryRow(ctx, `SELECT max(version), current_setting('teamie.runtime_writer_schema') FROM agent_runtime_schema_migrations`).Scan(&maxMigration, &writerSchema); err != nil || maxMigration != 16 || writerSchema != "16" {
		t.Fatalf("Runtime schema version changed: got %d / %s, want 16 / 16 (err=%v)", maxMigration, writerSchema, err)
	}

	// 2. Ordinary Input with text, user @mention, and non-image attachment produces byte-exact legacy JSON.
	const (
		wsID   = "11111111-1111-4111-8111-111111111111"
		convID = "22222222-2222-4222-8222-222222222222"
		msgID  = "33333333-3333-4333-8333-333333333333"
		sendID = "44444444-4444-4444-8444-444444444444"
		agID   = "55555555-5555-4555-8555-555555555555"
		attID  = "66666666-6666-4666-8666-666666666666"
	)
	ordinary := Input{
		EventID:        "9",
		WorkspaceID:    wsID,
		ConversationID: convID,
		MessageID:      msgID,
		SenderID:       sendID,
		SenderName:     "Alice",
		SenderKind:     "human",
		Body:           "@Agent hello",
		Attachments: []Attachment{{
			ID:        attID,
			Filename:  "spec.txt",
			MediaType: "text/plain",
			SizeBytes: 12,
			SHA256:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
		Mentions:  []MentionSpan{{UserID: agID, Start: 0, End: 6}},
		Seq:       9,
		CreatedAt: "2026-11-07T00:00:00Z",
	}
	rawOrdinary, err := json.Marshal(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	wantOrdinary := `{"eventId":"9","workspaceId":"11111111-1111-4111-8111-111111111111","conversationId":"22222222-2222-4222-8222-222222222222","messageId":"33333333-3333-4333-8333-333333333333","senderId":"44444444-4444-4444-8444-444444444444","senderName":"Alice","senderKind":"human","body":"@Agent hello","attachments":[{"id":"66666666-6666-4666-8666-666666666666","filename":"spec.txt","mediaType":"text/plain","sizeBytes":12,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],"mentions":[{"userId":"55555555-5555-4555-8555-555555555555","start":0,"end":6}],"seq":9,"createdAt":"2026-11-07T00:00:00Z"}`
	if string(rawOrdinary) != wantOrdinary {
		t.Fatalf("ordinary Input JSON changed:\ngot:  %s\nwant: %s", rawOrdinary, wantOrdinary)
	}
	for _, forbidden := range []string{"forwarded", "broadcastAddressed", "image", `"kind"`, "recipientIds", "recipientCount"} {
		if bytes.Contains(rawOrdinary, []byte(forbidden)) {
			t.Fatalf("ordinary Input leaked new collaboration key %s: %s", forbidden, rawOrdinary)
		}
	}

	// 3. Tool manifests expose zero new tools and unchanged parameter schemas.
	tb := &Tools{Store: s, Lease: lease}
	imWithMentionsAndFiles, err := tb.BuildIMWithMentions(true)
	if err != nil {
		t.Fatal(err)
	}
	dmTools, err := tb.BuildDirectMessageTools()
	if err != nil {
		t.Fatal(err)
	}
	presenceTools, err := tb.BuildPresenceTools()
	if err != nil {
		t.Fatal(err)
	}
	allTools := append(append([]tool.BaseTool{}, imWithMentionsAndFiles...), dmTools...)
	allTools = append(allTools, presenceTools...)
	var toolInfos []*schema.ToolInfo
	var gotNames []string
	for _, item := range allTools {
		info, err := item.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		toolInfos = append(toolInfos, info)
		gotNames = append(gotNames, info.Name)
	}
	wantNames := []string{
		"im_list_conversations",
		"im_list_messages",
		"im_get_message",
		"im_send_message",
		"im_get_attachment",
		"im_upload_file",
		"im_list_workspace_members",
		"im_open_direct_conversation",
		"im_create_group_conversation",
		"im_add_conversation_members",
		"im_retract_message",
		"im_enter_conversation",
		"im_leave_conversation",
	}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("IM tool manifest changed: got %v, want %v", gotNames, wantNames)
	}
	for _, info := range toolInfos {
		js, err := info.ParamsOneOf.ToJSONSchema()
		if err != nil {
			t.Fatal(err)
		}
		rawSchema, _ := json.Marshal(js)
		if bytes.Contains(rawSchema, []byte("forwarded")) || bytes.Contains(rawSchema, []byte("recipientIds")) || bytes.Contains(rawSchema, []byte("recipientCount")) || bytes.Contains(rawSchema, []byte("broadcastAddressed")) {
			t.Fatalf("tool %s schema gained collaboration fields: %s", info.Name, rawSchema)
		}
	}

	// 4. Model request serialization + hash for an existing Run hit the cached receipt byte-for-byte.
	budget, err := ParseContextBudgetSnapshot(run.ContextBudget)
	if err != nil {
		t.Fatal(err)
	}
	sysText := contextInstruction(&wire.AgentDefinition{Id: agID, Name: "Agent", Description: "Helper"}, budget) + fileDeliveryGuidance + mentionGuidance
	msgs := append([]*schema.AgenticMessage{
		schema.SystemAgenticMessage(sysText),
	}, inputMessages([]Input{ordinary})...)
	opts := &model.Options{Tools: toolInfos}
	req1, err := agenticRequest(msgs, opts, false)
	if err != nil {
		t.Fatal(err)
	}
	// Construct the exact pre-collaboration wire message using wantOrdinary directly.
	legacyMsgs := []*schema.AgenticMessage{
		msgs[0],
		schema.UserAgenticMessage("TEAMIE_NEW_MESSAGE " + wantOrdinary),
	}
	reqLegacy, err := agenticRequest(legacyMsgs, opts, false)
	if err != nil {
		t.Fatal(err)
	}
	if string(req1) != string(reqLegacy) || modelRequestHash(req1) != modelRequestHash(reqLegacy) {
		t.Fatalf("model request bytes/hash drifted from pre-collaboration wire format:\ngot:  %s\nwant: %s", req1, reqLegacy)
	}

	rawResp, err := (&schema.HumanReadableSerializer{}).Marshal(assistantText("CACHED_DETERMINISTIC_OK"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveModel(ctx, lease, run, 1, modelRequestHash(reqLegacy), rawResp); err != nil {
		t.Fatal(err)
	}
	gwModel := &GatewayModel{Store: s, Lease: lease} // nil Gateway client proves replay comes strictly from exact hash cache!
	replayed, err := gwModel.Generate(ctx, msgs, model.WithTools(toolInfos))
	if err != nil || agenticText(replayed) != "CACHED_DETERMINISTIC_OK" {
		t.Fatalf("existing Run model receipt cache missed or conflicted: msg=%v err=%v", replayed, err)
	}
}

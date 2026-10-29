package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"teamie/pkg/httpx"
	"teamie/pkg/postgres/testdb"
	"teamie/services/chat/internal/domain"
	"testing"
)

func setup(t *testing.T) (*Store, Workspace, Conversation, httpx.User, httpx.User) {
	t.Helper()
	ctx := context.Background()
	s, e := Open(ctx, testdb.URL(t), httpx.Flags{})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.DB.Close)
	owner := httpx.User{ID: httpx.ID(), DisplayName: "Owner", Kind: "human"}
	member := httpx.User{ID: httpx.ID(), DisplayName: "Member", Kind: "human"}
	ws, c, e := s.CreateWorkspace(ctx, owner.ID, "Test workspace")
	if e != nil {
		t.Fatal(e)
	}
	code, _, e := s.CreateInvite(ctx, owner.ID, ws.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.AcceptInvite(ctx, member.ID, code); e != nil {
		t.Fatal(e)
	}
	return s, ws, c, owner, member
}
func TestAtomicWorkspaceAuthorizationAndMessageLifecycle(t *testing.T) {
	ctx := context.Background()
	s, ws, c, owner, member := setup(t)
	members, e := s.Members(ctx, owner.ID, ws.ID)
	if e != nil || len(members) != 2 {
		t.Fatal("admission failed", e)
	}
	if _, e = s.CreateConversation(ctx, member.ID, ws.ID, "channel", "forbidden", nil); e == nil {
		t.Fatal("member created channel")
	}
	if _, _, e = s.Messages(ctx, httpx.ID(), c.ID, 0, 50); e == nil {
		t.Fatal("outsider read channel")
	}
	dm, e := s.CreateConversation(ctx, owner.ID, ws.ID, "direct", "", []string{member.ID})
	if e != nil {
		t.Fatal(e)
	}
	reverse, e := s.CreateConversation(ctx, member.ID, ws.ID, "direct", "", []string{owner.ID})
	if e != nil || dm.ID != reverse.ID {
		t.Fatal("direct pair not unique", e)
	}
	m, first, e := s.Send(ctx, owner, c.ID, "retry-key", "Original", nil)
	if e != nil || !first || m.Seq != 1 {
		t.Fatal("send failed", e)
	}
	retry, first, e := s.Send(ctx, owner, c.ID, "retry-key", "Original", nil)
	if e != nil || first || m.ID != retry.ID {
		t.Fatal("retry not idempotent", e)
	}
	if _, _, e = s.Send(ctx, owner, c.ID, "retry-key", "Conflict", nil); e == nil {
		t.Fatal("conflicting body accepted")
	}
	if _, _, e = s.Send(ctx, owner, dm.ID, "retry-key", "Original", nil); e == nil {
		t.Fatal("conflicting conversation accepted")
	}
	if _, e = s.ChangeMessage(ctx, member.ID, c.ID, m.ID, "Forbidden", false); e == nil {
		t.Fatal("non-author edited message")
	}
	if _, e = s.ChangeMessage(ctx, owner.ID, c.ID, m.ID, "Edited", false); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ChangeMessage(ctx, owner.ID, c.ID, m.ID, "", true); e != nil {
		t.Fatal(e)
	}
	history, _, e := s.Messages(ctx, member.ID, c.ID, 0, 50)
	if e != nil || len(history) != 1 || history[0].Body != "" || history[0].RetractedAt == nil {
		t.Fatal("tombstone not durable", e)
	}
	events, cursor, more, e := s.Sync(ctx, member.ID, "0", 100)
	if e != nil || more {
		t.Fatal("sync", e)
	}
	for _, ev := range events {
		if ev.Message != nil && ev.Message.ID == m.ID && ev.Message.Body != "" {
			t.Fatal("historical sync resurrected retracted content")
		}
	}
	pending, _, e := s.Send(ctx, owner, c.ID, "pending", "Protected payload", nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.RemoveMember(ctx, owner.ID, ws.ID, member.ID); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.Messages(ctx, member.ID, c.ID, 0, 50); e == nil {
		t.Fatal("removed member retained history")
	}
	if _, _, e = s.Send(ctx, member, c.ID, "forbidden", "Denied", nil); e == nil {
		t.Fatal("removed member sent")
	}
	events, next, more, e := s.Sync(ctx, member.ID, cursor, 100)
	if e != nil || more || next == cursor {
		t.Fatal("filtered stream did not advance", e)
	}
	for _, ev := range events {
		if ev.Message != nil && ev.Message.ID == pending.ID {
			t.Fatal("queued protected message leaked after revocation")
		}
	}
	if _, _, _, e = s.Sync(ctx, member.ID, "999999", 50); e == nil {
		t.Fatal("future cursor accepted")
	}
	if e = s.RemoveMember(ctx, owner.ID, ws.ID, owner.ID); e == nil {
		t.Fatal("owner removed themselves")
	}
}
func TestConcurrentOrderingIdempotenceAndReconnect(t *testing.T) {
	ctx := context.Background()
	s, _, c, owner, member := setup(t)
	const count = 16
	var wg sync.WaitGroup
	errs := make(chan error, count)
	seqs := make(chan int64, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m, _, e := s.Send(ctx, owner, c.ID, fmt.Sprintf("m-%d", i), "Concurrent", nil)
			if e != nil {
				errs <- e
				return
			}
			seqs <- m.Seq
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	close(seqs)
	seen := map[int64]bool{}
	for seq := range seqs {
		seen[seq] = true
	}
	for seq := int64(1); seq <= count; seq++ {
		if !seen[seq] {
			t.Fatalf("gap at seq %d", seq)
		}
	}
	// Concurrent retries can commit only one message and one durable event.
	ids := make(chan string, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, _, e := s.Send(ctx, owner, c.ID, "shared-retry", "Exactly once", nil)
			if e != nil {
				t.Error(e)
				return
			}
			ids <- m.ID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("duplicate retry created a second message")
		}
	}
	history, more, e := s.Messages(ctx, member.ID, c.ID, 0, 5)
	if e != nil || !more || len(history) != 5 {
		t.Fatal("pagination", e)
	}
	for i := 1; i < len(history); i++ {
		if history[i-1].Seq >= history[i].Seq {
			t.Fatal("history out of order")
		}
	}
	read, e := s.Read(ctx, member.ID, c.ID, 1000)
	if e != nil || read != count+1 {
		t.Fatal("read not bounded", e)
	}
	old, e := s.Read(ctx, member.ID, c.ID, 1)
	if e != nil || old != read {
		t.Fatal("read regressed", e)
	}
	cursor := "0"
	last := int64(0)
	messageEvents := 0
	for i := 0; i < 100; i++ {
		events, next, more, e := s.Sync(ctx, member.ID, cursor, 3)
		if e != nil {
			t.Fatal(e)
		}
		for _, ev := range events {
			n, _ := strconv.ParseInt(ev.ID, 10, 64)
			if n <= last {
				t.Fatal("durable event order regressed")
			}
			last = n
			if ev.Type == "message.created" {
				messageEvents++
			}
		}
		cursor = next
		if !more {
			break
		}
	}
	if messageEvents != count+1 {
		t.Fatalf("reconnect lost/duplicated events: %d", messageEvents)
	}
	events, _, _, e := s.Sync(ctx, member.ID, cursor, 100)
	if e != nil || len(events) != 0 {
		t.Fatal("applied cursor repeated payloads", e)
	}
}
func TestWriteFlagsDoNotHidePersistedContent(t *testing.T) {
	ctx := context.Background()
	s, _, c, owner, member := setup(t)
	m, _, e := s.Send(ctx, owner, c.ID, "one", "Persisted", nil)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "flags.json")
	payload, _ := json.Marshal(httpx.Snapshot{Revision: "disabled", Flags: map[string]bool{}})
	if e = os.WriteFile(path, payload, 0600); e != nil {
		t.Fatal(e)
	}
	s.Flags = httpx.Flags{Path: path}
	if _, e = s.ChangeMessage(ctx, owner.ID, c.ID, m.ID, "blocked", false); e == nil {
		t.Fatal("disabled edit accepted")
	}
	history, _, e := s.Messages(ctx, member.ID, c.ID, 0, 50)
	if e != nil || len(history) != 1 || history[0].Body != "Persisted" {
		t.Fatal("flag hid persisted content", e)
	}
}

func TestWorkspaceMemberCapAndBatchedChannelFanout(t *testing.T) {
	ctx := context.Background()
	s, ws, channel, owner, member := setup(t)
	code, _, err := s.CreateInvite(ctx, owner.ID, ws.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Fill workspace to MaxWorkspaceMembers - 1 (499 members: owner + member + 497 extra).
	extra := make([]string, 0, domain.MaxWorkspaceMembers-3)
	for len(extra) < domain.MaxWorkspaceMembers-3 {
		extra = append(extra, httpx.ID())
	}
	if _, err = s.DB.Exec(ctx, `INSERT INTO workspace_members(workspace_id,user_id,role) SELECT $1,unnest($2::text[]),'member'`, ws.ID, extra); err != nil {
		t.Fatal(err)
	}

	// Two concurrent invite acceptances race for the 500th slot: exactly 1 succeeds and 1 fails with 400 invalid_members.
	candidates := []string{httpx.ID(), httpx.ID()}
	start := make(chan struct{})
	results := make(chan error, len(candidates))
	var wg sync.WaitGroup
	for _, cid := range candidates {
		wg.Add(1)
		go func(uid string) {
			defer wg.Done()
			<-start
			_, _, acceptErr := s.AcceptInvite(ctx, uid, code)
			results <- acceptErr
		}(cid)
	}
	close(start)
	wg.Wait()
	close(results)

	succeeded := 0
	for resErr := range results {
		if resErr == nil {
			succeeded++
		} else {
			var he *httpx.Error
			if !errors.As(resErr, &he) || he.Status != 400 || he.Code != "invalid_members" {
				t.Fatalf("expected 400 invalid_members at workspace capacity, got %v", resErr)
			}
		}
	}
	if succeeded != 1 {
		t.Fatalf("expected 1 candidate to claim the 500th workspace slot, got %d", succeeded)
	}

	// Existing member re-accepting the invite at full capacity remains idempotent.
	if _, _, err = s.AcceptInvite(ctx, member.ID, code); err != nil {
		t.Fatalf("existing member idempotent invite acceptance failed at capacity: %v", err)
	}

	// Delegating an Agent into a full workspace is also rejected with 400 invalid_members.
	agent := httpx.User{ID: httpx.ID(), DisplayName: "Agent", Kind: "agent"}
	_, err = s.SetPrincipalWorkspaces(ctx, agent, owner, []string{ws.ID}, 1)
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != 400 || he.Code != "invalid_members" {
		t.Fatalf("expected 400 invalid_members for agent delegation into full workspace, got %v", err)
	}

	// Sending a message to the 500-member channel fans out to all 500 members atomically.
	msg, first, err := s.Send(ctx, owner, channel.ID, "fanout-500", "Hello 500 members", nil)
	if err != nil || !first {
		t.Fatalf("channel send to 500 members failed: %v", err)
	}
	var eventCount int
	if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM sync_events WHERE conversation_id=$1 AND payload#>>'{message,id}'=$2`, channel.ID, msg.ID).Scan(&eventCount); err != nil || eventCount != domain.MaxWorkspaceMembers {
		t.Fatalf("expected %d sync_events for 500-member channel send, got %d (%v)", domain.MaxWorkspaceMembers, eventCount, err)
	}
}

func TestSyncLimitMatchesContractCap(t *testing.T) {
	ctx := context.Background()
	s, _, c, owner, member := setup(t)
	if _, _, err := s.Send(ctx, owner, c.ID, "sync-cap-1", "Message 1", nil); err != nil {
		t.Fatal(err)
	}

	for _, invalidLimit := range []int{-1, 0, 101, 500} {
		_, _, _, err := s.Sync(ctx, member.ID, "0", invalidLimit)
		var he *httpx.Error
		if !errors.As(err, &he) || he.Status != 400 || he.Code != "invalid_query" {
			t.Fatalf("s.Sync limit=%d: expected 400 invalid_query, got %v", invalidLimit, err)
		}
	}

	token := strings.Repeat("m", 64)
	id := &fakeIdentity{users: map[string]httpx.User{token: member}}
	routes := (&Handler{Store: s, Identity: id, Origin: "https://teamie.example"}).Routes()

	callSync := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/sync"+query, nil)
		req.AddCookie(&http.Cookie{Name: httpx.CookieName, Value: token})
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, req)
		return w
	}

	for _, okQuery := range []string{"?cursor=0", "?cursor=0&limit=1", "?cursor=0&limit=100"} {
		if w := callSync(okQuery); w.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/sync%s: got %d %s, want 200", okQuery, w.Code, w.Body.String())
		}
	}
	for _, badQuery := range []string{"?cursor=0&limit=0", "?cursor=0&limit=-1", "?cursor=0&limit=101", "?cursor=0&limit=500", "?cursor=0&limit=abc"} {
		w := callSync(badQuery)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"invalid_query"`) {
			t.Fatalf("GET /api/v1/sync%s: got %d %s, want 400 invalid_query", badQuery, w.Code, w.Body.String())
		}
	}
}

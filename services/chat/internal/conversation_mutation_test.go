package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"teamie/pkg/httpx"
	"teamie/services/chat/internal/domain"
)

func TestConversationMutationHTTPHeaderAndCallerScope(t *testing.T) {
	s, ws, _, owner, member := setup(t)
	identity := &attachmentIdentity{httpx.Session{User: owner, CSRFToken: "csrf"}}
	handler := (&Handler{Store: s, Identity: identity, Origin: "http://localhost"}).Routes()
	post := func(key string) (int, Conversation) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"type": "group", "memberIds": []string{owner.ID, member.ID}})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+ws.ID+"/conversations", strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", "csrf")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var out struct {
			Conversation Conversation `json:"conversation"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Conversation
	}
	status, first := post("same-key")
	if status != 201 || first.ID == "" {
		t.Fatalf("create: %d %#v", status, first)
	}
	status, second := post("same-key")
	if status != 201 || second.ID != first.ID {
		t.Fatalf("header retry created another group: %d %#v", status, second)
	}
	identity.session.User = member
	status, other := post("same-key")
	if status != 201 || other.ID == first.ID || other.ID == "" {
		t.Fatalf("caller-scoped key: %d %#v", status, other)
	}
	status, _ = post("invalid/key")
	if status != 400 {
		t.Fatalf("invalid key status: %d", status)
	}
}

func TestConversationMutationConcurrentRetryAndRevocation(t *testing.T) {
	ctx := context.Background()
	s, ws, _, owner, member := setup(t)
	ids := make(chan string, 12)
	errs := make(chan error, 12)
	var workers sync.WaitGroup
	for range 12 {
		workers.Go(func() {
			c, err := s.App(nil, "").CreateConversationWithKey(ctx, member.ID, ws.ID, "group", "Shared", []string{owner.ID}, "once")
			ids <- c.ID
			errs <- err
		})
	}
	workers.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var id string
	for got := range ids {
		if id != "" && id != got {
			t.Fatal("duplicate conversation")
		}
		id = got
	}
	var groups, events, receipts int
	if err := s.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM conversations WHERE type='group'),(SELECT count(*) FROM sync_events WHERE payload->>'conversationId'=$1 AND payload->>'type'='conversation.created'),(SELECT count(*) FROM conversation_mutations)`, id).Scan(&groups, &events, &receipts); err != nil {
		t.Fatal(err)
	}
	if groups != 1 || events != 2 || receipts != 1 {
		t.Fatalf("side effects repeated: groups=%d events=%d receipts=%d", groups, events, receipts)
	}
	// A new application instance recovers the durable receipt and current ACL.
	got, err := s.App(nil, "").CreateConversationWithKey(ctx, member.ID, ws.ID, "group", " Shared ", []string{owner.ID, member.ID, owner.ID}, "once")
	if err != nil || got.ID != id {
		t.Fatal("canonical retry", got, err)
	}
	_, err = s.App(nil, "").CreateConversationWithKey(ctx, member.ID, ws.ID, "group", "Different", []string{owner.ID}, "once")
	var conflict *domain.Error
	if !errors.As(err, &conflict) || conflict.Status != 409 || conflict.Code != "idempotency_conflict" {
		t.Fatal("request-key conflict not rejected", err)
	}
	if _, err = s.GroupMember(ctx, owner.ID, id, member.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.App(nil, "").CreateConversationWithKey(ctx, member.ID, ws.ID, "group", "Shared", []string{owner.ID}, "once"); err == nil {
		t.Fatal("removed creator replay disclosed or restored group")
	}
	if _, err = s.App(nil, "").ConversationAccess(ctx, id, member.ID); err == nil {
		t.Fatal("replay restored removed participant")
	}
}

func TestConversationMutationReceiptAndSideEffectAreAtomic(t *testing.T) {
	ctx := context.Background()
	s, ws, _, owner, member := setup(t)
	_, err := s.DB.Exec(ctx, `CREATE FUNCTION fail_mutation_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected receipt failure'; END; $$; CREATE TRIGGER fail_mutation_receipt BEFORE INSERT ON conversation_mutations FOR EACH ROW EXECUTE FUNCTION fail_mutation_receipt()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.App(nil, "").CreateConversationWithKey(ctx, owner.ID, ws.ID, "group", "Atomic", []string{member.ID}, "retry-after-rollback"); err == nil {
		t.Fatal("receipt failure not injected")
	}
	var groups, receipts int
	if err = s.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM conversations WHERE type='group'),(SELECT count(*) FROM conversation_mutations)`).Scan(&groups, &receipts); err != nil {
		t.Fatal(err)
	}
	if groups != 0 || receipts != 0 {
		t.Fatal("partial transaction committed", groups, receipts)
	}
	if _, err = s.DB.Exec(ctx, `DROP TRIGGER fail_mutation_receipt ON conversation_mutations`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.App(nil, "").CreateConversationWithKey(ctx, owner.ID, ws.ID, "group", "Atomic", []string{member.ID}, "retry-after-rollback"); err != nil {
		t.Fatal("failed request consumed key", err)
	}
}

func TestMemberMutationRetryDoesNotUndoModeratorRemoval(t *testing.T) {
	ctx := context.Background()
	s, ws, _, owner, member := setup(t)
	agent := httpx.User{ID: httpx.ID(), DisplayName: "Agent", Kind: "agent"}
	if _, err := s.SetPrincipalWorkspaces(ctx, agent, member, []string{ws.ID}, 1); err != nil {
		t.Fatal(err)
	}
	group, err := s.CreateConversation(ctx, member.ID, ws.ID, "group", "Group", []string{owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.App(nil, "").AddConversationMemberWithKey(ctx, member.ID, group.ID, agent.ID, "add-once"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GroupMember(ctx, owner.ID, group.ID, agent.ID, false); err != nil {
		t.Fatal(err)
	}
	before, err := s.Head(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.App(nil, "").AddConversationMemberWithKey(ctx, member.ID, group.ID, agent.ID, "add-once")
	if err != nil || slices.Contains(got.MemberIDs, agent.ID) {
		t.Fatal("retry restored moderator-removed member", got, err)
	}
	after, err := s.Head(ctx, owner.ID)
	if err != nil || before != after {
		t.Fatal("retry emitted new sync event", before, after, err)
	}
	if _, err = s.GroupMember(ctx, owner.ID, group.ID, member.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.App(nil, "").AddConversationMemberWithKey(ctx, member.ID, group.ID, agent.ID, "add-once"); err == nil {
		t.Fatal("removed creator replayed management receipt")
	}
	// A distinct, explicit operation can legitimately admit the member again.
	if _, err = s.App(nil, "").AddConversationMemberWithKey(ctx, owner.ID, group.ID, agent.ID, "new-admission"); err != nil {
		t.Fatal(err)
	}
}

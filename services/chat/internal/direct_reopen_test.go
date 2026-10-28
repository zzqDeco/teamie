package chat

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

func TestDirectReopenRestoresDurableAudienceOnce(t *testing.T) {
	ctx := context.Background()
	s, ws, _, owner, member := setup(t)
	direct, err := s.CreateConversation(ctx, owner.ID, ws.ID, "direct", "", []string{member.ID})
	if err != nil {
		t.Fatal(err)
	}
	message, _, err := s.Send(ctx, owner, direct.ID, "before-removal", "Preserved direct history", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveMember(ctx, owner.ID, ws.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Message(ctx, member.ID, direct.ID, message.ID); err == nil {
		t.Fatal("removed participant retained direct history")
	}
	code, _, err := s.CreateInvite(ctx, owner.ID, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AcceptInvite(ctx, member.ID, code); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Message(ctx, member.ID, direct.ID, message.ID); err == nil {
		t.Fatal("workspace admission implicitly restored old direct membership")
	}
	cursors := map[string]string{}
	for _, id := range []string{owner.ID, member.ID} {
		_, cursor, more, err := s.Sync(ctx, id, "0", 100)
		if err != nil || more {
			t.Fatal(err)
		}
		cursors[id] = cursor
	}
	var beforeHints int
	if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM outbox").Scan(&beforeHints); err != nil {
		t.Fatal(err)
	}
	// Reopening from both endpoints, including concurrent retries, must restore
	// the pair once and publish exactly one complete event per participant.
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			actor, target := owner.ID, member.ID
			if i%2 != 0 {
				actor, target = target, actor
			}
			got, err := s.CreateConversation(ctx, actor, ws.ID, "direct", "", []string{target})
			if err != nil || got.ID != direct.ID {
				t.Errorf("direct reopen created another pair: %s %v", got.ID, err)
			}
		}(i)
	}
	wg.Wait()
	for _, id := range []string{owner.ID, member.ID} {
		events, cursor, more, err := s.Sync(ctx, id, cursors[id], 100)
		if err != nil || more || len(events) != 1 || events[0].Type != "conversation.created" || events[0].ConversationID != direct.ID || events[0].Conversation == nil || len(events[0].Conversation.MemberIDs) != 2 || events[0].Conversation.LastSeq != message.Seq {
			t.Fatalf("reconnect missed or duplicated restored direct chat for %s: %#v %v", id, events, err)
		}
		again, next, more, err := s.Sync(ctx, id, cursor, 100)
		if err != nil || more || len(again) != 0 || next != cursor {
			t.Fatal("accepted cursor replayed a restoration", err)
		}
	}
	var afterHints int
	if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM outbox").Scan(&afterHints); err != nil || afterHints != beforeHints+1 {
		t.Fatal("concurrent restoration omitted or duplicated its outbox hint", afterHints, beforeHints, err)
	}
	var payload []byte
	if err := s.DB.QueryRow(ctx, "SELECT payload FROM outbox ORDER BY id DESC LIMIT 1").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var hint struct {
		Type    string   `json:"type"`
		UserIDs []string `json:"userIds"`
	}
	if json.Unmarshal(payload, &hint) != nil || hint.Type != "sync.required" || len(hint.UserIDs) != 2 || !((hint.UserIDs[0] == owner.ID && hint.UserIDs[1] == member.ID) || (hint.UserIDs[0] == member.ID && hint.UserIDs[1] == owner.ID)) {
		t.Fatal("restoration hint lost its current audience")
	}
	if got, err := s.Message(ctx, member.ID, direct.ID, message.ID); err != nil || got.Body != message.Body {
		t.Fatal("explicit reopened direct chat lost its history", err)
	}
}

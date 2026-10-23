package chat

import (
	"context"
	"fmt"
	"testing"

	"teamie/pkg/httpx"
)

func TestHistorySearchBeforePaginationAndCurrentAuthorization(t *testing.T) {
	ctx := context.Background()
	s, ws, conversation, owner, member := setup(t)
	var latestMatch Message
	for n := 1; n <= 90; n++ {
		body := fmt.Sprintf("ordinary message %d", n)
		if n == 3 || n == 72 {
			body = fmt.Sprintf("Needle literal 10%%_ %d", n)
		}
		m, _, err := s.Send(ctx, owner, conversation.ID, fmt.Sprintf("history-%d", n), body, nil)
		if err != nil {
			t.Fatal(err)
		}
		if n == 72 {
			latestMatch = m
		}
	}
	first, more, err := s.MessageHistory(ctx, member.ID, conversation.ID, 0, 0, "needle", 1)
	if err != nil || len(first) != 1 || first[0].Seq != 72 || !more {
		t.Fatalf("search page: %#v %v %v", first, more, err)
	}
	second, more, err := s.MessageHistory(ctx, member.ID, conversation.ID, first[0].Seq, 0, "NEEDLE", 1)
	if err != nil || len(second) != 1 || second[0].Seq != 3 || more {
		t.Fatalf("distant search page: %#v %v %v", second, more, err)
	}
	literal, _, err := s.MessageHistory(ctx, member.ID, conversation.ID, 0, 0, "10%_", 50)
	if err != nil || len(literal) != 2 {
		t.Fatalf("query was not literal: %#v %v", literal, err)
	}
	nearby, more, err := s.MessageHistory(ctx, member.ID, conversation.ID, 0, 40, "", 5)
	if err != nil || more || len(nearby) != 5 || nearby[0].Seq != 38 || nearby[4].Seq != 42 {
		t.Fatalf("context window: %#v %v %v", nearby, more, err)
	}
	if _, err := s.ChangeMessage(ctx, owner.ID, conversation.ID, latestMatch.ID, "", true); err != nil {
		t.Fatal(err)
	}
	visible, _, err := s.MessageHistory(ctx, member.ID, conversation.ID, 0, 0, "needle", 50)
	if err != nil || len(visible) != 1 || visible[0].Seq != 3 {
		t.Fatalf("retracted body searched: %#v %v", visible, err)
	}
	if _, _, err := s.MessageHistory(ctx, httpx.ID(), conversation.ID, 0, 0, "needle", 10); err == nil {
		t.Fatal("outsider searched history")
	}
	if err := s.RemoveMember(ctx, owner.ID, ws.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.MessageHistory(ctx, member.ID, conversation.ID, 0, 0, "needle", 10); err == nil {
		t.Fatal("revoked member searched history")
	}
	if _, _, err := s.MessageHistory(ctx, owner.ID, conversation.ID, 10, 40, "", 5); err == nil {
		t.Fatal("ambiguous cursors accepted")
	}
}

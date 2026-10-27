package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"teamie/pkg/httpx"
)

func TestAttentionUnreadProjectionIsCompleteAndIndependentOfPageAndPreferences(t *testing.T) {
	ctx := context.Background()
	s, ws, channel, owner, member := setup(t)
	zero, err := s.CreateConversation(ctx, owner.ID, ws.ID, "channel", "zero attention", nil)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := s.CreateConversation(ctx, owner.ID, ws.ID, "direct", "", []string{member.ID})
	if err != nil {
		t.Fatal(err)
	}
	group, err := s.CreateConversation(ctx, member.ID, ws.ID, "group", "private", []string{owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	original := attentionMessage(t, s, owner, channel.ID, "", nil)
	attentionMessage(t, s, member, channel.ID, owner.ID, nil)
	reply := attentionMessage(t, s, member, channel.ID, "", &original.ID)
	both := attentionMessage(t, s, member, channel.ID, owner.ID, &original.ID)
	attentionMessage(t, s, owner, channel.ID, owner.ID, &original.ID)
	attentionMessage(t, s, member, direct.ID, "", nil)
	attentionMessage(t, s, member, group.ID, owner.ID, nil)
	if _, err = s.App(nil, "").SetNotificationLevel(ctx, owner.ID, channel.ID, "none"); err != nil {
		t.Fatal(err)
	}
	check := func(cursor string, unreadOnly bool, expected map[string]int64) string {
		t.Helper()
		page, e := s.Attention(ctx, owner.ID, cursor, 1, unreadOnly)
		if e != nil {
			t.Fatal(e)
		}
		got := map[string]int64{}
		var sum, channelSum int64
		for _, entry := range page.AttentionUnreadByConversation {
			if _, duplicate := got[entry.ConversationID]; duplicate {
				t.Fatal("duplicate projection", page)
			}
			got[entry.ConversationID] = entry.UnreadCount
			sum += entry.UnreadCount
			if entry.ConversationID == channel.ID || entry.ConversationID == zero.ID {
				channelSum += entry.UnreadCount
			}
		}
		if len(got) != len(expected) {
			t.Fatalf("incomplete projection: got %v want %v", got, expected)
		}
		for id, count := range expected {
			if actual, ok := got[id]; !ok || actual != count {
				t.Fatalf("projection: got %v want %v", got, expected)
			}
		}
		if sum != page.UnreadCount || channelSum != page.ChannelUnreadCount {
			t.Fatal("projection/global mismatch", page)
		}
		return page.Cursor
	}
	expected := map[string]int64{channel.ID: 3, zero.ID: 0, direct.ID: 0, group.ID: 1}
	cursor := check("", false, expected)
	if cursor == "" {
		t.Fatal("expected another item page")
	}
	check(cursor, true, expected)
	if _, err = s.Read(ctx, owner.ID, channel.ID, reply.Seq); err != nil {
		t.Fatal(err)
	}
	expected[channel.ID] = 1
	check("", false, expected)
	if _, err = s.ChangeAnnotatedMessage(ctx, member.ID, channel.ID, both.ID, "only reply", false, nil); err != nil {
		t.Fatal(err)
	}
	check("", true, expected)
	if _, err = s.ChangeMessage(ctx, owner.ID, channel.ID, original.ID, "", true); err != nil {
		t.Fatal(err)
	}
	expected[channel.ID] = 0
	check("", true, expected)
	if _, err = s.GroupMember(ctx, member.ID, group.ID, owner.ID, false); err != nil {
		t.Fatal(err)
	}
	delete(expected, group.ID)
	check("", false, expected)
}

func TestAttentionEmptyAuthorizedProjectionIsAnExplicitEmptyArray(t *testing.T) {
	s, _, _, _, _ := setup(t)
	outsider := httpx.User{ID: httpx.ID(), Kind: "human"}
	h := (&Handler{Store: s, Identity: attachmentIdentity{httpx.Session{User: outsider}}}).Routes()
	req := httptest.NewRequest("GET", "/api/v1/attention?limit=1", nil)
	req.AddCookie(&http.Cookie{Name: "teamie_session", Value: "fixture"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var body map[string]json.RawMessage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if value, exists := body["attentionUnreadByConversation"]; !exists || string(value) != "[]" {
		t.Fatal("missing complete empty projection", w.Body.String())
	}
}

package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"teamie/pkg/httpx"
	"teamie/pkg/imtext"
)

func attentionMessage(t *testing.T, s *Store, sender httpx.User, conversation, recipient string, reply *string) Message {
	t.Helper()
	var mentions []imtext.Mention
	body := "回复内容"
	if recipient != "" {
		body = "@伙伴 请看"
		mentions = []imtext.Mention{{UserID: recipient, Start: 0, End: 3}}
	}
	m, _, err := s.SendAnnotated(context.Background(), sender, conversation, httpx.ID(), body, reply, nil, mentions)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAttentionProjectsCurrentMentionsRepliesAndReadCursors(t *testing.T) {
	ctx := context.Background()
	s, ws, conversation, owner, member := setup(t)
	original := attentionMessage(t, s, owner, conversation.ID, "", nil)
	mention := attentionMessage(t, s, member, conversation.ID, owner.ID, nil)
	reply := attentionMessage(t, s, member, conversation.ID, "", &original.ID)
	both := attentionMessage(t, s, member, conversation.ID, owner.ID, &original.ID)
	attentionMessage(t, s, owner, conversation.ID, owner.ID, &original.ID)
	if _, _, err := s.Send(ctx, member, conversation.ID, "plain-mention", "@伙伴", nil); err != nil {
		t.Fatal(err)
	}
	agent := httpx.User{ID: httpx.ID(), DisplayName: "Agent", Kind: "agent"}
	if _, err := s.SetPrincipalWorkspaces(ctx, agent, owner, []string{ws.ID}, 1); err != nil {
		t.Fatal(err)
	}
	agentReply := attentionMessage(t, s, agent, conversation.ID, "", &original.ID)
	page, err := s.Attention(ctx, owner.ID, "", 50, false)
	if err != nil || len(page.Items) != 4 || page.UnreadCount != 4 {
		t.Fatal("projection", page, err)
	}
	byID := map[string]AttentionItem{}
	for _, item := range page.Items {
		byID[item.Message.ID] = item
	}
	if !slices.Equal(byID[both.ID].Reasons, []string{"mention", "reply"}) || !slices.Equal(byID[mention.ID].Reasons, []string{"mention"}) || !slices.Equal(byID[reply.ID].Reasons, []string{"reply"}) || byID[agentReply.ID].Message.SenderKind != "agent" {
		t.Fatal("reason dedup or sender equality", byID)
	}
	if _, err = s.Read(ctx, owner.ID, conversation.ID, reply.Seq); err != nil {
		t.Fatal(err)
	}
	page, err = s.Attention(ctx, owner.ID, "", 1, true)
	if err != nil || page.UnreadCount != 2 || len(page.Items) != 1 || page.Cursor == "" {
		t.Fatal("unread page", page, err)
	}
	page, err = s.Attention(ctx, owner.ID, page.Cursor, 1, true)
	if err != nil || page.UnreadCount != 2 || len(page.Items) != 1 || page.Cursor != "" || page.Items[0].IsRead {
		t.Fatal("global count/cursor", page, err)
	}
	if _, err = s.ChangeAnnotatedMessage(ctx, member.ID, conversation.ID, mention.ID, "不再提及", false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChangeAnnotatedMessage(ctx, member.ID, conversation.ID, both.ID, "只保留回复", false, nil); err != nil {
		t.Fatal(err)
	}
	page, err = s.Attention(ctx, owner.ID, "", 50, false)
	if err != nil || len(page.Items) != 3 {
		t.Fatal("edit did not update projection", page, err)
	}
	for _, item := range page.Items {
		if item.Message.ID == both.ID && !slices.Equal(item.Reasons, []string{"reply"}) {
			t.Fatal("stale mention reason", item)
		}
	}
	if _, err = s.ChangeMessage(ctx, owner.ID, conversation.ID, original.ID, "", true); err != nil {
		t.Fatal(err)
	}
	page, err = s.Attention(ctx, owner.ID, "", 50, false)
	if err != nil || len(page.Items) != 0 || page.UnreadCount != 0 {
		t.Fatal("reply target retraction was ignored", page, err)
	}
	attentionMessage(t, s, owner, conversation.ID, agent.ID, nil)
	page, err = s.Attention(ctx, agent.ID, "", 50, false)
	if err != nil || len(page.Items) != 1 || page.Items[0].Message.SenderID != owner.ID {
		t.Fatal("Agent recipient did not use same projection", page, err)
	}
	path := filepath.Join(t.TempDir(), "flags.json")
	if err = os.WriteFile(path, []byte(`{"revision":"off","flags":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	s.Flags = httpx.Flags{Path: path}
	page, err = s.Attention(ctx, agent.ID, "", 50, false)
	if err != nil || len(page.Items) != 1 {
		t.Fatal("disabled writes hid delivered mentions", page, err)
	}
}

func TestAttentionPaginationRevocationAndHTTPIdentity(t *testing.T) {
	ctx := context.Background()
	s, ws, conversation, owner, member := setup(t)
	for n := 0; n < 6; n++ {
		attentionMessage(t, s, member, conversation.ID, owner.ID, nil)
	}
	if _, err := s.DB.Exec(ctx, "UPDATE messages SET created_at=$1 WHERE conversation_id=$2", time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), conversation.ID); err != nil {
		t.Fatal(err)
	}
	seen, cursor := map[string]bool{}, ""
	for n := 0; n < 3; n++ {
		page, err := s.Attention(ctx, owner.ID, cursor, 2, false)
		if err != nil || len(page.Items) != 2 || page.UnreadCount != 6 {
			t.Fatal("tied timestamp page", page, err)
		}
		for _, item := range page.Items {
			if seen[item.Message.ID] {
				t.Fatal("duplicate page item")
			}
			seen[item.Message.ID] = true
		}
		cursor = page.Cursor
	}
	if len(seen) != 6 || cursor != "" {
		t.Fatal("incomplete cursor traversal", seen, cursor)
	}
	group, err := s.CreateConversation(ctx, member.ID, ws.ID, "group", "private", []string{owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	attentionMessage(t, s, member, group.ID, owner.ID, nil)
	page, err := s.Attention(ctx, owner.ID, "", 1, true)
	if err != nil || page.UnreadCount != 7 || page.ChannelUnreadCount != 6 {
		t.Fatal("channelUnreadCount did not separate channel from group unread", page, err)
	}
	if _, err = s.GroupMember(ctx, member.ID, group.ID, owner.ID, false); err != nil {
		t.Fatal(err)
	}
	page, err = s.Attention(ctx, owner.ID, "", 100, false)
	if err != nil || len(page.Items) != 6 || page.UnreadCount != 6 || page.ChannelUnreadCount != 6 {
		t.Fatal("workspace owner bypassed private group", page, err)
	}
	outsider := httpx.User{ID: httpx.ID(), Kind: "human", PlatformRole: "admin"}
	page, err = s.Attention(ctx, outsider.ID, "", 50, false)
	if err != nil || len(page.Items) != 0 || page.UnreadCount != 0 {
		t.Fatal("platform role bypassed workspace privacy", page, err)
	}
	h := (&Handler{Store: s, Identity: attachmentIdentity{httpx.Session{User: owner}}}).Routes()
	get := func(query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/attention?"+query, nil)
		r.AddCookie(&http.Cookie{Name: "teamie_session", Value: "fixture"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := get("limit=1&userId=" + member.ID)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.UnreadCount != 6 || page.Items[0].Message.SenderID != member.ID {
		t.Fatal("caller-controlled inbox identity", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "original_body_hash") || strings.Contains(w.Body.String(), "original_mentions") {
		t.Fatal("internal message columns exposed")
	}
	for _, query := range []string{"limit=0", "limit=101", "unreadOnly=maybe", "cursor=broken", "cursor=" + strings.Repeat("x", 513), "cursor=" + base64.RawURLEncoding.EncodeToString([]byte(`{"id":"x"}`))} {
		if w = get(query); w.Code != 400 {
			t.Fatal("invalid query accepted", query, w.Code, w.Body.String())
		}
	}
	attentionMessage(t, s, owner, conversation.ID, member.ID, nil)
	if err = s.RemoveMember(ctx, owner.ID, ws.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	page, err = s.Attention(ctx, member.ID, "", 50, false)
	if err != nil || len(page.Items) != 0 || page.UnreadCount != 0 {
		t.Fatal("removed member retained inbox access", page, err)
	}
}

func TestAttentionRechecksMembershipAfterWaitingForRevocation(t *testing.T) {
	for _, scope := range []string{"workspace", "conversation"} {
		t.Run(scope, func(t *testing.T) {
			ctx := context.Background()
			s, ws, conversation, owner, member := setup(t)
			if scope == "conversation" {
				var err error
				conversation, err = s.CreateConversation(ctx, owner.ID, ws.ID, "group", "private", []string{member.ID})
				if err != nil {
					t.Fatal(err)
				}
			}
			attentionMessage(t, s, owner, conversation.ID, member.ID, nil)
			tx, err := s.DB.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			table, key, value := "workspaces", "workspace_id", ws.ID
			if scope == "conversation" {
				table, key, value = "conversations", "conversation_id", conversation.ID
			}
			if _, err = tx.Exec(ctx, "SELECT id FROM "+table+" WHERE id=$1 FOR UPDATE", value); err != nil {
				t.Fatal(err)
			}
			type result struct {
				page AttentionPage
				err  error
			}
			done := make(chan result, 1)
			go func() { page, err := s.Attention(ctx, member.ID, "", 50, false); done <- result{page, err} }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				var waiting bool
				if err = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%FOR SHARE OF%' AND query LIKE '%workspace_members wm%')`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("attention did not wait for membership writer")
				}
				time.Sleep(time.Millisecond)
			}
			membershipTable := "workspace_members"
			if scope == "conversation" {
				membershipTable = "conversation_members"
			}
			if _, err = tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE %s=$1 AND user_id=$2", membershipTable, key), value, member.ID); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-done:
				for _, entry := range result.page.AttentionUnreadByConversation {
					if entry.ConversationID == conversation.ID {
						t.Fatal("revoked count escaped after lock wait", result)
					}
				}
				if result.err != nil || len(result.page.Items) != 0 || result.page.UnreadCount != 0 {
					t.Fatal("revoked payload escaped after lock wait", result)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("attention reader did not finish")
			}
		})
	}
}

func TestAttentionAggregatesOnlyCurrentDelegatedWorkspaces(t *testing.T) {
	ctx := context.Background()
	s, first, firstConversation, owner, grantor := setup(t)
	second, secondConversation, err := s.CreateWorkspace(ctx, grantor.ID, "Second workspace")
	if err != nil {
		t.Fatal(err)
	}
	agent := httpx.User{ID: httpx.ID(), DisplayName: "Agent", Kind: "agent"}
	if _, err = s.SetPrincipalWorkspaces(ctx, agent, grantor, []string{first.ID, second.ID}, 1); err != nil {
		t.Fatal(err)
	}
	attentionMessage(t, s, owner, firstConversation.ID, agent.ID, nil)
	attentionMessage(t, s, grantor, secondConversation.ID, agent.ID, nil)
	page, err := s.Attention(ctx, agent.ID, "", 50, false)
	if err != nil || len(page.Items) != 2 || page.UnreadCount != 2 {
		t.Fatal("missing authorized workspace", page, err)
	}
	seen := map[string]bool{}
	for _, item := range page.Items {
		seen[item.Conversation.WorkspaceID] = true
	}
	if !seen[first.ID] || !seen[second.ID] {
		t.Fatal("wrong workspace attribution", page)
	}
	if err = s.RemoveMember(ctx, owner.ID, first.ID, grantor.ID); err != nil {
		t.Fatal(err)
	}
	page, err = s.Attention(ctx, agent.ID, "", 50, false)
	if err != nil || len(page.Items) != 1 || page.UnreadCount != 1 || page.Items[0].Conversation.WorkspaceID != second.ID || page.Items[0].WorkspaceName != second.Name {
		t.Fatal("grantor revocation leaked a workspace or its count", page, err)
	}
}

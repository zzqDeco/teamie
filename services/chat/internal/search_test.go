package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"teamie/pkg/httpx"
)

func TestWorkspaceSearchACLsRetractionsPaginationAndAgents(t *testing.T) {
	ctx := context.Background()
	s, ws, channel, owner, member := setup(t)
	third := httpx.User{ID: httpx.ID(), DisplayName: "Third", Kind: "human"}
	code, _, err := s.CreateInvite(ctx, owner.ID, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AcceptInvite(ctx, third.ID, code); err != nil {
		t.Fatal(err)
	}

	chanMsg, _, err := s.Send(ctx, owner, channel.ID, httpx.ID(), "Alpha 架构评审记录", nil)
	if err != nil {
		t.Fatal(err)
	}
	dmWithOwner, err := s.CreateConversation(ctx, member.ID, ws.ID, "direct", "", []string{owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	dmMsg, _, err := s.Send(ctx, member, dmWithOwner.ID, httpx.ID(), "alpha 私聊讨论", nil)
	if err != nil {
		t.Fatal(err)
	}
	privateGroup, err := s.CreateConversation(ctx, member.ID, ws.ID, "group", "密室小组", []string{third.ID})
	if err != nil {
		t.Fatal(err)
	}
	groupMsg, _, err := s.Send(ctx, member, privateGroup.ID, httpx.ID(), "ALPHA 密室机密方案", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Owner is not a member of privateGroup -> sees channel + dmWithOwner, NOT privateGroup
	ownerPage, err := s.SearchWorkspaceMessages(ctx, owner.ID, ws.ID, "alpha", "", 20)
	if err != nil || len(ownerPage.Items) != 2 {
		t.Fatal("owner search ACL mismatch", ownerPage, err)
	}
	for _, item := range ownerPage.Items {
		if item.Conversation.ID == privateGroup.ID || item.Message.ID == groupMsg.ID {
			t.Fatal("workspace owner bypassed private group ACL", item)
		}
	}

	// Member belongs to all three -> sees all 3 messages
	memberPage, err := s.SearchWorkspaceMessages(ctx, member.ID, ws.ID, "Alpha", "", 20)
	if err != nil || len(memberPage.Items) != 3 {
		t.Fatal("member search mismatch", memberPage, err)
	}

	// Third belongs to channel + privateGroup, NOT dmWithOwner -> sees 2 messages
	thirdPage, err := s.SearchWorkspaceMessages(ctx, third.ID, ws.ID, "alpha", "", 20)
	if err != nil || len(thirdPage.Items) != 2 {
		t.Fatal("third member search mismatch", thirdPage, err)
	}
	for _, item := range thirdPage.Items {
		if item.Conversation.ID == dmWithOwner.ID || item.Message.ID == dmMsg.ID {
			t.Fatal("non-participant saw direct message", item)
		}
	}

	// Platform admin outside workspace gets 404
	adminOutsider := httpx.User{ID: httpx.ID(), DisplayName: "Admin", Kind: "human", PlatformRole: "admin"}
	if _, err = s.SearchWorkspaceMessages(ctx, adminOutsider.ID, ws.ID, "alpha", "", 20); err == nil {
		t.Fatal("platform admin bypassed workspace membership")
	} else {
		var ae *httpx.Error
		if !errors.As(err, &ae) || ae.Status != 404 {
			t.Fatal("expected 404 for non-member admin, got", err)
		}
	}

	// Platform admin who IS a workspace member still cannot bypass privateGroup
	code2, _, err := s.CreateInvite(ctx, owner.ID, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AcceptInvite(ctx, adminOutsider.ID, code2); err != nil {
		t.Fatal(err)
	}
	adminPage, err := s.SearchWorkspaceMessages(ctx, adminOutsider.ID, ws.ID, "alpha", "", 20)
	if err != nil || len(adminPage.Items) != 1 || adminPage.Items[0].Message.ID != chanMsg.ID {
		t.Fatal("platform admin member bypassed private conversations", adminPage, err)
	}

	// Agent principal follows the exact same workspace and conversation ACL rules
	agent := httpx.User{ID: httpx.ID(), DisplayName: "SearchBot", Kind: "agent"}
	if _, err = s.SetPrincipalWorkspaces(ctx, agent, member, []string{ws.ID}, 1); err != nil {
		t.Fatal(err)
	}
	agentPage, err := s.SearchWorkspaceMessages(ctx, agent.ID, ws.ID, "alpha", "", 20)
	if err != nil || len(agentPage.Items) != 1 || agentPage.Items[0].Message.ID != chanMsg.ID {
		t.Fatal("agent saw private conversations without membership", agentPage, err)
	}
	if _, err = s.GroupMember(ctx, member.ID, privateGroup.ID, agent.ID, true); err != nil {
		t.Fatal(err)
	}
	agentPage, err = s.SearchWorkspaceMessages(ctx, agent.ID, ws.ID, "alpha", "", 20)
	if err != nil || len(agentPage.Items) != 2 {
		t.Fatal("agent did not see group after admission", agentPage, err)
	}

	// Retracted message disappears from search results
	if _, err = s.ChangeMessage(ctx, member.ID, dmWithOwner.ID, dmMsg.ID, "", true); err != nil {
		t.Fatal(err)
	}
	ownerPage, err = s.SearchWorkspaceMessages(ctx, owner.ID, ws.ID, "alpha", "", 20)
	if err != nil || len(ownerPage.Items) != 1 || ownerPage.Items[0].Message.ID != chanMsg.ID {
		t.Fatal("retracted message still matched", ownerPage, err)
	}

	// Tied-timestamp cursor pagination stability and concurrent insertion
	for i := 0; i < 5; i++ {
		if _, _, err = s.Send(ctx, owner, channel.ID, httpx.ID(), fmt.Sprintf("分页关键字 Beta-%d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	tiedAt := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	if _, err = s.DB.Exec(ctx, "UPDATE messages SET created_at=$1 WHERE conversation_id=$2 AND strpos(body,'Beta-')>0", tiedAt, channel.ID); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	for pageNum := 0; pageNum < 3; pageNum++ {
		page, err := s.SearchWorkspaceMessages(ctx, owner.ID, ws.ID, "beta-", cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if pageNum == 0 {
			// Concurrent new message after page 1 is fetched does not shift cursor traversal of older items
			if _, _, err = s.Send(ctx, owner, channel.ID, httpx.ID(), "分页关键字 Beta-new", nil); err != nil {
				t.Fatal(err)
			}
		}
		for _, item := range page.Items {
			if seen[item.Message.ID] {
				t.Fatal("duplicate item across cursor pages", item.Message.ID)
			}
			seen[item.Message.ID] = true
		}
		cursor = page.Cursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 5 || cursor != "" {
		t.Fatal("expected 5 original Beta items across pages", len(seen), cursor)
	}

	// Removing member revokes both member and its delegated Agent
	if err = s.RemoveMember(ctx, owner.ID, ws.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SearchWorkspaceMessages(ctx, member.ID, ws.ID, "alpha", "", 20); err == nil {
		t.Fatal("removed member retained workspace search access")
	}
	if _, err = s.SearchWorkspaceMessages(ctx, agent.ID, ws.ID, "alpha", "", 20); err == nil {
		t.Fatal("delegated agent retained workspace search access after grantor removal")
	}

	// HTTP route validation (400 invalid_query on bad q, limit, cursor)
	h := (&Handler{Store: s, Identity: attachmentIdentity{httpx.Session{User: owner}}}).Routes()
	get := func(query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/workspaces/"+ws.ID+"/messages/search?"+query, nil)
		r.AddCookie(&http.Cookie{Name: "teamie_session", Value: "fixture"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := get("q=alpha&limit=10")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ownerPage) != nil || len(ownerPage.Items) != 1 {
		t.Fatal("valid HTTP search failed", w.Code, w.Body.String())
	}
	for _, bad := range []string{
		"",
		"q=",
		"q=%20%20",
		"q=" + strings.Repeat("中", 201),
		"q=alpha&limit=0",
		"q=alpha&limit=51",
		"q=alpha&cursor=broken",
		"q=alpha&cursor=" + strings.Repeat("x", 513),
		"q=alpha&cursor=" + base64.RawURLEncoding.EncodeToString([]byte(`{"id":"x"}`)),
	} {
		w = get(bad)
		if w.Code != 400 || !strings.Contains(w.Body.String(), `"invalid_query"`) {
			t.Fatal("expected 400 invalid_query for", bad, "got", w.Code, w.Body.String())
		}
	}
}

func TestWorkspaceSearchRechecksMembershipAfterWaitingForRevocation(t *testing.T) {
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
			if _, _, err := s.Send(ctx, owner, conversation.ID, httpx.ID(), "锁等待测试关键字", nil); err != nil {
				t.Fatal(err)
			}
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
				page WorkspaceSearchPage
				err  error
			}
			done := make(chan result, 1)
			go func() {
				page, err := s.SearchWorkspaceMessages(ctx, member.ID, ws.ID, "锁等待测试", "", 20)
				done <- result{page, err}
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				var waiting bool
				if err = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%FOR SHARE OF%')`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("workspace search did not wait for membership writer")
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
			case res := <-done:
				if scope == "workspace" {
					var ae *httpx.Error
					if !errors.As(res.err, &ae) || ae.Status != 404 {
						t.Fatal("expected 404 after workspace membership revocation, got", res)
					}
				} else if res.err != nil || len(res.page.Items) != 0 {
					t.Fatal("revoked conversation payload escaped after lock wait", res)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("workspace search reader did not finish")
			}
		})
	}
}

func TestWorkspaceSearchDoesNotLockUnmatchedConversations(t *testing.T) {
	ctx := context.Background()
	s, ws, channel, owner, member := setup(t)
	otherConv, err := s.CreateConversation(ctx, owner.ID, ws.ID, "group", "other", []string{member.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Send(ctx, owner, channel.ID, httpx.ID(), "无锁扫描匹配关键字", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Send(ctx, owner, otherConv.ID, httpx.ID(), "无关对话内容", nil); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT id FROM conversations WHERE id=$1 FOR UPDATE", otherConv.ID); err != nil {
		t.Fatal(err)
	}
	page, err := s.SearchWorkspaceMessages(ctx, member.ID, ws.ID, "无锁扫描匹配", "", 20)
	if err != nil || len(page.Items) != 1 || page.Items[0].Conversation.ID != channel.ID {
		t.Fatal("search blocked or failed while unrelated conversation held FOR UPDATE", page, err)
	}
}

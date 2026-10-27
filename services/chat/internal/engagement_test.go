package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"teamie/pkg/httpx"
	"teamie/services/chat/internal/domain"
)

func TestValidateReactionEmojiAndNotificationLevel(t *testing.T) {
	for _, valid := range []string{"👍", "🎉", "❤️", "🔥", "👀", "🚀", "💯", "👨‍💻"} {
		if err := domain.ValidateReactionEmoji(valid); err != nil {
			t.Fatalf("expected valid emoji %q, got %v", valid, err)
		}
	}
	for _, invalid := range []string{"", " ", "ok", "123", "#", "*", "+1", "👍👎"} {
		if err := domain.ValidateReactionEmoji(invalid); err == nil {
			t.Fatalf("expected invalid emoji %q to be rejected", invalid)
		}
	}
	for _, validLevel := range []string{"all", "mentions", "none"} {
		if err := domain.ValidateNotificationLevel(validLevel); err != nil {
			t.Fatalf("expected valid level %q, got %v", validLevel, err)
		}
	}
	for _, invalidLevel := range []string{"", "muted", "ALL", "default"} {
		if err := domain.ValidateNotificationLevel(invalidLevel); err == nil {
			t.Fatalf("expected invalid level %q to be rejected", invalidLevel)
		}
	}
}

type fakeIdentity struct {
	users map[string]httpx.User
}

func (f *fakeIdentity) Validate(_ context.Context, token string) (httpx.Session, error) {
	u, ok := f.users[token]
	if !ok {
		return httpx.Session{}, httpx.Err(401, "unauthenticated", "invalid session")
	}
	return httpx.Session{User: u, CSRFToken: "csrf"}, nil
}

func (f *fakeIdentity) Users(_ context.Context, ids []string) ([]httpx.User, error) {
	var out []httpx.User
	for _, u := range f.users {
		for _, id := range ids {
			if u.ID == id {
				out = append(out, u)
			}
		}
	}
	return out, nil
}

func (f *fakeIdentity) Ping(context.Context) error { return nil }

func TestReactionsReadReceiptsAndNotificationLevels(t *testing.T) {
	ctx := context.Background()
	s, ws, channelConv, owner, member := setup(t)
	outsider := httpx.User{ID: httpx.ID(), DisplayName: "Outsider", Kind: "human"}

	ownerTok := strings.Repeat("a", 64)
	memberTok := strings.Repeat("b", 64)
	outsiderTok := strings.Repeat("c", 64)
	id := &fakeIdentity{users: map[string]httpx.User{
		ownerTok:    owner,
		memberTok:   member,
		outsiderTok: outsider,
	}}
	origin := "https://teamie.example"
	srv := httptest.NewServer((&Handler{Store: s, Identity: id, Origin: origin}).Routes())
	defer srv.Close()

	doJSON := func(method, path, token string, body any) (int, map[string]any) {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Cookie", httpx.CookieName+"="+token)
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", "csrf")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}

	// 1. Send message and test reactions (idempotent PUT, DELETE, Unicode validation, retraction clears reactions)
	msg, _, err := s.Send(ctx, owner, channelConv.ID, "react-msg-1", "Hello reactions", nil)
	if err != nil {
		t.Fatal(err)
	}
	reactPath := "/api/v1/conversations/" + channelConv.ID + "/messages/" + msg.ID + "/reactions"

	// Invalid ASCII / plain text emoji rejected with 400
	if status, _ := doJSON(http.MethodPut, reactPath, ownerTok, map[string]any{"emoji": "thumbsup"}); status != 400 {
		t.Fatalf("plain text reaction status = %d, want 400", status)
	}
	// Outsider rejected with 404
	if status, _ := doJSON(http.MethodPut, reactPath, outsiderTok, map[string]any{"emoji": "👍"}); status != 404 {
		t.Fatalf("outsider reaction status = %d, want 404", status)
	}
	// Owner and member add 👍
	status, resp := doJSON(http.MethodPut, reactPath, ownerTok, map[string]any{"emoji": "👍"})
	if status != 200 {
		t.Fatalf("owner put reaction status = %d, resp = %#v", status, resp)
	}
	// Idempotent repeat PUT
	status, _ = doJSON(http.MethodPut, reactPath, ownerTok, map[string]any{"emoji": "👍"})
	if status != 200 {
		t.Fatalf("idempotent put reaction status = %d", status)
	}
	status, resp = doJSON(http.MethodPut, reactPath, memberTok, map[string]any{"emoji": "👍"})
	if status != 200 {
		t.Fatalf("member put reaction status = %d", status)
	}
	reactions, _ := resp["reactions"].([]any)
	if len(reactions) != 1 {
		t.Fatalf("expected 1 aggregated reaction, got %#v", reactions)
	}
	r0 := reactions[0].(map[string]any)
	if r0["emoji"] != "👍" || len(r0["userIds"].([]any)) != 2 {
		t.Fatalf("unexpected reaction summary: %#v", r0)
	}

	// Member removes 👍
	status, resp = doJSON(http.MethodDelete, reactPath, memberTok, map[string]any{"emoji": "👍"})
	if status != 200 || len(resp["reactions"].([]any)) != 1 || len(resp["reactions"].([]any)[0].(map[string]any)["userIds"].([]any)) != 1 {
		t.Fatalf("after member delete: %d %#v", status, resp)
	}
	// Owner removes 👍 -> empty reactions array []
	status, resp = doJSON(http.MethodDelete, reactPath, ownerTok, map[string]any{"emoji": "👍"})
	if status != 200 || len(resp["reactions"].([]any)) != 0 {
		t.Fatalf("after owner delete: %d %#v", status, resp)
	}

	// Add 🎉 then retract message -> reactions cleared and further reactions rejected with 409 message_retracted
	_, _ = doJSON(http.MethodPut, reactPath, memberTok, map[string]any{"emoji": "🎉"})
	if _, err := s.ChangeMessage(ctx, owner.ID, channelConv.ID, msg.ID, "", true); err != nil {
		t.Fatal(err)
	}
	if status, body := doJSON(http.MethodPut, reactPath, memberTok, map[string]any{"emoji": "🔥"}); status != 409 || body["error"] != "message_retracted" {
		t.Fatalf("reaction on retracted message = %d %#v, want 409 message_retracted", status, body)
	}
	fetched, err := s.Message(ctx, owner.ID, channelConv.ID, msg.ID)
	if err != nil || len(fetched.Reactions) != 0 {
		t.Fatalf("retracted message retained reactions: %#v %v", fetched.Reactions, err)
	}

	// 2. Read receipts in direct, group, and channel conversations
	dmConv, err := s.CreateConversation(ctx, owner.ID, ws.ID, "direct", "", []string{member.ID})
	if err != nil {
		t.Fatal(err)
	}
	groupConv, err := s.CreateConversation(ctx, owner.ID, ws.ID, "group", "Eng Group", []string{member.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Send(ctx, owner, dmConv.ID, "dm-1", "DM hello", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Send(ctx, owner, groupConv.ID, "grp-1", "Group hello", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Send(ctx, owner, channelConv.ID, "ch-2", "Channel hello", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, member.ID, dmConv.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, member.ID, groupConv.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, member.ID, channelConv.ID, 2); err != nil {
		t.Fatal(err)
	}

	// Direct & group include members array; channel omits members array
	status, dmReceipts := doJSON(http.MethodGet, "/api/v1/conversations/"+dmConv.ID+"/read-receipts", ownerTok, nil)
	if status != 200 || dmReceipts["members"] == nil || len(dmReceipts["members"].([]any)) != 2 {
		t.Fatalf("dm read receipts = %d %#v", status, dmReceipts)
	}
	status, grpReceipts := doJSON(http.MethodGet, "/api/v1/conversations/"+groupConv.ID+"/read-receipts", ownerTok, nil)
	if status != 200 || grpReceipts["members"] == nil || len(grpReceipts["members"].([]any)) != 2 {
		t.Fatalf("group read receipts = %d %#v", status, grpReceipts)
	}
	status, chReceipts := doJSON(http.MethodGet, "/api/v1/conversations/"+channelConv.ID+"/read-receipts", ownerTok, nil)
	if status != 200 || chReceipts["members"] != nil || len(chReceipts["readSeqCounts"].([]any)) == 0 {
		t.Fatalf("channel read receipts should omit members and return readSeqCounts: %d %#v", status, chReceipts)
	}

	// 3. Notification levels and caller-scoped preference.updated sync event
	_, startCursor, _, err := s.Sync(ctx, owner.ID, "0", 500)
	if err != nil {
		t.Fatal(err)
	}
	_, memberCursor, _, err := s.Sync(ctx, member.ID, "0", 500)
	if err != nil {
		t.Fatal(err)
	}

	if status, _ := doJSON(http.MethodPut, "/api/v1/conversations/"+channelConv.ID+"/notification-level", ownerTok, map[string]any{"level": "invalid"}); status != 400 {
		t.Fatalf("invalid notification level status = %d, want 400", status)
	}
	status, prefResp := doJSON(http.MethodPut, "/api/v1/conversations/"+channelConv.ID+"/notification-level", ownerTok, map[string]any{"level": "mentions"})
	if status != 200 || prefResp["notificationLevel"] != "mentions" {
		t.Fatalf("set notification level = %d %#v", status, prefResp)
	}

	// Owner receives preference.updated; member does NOT receive owner's preference.updated
	ownerEvents, _, _, err := s.Sync(ctx, owner.ID, startCursor, 50)
	if err != nil || len(ownerEvents) != 1 || ownerEvents[0].Type != "preference.updated" || ownerEvents[0].NotificationLevel == nil || *ownerEvents[0].NotificationLevel != "mentions" {
		t.Fatalf("owner sync events = %#v, err = %v", ownerEvents, err)
	}
	memberEvents, _, _, err := s.Sync(ctx, member.ID, memberCursor, 50)
	if err != nil || len(memberEvents) != 0 {
		t.Fatalf("member received owner's caller-scoped preference event: %#v", memberEvents)
	}

	// Bootstrap reflects persisted notificationLevel even when im.notifications.configure is disabled
	flagsPath := filepath.Join(t.TempDir(), "flags.json")
	_ = os.WriteFile(flagsPath, []byte(`{"revision":"disabled","flags":{"im.notifications.configure":false,"im.reactions.create":false,"im.read_receipts.view":false}}`), 0600)
	s.Flags = httpx.Flags{Path: flagsPath}
	srv.Config.Handler = (&Handler{Store: s, Identity: id, Origin: origin}).Routes()

	if status, _ := doJSON(http.MethodPut, "/api/v1/conversations/"+channelConv.ID+"/notification-level", ownerTok, map[string]any{"level": "none"}); status != 403 {
		t.Fatalf("disabled notification-level write status = %d, want 403", status)
	}
	if status, _ := doJSON(http.MethodGet, "/api/v1/conversations/"+dmConv.ID+"/read-receipts", ownerTok, nil); status != 403 {
		t.Fatalf("disabled read-receipts status = %d, want 403", status)
	}
	convs, err := s.Conversations(ctx, owner.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	foundLevel := ""
	for _, c := range convs {
		if c.ID == channelConv.ID {
			foundLevel = c.NotificationLevel
		}
	}
	if foundLevel != "mentions" {
		t.Fatalf("bootstrap notificationLevel = %q, want mentions", foundLevel)
	}
}

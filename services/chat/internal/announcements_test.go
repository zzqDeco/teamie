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
)

func TestPlatformAnnouncementsLifecycleAuthorizationReadWatermarkAndDisablement(t *testing.T) {
	ctx := context.Background()
	s, _, _, owner, member := setup(t)
	admin := httpx.User{ID: owner.ID, Email: "admin@example.test", DisplayName: "Platform Admin", Kind: "human", PlatformRole: "admin"}
	regular := httpx.User{ID: member.ID, Email: "member@example.test", DisplayName: "Member", Kind: "human", PlatformRole: "user"}
	newbie := httpx.User{ID: httpx.ID(), Email: "newbie@example.test", DisplayName: "New User", Kind: "human", PlatformRole: "user"}
	agentPrincipal := httpx.User{ID: httpx.ID(), Email: "agent@teamie.internal", DisplayName: "Helper Agent", Kind: "agent", PlatformRole: "admin"}

	adminTok := strings.Repeat("a", 64)
	regularTok := strings.Repeat("b", 64)
	newbieTok := strings.Repeat("c", 64)
	agentTok := strings.Repeat("d", 64)

	id := &fakeIdentity{users: map[string]httpx.User{
		adminTok:   admin,
		regularTok: regular,
		newbieTok:  newbie,
		agentTok:   agentPrincipal,
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

	// Bootstrap capability reflects both feature flag and human platform-admin role.
	if status, boot := doJSON(http.MethodGet, "/api/v1/bootstrap", adminTok, nil); status != 200 {
		t.Fatalf("admin bootstrap status = %d", status)
	} else if caps, _ := boot["capabilities"].(map[string]any); caps["im.announcements.publish"] != true {
		t.Fatalf("expected admin im.announcements.publish=true, got %v", caps["im.announcements.publish"])
	}
	if status, boot := doJSON(http.MethodGet, "/api/v1/bootstrap", regularTok, nil); status != 200 {
		t.Fatalf("regular bootstrap status = %d", status)
	} else if caps, _ := boot["capabilities"].(map[string]any); caps["im.announcements.publish"] != false {
		t.Fatalf("expected non-admin im.announcements.publish=false, got %v", caps["im.announcements.publish"])
	}

	// Non-admin humans and Agent principals cannot publish announcements.
	for name, tok := range map[string]string{"regular": regularTok, "agent": agentTok} {
		if status, _ := doJSON(http.MethodPost, "/api/v1/admin/announcements", tok, map[string]any{
			"title": "Unauthorized",
			"body":  "Nope",
		}); status != 403 {
			t.Fatalf("%s create announcement status = %d, want 403", name, status)
		}
	}

	// Invalid inputs rejected with 400.
	if status, _ := doJSON(http.MethodPost, "/api/v1/admin/announcements", adminTok, map[string]any{
		"title": "   ",
		"body":  "Valid body",
	}); status != 400 {
		t.Fatalf("blank title status = %d, want 400", status)
	}
	if status, _ := doJSON(http.MethodPost, "/api/v1/admin/announcements", adminTok, map[string]any{
		"title": strings.Repeat("标", 121),
		"body":  "Valid body",
	}); status != 400 {
		t.Fatalf("overlong title status = %d, want 400", status)
	}
	if status, _ := doJSON(http.MethodPost, "/api/v1/admin/announcements", adminTok, map[string]any{
		"title": "Valid title",
		"body":  "",
	}); status != 400 {
		t.Fatalf("empty body status = %d, want 400", status)
	}

	// Admin publishes v0.4.5 and v0.4.6 announcements.
	status, created1 := doJSON(http.MethodPost, "/api/v1/admin/announcements", adminTok, map[string]any{
		"title": "  Teamie v0.4.5 发布  ",
		"body":  "### 更新内容\n- Agent 对话自然度升级",
	})
	if status != 201 {
		t.Fatalf("create announcement 1 status = %d, resp = %v", status, created1)
	}
	ann1 := created1["announcement"].(map[string]any)
	if ann1["title"] != "Teamie v0.4.5 发布" || int64(ann1["seq"].(float64)) != 1 {
		t.Fatalf("unexpected announcement 1: %v", ann1)
	}

	status, created2 := doJSON(http.MethodPost, "/api/v1/admin/announcements", adminTok, map[string]any{
		"title": "误发公告草稿",
		"body":  "待撤回的草稿内容",
	})
	if status != 201 {
		t.Fatalf("create announcement 2 status = %d, resp = %v", status, created2)
	}
	ann2 := created2["announcement"].(map[string]any)
	ann2ID := ann2["id"].(string)
	if int64(ann2["seq"].(float64)) != 2 {
		t.Fatalf("expected seq 2, got %v", ann2["seq"])
	}

	// Verify durable outbox hints were recorded atomically on publish.
	var outboxHints int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE payload->>'type' = 'announcement.published'`).Scan(&outboxHints); err != nil || outboxHints != 2 {
		t.Fatalf("expected 2 announcement.published outbox hints, got %d (err=%v)", outboxHints, err)
	}

	// Publishing admin's own readSeq is already advanced to 2 so they don't pop their own modal.
	if st, adminList := doJSON(http.MethodGet, "/api/v1/announcements", adminTok, nil); st != 200 {
		t.Fatalf("admin list status = %d", st)
	} else if int64(adminList["latestSeq"].(float64)) != 2 || int64(adminList["readSeq"].(float64)) != 2 {
		t.Fatalf("expected admin latestSeq=2 readSeq=2, got %v", adminList)
	}

	// Regular user has readSeq=0, latestSeq=2, newest item first; acknowledging clamps future seqs and stays monotonic.
	st, regList := doJSON(http.MethodGet, "/api/v1/announcements", regularTok, nil)
	if st != 200 || int64(regList["latestSeq"].(float64)) != 2 || int64(regList["readSeq"].(float64)) != 0 {
		t.Fatalf("unexpected regular list: status=%d body=%v", st, regList)
	}
	if st, _ := doJSON(http.MethodPost, "/api/v1/announcements/read", regularTok, map[string]any{"seq": -1}); st != 400 {
		t.Fatalf("negative read seq status = %d, want 400", st)
	}
	if st, readResp := doJSON(http.MethodPost, "/api/v1/announcements/read", regularTok, map[string]any{"seq": 999999}); st != 200 || int64(readResp["readSeq"].(float64)) != 2 {
		t.Fatalf("clamped read seq failed: status=%d resp=%v", st, readResp)
	}
	if st, readResp := doJSON(http.MethodPost, "/api/v1/announcements/read", regularTok, map[string]any{"seq": 1}); st != 200 || int64(readResp["readSeq"].(float64)) != 2 {
		t.Fatalf("monotonic read seq regressed: status=%d resp=%v", st, readResp)
	}

	// Non-admin cannot delete; admin deletes mistaken announcement #2.
	if st, _ := doJSON(http.MethodDelete, "/api/v1/admin/announcements/"+ann2ID, regularTok, nil); st != 403 {
		t.Fatalf("non-admin delete status = %d, want 403", st)
	}
	if st, _ := doJSON(http.MethodDelete, "/api/v1/admin/announcements/"+ann2ID, adminTok, nil); st != 204 {
		t.Fatalf("admin delete status = %d, want 204", st)
	}
	if st, _ := doJSON(http.MethodDelete, "/api/v1/admin/announcements/"+ann2ID, adminTok, nil); st != 404 {
		t.Fatalf("repeat delete status = %d, want 404", st)
	}

	// Regular user who already read #2 sees latestSeq=1 <= readSeq=2 (no duplicate popup!).
	if st, regAfterDel := doJSON(http.MethodGet, "/api/v1/announcements", regularTok, nil); st != 200 || int64(regAfterDel["latestSeq"].(float64)) != 1 || int64(regAfterDel["readSeq"].(float64)) != 2 {
		t.Fatalf("unexpected regular state after delete: status=%d body=%v", st, regAfterDel)
	}

	// Brand-new registered user (newbie, zero workspaces!) entering the system for the first time sees latestSeq=1 > readSeq=0 with announcement #1!
	st, newbieList := doJSON(http.MethodGet, "/api/v1/announcements", newbieTok, nil)
	if st != 200 || int64(newbieList["latestSeq"].(float64)) != 1 || int64(newbieList["readSeq"].(float64)) != 0 {
		t.Fatalf("newbie should see latestSeq=1 readSeq=0, got status=%d body=%v", st, newbieList)
	}
	items := newbieList["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["title"] != "Teamie v0.4.5 发布" {
		t.Fatalf("unexpected newbie items: %v", items)
	}

	// Admin publishes announcement #3 -> BIGSERIAL assigns seq=3 > 2 so everyone receives it!
	status, created3 := doJSON(http.MethodPost, "/api/v1/admin/announcements", adminTok, map[string]any{
		"title": "Teamie v0.4.6 平台公告上线",
		"body":  "支持弹窗通知、历史回看与 Agent 同步知晓。",
	})
	if status != 201 || int64(created3["announcement"].(map[string]any)["seq"].(float64)) != 3 {
		t.Fatalf("expected announcement 3 seq=3, got status=%d resp=%v", status, created3)
	}
	if st, regList3 := doJSON(http.MethodGet, "/api/v1/announcements", regularTok, nil); st != 200 || int64(regList3["latestSeq"].(float64)) != 3 || int64(regList3["readSeq"].(float64)) != 2 {
		t.Fatalf("regular user should see unread seq 3, got status=%d body=%v", st, regList3)
	}

	// Disabling im.announcements.publish blocks publish/delete with 403 feature_disabled while preserving persisted reads & read cursors!
	flagsPath := filepath.Join(t.TempDir(), "flags.json")
	if err := os.WriteFile(flagsPath, []byte(`{"revision":"announcements-off","flags":{"im.announcements.publish":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	s.Flags = httpx.Flags{Path: flagsPath}
	srv.Config.Handler = (&Handler{Store: s, Identity: id, Origin: origin}).Routes()
	if st, resp := doJSON(http.MethodPost, "/api/v1/admin/announcements", adminTok, map[string]any{
		"title": "Blocked",
		"body":  "Blocked",
	}); st != 403 || resp["error"].(map[string]any)["code"] != "feature_disabled" {
		t.Fatalf("expected 403 feature_disabled on publish, got %d %v", st, resp)
	}
	if st, newbieRead := doJSON(http.MethodGet, "/api/v1/announcements?limit=1", newbieTok, nil); st != 200 || newbieRead["hasMore"] != true || len(newbieRead["items"].([]any)) != 1 {
		t.Fatalf("persisted announcements must remain readable when publish flag disabled: %d %v", st, newbieRead)
	}
	if st, ack := doJSON(http.MethodPost, "/api/v1/announcements/read", newbieTok, map[string]any{"seq": 3}); st != 200 || int64(ack["readSeq"].(float64)) != 3 {
		t.Fatalf("newbie read acknowledgement must remain available when publish flag disabled: %d %v", st, ack)
	}
}

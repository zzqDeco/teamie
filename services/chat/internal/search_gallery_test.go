package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"teamie/pkg/httpx"
	"teamie/services/chat/internal/domain"
)

func TestGlobalSearchCrossWorkspaceACLsFiltersMultiTermAndPagination(t *testing.T) {
	ctx := context.Background()
	s, ws1, ch1, owner, member := setup(t)
	third := httpx.User{ID: httpx.ID(), DisplayName: "Third", Kind: "human"}
	code, _, err := s.CreateInvite(ctx, owner.ID, ws1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AcceptInvite(ctx, third.ID, code); err != nil {
		t.Fatal(err)
	}

	// Second workspace where member is owner and owner is NOT a member.
	ws2, ch2, err := s.CreateWorkspace(ctx, member.ID, "Studio Ops")
	if err != nil {
		t.Fatal(err)
	}

	// Private group in ws1 with member + third (owner is NOT a member).
	privateGroup, err := s.CreateConversation(ctx, member.ID, ws1.ID, "group", "密室小组", []string{third.ID})
	if err != nil {
		t.Fatal(err)
	}

	// Messages spanning both workspaces and testing CJK, >=3-char trigrams,
	// quoted phrases, LIKE metacharacters, attachments, and forwarding.
	m1, _, err := s.Send(ctx, owner, ch1.ID, httpx.ID(), "发布 v0.4.11 deployment 失败 100% foo_bar a\\b", nil)
	if err != nil {
		t.Fatal(err)
	}
	m2, _, err := s.Send(ctx, member, privateGroup.ID, httpx.ID(), `发布 "error code" 500 密室记录`, nil)
	if err != nil {
		t.Fatal(err)
	}
	m3, _, err := s.Send(ctx, member, ch2.ID, httpx.ID(), "发布 v0.4.11 跨空间回放记录 500 error code", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Attachment message in ch1 for hasFiles + filename matching.
	attFile, err := s.UploadAttachment(ctx, owner.ID, ch1.ID, "Release_Report_v0411.txt", []byte("plain attachment content"))
	if err != nil {
		t.Fatal(err)
	}
	mFile, _, err := s.SendWithAttachments(ctx, owner, ch1.ID, httpx.ID(), "附件说明", nil, []string{attFile.ID})
	if err != nil {
		t.Fatal(err)
	}

	// Forward m1 into ch1 with a comment ending in "cross" while forwarding a message starting with "boundary".
	mBoundarySrc, _, err := s.Send(ctx, owner, ch1.ID, httpx.ID(), "boundary 原文关键字", nil)
	if err != nil {
		t.Fatal(err)
	}
	mForwarded, _, err := s.App(nil, "").SendMessage(ctx, toDomainUser(owner), domain.SendRequest{
		ConversationID:  ch1.ID,
		ClientMessageID: httpx.ID(),
		Body:            "转发补充 cross",
		ForwardOf:       &domain.ForwardSource{ConversationID: ch1.ID, MessageID: mBoundarySrc.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Forward mFile into ch1 so we verify forwarded attachment filename search under hasFiles=true.
	mForwardedFile, _, err := s.App(nil, "").SendMessage(ctx, toDomainUser(owner), domain.SendRequest{
		ConversationID:  ch1.ID,
		ClientMessageID: httpx.ID(),
		Body:            "转发文件评论",
		ForwardOf:       &domain.ForwardSource{ConversationID: ch1.ID, MessageID: mFile.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1) Cross-workspace search for member sees ch1 (ws1), privateGroup (ws1), and ch2 (ws2).
	memberAll, err := s.SearchMessages(ctx, member.ID, "发布", "", "", "", "", "", "", "", 20)
	if err != nil || len(memberAll.Items) != 3 {
		t.Fatalf("expected 3 cross-workspace hits for member, got %d err=%v", len(memberAll.Items), err)
	}
	wsNames := map[string]bool{}
	for _, it := range memberAll.Items {
		wsNames[it.WorkspaceName] = true
	}
	if !wsNames[ws1.Name] || !wsNames[ws2.Name] {
		t.Fatalf("expected both workspace names in global search, got %v", wsNames)
	}

	// 2) Owner cannot see privateGroup (ws1) or ws2.
	ownerAll, err := s.SearchMessages(ctx, owner.ID, "发布", "", "", "", "", "", "", "", 20)
	if err != nil || len(ownerAll.Items) != 1 || ownerAll.Items[0].Message.ID != m1.ID {
		t.Fatalf("owner global search ACL violation: items=%+v err=%v", ownerAll.Items, err)
	}

	// 3) Platform admin outside workspaces sees 0 globally and 404 on explicit unauthorized workspace/conversation.
	admin := httpx.User{ID: httpx.ID(), DisplayName: "Admin", Kind: "human", PlatformRole: "admin"}
	adminAll, err := s.SearchMessages(ctx, admin.ID, "发布", "", "", "", "", "", "", "", 20)
	if err != nil || len(adminAll.Items) != 0 {
		t.Fatalf("outsider admin should see 0 results globally, got %d err=%v", len(adminAll.Items), err)
	}
	if _, err = s.SearchMessages(ctx, admin.ID, "不存在的关键字", ws1.ID, "", "", "", "", "", "", 20); err == nil {
		t.Fatal("expected 404 for outsider admin with explicit workspaceId even on 0 matches")
	} else {
		var ae *httpx.Error
		if !errors.As(err, &ae) || ae.Status != 404 {
			t.Fatalf("expected 404, got %v", err)
		}
	}
	if _, err = s.SearchMessages(ctx, owner.ID, "不存在的关键字", "", privateGroup.ID, "", "", "", "", "", 20); err == nil {
		t.Fatal("expected 404 for owner targeting private group even on 0 matches")
	} else {
		var ae *httpx.Error
		if !errors.As(err, &ae) || ae.Status != 404 {
			t.Fatalf("expected 404, got %v", err)
		}
	}
	// Mismatched workspaceId + conversationId returns 404 even when member has access to both separately.
	if _, err = s.SearchMessages(ctx, member.ID, "发布", ws2.ID, ch1.ID, "", "", "", "", "", 20); err == nil {
		t.Fatal("expected 404 for mismatched workspaceId and conversationId")
	} else {
		var ae *httpx.Error
		if !errors.As(err, &ae) || ae.Status != 404 {
			t.Fatalf("expected 404, got %v", err)
		}
	}

	// 4) Agent principal follows exact same cross-workspace & group ACLs.
	agent := httpx.User{ID: httpx.ID(), DisplayName: "SearchAgent", Kind: "agent"}
	if _, err = s.SetPrincipalWorkspaces(ctx, agent, member, []string{ws1.ID, ws2.ID}, 1); err != nil {
		t.Fatal(err)
	}
	agentAll, err := s.SearchMessages(ctx, agent.ID, "发布", "", "", "", "", "", "", "", 20)
	if err != nil || len(agentAll.Items) != 2 {
		t.Fatalf("expected agent to see ch1+ch2 (2 items) and not privateGroup, got %d err=%v", len(agentAll.Items), err)
	}
	for _, it := range agentAll.Items {
		if it.Conversation.ID == privateGroup.ID {
			t.Fatal("agent bypassed privateGroup ACL")
		}
	}

	// 5) Multi-term AND (short CJK + >=3-char trigram + LIKE metacharacters).
	multiHit, err := s.SearchMessages(ctx, member.ID, `发布 deployment 100% foo_bar a\b`, "", "", "", "", "", "", "", 20)
	if err != nil || len(multiHit.Items) != 1 || multiHit.Items[0].Message.ID != m1.ID {
		t.Fatalf("multi-term + metachar search failed: items=%+v err=%v", multiHit.Items, err)
	}

	// 6) Balanced quoted phrase matches m2 ("error code" + 500) and m3 (has "500 error code"),
	//    while reversed phrase `"code error"` matches neither.
	phraseHit, err := s.SearchMessages(ctx, member.ID, `"error code" 500`, "", "", "", "", "", "", "", 20)
	if err != nil || len(phraseHit.Items) != 2 {
		t.Fatalf("quoted phrase search expected 2 hits (m2, m3), got %d err=%v", len(phraseHit.Items), err)
	}
	phraseMiss, err := s.SearchMessages(ctx, member.ID, `"code error"`, "", "", "", "", "", "", "", 20)
	if err != nil || len(phraseMiss.Items) != 0 {
		t.Fatalf("reversed quoted phrase should not match, got %d err=%v", len(phraseMiss.Items), err)
	}

	// 7) Forwarded body search matches mForwarded, while cross-field boundary `"cross boundary"` MUST NOT match!
	fwdHit, err := s.SearchMessages(ctx, owner.ID, "原文关键字", "", ch1.ID, "", "", "", "", "", 20)
	if err != nil || len(fwdHit.Items) != 2 {
		t.Fatalf("expected source + forwarded message (2 hits), got %d err=%v", len(fwdHit.Items), err)
	}
	boundaryMiss, err := s.SearchMessages(ctx, owner.ID, `"cross boundary"`, "", ch1.ID, "", "", "", "", "", 20)
	if err != nil || len(boundaryMiss.Items) != 0 {
		t.Fatalf("cross-field boundary phrase falsely matched: %+v err=%v", boundaryMiss.Items, err)
	}

	// 8) hasFiles=true matches both direct attachment and forwarded attachment by filename (even when body lacks term).
	fileHit, err := s.SearchMessages(ctx, owner.ID, "release_report_v0411", "", ch1.ID, "", "true", "", "", "", 20)
	if err != nil || len(fileHit.Items) != 2 {
		t.Fatalf("expected 2 file hits (direct + forwarded), got %d err=%v", len(fileHit.Items), err)
	}
	foundDirect, foundFwdFile := false, false
	for _, it := range fileHit.Items {
		if it.Message.ID == mFile.ID {
			foundDirect = true
		}
		if it.Message.ID == mForwardedFile.ID {
			foundFwdFile = true
		}
	}
	if !foundDirect || !foundFwdFile {
		t.Fatalf("missing direct or forwarded file hit: direct=%v fwd=%v", foundDirect, foundFwdFile)
	}

	// 9) Filter-only query (empty q + senderId + workspaceId + after/before).
	t0 := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(2 * time.Hour)
	if _, err = s.DB.Exec(ctx, "UPDATE messages SET created_at=$1 WHERE id=$2", t0, m1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE messages SET created_at=$1 WHERE id=$2", t1, m3.ID); err != nil {
		t.Fatal(err)
	}
	filterOnly, err := s.SearchMessages(
		ctx, member.ID, "", "", "", member.ID, "",
		t1.Add(-time.Minute).Format(time.RFC3339), t1.Add(time.Minute).Format(time.RFC3339), "", 20,
	)
	if err != nil || len(filterOnly.Items) != 1 || filterOnly.Items[0].Message.ID != m3.ID {
		t.Fatalf("filter-only sender+date search mismatch: items=%+v err=%v", filterOnly.Items, err)
	}

	// 10) Retracting m2 removes it immediately from search.
	if _, err = s.ChangeMessage(ctx, member.ID, privateGroup.ID, m2.ID, "", true); err != nil {
		t.Fatal(err)
	}
	afterRetract, err := s.SearchMessages(ctx, member.ID, "密室记录", "", "", "", "", "", "", "", 20)
	if err != nil || len(afterRetract.Items) != 0 {
		t.Fatalf("retracted message should not match, got %d err=%v", len(afterRetract.Items), err)
	}

	// 11) Tied-timestamp cursor pagination across 5 messages with concurrent insertion.
	tiedAt := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if _, _, err = s.Send(ctx, owner, ch1.ID, httpx.ID(), fmt.Sprintf("分页检索项 Gamma-%d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.DB.Exec(ctx, "UPDATE messages SET created_at=$1 WHERE conversation_id=$2 AND strpos(body,'Gamma-')>0", tiedAt, ch1.ID); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	for p := 0; p < 5; p++ {
		page, err := s.SearchMessages(ctx, owner.ID, "分页检索项 gamma-", "", "", "", "", "", "", cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if p == 0 {
			if _, _, err = s.Send(ctx, owner, ch1.ID, httpx.ID(), "分页检索项 Gamma-new", nil); err != nil {
				t.Fatal(err)
			}
		}
		for _, it := range page.Items {
			if seen[it.Message.ID] {
				t.Fatalf("duplicate hit across global search pages: %s", it.Message.ID)
			}
			seen[it.Message.ID] = true
		}
		cursor = page.Cursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatalf("expected 5 tied-timestamp items across pages, got %d", len(seen))
	}

	// 12) HTTP endpoint contract check (401, 400, 404, 200).
	auth := newActiveTestIdentity(map[string]httpx.User{
		"tok-member": member,
		"tok-owner":  owner,
	})
	ts := httptest.NewServer((&Handler{Store: s, Identity: auth, Origin: "http://example.test"}).Routes())
	defer ts.Close()

	// Unauthenticated -> 401
	res, err := http.Get(ts.URL + "/api/v1/messages/search?q=发布")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("expected 401, got %d", res.StatusCode)
	}

	doGet := func(tok, rawPath string) *http.Response {
		req, err := http.NewRequest("GET", ts.URL+rawPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: "teamie_session", Value: tok})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Empty query without filters -> 400
	resp := doGet("tok-member", "/api/v1/messages/search")
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400 for empty search, got %d", resp.StatusCode)
	}

	// >8 distinct terms -> 400
	resp = doGet("tok-member", "/api/v1/messages/search?q="+url.QueryEscape("a b c d e f g h i"))
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400 for 9 terms, got %d", resp.StatusCode)
	}

	// Explicit unauthorized conversation -> 404
	resp = doGet("tok-owner", "/api/v1/messages/search?q=x&conversationId="+url.QueryEscape(privateGroup.ID))
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404 for unauthorized conversationId, got %d", resp.StatusCode)
	}

	// Valid search -> 200 with workspaceName
	resp = doGet("tok-member", "/api/v1/messages/search?q="+url.QueryEscape("发布 v0.4.11"))
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var httpPage struct {
		Items []struct {
			Message       Message               `json:"message"`
			Conversation  AttentionConversation `json:"conversation"`
			WorkspaceName string                `json:"workspaceName"`
		} `json:"items"`
		Cursor string `json:"cursor"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&httpPage); err != nil {
		t.Fatal(err)
	}
	if len(httpPage.Items) != 2 {
		t.Fatalf("expected 2 HTTP global search hits, got %d", len(httpPage.Items))
	}
	// Verify forwarded message is non-empty sentinel check.
	if mForwarded.ID == "" {
		t.Fatal("expected forwarded message ID")
	}
}

func TestConversationAttachmentGalleryAndThumbnailIsolation(t *testing.T) {
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
	privateGroup, err := s.CreateConversation(ctx, member.ID, ws.ID, "group", "设计画廊", []string{third.ID})
	if err != nil {
		t.Fatal(err)
	}

	pngRaw := encodePNG(t, 120, 80, color.NRGBA{R: 116, G: 99, B: 212, A: 255})

	// 1) Upload modern image (generates thumbnail + image dimensions).
	imgModern, err := s.UploadAttachment(ctx, member.ID, privateGroup.ID, "Mockup_Home.png", pngRaw)
	if err != nil {
		t.Fatal(err)
	}
	if imgModern.Image == nil {
		t.Fatal("expected thumbnail image metadata for uploaded PNG")
	}
	// 2) Upload legacy image (we'll clear image_width/height & thumbnail to simulate pre-Schema-12 upload).
	imgLegacy, err := s.UploadAttachment(ctx, member.ID, privateGroup.ID, "Legacy_Diagram.png", pngRaw)
	if err != nil {
		t.Fatal(err)
	}
	// 3) Upload ordinary document file.
	docFile, err := s.UploadAttachment(ctx, member.ID, privateGroup.ID, "Design_Spec.txt", []byte("design specification"))
	if err != nil {
		t.Fatal(err)
	}
	// 4) Upload pending unlinked file (must NEVER appear in gallery).
	if _, err = s.UploadAttachment(ctx, member.ID, privateGroup.ID, "Unlinked_Secret.txt", []byte("pending only")); err != nil {
		t.Fatal(err)
	}

	m1, _, err := s.SendWithAttachments(ctx, member, privateGroup.ID, httpx.ID(), "第一批素材", nil, []string{imgModern.ID, docFile.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.SendWithAttachments(ctx, member, privateGroup.ID, httpx.ID(), "历史无缩略图图片", nil, []string{imgLegacy.ID})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate pre-Schema-12 legacy image row.
	if _, err = s.DB.Exec(ctx, "DELETE FROM attachment_thumbnails WHERE attachment_id=$1", imgLegacy.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE attachments SET image_width=NULL, image_height=NULL WHERE id=$1", imgLegacy.ID); err != nil {
		t.Fatal(err)
	}

	// 5) Forward a message with an image + doc from channel into privateGroup (member has access to both).
	chImg, err := s.UploadAttachment(ctx, member.ID, channel.ID, "Forwarded_Banner.png", pngRaw)
	if err != nil {
		t.Fatal(err)
	}
	chMsg, _, err := s.SendWithAttachments(ctx, member, channel.ID, httpx.ID(), "源消息", nil, []string{chImg.ID})
	if err != nil {
		t.Fatal(err)
	}
	fwdMsg, _, err := s.App(nil, "").SendMessage(ctx, toDomainUser(member), domain.SendRequest{
		ConversationID:  privateGroup.ID,
		ClientMessageID: httpx.ID(),
		Body:            "转到设计组",
		ForwardOf:       &domain.ForwardSource{ConversationID: channel.ID, MessageID: chMsg.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Check kind=all, kind=image, kind=file form an exact exhaustive partition (total 4 published attachments).
	allPage, err := s.ListConversationAttachments(ctx, member.ID, privateGroup.ID, "all", "", "", 40)
	if err != nil || len(allPage.Items) != 4 {
		t.Fatalf("expected 4 published attachments (excluding pending), got %d err=%v", len(allPage.Items), err)
	}
	imgPage, err := s.ListConversationAttachments(ctx, member.ID, privateGroup.ID, "image", "", "", 40)
	if err != nil || len(imgPage.Items) != 3 {
		t.Fatalf("expected 3 image attachments (modern + legacy + forwarded), got %d err=%v", len(imgPage.Items), err)
	}
	filePage, err := s.ListConversationAttachments(ctx, member.ID, privateGroup.ID, "file", "", "", 40)
	if err != nil || len(filePage.Items) != 1 || filePage.Items[0].Attachment.ID != docFile.ID {
		t.Fatalf("expected 1 file attachment (docFile), got %+v err=%v", filePage.Items, err)
	}
	// Verify forwarded attachment flag & newest-first ordering.
	if !allPage.Items[0].IsForwarded || allPage.Items[0].MessageID != fwdMsg.ID || allPage.Items[0].Attachment.Filename != "Forwarded_Banner.png" {
		t.Fatalf("expected newest gallery item to be forwarded copy, got %+v", allPage.Items[0])
	}
	if allPage.Items[1].IsForwarded || allPage.Items[1].Attachment.ID != imgLegacy.ID {
		t.Fatalf("expected second item to be legacy image, got %+v", allPage.Items[1])
	}

	// Filename filter q works case-insensitively.
	qPage, err := s.ListConversationAttachments(ctx, third.ID, privateGroup.ID, "all", "design_spec", "", 40)
	if err != nil || len(qPage.Items) != 1 || qPage.Items[0].Attachment.ID != docFile.ID {
		t.Fatalf("filename filter mismatch: %+v err=%v", qPage.Items, err)
	}

	// Cursor pagination with limit=2 traverses all 4 items without skips/duplicates.
	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 4; i++ {
		page, err := s.ListConversationAttachments(ctx, third.ID, privateGroup.ID, "all", "", cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range page.Items {
			if seen[it.Attachment.ID] {
				t.Fatalf("duplicate attachment across gallery pages: %s", it.Attachment.ID)
			}
			seen[it.Attachment.ID] = true
		}
		cursor = page.Cursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 4 {
		t.Fatalf("expected 4 unique gallery attachments across pages, got %d", len(seen))
	}

	// ACL checks: workspace owner outside privateGroup and platform admin both get 404.
	if _, err = s.ListConversationAttachments(ctx, owner.ID, privateGroup.ID, "all", "", "", 40); err == nil {
		t.Fatal("workspace owner bypassed privateGroup gallery ACL")
	} else {
		var ae *httpx.Error
		if !errors.As(err, &ae) || ae.Status != 404 {
			t.Fatalf("expected 404 for owner, got %v", err)
		}
	}
	admin := httpx.User{ID: httpx.ID(), DisplayName: "Admin", Kind: "human", PlatformRole: "admin"}
	if _, err = s.ListConversationAttachments(ctx, admin.ID, privateGroup.ID, "all", "", "", 40); err == nil {
		t.Fatal("platform admin bypassed privateGroup gallery ACL")
	}

	// Disabled im.attachments.upload keeps gallery readable.
	origFlags := s.Flags
	s.Flags = writeFlagsFile(t, map[string]bool{"im.attachments.upload": false})
	if page, err := s.ListConversationAttachments(ctx, member.ID, privateGroup.ID, "all", "", "", 40); err != nil || len(page.Items) != 4 {
		t.Fatalf("disabled upload flag should preserve gallery reads: count=%d err=%v", len(page.Items), err)
	}
	s.Flags = origFlags

	// HTTP endpoint & isolated ThumbnailTransfers (4 slots) vs DefaultTransfers (2 slots).
	auth := newActiveTestIdentity(map[string]httpx.User{
		"tok-member": member,
	})
	ts := httptest.NewServer((&Handler{Store: s, Identity: auth, Origin: "http://example.test"}).Routes())
	defer ts.Close()

	doAuthGet := func(path string) *http.Response {
		req, err := http.NewRequest("GET", ts.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: "teamie_session", Value: "tok-member"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Gallery HTTP GET returns 200 with expected items.
	gResp := doAuthGet(fmt.Sprintf("/api/v1/conversations/%s/attachments?kind=image&limit=10", privateGroup.ID))
	defer gResp.Body.Close()
	if gResp.StatusCode != 200 {
		t.Fatalf("expected 200 for gallery GET, got %d", gResp.StatusCode)
	}
	var galleryResp struct {
		Items []struct {
			Attachment  Attachment `json:"attachment"`
			MessageID   string     `json:"messageId"`
			MessageSeq  int64      `json:"messageSeq"`
			IsForwarded bool       `json:"isForwarded"`
		} `json:"items"`
	}
	if err = json.NewDecoder(gResp.Body).Decode(&galleryResp); err != nil {
		t.Fatal(err)
	}
	if len(galleryResp.Items) != 3 {
		t.Fatalf("expected 3 HTTP gallery image items, got %d", len(galleryResp.Items))
	}

	// Fill all 4 thumbnailTransfers slots -> 5th thumbnail returns 429, while /content STILL returns 200!
	if cap(thumbnailTransfers) != 4 {
		t.Fatalf("expected DefaultThumbnailTransfers capacity 4, got %d", cap(thumbnailTransfers))
	}
	for i := 0; i < cap(thumbnailTransfers); i++ {
		thumbnailTransfers <- struct{}{}
	}
	thumbBusyResp := doAuthGet(fmt.Sprintf("/api/v1/conversations/%s/attachments/%s/thumbnail", privateGroup.ID, imgModern.ID))
	thumbBusyResp.Body.Close()
	contentOkResp := doAuthGet(fmt.Sprintf("/api/v1/conversations/%s/attachments/%s/content", privateGroup.ID, imgModern.ID))
	contentOkResp.Body.Close()
	for i := 0; i < cap(thumbnailTransfers); i++ {
		<-thumbnailTransfers
	}
	if thumbBusyResp.StatusCode != 429 {
		t.Fatalf("expected 429 when all 4 thumbnail slots are held, got %d", thumbBusyResp.StatusCode)
	}
	if contentOkResp.StatusCode != 200 {
		t.Fatalf("saturated thumbnail slots must NOT block content downloads; got %d", contentOkResp.StatusCode)
	}

	// Fill both heavy attachmentTransfers slots -> /content returns 429, while /thumbnail STILL returns 200!
	for i := 0; i < cap(attachmentTransfers); i++ {
		attachmentTransfers <- struct{}{}
	}
	contentBusyResp := doAuthGet(fmt.Sprintf("/api/v1/conversations/%s/attachments/%s/content", privateGroup.ID, imgModern.ID))
	contentBusyResp.Body.Close()
	thumbOkResp := doAuthGet(fmt.Sprintf("/api/v1/conversations/%s/attachments/%s/thumbnail", privateGroup.ID, imgModern.ID))
	thumbOkResp.Body.Close()
	for i := 0; i < cap(attachmentTransfers); i++ {
		<-attachmentTransfers
	}
	if contentBusyResp.StatusCode != 429 {
		t.Fatalf("expected 429 when heavy transfer slots are held, got %d", contentBusyResp.StatusCode)
	}
	if thumbOkResp.StatusCode != 200 {
		t.Fatalf("saturated heavy transfer slots must NOT block thumbnail reads; got %d", thumbOkResp.StatusCode)
	}

	// Retracting m1 removes imgModern and docFile from gallery immediately.
	if _, err = s.ChangeMessage(ctx, member.ID, privateGroup.ID, m1.ID, "", true); err != nil {
		t.Fatal(err)
	}
	afterRetract, err := s.ListConversationAttachments(ctx, member.ID, privateGroup.ID, "all", "", "", 40)
	if err != nil || len(afterRetract.Items) != 2 {
		t.Fatalf("expected 2 attachments after retracting m1, got %d err=%v", len(afterRetract.Items), err)
	}
}

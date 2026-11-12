package chat

import (
	"context"
	"image/color"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"teamie/pkg/httpx"
)

func TestGroupDeletionACLChannelDirectAndDisabledCreateFlag(t *testing.T) {
	s, ws, channel, owner, creator := setup(t)
	ctx := context.Background()

	member := httpx.User{ID: httpx.ID(), DisplayName: "Member", Kind: "human"}
	platformAdmin := httpx.User{ID: httpx.ID(), DisplayName: "Admin", Kind: "human", PlatformRole: "admin"}
	outsider := httpx.User{ID: httpx.ID(), DisplayName: "Outsider", Kind: "human"}

	if _, err := s.DB.Exec(ctx, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES ($1,$2,'member'),($1,$3,'member')`, ws.ID, member.ID, platformAdmin.ID); err != nil {
		t.Fatal(err)
	}

	direct, err := s.CreateConversation(ctx, creator.ID, ws.ID, "direct", "", []string{member.ID})
	if err != nil {
		t.Fatal(err)
	}
	group, err := s.CreateConversation(ctx, creator.ID, ws.ID, "group", "Sprint Room", []string{member.ID, platformAdmin.ID})
	if err != nil {
		t.Fatal(err)
	}

	idDir := newActiveTestIdentity(map[string]httpx.User{
		"tok-owner":    owner,
		"tok-creator":  creator,
		"tok-member":   member,
		"tok-admin":    platformAdmin,
		"tok-outsider": outsider,
	})
	origin := "https://app.example.test"
	srv := httptest.NewServer((&Handler{Store: s, Identity: idDir, Origin: origin}).Routes())
	defer srv.Close()
	cli := &collabHTTPClient{t: t, srv: srv, origin: origin}

	// Non-workspace member gets 404 without revealing private group existence.
	status, _ := cli.json(http.MethodDelete, "/api/v1/conversations/"+group.ID, "tok-outsider", nil)
	if status != http.StatusNotFound {
		t.Fatalf("expected 404 for workspace non-member, got %d", status)
	}

	// Channels and direct chats cannot be deleted.
	for _, nonGroupID := range []string{channel.ID, direct.ID} {
		status, out := cli.json(http.MethodDelete, "/api/v1/conversations/"+nonGroupID, "tok-owner", nil)
		if status != http.StatusBadRequest {
			t.Fatalf("expected 400 invalid_conversation for %s, got %d (%v)", nonGroupID, status, out)
		}
	}

	// Ordinary member and platform admin (not owner/creator) get 403.
	for _, tok := range []string{"tok-member", "tok-admin"} {
		status, _ = cli.json(http.MethodDelete, "/api/v1/conversations/"+group.ID, tok, nil)
		if status != http.StatusForbidden {
			t.Fatalf("expected 403 for %s, got %d", tok, status)
		}
	}

	// Removed creator cannot dissolve the group; non-recipient workspace owner still can.
	if _, err = s.GroupMember(ctx, owner.ID, group.ID, creator.ID, false); err != nil {
		t.Fatal(err)
	}
	status, _ = cli.json(http.MethodDelete, "/api/v1/conversations/"+group.ID, "tok-creator", nil)
	if status != http.StatusForbidden {
		t.Fatalf("expected 403 for removed creator, got %d", status)
	}
	status, _ = cli.json(http.MethodDelete, "/api/v1/conversations/"+group.ID, "tok-owner", nil)
	if status != http.StatusNoContent {
		t.Fatalf("expected 204 for non-recipient workspace owner, got %d", status)
	}
	// Repeating delete on a dissolved group returns 404.
	status, _ = cli.json(http.MethodDelete, "/api/v1/conversations/"+group.ID, "tok-owner", nil)
	if status != http.StatusNotFound {
		t.Fatalf("expected 404 on second delete, got %d", status)
	}

	// Both former recipients and the non-recipient workspace owner receive membership.changed.
	for _, uid := range []string{member.ID, platformAdmin.ID, owner.ID} {
		events, _, _, syncErr := s.Sync(ctx, uid, "0", 100)
		if syncErr != nil {
			t.Fatal(syncErr)
		}
		found := false
		for _, ev := range events {
			if ev.Type == "membership.changed" && ev.ConversationID == group.ID && ev.WorkspaceID == ws.ID {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected membership.changed for user %s on dissolved group", uid)
		}
	}

	// Disabled im.group.create must not block dissolving an existing group by its current creator.
	group2, err := s.CreateConversation(ctx, creator.ID, ws.ID, "group", "Cleanup When Disabled", []string{member.ID})
	if err != nil {
		t.Fatal(err)
	}
	s.Flags = writeFlagsFile(t, map[string]bool{"im.group.create": false})
	status, _ = cli.json(http.MethodDelete, "/api/v1/conversations/"+group2.ID, "tok-creator", nil)
	if status != http.StatusNoContent {
		t.Fatalf("expected 204 when im.group.create=false, got %d", status)
	}
}

func TestGroupDeletionCleansAllArtifactsPreservesReceiptsAndForwards(t *testing.T) {
	s, ws, channel, owner, member := setup(t)
	ctx := context.Background()

	idDir := newActiveTestIdentity(map[string]httpx.User{
		"tok-owner":  owner,
		"tok-member": member,
	})
	origin := "https://app.example.test"
	srv := httptest.NewServer((&Handler{Store: s, Identity: idDir, Origin: origin}).Routes())
	defer srv.Close()
	cli := &collabHTTPClient{t: t, srv: srv, origin: origin}

	extra := httpx.User{ID: httpx.ID(), DisplayName: "Extra", Kind: "human"}
	if _, err := s.DB.Exec(ctx, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES ($1,$2,'member')`, ws.ID, extra.ID); err != nil {
		t.Fatal(err)
	}

	group, err := s.App(nil, "").CreateConversationWithKey(ctx, owner.ID, ws.ID, "group", "Artifact Group", []string{member.ID}, "create-key-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.App(nil, "").AddConversationMemberWithKey(ctx, owner.ID, group.ID, extra.ID, "add-key-1"); err != nil {
		t.Fatal(err)
	}

	pngBytes := encodePNG(t, 48, 32, color.NRGBA{R: 20, G: 120, B: 220, A: 255})
	upStatus, upResp := cli.upload(group.ID, "tok-owner", "diagram.png", pngBytes, "upload-key-1")
	if upStatus != http.StatusCreated {
		t.Fatalf("upload failed: %d %v", upStatus, upResp)
	}
	attMap := upResp["attachment"].(map[string]any)
	attID := attMap["id"].(string)

	// Send root message with image + mention, plus a reply message.
	sendStatus, sendResp := cli.json(http.MethodPost, "/api/v1/conversations/"+group.ID+"/messages", "tok-owner", map[string]any{
		"clientMessageId": "msg-root-1",
		"body":            "@Member secret design spec",
		"attachmentIds":   []string{attID},
		"mentions": []map[string]any{
			{"userId": member.ID, "start": 0, "end": 7},
		},
	})
	if sendStatus != http.StatusCreated {
		t.Fatalf("send root failed: %d %v", sendStatus, sendResp)
	}
	rootMsg := sendResp["message"].(map[string]any)
	rootID := rootMsg["id"].(string)

	replyStatus, replyResp := cli.json(http.MethodPost, "/api/v1/conversations/"+group.ID+"/messages", "tok-member", map[string]any{
		"clientMessageId": "msg-reply-1",
		"body":            "Looks good to me",
		"replyToId":       rootID,
	})
	if replyStatus != http.StatusCreated {
		t.Fatalf("send reply failed: %d %v", replyStatus, replyResp)
	}

	// Reaction, Pin, Save, Notification level, Read cursor + Manual unread marker.
	if st, _ := cli.json(http.MethodPut, "/api/v1/conversations/"+group.ID+"/messages/"+rootID+"/reactions/%F0%9F%91%8D", "tok-member", nil); st != http.StatusOK {
		t.Fatalf("put reaction status=%d", st)
	}
	if st, _ := cli.json(http.MethodPut, "/api/v1/conversations/"+group.ID+"/pins/"+rootID, "tok-owner", nil); st != http.StatusOK {
		t.Fatalf("put pin status=%d", st)
	}
	if st, _ := cli.json(http.MethodPut, "/api/v1/conversations/"+group.ID+"/messages/"+rootID+"/saved", "tok-member", nil); st != http.StatusOK {
		t.Fatalf("save message status=%d", st)
	}
	if st, _ := cli.json(http.MethodPut, "/api/v1/conversations/"+group.ID+"/notification-level", "tok-member", map[string]any{"level": "mentions"}); st != http.StatusOK {
		t.Fatalf("set notification level status=%d", st)
	}
	if st, _ := cli.json(http.MethodPost, "/api/v1/conversations/"+group.ID+"/read", "tok-member", map[string]any{"seq": 2}); st != http.StatusOK {
		t.Fatalf("mark read status=%d", st)
	}
	if st, _ := cli.json(http.MethodPost, "/api/v1/conversations/"+group.ID+"/unread", "tok-member", map[string]any{"seq": 1}); st != http.StatusOK {
		t.Fatalf("mark unread status=%d", st)
	}

	// Forward the root message + attachment into #general BEFORE dissolving the group.
	fwdStatus, fwdResp := cli.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", "tok-owner", map[string]any{
		"clientMessageId": "fwd-to-channel-1",
		"body":            "Archived into general",
		"forwardOf": map[string]any{
			"conversationId": group.ID,
			"messageId":      rootID,
		},
	})
	if fwdStatus != http.StatusCreated {
		t.Fatalf("forward failed: %d %v", fwdStatus, fwdResp)
	}
	fwdMsg := fwdResp["message"].(map[string]any)
	fwdSnap := fwdMsg["forwarded"].(map[string]any)
	fwdAtts := fwdSnap["attachments"].([]any)
	copiedAttID := fwdAtts[0].(map[string]any)["id"].(string)

	// Dissolve the group!
	delStatus, _ := cli.json(http.MethodDelete, "/api/v1/conversations/"+group.ID, "tok-owner", nil)
	if delStatus != http.StatusNoContent {
		t.Fatalf("delete group failed: %d", delStatus)
	}

	// 1. All group-owned rows are physically deleted.
	var remaining int
	if err = s.DB.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM conversations WHERE id=$1) +
		(SELECT count(*) FROM conversation_members WHERE conversation_id=$1) +
		(SELECT count(*) FROM messages WHERE conversation_id=$1) +
		(SELECT count(*) FROM attachments WHERE conversation_id=$1) +
		(SELECT count(*) FROM attachment_thumbnails WHERE attachment_id=$2) +
		(SELECT count(*) FROM message_reactions WHERE conversation_id=$1) +
		(SELECT count(*) FROM conversation_pins WHERE conversation_id=$1) +
		(SELECT count(*) FROM saved_messages WHERE conversation_id=$1) +
		(SELECT count(*) FROM read_cursors WHERE conversation_id=$1) +
		(SELECT count(*) FROM conversation_notification_levels WHERE conversation_id=$1)`,
		group.ID, attID,
	).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("expected 0 remaining group rows, got %d (err=%v)", remaining, err)
	}

	// 2. Idempotency receipts survive: retries never recreate a deleted group or reuse spent upload keys.
	_, err = s.App(nil, "").CreateConversationWithKey(ctx, owner.ID, ws.ID, "group", "Artifact Group", []string{member.ID}, "create-key-1")
	requireGroupError(t, compatErr(err), 404, "not_found")
	_, err = s.App(nil, "").CreateConversationWithKey(ctx, owner.ID, ws.ID, "group", "Different Name", []string{member.ID}, "create-key-1")
	requireGroupError(t, compatErr(err), 409, "idempotency_conflict")

	_, err = s.App(nil, "").AddConversationMemberWithKey(ctx, owner.ID, group.ID, extra.ID, "add-key-1")
	requireGroupError(t, compatErr(err), 404, "not_found")
	_, err = s.App(nil, "").AddConversationMemberWithKey(ctx, owner.ID, channel.ID, extra.ID, "add-key-1")
	requireGroupError(t, compatErr(err), 409, "idempotency_conflict")

	// Spent upload_key on another conversation still returns 409 upload_conflict.
	conflictStatus, _ := cli.upload(channel.ID, "tok-owner", "diagram.png", pngBytes, "upload-key-1")
	if conflictStatus != http.StatusConflict {
		t.Fatalf("expected 409 upload_conflict when reusing spent upload_key on another conversation, got %d", conflictStatus)
	}

	// 3. Forwarded snapshot & copied attachment/thumbnail in #general remain readable and downloadable.
	dlStatus, _, dlBytes := cli.rawGet("/api/v1/conversations/"+channel.ID+"/attachments/"+copiedAttID+"/content", "tok-member")
	if dlStatus != http.StatusOK || len(dlBytes) != len(pngBytes) {
		t.Fatalf("expected copied forward attachment to remain downloadable, got status=%d bytes=%d", dlStatus, len(dlBytes))
	}
	thumbStatus, _, thumbBytes := cli.rawGet("/api/v1/conversations/"+channel.ID+"/attachments/"+copiedAttID+"/thumbnail", "tok-member")
	if thumbStatus != http.StatusOK || len(thumbBytes) == 0 {
		t.Fatalf("expected copied forward thumbnail to remain downloadable, got status=%d bytes=%d", thumbStatus, len(thumbBytes))
	}
	// Original attachment download returns 404.
	origStatus, _, _ := cli.rawGet("/api/v1/conversations/"+group.ID+"/attachments/"+attID+"/content", "tok-member")
	if origStatus != http.StatusNotFound {
		t.Fatalf("expected 404 on dissolved group attachment, got %d", origStatus)
	}

	// 4. Sync withholds all group history while emitting membership.changed; Attention, Saved, Search omit group.
	events, _, _, err := s.Sync(ctx, member.ID, "0", 100)
	if err != nil {
		t.Fatal(err)
	}
	sawMembershipChanged := false
	for _, ev := range events {
		if ev.ConversationID == group.ID {
			if ev.Type != "membership.changed" {
				t.Fatalf("unexpected leaked sync event from dissolved group: %+v", ev)
			}
			sawMembershipChanged = true
		}
	}
	if !sawMembershipChanged {
		t.Fatal("expected membership.changed sync event for dissolved group")
	}

	attPage, err := s.Attention(ctx, member.ID, "", 50, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range attPage.Items {
		if it.Conversation.ID == group.ID {
			t.Fatal("dissolved group leaked into attention items")
		}
	}
	for _, u := range attPage.AttentionUnreadByConversation {
		if u.ConversationID == group.ID {
			t.Fatal("dissolved group leaked into attentionUnreadByConversation")
		}
	}

	_, savedResp := cli.json(http.MethodGet, "/api/v1/saved", "tok-member", nil)
	if items, _ := savedResp["items"].([]any); len(items) != 0 {
		t.Fatalf("expected saved items from dissolved group to be removed, got %v", items)
	}

	searchPage, err := s.SearchWorkspaceMessages(ctx, member.ID, ws.ID, "secret design spec", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range searchPage.Items {
		if it.Conversation.ID == group.ID {
			t.Fatal("dissolved group leaked into workspace search")
		}
	}
	if len(searchPage.Items) != 1 || searchPage.Items[0].Conversation.ID != channel.ID {
		t.Fatalf("expected forwarded copy in #general to remain searchable, got %+v", searchPage.Items)
	}
}

func TestOrdinaryMemberSelfLeaveAndConcurrentGroupDissolution(t *testing.T) {
	s, ws, channel, owner, creator := setup(t)
	ctx := context.Background()

	member := httpx.User{ID: httpx.ID(), DisplayName: "Member", Kind: "human"}
	outsiderInWS := httpx.User{ID: httpx.ID(), DisplayName: "OutsiderInWS", Kind: "human"}
	agent := httpx.User{ID: httpx.ID(), DisplayName: "HelperAgent", Kind: "agent"}

	if _, err := s.DB.Exec(ctx, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES ($1,$2,'member'),($1,$3,'member'),($1,$4,'member')`, ws.ID, member.ID, outsiderInWS.ID, agent.ID); err != nil {
		t.Fatal(err)
	}

	group, err := s.CreateConversation(ctx, creator.ID, ws.ID, "group", "Self Leave Room", []string{member.ID, agent.ID, owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	msg, _, err := s.Send(ctx, creator, group.ID, "m1", "Welcome everyone", nil)
	if err != nil {
		t.Fatal(err)
	}

	idDir := newActiveTestIdentity(map[string]httpx.User{
		"tok-owner":    owner,
		"tok-creator":  creator,
		"tok-member":   member,
		"tok-outsider": outsiderInWS,
		"tok-agent":    agent,
	})
	origin := "https://app.example.test"
	srv := httptest.NewServer((&Handler{Store: s, Identity: idDir, Origin: origin}).Routes())
	defer srv.Close()
	cli := &collabHTTPClient{t: t, srv: srv, origin: origin}

	// Prepare member private state (saved item, notification level, manual unread).
	if st, _ := cli.json(http.MethodPut, "/api/v1/conversations/"+group.ID+"/messages/"+msg.ID+"/saved", "tok-member", nil); st != http.StatusOK {
		t.Fatalf("save failed: %d", st)
	}
	if st, _ := cli.json(http.MethodPut, "/api/v1/conversations/"+group.ID+"/notification-level", "tok-member", map[string]any{"level": "none"}); st != http.StatusOK {
		t.Fatalf("notification level failed: %d", st)
	}
	if st, _ := cli.json(http.MethodPost, "/api/v1/conversations/"+group.ID+"/unread", "tok-member", map[string]any{"seq": 1}); st != http.StatusOK {
		t.Fatalf("mark unread failed: %d", st)
	}

	// Non-member workspace participant calling self-leave gets 403 without leaking group info or emitting sync events.
	var eventsBefore int
	if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM sync_events`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	if st, out := cli.json(http.MethodDelete, "/api/v1/conversations/"+group.ID+"/members/"+outsiderInWS.ID, "tok-outsider", nil); st != http.StatusForbidden || out["conversation"] != nil {
		t.Fatalf("expected 403 with no leaked conversation for non-member self-leave, got %d %v", st, out)
	}
	var eventsAfter int
	if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM sync_events`).Scan(&eventsAfter); err != nil || eventsAfter != eventsBefore {
		t.Fatalf("denied self-leave emitted sync events: before=%d after=%d", eventsBefore, eventsAfter)
	}

	// Ordinary member still cannot remove someone else.
	if st, _ := cli.json(http.MethodDelete, "/api/v1/conversations/"+group.ID+"/members/"+agent.ID, "tok-member", nil); st != http.StatusForbidden {
		t.Fatalf("expected 403 when ordinary member removes another user, got %d", st)
	}

	// Ordinary member leaves successfully even when im.group.create is disabled!
	s.Flags = writeFlagsFile(t, map[string]bool{"im.group.create": false})
	st, leaveResp := cli.json(http.MethodDelete, "/api/v1/conversations/"+group.ID+"/members/"+member.ID, "tok-member", nil)
	if st != http.StatusOK {
		t.Fatalf("expected 200 on ordinary member self-leave, got %d (%v)", st, leaveResp)
	}
	convDTO := leaveResp["conversation"].(map[string]any)
	if convDTO["canManageMembers"] != false {
		t.Fatalf("expected canManageMembers=false on ordinary self-leave, got %v", convDTO["canManageMembers"])
	}
	s.Flags = httpx.Flags{}

	// Member's private state (saved_messages, notification_level, manual_unread_seq) was cleaned up and history is inaccessible.
	var cleaned int
	if err = s.DB.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM conversation_members WHERE conversation_id=$1 AND user_id=$2) +
		(SELECT count(*) FROM saved_messages WHERE conversation_id=$1 AND user_id=$2) +
		(SELECT count(*) FROM conversation_notification_levels WHERE conversation_id=$1 AND user_id=$2) +
		(SELECT count(*) FROM read_cursors WHERE conversation_id=$1 AND user_id=$2 AND manual_unread_seq IS NOT NULL)`,
		group.ID, member.ID,
	).Scan(&cleaned); err != nil || cleaned != 0 {
		t.Fatalf("expected self-leave to clean private state, remaining=%d err=%v", cleaned, err)
	}
	if _, _, err = s.Messages(ctx, member.ID, group.ID, 0, 50); err == nil {
		t.Fatal("left member still read group history")
	}

	// Agent principal can also leave under identical IM rules.
	if agentConv, leaveErr := s.GroupMember(ctx, agent.ID, group.ID, agent.ID, false); leaveErr != nil || agentConv.CanManageMembers {
		t.Fatalf("expected agent self-leave to succeed with CanManageMembers=false, got %+v err=%v", agentConv, leaveErr)
	}

	// Non-owner creator leaving loses management & dissolution rights; workspace owner can still dissolve.
	creatorLeft, err := s.GroupMember(ctx, creator.ID, group.ID, creator.ID, false)
	if err != nil || creatorLeft.CanManageMembers {
		t.Fatalf("expected creator self-leave to clear CanManageMembers, got %+v err=%v", creatorLeft, err)
	}
	requireGroupError(t, s.DeleteConversation(ctx, creator.ID, group.ID), 403, "forbidden")
	if err = s.DeleteConversation(ctx, owner.ID, group.ID); err != nil {
		t.Fatalf("expected workspace owner to dissolve group after creator left, got %v", err)
	}

	// Concurrent stress: Send, Forward, Attention, WorkspaceSearch racing with DeleteConversation.
	raceGroup, err := s.CreateConversation(ctx, creator.ID, ws.ID, "group", "Race Group", []string{member.ID, owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	seedMsg, _, err := s.Send(ctx, creator, raceGroup.ID, "race-seed", "Race searchable payload", nil)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(5)
	go func() {
		defer wg.Done()
		<-start
		_, _, _ = s.Send(ctx, member, raceGroup.ID, "race-send", "Concurrent send", nil)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, _ = cli.json(http.MethodPost, "/api/v1/conversations/"+channel.ID+"/messages", "tok-member", map[string]any{
			"clientMessageId": "race-fwd",
			"body":            "Concurrent forward",
			"forwardOf":       map[string]any{"conversationId": raceGroup.ID, "messageId": seedMsg.ID},
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		_, _ = s.Attention(ctx, member.ID, "", 50, false)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, _ = s.SearchWorkspaceMessages(ctx, member.ID, ws.ID, "Race searchable", "", 20)
	}()
	var deleteErr error
	go func() {
		defer wg.Done()
		<-start
		deleteErr = s.DeleteConversation(ctx, creator.ID, raceGroup.ID)
	}()
	close(start)
	wg.Wait()
	if deleteErr != nil {
		t.Fatalf("concurrent DeleteConversation failed: %v", deleteErr)
	}
	var raceRemaining int
	if err = s.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM conversations WHERE id=$1)+(SELECT count(*) FROM messages WHERE conversation_id=$1)`, raceGroup.ID).Scan(&raceRemaining); err != nil || raceRemaining != 0 {
		t.Fatalf("expected 0 rows after concurrent DeleteConversation, got %d err=%v", raceRemaining, err)
	}
}

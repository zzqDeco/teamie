package pg_test

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"teamie/pkg/postgres"
	"teamie/services/chat/internal/domain"
	"teamie/services/chat/internal/infra/pg"
)

func TestEvaluatePushRecipients_SQLRules(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL_TEST")
	if dbURL == "" {
		t.Skip("DATABASE_URL_TEST not set")
	}
	ctx := context.Background()
	pool, err := postgres.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pg.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repo := pg.New(postgres.New(pool))

	wsID := "push-ws-" + domain.NewID()
	groupID := "push-group-" + domain.NewID()
	directID := "push-dm-" + domain.NewID()
	chID := "push-ch-" + domain.NewID()
	sender := "push-sender-" + domain.NewID()
	uAll := "push-all-" + domain.NewID()
	uMentions := "push-mentions-" + domain.NewID()
	uNone := "push-none-" + domain.NewID()
	uRead := "push-read-" + domain.NewID()
	uOutsider := "push-outsider-" + domain.NewID()
	uReplied := "push-replied-" + domain.NewID()

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM workspaces WHERE id=$1", wsID)
	})

	_, err = repo.InsertWorkspace(ctx, domain.Workspace{ID: wsID, Name: "PushTest", CreatedBy: sender})
	if err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{sender, uAll, uMentions, uNone, uRead, uReplied} {
		if _, err := repo.InsertWorkspaceMember(ctx, wsID, uid, "member", nil, true); err != nil {
			t.Fatal(err)
		}
	}

	// Group conversation: ordinary messages wake both 'all' and 'mentions' levels,
	// while excluding sender, 'none' (muted), already-read members, and outsiders.
	if _, err := repo.InsertConversation(ctx, domain.Conversation{ID: groupID, WorkspaceID: wsID, Type: "group", Name: "G", CreatedBy: sender}, ""); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{sender, uAll, uMentions, uNone, uRead} {
		if _, err := repo.InsertConversationMember(ctx, groupID, uid, true); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = repo.SetNotificationLevel(ctx, groupID, uMentions, "mentions")
	_, _ = repo.SetNotificationLevel(ctx, groupID, uNone, "none")
	_ = repo.UpsertReadCursor(ctx, groupID, uRead, 5)

	msgID := domain.NewID()
	if _, err := repo.InsertMessage(ctx, domain.Message{
		ID: msgID, ConversationID: groupID, SenderID: sender, SenderName: "Sender", SenderKind: "human",
		ClientMessageID: domain.NewID(), Seq: 5, Body: "hello group",
	}, domain.HashString("hello group"), nil); err != nil {
		t.Fatal(err)
	}
	gotWS, eligible, err := repo.EvaluatePushRecipients(ctx, groupID, msgID, []string{sender, uAll, uMentions, uNone, uRead, uOutsider})
	if err != nil {
		t.Fatal(err)
	}
	wantGroup := []string{uAll, uMentions}
	slices.Sort(wantGroup)
	if gotWS != wsID || !slices.Equal(eligible, wantGroup) {
		t.Fatalf("group ordinary message expected %v, got ws=%s eligible=%v", wantGroup, gotWS, eligible)
	}

	// Direct conversation: ordinary messages wake 'mentions' mode and remain muted in 'none' mode.
	if _, err := repo.InsertConversation(ctx, domain.Conversation{ID: directID, WorkspaceID: wsID, Type: "direct", Name: "", CreatedBy: sender}, sender+":"+uMentions); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{sender, uMentions} {
		if _, err := repo.InsertConversationMember(ctx, directID, uid, true); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = repo.SetNotificationLevel(ctx, directID, uMentions, "mentions")
	dmMsgID := domain.NewID()
	if _, err := repo.InsertMessage(ctx, domain.Message{
		ID: dmMsgID, ConversationID: directID, SenderID: sender, SenderName: "Sender", SenderKind: "human",
		ClientMessageID: domain.NewID(), Seq: 1, Body: "hello dm",
	}, domain.HashString("hello dm"), nil); err != nil {
		t.Fatal(err)
	}
	gotWS, eligible, err = repo.EvaluatePushRecipients(ctx, directID, dmMsgID, []string{sender, uMentions, uOutsider})
	if err != nil {
		t.Fatal(err)
	}
	if gotWS != wsID || !slices.Equal(eligible, []string{uMentions}) {
		t.Fatalf("direct ordinary message in mentions mode expected [%s], got ws=%s eligible=%v", uMentions, gotWS, eligible)
	}
	_, _ = repo.SetNotificationLevel(ctx, directID, uMentions, "none")
	_, eligible, err = repo.EvaluatePushRecipients(ctx, directID, dmMsgID, []string{sender, uMentions})
	if err != nil || len(eligible) != 0 {
		t.Fatalf("direct ordinary message in none mode expected [], got %v err=%v", eligible, err)
	}

	// Channel conversation: plain message wakes nobody; @mention + reply wakes only mentioned & replied (and not muted).
	if _, err := repo.InsertConversation(ctx, domain.Conversation{ID: chID, WorkspaceID: wsID, Type: "channel", Name: "general", CreatedBy: sender}, ""); err != nil {
		t.Fatal(err)
	}
	_, _ = repo.SetNotificationLevel(ctx, chID, uNone, "none")
	parentID := domain.NewID()
	if _, err := repo.InsertMessage(ctx, domain.Message{
		ID: parentID, ConversationID: chID, SenderID: uReplied, SenderName: "Replied", SenderKind: "human",
		ClientMessageID: domain.NewID(), Seq: 1, Body: "parent",
	}, domain.HashString("parent"), nil); err != nil {
		t.Fatal(err)
	}
	chMsgID := domain.NewID()
	if _, err := repo.InsertMessage(ctx, domain.Message{
		ID: chMsgID, ConversationID: chID, SenderID: sender, SenderName: "Sender", SenderKind: "human",
		ClientMessageID: domain.NewID(), Seq: 2, Body: "@m", ReplyToID: &parentID,
	}, domain.HashString("@m"), nil); err != nil {
		t.Fatal(err)
	}
	mentions := []domain.Mention{{UserID: uMentions, Start: 0, End: 2}, {UserID: uNone, Start: 0, End: 2}}
	if err := repo.SetMessageInitialMentions(ctx, chMsgID, mentions, mentions, nil); err != nil {
		t.Fatal(err)
	}
	_, eligible, err = repo.EvaluatePushRecipients(ctx, chID, chMsgID, []string{sender, uAll, uMentions, uNone, uReplied, uOutsider})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{uMentions, uReplied}
	slices.Sort(want)
	if !slices.Equal(eligible, want) {
		t.Fatalf("channel mention+reply expected %v, got %v", want, eligible)
	}

	// Retracted message yields zero recipients.
	if err := repo.RetractMessageAndDeleteAttachments(ctx, chMsgID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_, eligible, err = repo.EvaluatePushRecipients(ctx, chID, chMsgID, []string{uMentions, uReplied})
	if err != nil || len(eligible) != 0 {
		t.Fatalf("expected 0 recipients after retraction, got %v err=%v", eligible, err)
	}
}

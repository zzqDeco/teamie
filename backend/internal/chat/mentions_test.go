package chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"teamie/backend/internal/config"
	"teamie/backend/internal/database"
	"teamie/backend/internal/testdb"
	"teamie/backend/pkg/imtext"
)

func TestMentionsConcurrentSendEditRetractionAndReplay(t *testing.T) {
	s, _, c, owner, member := setup(t)
	ctx := context.Background()
	body := "😀 @同名伙伴 请处理文件"
	mentions := []imtext.Mention{{UserID: member.ID, Start: 3, End: 8}}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			m, _, err := s.SendAnnotated(ctx, owner, c.ID, "mention-once", body, nil, nil, mentions)
			if err != nil || !reflect.DeepEqual(m.Mentions, mentions) {
				t.Error(m, err)
			}
		})
	}
	group.Wait()
	m, first, err := s.SendAnnotated(ctx, owner, c.ID, "mention-once", body, nil, nil, mentions)
	if err != nil || first {
		t.Fatal(m, first, err)
	}
	_, _, err = s.SendAnnotated(ctx, owner, c.ID, "mention-once", body, nil, nil, []imtext.Mention{{UserID: owner.ID, Start: 3, End: 8}})
	uploadError(t, err, 409, "idempotency_conflict")
	_, _, err = s.Send(ctx, owner, c.ID, "mention-once", body, nil)
	uploadError(t, err, 409, "idempotency_conflict")
	// Editing does not change the original retry identity.
	edited, err := s.ChangeAnnotatedMessage(ctx, owner.ID, c.ID, m.ID, "@同名伙伴 已补充要求", false, []imtext.Mention{{UserID: member.ID, Start: 0, End: 5}})
	if err != nil || len(edited.Mentions) != 1 || edited.Mentions[0].Start != 0 {
		t.Fatal(edited, err)
	}
	retry, _, err := s.SendAnnotated(ctx, owner, c.ID, "mention-once", body, nil, nil, mentions)
	if err != nil || retry.Body != edited.Body || !reflect.DeepEqual(retry.Mentions, edited.Mentions) {
		t.Fatal(retry, err)
	}
	// A committed retry stays readable when annotation writes are disabled.
	flags := filepath.Join(t.TempDir(), "flags.json")
	if err = os.WriteFile(flags, []byte(`{"revision":"mentions-off","flags":{"im.message.mentions":false,"im.message.retract":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	s.Flags = config.Flags{Path: flags}
	if _, _, err = s.SendAnnotated(ctx, owner, c.ID, "mention-once", body, nil, nil, mentions); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.SendAnnotated(ctx, owner, c.ID, "new-disabled-mention", body, nil, nil, mentions)
	uploadError(t, err, 403, "feature_disabled")
	history, _, err := s.Messages(ctx, member.ID, c.ID, 0, 50)
	if err != nil || len(history) != 1 || len(history[0].Mentions) != 1 {
		t.Fatal(history, err)
	}
	if _, err = s.ChangeMessage(ctx, owner.ID, c.ID, m.ID, "", true); err != nil {
		t.Fatal(err)
	}
	events, _, _, err := s.Sync(ctx, member.ID, "0", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Message != nil && event.Message.ID == m.ID && (event.Message.Body != "" || len(event.Message.Mentions) != 0) {
			t.Fatal("old sync event resurrected a retracted mention", event)
		}
	}
}

func TestMentionsValidateUTF16AndCurrentConversationMembership(t *testing.T) {
	s, ws, c, owner, member := setup(t)
	ctx := context.Background()
	for i, mentions := range [][]imtext.Mention{
		{{UserID: member.ID, Start: 1, End: 8}}, // inside the emoji surrogate pair
		{{UserID: member.ID, Start: 3, End: 999}},
		{{UserID: member.ID, Start: 3, End: 8}, {UserID: owner.ID, Start: 3, End: 8}},
		{{UserID: "unknown", Start: 3, End: 8}},
		{{UserID: "", Start: 3, End: 8}},
		{{UserID: member.ID, Start: 4, End: 8}},
	} {
		_, _, err := s.SendAnnotated(ctx, owner, c.ID, string(rune('a'+i)), "😀 @同名伙伴", nil, nil, mentions)
		uploadError(t, err, 400, "invalid_mentions")
	}
	if _, err := s.DB.Exec(ctx, "INSERT INTO workspace_members(workspace_id,user_id,role) VALUES($1,'third','member')", ws.ID); err != nil {
		t.Fatal(err)
	}
	private, err := s.CreateConversation(ctx, owner.ID, ws.ID, "group", "Private", []string{"third"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.SendAnnotated(ctx, owner, private.ID, "workspace-is-not-membership", "@同名伙伴", nil, nil, []imtext.Mention{{UserID: member.ID, Start: 0, End: 5}})
	uploadError(t, err, 400, "invalid_mentions")
	if err = s.RemoveMember(ctx, owner.ID, ws.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.SendAnnotated(ctx, owner, c.ID, "removed-recipient", "@同名伙伴", nil, nil, []imtext.Mention{{UserID: member.ID, Start: 0, End: 5}})
	uploadError(t, err, 400, "invalid_mentions")
	var count int
	if err = s.DB.QueryRow(ctx, "SELECT count(*) FROM messages").Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid annotations partially committed a message", count, err)
	}
}

func TestMentionsMigrationKeepsSchemaFourMessagesAndUploadReceipts(t *testing.T) {
	ctx := context.Background()
	url := testdb.URL(t)
	db, err := database.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(ctx, schema+schemaV2+schemaV3+schemaV4); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO chat_schema_migrations(version) VALUES(2),(3),(4);
INSERT INTO workspaces(id,name,created_by) VALUES('ws','Existing','owner');
INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws','owner','owner');
INSERT INTO conversations(id,workspace_id,type,name,last_seq) VALUES('c','ws','channel','general',1);
INSERT INTO messages(id,conversation_id,sender_id,sender_name,sender_kind,client_message_id,seq,body,original_body_hash) VALUES('old','c','owner','Owner','human','old',1,'Preserved','hash');
INSERT INTO attachment_uploads(uploader_id,upload_key,conversation_id,fingerprint) VALUES('owner','spent','c','original');`); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, url, config.Flags{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := s.Message(ctx, "owner", "c", "old")
	encoded, _ := json.Marshal(m)
	if err != nil || m.Body != "Preserved" || len(m.Mentions) != 0 || string(encoded) == "" {
		t.Fatal(m, err)
	}
	_, err = s.LookupAttachmentUpload(ctx, "owner", "c", "spent")
	uploadError(t, err, 410, "attachment_upload_unavailable")
}

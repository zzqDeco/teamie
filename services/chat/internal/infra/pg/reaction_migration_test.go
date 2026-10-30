package pg

import (
	"bytes"
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"teamie/pkg/postgres"
	"teamie/pkg/postgres/testdb"
)

func TestReactionSchemaSevenPreservesSchemaSixMessagesAndAttachments(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	err = postgres.Migrate(ctx, pool, 704102, "chat_schema_migrations", []postgres.Migration{
		{Version: 1, SQL: SchemaV1}, {Version: 2, SQL: SchemaV2},
		{Version: 3, SQL: SchemaV3}, {Version: 4, SQL: SchemaV4},
		{Version: 5, SQL: SchemaV5}, {Version: 6, SQL: SchemaV6},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
INSERT INTO workspaces(id,name,created_by) VALUES('w','Original workspace','owner');
INSERT INTO conversations(id,workspace_id,type,name,last_seq) VALUES('c','w','channel','Original channel',1);
INSERT INTO messages(id,conversation_id,sender_id,sender_name,sender_kind,client_message_id,seq,body,original_body_hash,attachments,original_attachment_ids)
VALUES('m','c','owner','Owner','human','stable-send',1,'Original body','original-hash','[{"id":"a","filename":"binary.bin","mediaType":"application/octet-stream","sizeBytes":4,"sha256":"original-sha"}]','{a}');
INSERT INTO attachments(id,conversation_id,uploader_id,message_id,filename,media_type,size_bytes,sha256,content)
VALUES('a','c','owner','m','binary.bin','application/octet-stream',4,'original-sha',decode('00fffe80','hex'));
INSERT INTO attachment_uploads(uploader_id,upload_key,conversation_id,fingerprint,attachment_id)
VALUES('owner','stable-upload','c','original-fingerprint','a');
INSERT INTO read_cursors(conversation_id,user_id,seq) VALUES('c','owner',1);`)
	if err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var body, clientID, hash, uploadID string
	var seq, version, readSeq int64
	var content []byte
	var originalIDs []string
	if err = pool.QueryRow(ctx, `SELECT m.body,m.client_message_id,m.original_body_hash,m.seq,m.reactions_version,m.original_attachment_ids,a.content,u.attachment_id,r.seq
FROM messages m JOIN attachments a ON a.message_id=m.id JOIN attachment_uploads u ON u.attachment_id=a.id JOIN read_cursors r ON r.conversation_id=m.conversation_id WHERE m.id='m'`).Scan(&body, &clientID, &hash, &seq, &version, &originalIDs, &content, &uploadID, &readSeq); err != nil {
		t.Fatal(err)
	}
	if body != "Original body" || clientID != "stable-send" || hash != "original-hash" || seq != 1 || readSeq != 1 || version != 0 || uploadID != "a" || len(originalIDs) != 1 || originalIDs[0] != "a" || !bytes.Equal(content, []byte{0, 255, 254, 128}) {
		t.Fatalf("upgrade changed existing message, binary, receipt or cursor: body=%q client=%q seq=%d version=%d content=%x", body, clientID, seq, version, content)
	}
	var head int
	if err = pool.QueryRow(ctx, "SELECT max(version) FROM chat_schema_migrations").Scan(&head); err != nil || head != 10 {
		t.Fatalf("schema ledger=%d err=%v", head, err)
	}
	var retired bool
	if err = pool.QueryRow(ctx, "SELECT to_regclass('marketplace_apps') IS NULL").Scan(&retired); err != nil || !retired {
		t.Fatalf("expected embedded marketplace tables to be retired at schema 10, retired=%v err=%v", retired, err)
	}
	if err = Migrate(ctx, pool); err != nil {
		t.Fatalf("repeating the migration was not idempotent: %v", err)
	}
}

func TestRetireEmbeddedMarketplaceRefusesUnmigratedDataUntilStamped(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	err = postgres.Migrate(ctx, pool, 704102, "chat_schema_migrations", []postgres.Migration{
		{Version: 1, SQL: SchemaV1}, {Version: 2, SQL: SchemaV2},
		{Version: 3, SQL: SchemaV3}, {Version: 4, SQL: SchemaV4},
		{Version: 5, SQL: SchemaV5}, {Version: 6, SQL: SchemaV6},
		{Version: 7, SQL: SchemaV7}, {Version: 8, SQL: SchemaV8},
		{Version: 9, SQL: SchemaV9},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = pool.Exec(ctx, `
INSERT INTO workspaces(id, name, created_by) VALUES ('ws-1', 'Workspace', 'owner');
INSERT INTO marketplace_apps(id, slug, name, description, web_url, mcp_url, signing_secret, enabled, owner_id, is_published, published_version, review_status, draft_name, draft_description, draft_web_url, draft_mcp_url)
VALUES ('app-1', 'legacy-app', 'Legacy App', 'Desc', 'https://legacy.example/ui', 'https://legacy.example/mcp', repeat('k', 64), true, 'owner', true, 1, 'approved', 'Legacy App', 'Desc', 'https://legacy.example/ui', 'https://legacy.example/mcp');
INSERT INTO workspace_marketplace_apps(workspace_id, app_id, installed_by) VALUES ('ws-1', 'app-1', 'owner');
`)
	if err != nil {
		t.Fatal(err)
	}

	// Without the verified transfer stamp, migration 010 must fail closed and preserve data.
	if err = Migrate(ctx, pool); err == nil {
		t.Fatal("expected migration 010 to refuse dropping non-empty unstamped marketplace tables")
	}
	var count int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM marketplace_apps WHERE id = 'app-1'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected legacy marketplace row to remain intact after refused migration, count=%d err=%v", count, err)
	}

	// Once deploy/postgres/02-migrate-chat-marketplace.sh stamps verified transfer, migration 010 retires the tables cleanly.
	if _, err = pool.Exec(ctx, "COMMENT ON TABLE marketplace_apps IS 'teamie.transferred_to_marketplace_v1'"); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, pool); err != nil {
		t.Fatalf("expected stamped migration 010 to succeed, got: %v", err)
	}
	var retired bool
	if err = pool.QueryRow(ctx, "SELECT to_regclass('marketplace_apps') IS NULL").Scan(&retired); err != nil || !retired {
		t.Fatalf("expected embedded marketplace tables to be dropped after stamped migration, retired=%v err=%v", retired, err)
	}
}

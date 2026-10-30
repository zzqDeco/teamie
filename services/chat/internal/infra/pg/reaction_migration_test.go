package pg

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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

func TestTransferScriptReconcilesInterruptedSourceMutationsAndRefusesTargetConflicts(t *testing.T) {
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql CLI not installed; skipping shell transfer script integration test")
	}
	baseDSN := os.Getenv("DATABASE_URL_TEST")
	if baseDSN == "" {
		t.Skip("DATABASE_URL_TEST is required")
	}
	parsed, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)

	var superUser string
	var isSuper bool
	if err := adminPool.QueryRow(ctx, "SELECT current_user, rolsuper FROM pg_roles WHERE rolname = current_user").Scan(&superUser, &isSuper); err != nil {
		t.Fatal(err)
	}
	if !isSuper {
		t.Skip("test database user is not superuser; skipping cross-database transfer script test")
	}

	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(rnd[:])
	chatDB := "teamie_chat_mig_" + suffix
	mktDB := "teamie_mkt_mig_" + suffix
	mktRole := "teamie_mkt_role_" + suffix

	if _, err := adminPool.Exec(ctx, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'mig-pass'", mktRole)); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", chatDB)); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s OWNER %s", mktDB, mktRole)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(), fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", chatDB))
		_, _ = adminPool.Exec(context.Background(), fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", mktDB))
		_, _ = adminPool.Exec(context.Background(), fmt.Sprintf("DROP ROLE IF EXISTS %s", mktRole))
	})

	dbURL := func(dbName string) string {
		u := *parsed
		u.Path = "/" + dbName
		q := u.Query()
		q.Del("search_path")
		u.RawQuery = q.Encode()
		return u.String()
	}

	chatPool, err := pgxpool.New(ctx, dbURL(chatDB))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(chatPool.Close)

	mktPool, err := pgxpool.New(ctx, dbURL(mktDB))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mktPool.Close)

	if err := postgres.Migrate(ctx, chatPool, 704102, "chat_schema_migrations", []postgres.Migration{
		{Version: 1, SQL: SchemaV1}, {Version: 2, SQL: SchemaV2},
		{Version: 3, SQL: SchemaV3}, {Version: 4, SQL: SchemaV4},
		{Version: 5, SQL: SchemaV5}, {Version: 6, SQL: SchemaV6},
		{Version: 7, SQL: SchemaV7}, {Version: 8, SQL: SchemaV8},
		{Version: 9, SQL: SchemaV9},
	}); err != nil {
		t.Fatal(err)
	}

	_, err = chatPool.Exec(ctx, `
INSERT INTO workspaces(id, name, created_by) VALUES ('ws-1', 'WS 1', 'owner'), ('ws-2', 'WS 2', 'owner');
INSERT INTO marketplace_apps(
  id, slug, name, description, web_url, mcp_url, signing_secret, enabled,
  owner_id, is_published, published_version, review_status,
  draft_name, draft_description, draft_web_url, draft_mcp_url,
  has_draft_changes, submission_revision
) VALUES (
  'app-1', 'board-game', 'Board Game', 'old-description',
  'https://game.example/ui', 'https://game.example/mcp', repeat('s', 64), true,
  'owner', true, 1, 'approved',
  'Board Game', 'old-description', 'https://game.example/ui', 'https://game.example/mcp',
  false, 1
);
INSERT INTO workspace_marketplace_apps(workspace_id, app_id, installed_by) VALUES
  ('ws-1', 'app-1', 'owner'),
  ('ws-2', 'app-1', 'owner');
INSERT INTO marketplace_app_reviews(id, app_id, revision, action, actor_id, note, web_url, mcp_url)
VALUES ('rev-1', 'app-1', 1, 'approve', 'admin', 'LGTM', 'https://game.example/ui', 'https://game.example/mcp');
`)
	if err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Clean(filepath.Join("..", "..", "..", "..", "..", "deploy", "postgres", "02-migrate-chat-marketplace.sh"))
	pw, _ := parsed.User.Password()
	if pw == "" {
		pw = "local-trust"
	}
	host := parsed.Hostname()
	if host == "" {
		host = "127.0.0.1"
	}
	port := parsed.Port()
	if port == "" {
		port = "5432"
	}

	runTransfer := func() (string, error) {
		cmd := exec.Command("bash", scriptPath)
		cmd.Env = append(os.Environ(),
			"PGHOST="+host,
			"PGPORT="+port,
			"POSTGRES_SUPERUSER="+superUser,
			"POSTGRES_PASSWORD="+pw,
			"TEAMIE_CHAT_DB="+chatDB,
			"TEAMIE_MARKETPLACE_DB="+mktDB,
			"TEAMIE_MARKETPLACE_ROLE="+mktRole,
		)
		out, runErr := cmd.CombinedOutput()
		return string(out), runErr
	}

	// 1. First transfer + unchanged retry succeed and set role ownership properly.
	if out, err := runTransfer(); err != nil {
		t.Fatalf("first transfer failed: %v\n%s", err, out)
	}
	if out, err := runTransfer(); err != nil {
		t.Fatalf("unchanged retry failed: %v\n%s", err, out)
	}

	// 2. Simulate interrupted upgrade before migration 010: operator restores old Chat,
	// mutates enabled/admin_suspended/draft_description, uninstalls ws-2, and adds a suspend review
	// (even without bumping updated_at). Retrying transfer must synchronize target!
	_, err = chatPool.Exec(ctx, `
UPDATE marketplace_apps
SET enabled = false,
    admin_suspended = true,
    suspended_reason = 'incident',
    draft_description = 'new-draft',
    has_draft_changes = true
WHERE id = 'app-1';
DELETE FROM workspace_marketplace_apps WHERE workspace_id = 'ws-2' AND app_id = 'app-1';
INSERT INTO marketplace_app_reviews(id, app_id, revision, action, actor_id, note, web_url, mcp_url)
VALUES ('rev-2', 'app-1', 1, 'suspend', 'admin', 'incident', 'https://game.example/ui', 'https://game.example/mcp');
`)
	if err != nil {
		t.Fatal(err)
	}

	if out, err := runTransfer(); err != nil {
		t.Fatalf("reconciling retry after source mutation failed: %v\n%s", err, out)
	}

	var targetEnabled, targetSuspended bool
	var targetDraftDesc string
	var installCount, reviewCount int
	if err := mktPool.QueryRow(ctx, `
SELECT enabled, admin_suspended, draft_description FROM marketplace_apps WHERE id = 'app-1'
`).Scan(&targetEnabled, &targetSuspended, &targetDraftDesc); err != nil {
		t.Fatal(err)
	}
	if targetEnabled || !targetSuspended || targetDraftDesc != "new-draft" {
		t.Fatalf("expected target to sync latest source state (enabled=false, suspended=true, draft=new-draft), got enabled=%v suspended=%v draft=%q", targetEnabled, targetSuspended, targetDraftDesc)
	}
	if err := mktPool.QueryRow(ctx, `SELECT count(*) FROM workspace_marketplace_apps WHERE app_id = 'app-1'`).Scan(&installCount); err != nil || installCount != 1 {
		t.Fatalf("expected ws-2 uninstall to sync (installCount=1), got %d (%v)", installCount, err)
	}
	if err := mktPool.QueryRow(ctx, `SELECT count(*) FROM marketplace_app_reviews WHERE app_id = 'app-1'`).Scan(&reviewCount); err != nil || reviewCount != 2 {
		t.Fatalf("expected rev-2 to sync (reviewCount=2), got %d (%v)", reviewCount, err)
	}

	// 3. Conflicting newer target write (m.updated_at > s.updated_at with diverged fields)
	// must clear the prior transfer stamp, fail the script, and cause migration 010 to refuse retirement!
	if _, err := mktPool.Exec(ctx, `
UPDATE marketplace_apps
SET draft_description = 'newer-target-edit',
    updated_at = now() + interval '10 minutes'
WHERE id = 'app-1'
`); err != nil {
		t.Fatal(err)
	}
	if out, err := runTransfer(); err == nil {
		t.Fatalf("expected transfer script to fail on conflicting newer target row, but succeeded:\n%s", out)
	}
	if err := Migrate(ctx, chatPool); err == nil {
		t.Fatal("expected migration 010 to refuse dropping source tables after failed/conflicted retry cleared transfer stamp")
	}

	// Align target back to source, re-run transfer, and verify migration 010 cleanly retires legacy tables.
	if _, err := mktPool.Exec(ctx, `DELETE FROM marketplace_apps WHERE id = 'app-1'`); err != nil {
		t.Fatal(err)
	}
	if out, err := runTransfer(); err != nil {
		t.Fatalf("expected clean retry after resolving conflict to succeed: %v\n%s", err, out)
	}
	if err := Migrate(ctx, chatPool); err != nil {
		t.Fatalf("expected migration 010 to retire stamped tables cleanly: %v", err)
	}
}

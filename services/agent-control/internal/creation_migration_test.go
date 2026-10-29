package control

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"teamie/pkg/postgres"
	"teamie/services/agent-control/internal/infra/pg"
	"testing"
)

func TestCreationHashMigrationPreservesLegacyProfilesAndFencesOldWriters(t *testing.T) {
	ctx := context.Background()
	db, err := pgxpool.New(ctx, databaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := []postgres.Migration{{Version: 1, SQL: pg.SchemaV1}, {Version: 2, SQL: pg.SchemaV2}}
	if err = postgres.Migrate(ctx, db, 704201, "agent_control_schema_migrations", old); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO agents(id,owner_id,name,model_id,workspace_ids,enabled,revision,applied_revision) VALUES('legacy','owner','Old name','model-one',ARRAY['ws1'],true,4,4)`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = pg.Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	a, ok, err := pg.New(postgres.New(db)).GetByID(ctx, "legacy")
	if err != nil || !ok || a.Name != "Old name" || a.ModelID != "model-one" || !same(a.WorkspaceIDs, []string{"ws1"}) || !a.Enabled || a.Revision != 4 || a.AppliedRevision != 4 || a.CreationHash != "" {
		t.Fatalf("migration changed legacy profile: %+v %v", a, err)
	}
	if err = postgres.Migrate(ctx, db, 704201, "agent_control_schema_migrations", old); err == nil {
		t.Fatal("old writer accepted schema 3")
	}
}

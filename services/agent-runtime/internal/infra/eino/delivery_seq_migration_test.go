package eino

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func seedLegacyDeliveryInputs(t *testing.T, db *pgx.Conn, agents []string) {
	t.Helper()
	for _, event := range []int{3, 1, 2} {
		for _, agent := range agents {
			input := Input{EventID: strconv.Itoa(event), Body: "original source " + strconv.Itoa(event)}
			raw, _ := json.Marshal(input)
			if _, err := db.Exec(context.Background(), `INSERT INTO runtime_inputs(agent_id,event_id,payload) VALUES($1,$2,$3)`, agent, event, raw); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func requireDeliveryOrder(t *testing.T, s *Store, lease *Lease, expected []string) {
	t.Helper()
	for name, read := range map[string]func(context.Context, *Lease) ([]Input, error){"Pending": s.Pending, "FreshPending": s.FreshPending} {
		inputs, err := read(context.Background(), lease)
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, input := range inputs {
			ids = append(ids, input.EventID)
		}
		if !reflect.DeepEqual(ids, expected) {
			t.Fatal(name+" changed delivery order", ids, expected)
		}
	}
}

func TestDeliverySeqFirstV1UpgradePreservesPerAgentEventOrder(t *testing.T) {
	dsn, raw := runtimeMigrationDatabase(t)
	ctx := context.Background()
	v1, _, _ := strings.Cut(runtimeV037DDL(t), "-- schemaV2SQL\n")
	if _, err := raw.Exec(ctx, v1); err != nil {
		t.Fatal(err)
	}
	agents := []string{uuid.NewString(), uuid.NewString()}
	seedLegacyDeliveryInputs(t, raw, agents)
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	for _, agent := range agents {
		lease, err := s.Acquire(ctx, agent, "delivery-upgrade")
		if err != nil {
			t.Fatal(err)
		}
		defer s.Release(ctx, lease)
		requireDeliveryOrder(t, s, lease, []string{"1", "2", "3"})
		if err = s.Ingest(ctx, lease, "4", []Input{{EventID: "4", Body: "new source after migration"}}); err != nil {
			t.Fatal(err)
		}
		requireDeliveryOrder(t, s, lease, []string{"1", "2", "3", "4"})
	}
	var distinct, total int
	if err = s.DB.QueryRow(ctx, `SELECT count(DISTINCT delivery_seq),count(*) FROM runtime_inputs`).Scan(&distinct, &total); err != nil || total != 8 || distinct != total {
		t.Fatal("identity sequence collided with deterministic backfill", distinct, total, err)
	}
}

func TestDeliverySeqExistingV2AndV3OrderNeverRewrites(t *testing.T) {
	dsn, raw := runtimeMigrationDatabase(t)
	ctx := context.Background()
	if _, err := raw.Exec(ctx, runtimeV037DDL(t)); err != nil {
		t.Fatal(err)
	}
	agent := uuid.NewString()
	seedLegacyDeliveryInputs(t, raw, []string{agent})
	read := func() string {
		t.Helper()
		var value string
		if err := raw.QueryRow(ctx, `SELECT jsonb_object_agg(event_id::text,delivery_seq)::text FROM runtime_inputs`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := read()
	for _, version := range []string{"existing-schema2", "existing-schema3"} {
		s, err := Open(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		lease, err := s.Acquire(ctx, agent, version)
		if err != nil {
			t.Fatal(err)
		}
		// Existing delivery order, including interleaved background inputs, must
		// remain authoritative even when it differs from numeric event order.
		requireDeliveryOrder(t, s, lease, []string{"3", "1", "2"})
		s.Release(ctx, lease)
		s.DB.Close()
		if got := read(); got != before {
			t.Fatal("migration rewrote "+version+" delivery sequence", got, before)
		}
	}
}

func TestDeliverySeqEmptyV1UpgradeStartsIdentityAtOne(t *testing.T) {
	dsn, raw := runtimeMigrationDatabase(t)
	ctx := context.Background()
	v1, _, _ := strings.Cut(runtimeV037DDL(t), "-- schemaV2SQL\n")
	if _, err := raw.Exec(ctx, v1); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	lease, err := s.Acquire(ctx, uuid.NewString(), "empty-upgrade")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release(ctx, lease)
	if err = s.Ingest(ctx, lease, "1", []Input{{EventID: "1"}}); err != nil {
		t.Fatal(err)
	}
	var sequence int
	if err = s.DB.QueryRow(ctx, `SELECT delivery_seq FROM runtime_inputs`).Scan(&sequence); err != nil || sequence != 1 {
		t.Fatal("empty upgrade did not initialize the identity sequence", sequence, err)
	}
}

package identity

import (
	"context"
	"testing"
)

func TestNewSessionCollectsExpiredRowsInBoundedCommittedBatches(t *testing.T) {
	s, ss, token := passwordStore(t)
	ctx := context.Background()
	if _, err := s.DB.Exec(ctx, `INSERT INTO identity_sessions(token_hash,user_id,csrf_token,expires_at)
		SELECT 'expired-'||i,$1,'unused',now()-interval '1 day' FROM generate_series(1,1002) i`, ss.User.ID); err != nil {
		t.Fatal(err)
	}
	locked, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Rollback(ctx)
	if _, err = locked.Exec(ctx, `SELECT token_hash FROM identity_sessions WHERE token_hash='expired-1' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	count := func(want int) {
		t.Helper()
		var got int
		if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM identity_sessions WHERE token_hash LIKE 'expired-%'`).Scan(&got); err != nil || got != want {
			t.Fatalf("expired rows=%d want=%d: %v", got, want, err)
		}
		if _, err := s.Validate(ctx, token); err != nil {
			t.Fatal("cleanup removed a live session", err)
		}
	}
	// A failed credential transaction cannot spend either cleanup or a new login.
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, rolledBack, err := s.newSession(ctx, tx, ss.User)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	count(1002)
	if _, err = s.Validate(ctx, rolledBack); err == nil {
		t.Fatal("rolled-back new session authenticated")
	}
	for _, want := range []int{2, 1} {
		tx, err = s.DB.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, created, createErr := s.newSession(ctx, tx, ss.User)
		if createErr != nil {
			tx.Rollback(ctx)
			t.Fatal(createErr)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		count(want)
		if _, err = s.Validate(ctx, created); err != nil {
			t.Fatal("committed session unavailable", err)
		}
	}
	if err = locked.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, _, err = s.newSession(ctx, tx, ss.User); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	count(0)
}

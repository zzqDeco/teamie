package chat

import (
	"context"
	"errors"
	"sync"
	"testing"

	"teamie/pkg/httpx"
)

func requireGroupError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var typed *httpx.Error
	if !errors.As(err, &typed) || typed.Status != status || typed.Code != code {
		t.Fatalf("expected %d %s, got %v", status, code, err)
	}
}

func TestRemovedGroupCreatorCannotRejoinOrManage(t *testing.T) {
	s, ws, _, owner, creator := setup(t)
	ctx := context.Background()
	group, err := s.CreateConversation(ctx, creator.ID, ws.ID, "group", "Private creator group", []string{owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Send(ctx, creator, group.ID, "private-history", "private group message", nil); err != nil {
		t.Fatal(err)
	}
	guest, another := httpx.ID(), httpx.ID()
	if _, err = s.DB.Exec(ctx, `INSERT INTO workspace_members(workspace_id,user_id,role) SELECT $1,unnest($2::text[]),'member'`, ws.ID, []string{guest, another}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GroupMember(ctx, creator.ID, group.ID, guest, true); err != nil {
		t.Fatal("current creator could not add a member", err)
	}
	if _, err = s.GroupMember(ctx, owner.ID, group.ID, creator.ID, false); err != nil {
		t.Fatal("workspace owner could not remove creator", err)
	}
	var eventsBefore int
	if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM sync_events`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []struct {
		target string
		add    bool
	}{{creator.ID, true}, {another, true}, {guest, false}} {
		_, err = s.GroupMember(ctx, creator.ID, group.ID, attempt.target, attempt.add)
		requireGroupError(t, err, 403, "forbidden")
	}
	var eventsAfter, removed int
	if err = s.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM sync_events),(SELECT count(*) FROM conversation_members WHERE conversation_id=$1 AND user_id=$2)`, group.ID, creator.ID).Scan(&eventsAfter, &removed); err != nil || removed != 0 || eventsAfter != eventsBefore {
		t.Fatal("denied creator management changed membership or fanout", err)
	}
	if _, _, err = s.Messages(ctx, creator.ID, group.ID, 0, 50); err == nil {
		t.Fatal("removed creator regained private group history")
	}
	// Workspace owners retain their explicit management permission even when
	// they are not group recipients; that permission does not grant history.
	if _, err = s.GroupMember(ctx, owner.ID, group.ID, owner.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GroupMember(ctx, owner.ID, group.ID, creator.ID, true); err != nil {
		t.Fatal("nonrecipient workspace owner lost legitimate management", err)
	}
	if _, _, err = s.Messages(ctx, owner.ID, group.ID, 0, 50); err == nil {
		t.Fatal("workspace management permission bypassed history membership")
	}
	if _, err = s.GroupMember(ctx, creator.ID, group.ID, guest, false); err != nil {
		t.Fatal("restored creator could not manage current group", err)
	}
}

func TestGroupMemberConcurrentAddsRespectFiftyAndRemainIdempotent(t *testing.T) {
	s, ws, _, owner, member := setup(t)
	ctx := context.Background()
	participants := []string{member.ID}
	for len(participants) < 48 {
		participants = append(participants, httpx.ID())
	}
	candidates := []string{httpx.ID(), httpx.ID()}
	if _, err := s.DB.Exec(ctx, `INSERT INTO workspace_members(workspace_id,user_id,role) SELECT $1,unnest($2::text[]),'member' ON CONFLICT DO NOTHING`, ws.ID, append(append([]string{}, participants...), candidates...)); err != nil {
		t.Fatal(err)
	}
	group, err := s.CreateConversation(ctx, owner.ID, ws.ID, "group", "Near capacity", participants)
	if err != nil || len(group.MemberIDs) != 49 {
		t.Fatal("fixture did not start at 49 members", len(group.MemberIDs), err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, candidate := range candidates {
		workers.Add(1)
		go func(target string) {
			defer workers.Done()
			<-start
			_, err := s.GroupMember(ctx, owner.ID, group.ID, target, true)
			results <- err
		}(candidate)
	}
	close(start)
	workers.Wait()
	close(results)
	succeeded := 0
	for result := range results {
		if result == nil {
			succeeded++
		} else {
			requireGroupError(t, result, 400, "invalid_members")
		}
	}
	var members, eventsBefore int
	if err = s.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM conversation_members WHERE conversation_id=$1),(SELECT count(*) FROM sync_events)`, group.ID).Scan(&members, &eventsBefore); err != nil || succeeded != 1 || members != 50 {
		t.Fatal("concurrent distinct adds exceeded the group boundary", succeeded, members, err)
	}
	for i := 0; i < 2; i++ {
		current, err := s.GroupMember(ctx, owner.ID, group.ID, member.ID, true)
		if err != nil || len(current.MemberIDs) != 50 || !current.CanManageMembers {
			t.Fatal("idempotent add of an existing full-group member was rejected", err)
		}
	}
	var eventsAfter int
	if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM sync_events`).Scan(&eventsAfter); err != nil || eventsAfter != eventsBefore {
		t.Fatal("idempotent membership retry emitted new sync effects", eventsBefore, eventsAfter, err)
	}
}

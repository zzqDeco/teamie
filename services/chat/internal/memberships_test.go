package chat

import (
	"context"
	"teamie/contracts/gen/im/kitex_gen/im"
	"teamie/pkg/httpx"
	"teamie/pkg/postgres"
	"teamie/pkg/postgres/testdb"
	"testing"
)

func TestDelegatedMembershipAndExplicitGroupRevocation(t *testing.T) {
	ctx := context.Background()
	s, ws, channel, owner, member := setup(t)
	a := httpx.User{ID: httpx.ID(), DisplayName: "Assistant", Kind: "agent"}
	if _, e := s.SetPrincipalWorkspaces(ctx, a, member, []string{"outside"}, 1); e == nil {
		t.Fatal("grant outside owner's membership")
	}
	if _, e := s.SetPrincipalWorkspaces(ctx, a, member, []string{ws.ID}, 1); e != nil {
		t.Fatal(e)
	}
	if _, _, e := s.Send(ctx, a, channel.ID, "public", "Agent public message", nil); e != nil {
		t.Fatal("normal public channel inaccessible", e)
	}
	g, e := s.CreateConversation(ctx, owner.ID, ws.ID, "group", "Private", []string{member.ID})
	if e != nil {
		t.Fatal(e)
	}
	m, _, e := s.Send(ctx, owner, g.ID, "private-before", "Private", nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Message(ctx, a.ID, g.ID, m.ID); e == nil {
		t.Fatal("workspace membership exposed private group")
	}
	if _, e = s.GroupMember(ctx, member.ID, g.ID, a.ID, true); e == nil {
		t.Fatal("ordinary member managed group")
	}
	if _, e = s.GroupMember(ctx, owner.ID, g.ID, a.ID, true); e != nil {
		t.Fatal(e)
	}
	if got, e := s.Message(ctx, a.ID, g.ID, m.ID); e != nil || got.Body != "Private" {
		t.Fatal("explicit group admission", e)
	}
	if _, e = s.Message(ctx, a.ID, channel.ID, m.ID); e == nil {
		t.Fatal("point lookup crossed conversations")
	}
	if _, e = s.ChangeMessage(ctx, owner.ID, g.ID, m.ID, "", true); e != nil {
		t.Fatal(e)
	}
	if got, e := s.Message(ctx, a.ID, g.ID, m.ID); e != nil || got.Body != "" || got.RetractedAt == nil {
		t.Fatal("point lookup resurrected tombstone", e)
	}
	if _, e = s.GroupMember(ctx, owner.ID, g.ID, a.ID, false); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Message(ctx, a.ID, g.ID, m.ID); e == nil {
		t.Fatal("removed group participant read history")
	}
	if _, _, e = s.Send(ctx, a, g.ID, "removed", "Denied", nil); e == nil {
		t.Fatal("removed group participant sent")
	}
	events, _, _, e := s.Sync(ctx, a.ID, "0", 100)
	if e != nil {
		t.Fatal(e)
	}
	for _, ev := range events {
		if ev.Message != nil && ev.ConversationID == g.ID {
			t.Fatal("queued private payload leaked after group removal")
		}
	}
	if _, e = s.GroupMember(ctx, owner.ID, g.ID, a.ID, true); e != nil {
		t.Fatal(e)
	}
	if e = s.RemoveMember(ctx, owner.ID, ws.ID, a.ID); e != nil {
		t.Fatal(e)
	}
	actual, e := s.SetPrincipalWorkspaces(ctx, a, member, []string{ws.ID}, 1)
	if e != nil || len(actual) != 0 {
		t.Fatal("reconciliation rejoined moderated removal", actual, e)
	}
	actual, e = s.SetPrincipalWorkspaces(ctx, a, member, []string{ws.ID}, 2)
	if e != nil || len(actual) != 1 {
		t.Fatal("explicit new grant failed", actual, e)
	}
	if _, e = s.Message(ctx, a.ID, g.ID, m.ID); e == nil {
		t.Fatal("workspace regrant restored old explicit private membership")
	}
	if _, e = s.GroupMember(ctx, owner.ID, g.ID, a.ID, true); e != nil {
		t.Fatal(e)
	}
	if e = s.RemoveMember(ctx, owner.ID, ws.ID, member.ID); e != nil {
		t.Fatal(e)
	}
	if actual, e = s.ParticipantWorkspaces(ctx, a.ID); e != nil || len(actual) != 0 {
		t.Fatal("grantor removal retained delegated membership", actual, e)
	}
	if _, _, e = s.Messages(ctx, a.ID, channel.ID, 0, 50); e == nil {
		t.Fatal("delegated revocation retained public history")
	}
	var n int
	if e = s.DB.QueryRow(ctx, "SELECT count(*) FROM conversation_members WHERE user_id=$1", a.ID).Scan(&n); e != nil || n != 0 {
		t.Fatal("delegated revocation retained private memberships", n, e)
	}
}

func TestGroupPermissionsArePerRecipientAndLegacySafe(t *testing.T) {
	ctx := context.Background()
	s, ws, _, owner, member := setup(t)
	g, e := s.CreateConversation(ctx, member.ID, ws.ID, "group", "Owned by member", []string{owner.ID})
	if e != nil {
		t.Fatal(e)
	}
	outsider := httpx.User{ID: httpx.ID(), Kind: "human"}
	code, _, e := s.CreateInvite(ctx, owner.ID, ws.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.AcceptInvite(ctx, outsider.ID, code); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GroupMember(ctx, member.ID, g.ID, outsider.ID, true); e != nil {
		t.Fatal("creator cannot manage", e)
	}
	c, e := conversationAccess(ctx, s.DB, g.ID, outsider.ID)
	if e != nil || c.CanManageMembers {
		t.Fatal("ordinary recipient received management permission", e)
	}
	if _, e = s.GroupMember(ctx, owner.ID, g.ID, outsider.ID, false); e != nil {
		t.Fatal("workspace owner cannot manage", e)
	}
	if _, e = s.DB.Exec(ctx, "UPDATE conversations SET created_by=NULL WHERE id=$1", g.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GroupMember(ctx, member.ID, g.ID, outsider.ID, true); e == nil {
		t.Fatal("legacy group fabricated creator ownership")
	}
	if _, e = s.GroupMember(ctx, owner.ID, g.ID, outsider.ID, true); e != nil {
		t.Fatal("legacy workspace owner cannot manage", e)
	}
}

func TestChatV1UpgradePreservesMessages(t *testing.T) {
	ctx := context.Background()
	url := testdb.URL(t)
	db, e := postgres.Open(ctx, url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = db.Exec(ctx, schema); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(ctx, "INSERT INTO workspaces(id,name,created_by) VALUES('ws','Existing','owner'); INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws','owner','owner'); INSERT INTO conversations(id,workspace_id,type,name,last_seq) VALUES('conversation','ws','channel','general',1); INSERT INTO messages(id,conversation_id,sender_id,sender_name,sender_kind,client_message_id,seq,body,original_body_hash) VALUES('message','conversation','owner','Owner','human','retry',1,'Preserved','hash')"); e != nil {
		t.Fatal(e)
	}
	s, e := Open(ctx, url, httpx.Flags{})
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	m, e := s.Message(ctx, "owner", "conversation", "message")
	if e != nil || m.Body != "Preserved" || m.Seq != 1 {
		t.Fatal("published data lost", e)
	}
	var version int
	if e = s.DB.QueryRow(ctx, "SELECT max(version) FROM chat_schema_migrations").Scan(&version); e != nil || version != 10 {
		t.Fatal("ledger", version, e)
	}
	var retiredTables int
	if e = s.DB.QueryRow(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('marketplace_apps','marketplace_app_reviews','workspace_marketplace_apps')").Scan(&retiredTables); e != nil || retiredTables != 0 {
		t.Fatal("embedded marketplace tables were not retired", retiredTables, e)
	}
}

func TestWorkspaceRoleAndConversationScopeRPC(t *testing.T) {
	ctx := context.Background()
	s, ws, channel, owner, member := setup(t)
	outsider := httpx.User{ID: httpx.ID(), Kind: "human"}
	group, err := s.CreateConversation(ctx, owner.ID, ws.ID, "group", "Room", []string{member.ID})
	if err != nil {
		t.Fatal(err)
	}
	rpc := &RPC{Store: s}

	ownerRole, err := rpc.GetWorkspaceRole(ctx, &im.WorkspaceAccessRequest{WorkspaceId: ws.ID, UserId: owner.ID})
	if err != nil || !ownerRole.Allowed || ownerRole.Role != "owner" || ownerRole.OwnerId != owner.ID {
		t.Fatalf("unexpected owner role response: %+v (%v)", ownerRole, err)
	}
	memberRole, err := rpc.GetWorkspaceRole(ctx, &im.WorkspaceAccessRequest{WorkspaceId: ws.ID, UserId: member.ID})
	if err != nil || !memberRole.Allowed || memberRole.Role != "member" || memberRole.OwnerId != owner.ID {
		t.Fatalf("unexpected member role response: %+v (%v)", memberRole, err)
	}
	outsiderRole, err := rpc.GetWorkspaceRole(ctx, &im.WorkspaceAccessRequest{WorkspaceId: ws.ID, UserId: outsider.ID})
	if err != nil || outsiderRole.Allowed {
		t.Fatalf("expected outsider denied, got: %+v (%v)", outsiderRole, err)
	}

	groupScope, err := rpc.GetConversationScope(ctx, &im.AccessRequest{ConversationId: group.ID, UserId: member.ID})
	if err != nil || !groupScope.Allowed || groupScope.WorkspaceId != ws.ID || groupScope.WorkspaceOwnerId != owner.ID || groupScope.ConversationType != "group" {
		t.Fatalf("unexpected group scope response: %+v (%v)", groupScope, err)
	}
	chScope, err := rpc.GetConversationScope(ctx, &im.AccessRequest{ConversationId: channel.ID, UserId: member.ID})
	if err != nil || !chScope.Allowed || chScope.ConversationType != "channel" {
		t.Fatalf("unexpected channel scope response: %+v (%v)", chScope, err)
	}
	outsiderScope, err := rpc.GetConversationScope(ctx, &im.AccessRequest{ConversationId: group.ID, UserId: outsider.ID})
	if err != nil || outsiderScope.Allowed {
		t.Fatalf("expected outsider conversation scope denied, got: %+v (%v)", outsiderScope, err)
	}
}

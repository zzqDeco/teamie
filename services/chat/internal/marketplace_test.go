package chat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"teamie/pkg/httpx"
	"teamie/pkg/postgres/testdb"
	"teamie/services/chat/internal/app"
	"teamie/services/chat/internal/domain"
	"teamie/services/chat/internal/infra/flags"
)

func TestMarketplaceDomainValidationAndSymmetricContextToken(t *testing.T) {
	for _, valid := range []string{"board", "shared-notes_1", "a", "app-123"} {
		if !domain.ValidMarketplaceSlug(valid) {
			t.Fatalf("expected valid slug %q", valid)
		}
	}
	for _, invalid := range []string{"", "-bad", "_bad", "Upper", "has space", strings.Repeat("a", 65)} {
		if domain.ValidMarketplaceSlug(invalid) {
			t.Fatalf("expected invalid slug %q", invalid)
		}
	}
	for _, validURL := range []string{"https://app.example.com/ui", "http://localhost:9000/mcp"} {
		if !domain.ValidMarketplaceHTTPURL(validURL) {
			t.Fatalf("expected valid URL %q", validURL)
		}
	}
	for _, invalidURL := range []string{"", "javascript:alert(1)", "ftp://files.example.com", "https://user:pass@example.com/mcp"} {
		if domain.ValidMarketplaceHTTPURL(invalidURL) {
			t.Fatalf("expected invalid URL %q", invalidURL)
		}
	}

	secret := domain.NewMarketplaceSigningSecret()
	now := time.Unix(1700000000, 0).UTC()
	claims := domain.MarketplaceContextClaims{
		Issuer:         "teamie",
		Audience:       "app-1",
		Subject:        "user-1",
		AppID:          "app-1",
		Slug:           "board",
		WorkspaceID:    "ws-1",
		ConversationID: "conv-group-1",
		UserID:         "user-1",
		DisplayName:    "Alice",
		IssuedAt:       now.Unix(),
		ExpiresAt:      now.Add(domain.MarketplaceSessionTTL).Unix(),
	}
	token, err := domain.SignMarketplaceContextToken(secret, claims)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT parts, got %d", len(parts))
	}
	rawPayload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var rawMap map[string]any
	if err := json.Unmarshal(rawPayload, &rawMap); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if _, hasKind := rawMap["kind"]; hasKind {
		t.Fatalf("context token must not expose participant kind: %s", string(rawPayload))
	}
	if _, hasParticipantKind := rawMap["participantKind"]; hasParticipantKind {
		t.Fatalf("context token must not expose participantKind: %s", string(rawPayload))
	}

	verified, err := domain.VerifyMarketplaceContextToken(secret, token, now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("verify token: %v", err)
	}
	if verified != claims {
		t.Fatalf("verified claims mismatch: got %+v want %+v", verified, claims)
	}
	if _, err := domain.VerifyMarketplaceContextToken("wrong-secret", token, now); err == nil {
		t.Fatal("expected signature verification failure with wrong secret")
	}
	if _, err := domain.VerifyMarketplaceContextToken(secret, token, now.Add(2*time.Hour)); err == nil {
		t.Fatal("expected expired token error")
	}
}

type memMarketplaceHarness struct {
	mu          sync.Mutex
	apps        map[string]domain.MarketplaceApp
	installed   map[string]map[string]bool
	wsRoles     map[string]map[string]string
	convs       map[string]domain.Conversation
	convMembers map[string]map[string]bool
}

func newMemMarketplaceHarness() *memMarketplaceHarness {
	return &memMarketplaceHarness{
		apps:        map[string]domain.MarketplaceApp{},
		installed:   map[string]map[string]bool{},
		wsRoles:     map[string]map[string]string{},
		convs:       map[string]domain.Conversation{},
		convMembers: map[string]map[string]bool{},
	}
}

func (m *memMarketplaceHarness) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}
func (m *memMarketplaceHarness) LockSenderClientMessage(context.Context, string, string) error {
	return nil
}
func (m *memMarketplaceHarness) LockAttachmentSender(context.Context, string) error    { return nil }
func (m *memMarketplaceHarness) LockPrincipalMembership(context.Context, string) error { return nil }
func (m *memMarketplaceHarness) Ping(context.Context) error                            { return nil }

func (m *memMarketplaceHarness) WorkspaceLock(_ context.Context, workspaceID, userID string, owner, _ bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	role, ok := m.wsRoles[workspaceID][userID]
	if !ok {
		return domain.ErrNotFound()
	}
	if owner && role != "owner" {
		return domain.ErrWorkspaceOwnerRequired()
	}
	return nil
}
func (m *memMarketplaceHarness) ListWorkspaces(context.Context, string) ([]domain.Workspace, error) {
	return nil, nil
}
func (m *memMarketplaceHarness) WorkspaceMemberExists(context.Context, string, string) (bool, error) {
	return false, nil
}
func (m *memMarketplaceHarness) LockWorkspaceByIDForUpdate(context.Context, string) error { return nil }
func (m *memMarketplaceHarness) InsertWorkspace(context.Context, domain.Workspace) (time.Time, error) {
	return time.Now(), nil
}
func (m *memMarketplaceHarness) InsertWorkspaceMember(context.Context, string, string, string, *string, bool) (bool, error) {
	return false, nil
}
func (m *memMarketplaceHarness) ListWorkspaceMembers(context.Context, string) ([]domain.Member, error) {
	return nil, nil
}
func (m *memMarketplaceHarness) WorkspaceMemberIDs(context.Context, string) ([]string, error) {
	return nil, nil
}
func (m *memMarketplaceHarness) InsertWorkspaceInvite(context.Context, string, string, string, time.Time) error {
	return nil
}
func (m *memMarketplaceHarness) FindValidInvite(context.Context, string) (domain.Workspace, time.Time, error) {
	return domain.Workspace{}, time.Time{}, nil
}
func (m *memMarketplaceHarness) LockValidInviteWorkspace(context.Context, string) (domain.Workspace, error) {
	return domain.Workspace{}, nil
}
func (m *memMarketplaceHarness) GetWorkspaceMemberRole(context.Context, string, string) (string, bool, error) {
	return "", false, nil
}
func (m *memMarketplaceHarness) FindMemberAndDelegatedIDs(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (m *memMarketplaceHarness) DeleteConversationMembersInWorkspace(context.Context, string, []string) error {
	return nil
}
func (m *memMarketplaceHarness) DeleteWorkspaceMembers(context.Context, string, []string) error {
	return nil
}
func (m *memMarketplaceHarness) SharedWorkspaceMemberIDs(context.Context, string) ([]string, error) {
	return nil, nil
}

func (m *memMarketplaceHarness) GetConversationWorkspaceID(_ context.Context, conversationID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.convs[conversationID]
	if !ok {
		return "", domain.ErrNotFound()
	}
	return c.WorkspaceID, nil
}
func (m *memMarketplaceHarness) LockConversationForShare(_ context.Context, conversationID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.convs[conversationID]; !ok {
		return domain.ErrNotFound()
	}
	return nil
}
func (m *memMarketplaceHarness) ConversationAccess(_ context.Context, conversationID, userID string) (domain.Conversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.convs[conversationID]
	if !ok {
		return domain.Conversation{}, domain.ErrNotFound()
	}
	if _, member := m.wsRoles[c.WorkspaceID][userID]; !member {
		return domain.Conversation{}, domain.ErrNotFound()
	}
	if c.Type != "channel" && !m.convMembers[conversationID][userID] {
		return domain.Conversation{}, domain.ErrNotFound()
	}
	return c, nil
}
func (m *memMarketplaceHarness) ListAuthorizedConversationIDs(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (m *memMarketplaceHarness) LockConversation(context.Context, string, string) (domain.Conversation, error) {
	return domain.Conversation{}, nil
}
func (m *memMarketplaceHarness) FindDirectConversationByPairKey(context.Context, string, string) (string, bool, error) {
	return "", false, nil
}
func (m *memMarketplaceHarness) InsertConversation(context.Context, domain.Conversation, string) (time.Time, error) {
	return time.Now(), nil
}
func (m *memMarketplaceHarness) InsertConversationMember(context.Context, string, string, bool) (bool, error) {
	return false, nil
}
func (m *memMarketplaceHarness) DeleteConversationMember(context.Context, string, string) error {
	return nil
}
func (m *memMarketplaceHarness) LockConversationWithActorRole(context.Context, string, string) (domain.Conversation, string, error) {
	return domain.Conversation{}, "", nil
}
func (m *memMarketplaceHarness) ConversationRecipients(context.Context, domain.Conversation) ([]string, error) {
	return nil, nil
}
func (m *memMarketplaceHarness) CountConversationMembersAndCheckTarget(context.Context, string, string) (int, bool, error) {
	return 0, false, nil
}
func (m *memMarketplaceHarness) IncrementConversationSeq(context.Context, string) (int64, error) {
	return 0, nil
}
func (m *memMarketplaceHarness) UpsertReadCursor(context.Context, string, string, int64) error {
	return nil
}
func (m *memMarketplaceHarness) SetNotificationLevel(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (m *memMarketplaceHarness) QueryReadReceipts(context.Context, domain.Conversation, string) (domain.ReadReceipts, error) {
	return domain.ReadReceipts{}, nil
}
func (m *memMarketplaceHarness) EnqueueOutboxHint(context.Context, any) error { return nil }
func (m *memMarketplaceHarness) SyncHead(context.Context, string) (int64, error) {
	return 0, nil
}
func (m *memMarketplaceHarness) EnsureAndLockSyncHeads(context.Context, []string) error { return nil }
func (m *memMarketplaceHarness) Fanout(context.Context, []string, domain.Event) error   { return nil }
func (m *memMarketplaceHarness) QuerySyncEvents(context.Context, string, int64, int64, int) ([]domain.Event, int64, error) {
	return nil, 0, nil
}

func (m *memMarketplaceHarness) ListAllMarketplaceApps(context.Context) ([]domain.MarketplaceApp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.MarketplaceApp, 0, len(m.apps))
	for _, a := range m.apps {
		out = append(out, a)
	}
	return out, nil
}
func (m *memMarketplaceHarness) ListEnabledMarketplaceApps(context.Context) ([]domain.MarketplaceApp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.MarketplaceApp{}
	for _, a := range m.apps {
		if a.Enabled {
			out = append(out, a)
		}
	}
	return out, nil
}
func (m *memMarketplaceHarness) GetMarketplaceAppByID(_ context.Context, id string, _ bool) (domain.MarketplaceApp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.apps[id]
	if !ok {
		return domain.MarketplaceApp{}, domain.ErrMarketplaceAppUnavailable()
	}
	return a, nil
}
func (m *memMarketplaceHarness) InsertMarketplaceApp(_ context.Context, a domain.MarketplaceApp) (domain.MarketplaceApp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.apps {
		if existing.Slug == a.Slug {
			return domain.MarketplaceApp{}, domain.ErrDuplicateMarketplaceSlug()
		}
	}
	now := time.Now().UTC()
	a.CreatedAt = now
	a.UpdatedAt = now
	m.apps[a.ID] = a
	return a, nil
}
func (m *memMarketplaceHarness) UpdateMarketplaceApp(_ context.Context, a domain.MarketplaceApp) (domain.MarketplaceApp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.apps[a.ID]; !ok {
		return domain.MarketplaceApp{}, domain.ErrMarketplaceAppUnavailable()
	}
	for id, existing := range m.apps {
		if id != a.ID && existing.Slug == a.Slug {
			return domain.MarketplaceApp{}, domain.ErrDuplicateMarketplaceSlug()
		}
	}
	a.UpdatedAt = time.Now().UTC()
	m.apps[a.ID] = a
	return a, nil
}
func (m *memMarketplaceHarness) DeleteMarketplaceApp(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.apps[id]; !ok {
		return false, nil
	}
	delete(m.apps, id)
	for wsID := range m.installed {
		delete(m.installed[wsID], id)
	}
	return true, nil
}
func (m *memMarketplaceHarness) ListWorkspaceMarketplaceApps(_ context.Context, workspaceID string) ([]domain.MarketplaceApp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.MarketplaceApp{}
	for appID := range m.installed[workspaceID] {
		if a, ok := m.apps[appID]; ok && a.Enabled {
			out = append(out, a)
		}
	}
	return out, nil
}
func (m *memMarketplaceHarness) InstallWorkspaceMarketplaceApp(_ context.Context, workspaceID, appID, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.installed[workspaceID] == nil {
		m.installed[workspaceID] = map[string]bool{}
	}
	m.installed[workspaceID][appID] = true
	return nil
}
func (m *memMarketplaceHarness) UninstallWorkspaceMarketplaceApp(_ context.Context, workspaceID, appID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.installed[workspaceID], appID)
	return nil
}
func (m *memMarketplaceHarness) GetInstalledWorkspaceMarketplaceApp(_ context.Context, workspaceID, appID string) (domain.MarketplaceApp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.installed[workspaceID][appID] {
		return domain.MarketplaceApp{}, domain.ErrMarketplaceAppUnavailable()
	}
	a, ok := m.apps[appID]
	if !ok || !a.Enabled {
		return domain.MarketplaceApp{}, domain.ErrMarketplaceAppUnavailable()
	}
	return a, nil
}

func TestMarketplaceUseCasesThreeTierPermissionsAndGroupIsolation(t *testing.T) {
	ctx := context.Background()
	h := newMemMarketplaceHarness()
	svc := &app.Service{
		Tx:            h,
		Workspaces:    h,
		Conversations: h,
		Marketplace:   h,
		Flags:         flags.New(httpx.Flags{}),
	}

	admin := domain.User{ID: "u-admin", DisplayName: "Admin", Kind: "human", PlatformRole: "admin"}
	wsOwner := domain.User{ID: "u-owner", DisplayName: "Owner", Kind: "human", PlatformRole: "user"}
	wsMember := domain.User{ID: "u-member", DisplayName: "Member", Kind: "human", PlatformRole: "user"}
	outsider := domain.User{ID: "u-outsider", DisplayName: "Outsider", Kind: "human", PlatformRole: "user"}
	agentMember := domain.User{ID: "u-agent", DisplayName: "Bot", Kind: "agent", PlatformRole: "user"}

	h.wsRoles["ws-1"] = map[string]string{
		wsOwner.ID:     "owner",
		wsMember.ID:    "member",
		agentMember.ID: "member",
	}
	h.convs["c-channel"] = domain.Conversation{ID: "c-channel", WorkspaceID: "ws-1", Type: "channel", Name: "general"}
	h.convs["c-direct"] = domain.Conversation{ID: "c-direct", WorkspaceID: "ws-1", Type: "direct"}
	h.convs["c-group"] = domain.Conversation{ID: "c-group", WorkspaceID: "ws-1", Type: "group", Name: "Team Group"}
	h.convMembers["c-direct"] = map[string]bool{wsOwner.ID: true, wsMember.ID: true}
	h.convMembers["c-group"] = map[string]bool{wsOwner.ID: true, wsMember.ID: true, agentMember.ID: true}

	// 1. Only platform admin can manage catalog.
	if _, _, err := svc.CreateAdminMarketplaceApp(ctx, wsOwner, domain.CreateMarketplaceAppInput{
		Slug:   "collab-pad",
		Name:   "Collab Pad",
		WebURL: "https://pad.example.com/ui",
		MCPURL: "https://pad.example.com/mcp",
	}); err == nil {
		t.Fatal("non-admin must not create marketplace app")
	}

	createdApp, secret1, err := svc.CreateAdminMarketplaceApp(ctx, admin, domain.CreateMarketplaceAppInput{
		Slug:        "collab-pad",
		Name:        "Collab Pad",
		Description: "Collaborative MCP workspace tool",
		WebURL:      "https://pad.example.com/ui",
		MCPURL:      "https://pad.example.com/mcp",
	})
	if err != nil || !strings.HasPrefix(secret1, "tmsec_") {
		t.Fatalf("admin create app failed: %v", err)
	}

	// Duplicate slug rejected.
	if _, _, err := svc.CreateAdminMarketplaceApp(ctx, admin, domain.CreateMarketplaceAppInput{
		Slug:   "collab-pad",
		Name:   "Another",
		WebURL: "https://pad.example.com/ui",
		MCPURL: "https://pad.example.com/mcp",
	}); err == nil {
		t.Fatal("duplicate slug must be rejected")
	}

	// 2. Only workspace owner (not ordinary member or platform admin outside workspace) can install into ws-1.
	if _, err := svc.InstallWorkspaceApp(ctx, wsMember, "ws-1", createdApp.ID); err == nil {
		t.Fatal("non-owner workspace member must not install app")
	}
	if _, err := svc.InstallWorkspaceApp(ctx, admin, "ws-1", createdApp.ID); err == nil {
		t.Fatal("platform admin who is not workspace owner must not install app into workspace")
	}
	if _, err := svc.InstallWorkspaceApp(ctx, agentMember, "ws-1", createdApp.ID); err == nil {
		t.Fatal("agent principal must not install workspace app")
	}

	if _, err := svc.InstallWorkspaceApp(ctx, wsOwner, "ws-1", createdApp.ID); err != nil {
		t.Fatalf("workspace owner install failed: %v", err)
	}

	// 3. Channel and direct conversations reject marketplace apps with 403 marketplace_group_only.
	for _, nonGroupID := range []string{"c-channel", "c-direct"} {
		if _, err := svc.ListConversationApps(ctx, wsOwner, nonGroupID); err == nil {
			t.Fatalf("expected error listing apps on %s", nonGroupID)
		} else {
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != "marketplace_group_only" {
				t.Fatalf("expected marketplace_group_only on %s, got %v", nonGroupID, err)
			}
		}
		if _, err := svc.CreateConversationAppSession(ctx, wsOwner, nonGroupID, createdApp.ID); err == nil {
			t.Fatalf("expected error creating app session on %s", nonGroupID)
		}
	}

	// 4. Group conversation members (both human and Agent) can list apps and mint identical symmetric Context Tokens.
	if _, err := svc.ListConversationApps(ctx, outsider, "c-group"); err == nil {
		t.Fatal("outsider must not list group conversation apps")
	}
	groupApps, err := svc.ListConversationApps(ctx, agentMember, "c-group")
	if err != nil || len(groupApps) != 1 || groupApps[0].ID != createdApp.ID {
		t.Fatalf("agent group member list apps failed: %v (%+v)", err, groupApps)
	}

	humanSession, err := svc.CreateConversationAppSession(ctx, wsMember, "c-group", createdApp.ID)
	if err != nil {
		t.Fatalf("human group session failed: %v", err)
	}
	agentSession, err := svc.CreateConversationAppSession(ctx, agentMember, "c-group", createdApp.ID)
	if err != nil {
		t.Fatalf("agent group session failed: %v", err)
	}

	humanClaims, err := domain.VerifyMarketplaceContextToken(secret1, humanSession.Token, time.Now().UTC())
	if err != nil || humanClaims.UserID != wsMember.ID || humanClaims.ConversationID != "c-group" || humanClaims.WorkspaceID != "ws-1" {
		t.Fatalf("verify human token failed: %v (%+v)", err, humanClaims)
	}
	agentClaims, err := domain.VerifyMarketplaceContextToken(secret1, agentSession.Token, time.Now().UTC())
	if err != nil || agentClaims.UserID != agentMember.ID || agentClaims.ConversationID != "c-group" || agentClaims.WorkspaceID != "ws-1" {
		t.Fatalf("verify agent token failed: %v (%+v)", err, agentClaims)
	}

	// 5. Secret rotation invalidates old tokens and issues a new secret.
	_, secret2, err := svc.RotateAdminMarketplaceAppSecret(ctx, admin, createdApp.ID)
	if err != nil || secret2 == "" || secret2 == secret1 {
		t.Fatalf("rotate secret failed: %v", err)
	}
	if _, err := domain.VerifyMarketplaceContextToken(secret2, humanSession.Token, time.Now().UTC()); err == nil {
		t.Fatal("old token must not verify under rotated secret")
	}

	// 6. Disabling or uninstalling the app immediately blocks group session creation.
	disabled := false
	if _, err := svc.UpdateAdminMarketplaceApp(ctx, admin, createdApp.ID, domain.UpdateMarketplaceAppInput{Enabled: &disabled}); err != nil {
		t.Fatalf("disable app failed: %v", err)
	}
	if _, err := svc.CreateConversationAppSession(ctx, wsMember, "c-group", createdApp.ID); err == nil {
		t.Fatal("disabled app must not issue group sessions")
	}
	enabled := true
	if _, err := svc.UpdateAdminMarketplaceApp(ctx, admin, createdApp.ID, domain.UpdateMarketplaceAppInput{Enabled: &enabled}); err != nil {
		t.Fatalf("re-enable app failed: %v", err)
	}
	if err := svc.UninstallWorkspaceApp(ctx, wsOwner, "ws-1", createdApp.ID); err != nil {
		t.Fatalf("uninstall app failed: %v", err)
	}
	if _, err := svc.CreateConversationAppSession(ctx, wsMember, "c-group", createdApp.ID); err == nil {
		t.Fatal("uninstalled app must not issue group sessions")
	}
}

type fakeMarketplaceIdentity struct {
	sessions map[string]httpx.Session
	users    map[string]httpx.User
}

func (f *fakeMarketplaceIdentity) Validate(_ context.Context, token string) (httpx.Session, error) {
	if s, ok := f.sessions[token]; ok {
		return s, nil
	}
	return httpx.Session{}, httpx.Err(401, "unauthenticated", "Sign in to continue")
}

func (f *fakeMarketplaceIdentity) ValidatePrincipal(_ context.Context, token, serviceKey string) (httpx.Session, error) {
	if serviceKey == "svc-secret" {
		if u, ok := f.users[token]; ok {
			return httpx.Session{User: u, ServicePrincipal: true}, nil
		}
	}
	return httpx.Session{}, httpx.Err(401, "unauthenticated", "Invalid principal")
}

func (f *fakeMarketplaceIdentity) Users(_ context.Context, ids []string) ([]httpx.User, error) {
	out := []httpx.User{}
	for _, id := range ids {
		for _, u := range f.users {
			if u.ID == id {
				out = append(out, u)
			}
		}
	}
	return out, nil
}

func (f *fakeMarketplaceIdentity) Ping(context.Context) error { return nil }

func TestMarketplaceHTTPAndPostgresEndToEnd(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, testdb.URL(t), httpx.Flags{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.DB.Close)

	t.Setenv("INTERNAL_SERVICE_KEY", "svc-secret")
	admin := httpx.User{ID: httpx.ID(), Email: "admin@example.com", DisplayName: "Platform Admin", Kind: "human", PlatformRole: "admin"}
	owner := httpx.User{ID: httpx.ID(), Email: "owner@example.com", DisplayName: "Workspace Owner", Kind: "human", PlatformRole: "user"}
	member := httpx.User{ID: httpx.ID(), Email: "member@example.com", DisplayName: "Group Member", Kind: "human", PlatformRole: "user"}
	agent := httpx.User{ID: httpx.ID(), DisplayName: "Helper Agent", Kind: "agent", PlatformRole: "user"}

	ws, ch, err := s.CreateWorkspace(ctx, owner.ID, "Eng Workspace")
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := s.CreateInvite(ctx, owner.ID, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AcceptInvite(ctx, member.ID, code); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPrincipalWorkspaces(ctx, agent, owner, []string{ws.ID}, 1); err != nil {
		t.Fatal(err)
	}
	group, err := s.CreateConversation(ctx, owner.ID, ws.ID, "group", "Project Room", []string{member.ID, agent.ID})
	if err != nil {
		t.Fatal(err)
	}

	idAuth := &fakeMarketplaceIdentity{
		sessions: map[string]httpx.Session{
			"admin-tok":  {User: admin, CSRFToken: "csrf-admin"},
			"owner-tok":  {User: owner, CSRFToken: "csrf-owner"},
			"member-tok": {User: member, CSRFToken: "csrf-member"},
		},
		users: map[string]httpx.User{
			"agent-principal-tok": agent,
			admin.ID:              admin,
			owner.ID:              owner,
			member.ID:             member,
		},
	}
	srv := httptest.NewServer((&Handler{Store: s, Identity: idAuth, Origin: "https://teamie.example.com"}).Routes())
	defer srv.Close()

	doHuman := func(tok, csrf, method, path string, body any) (*http.Response, map[string]any) {
		t.Helper()
		var rdr *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req, err := http.NewRequest(method, srv.URL+path, rdr)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: httpx.CookieName, Value: tok})
		req.Header.Set("X-CSRF-Token", csrf)
		req.Header.Set("Origin", "https://teamie.example.com")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp, out
	}

	// Admin creates app
	resp, createdBody := doHuman("admin-tok", "csrf-admin", "POST", "/api/v1/admin/marketplace/apps", map[string]any{
		"slug":        "task-board",
		"name":        "Task Board",
		"description": "Shared group task board",
		"webUrl":      "https://board.example.com/ui",
		"mcpUrl":      "https://board.example.com/mcp",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("expected 201 creating app, got %d: %+v", resp.StatusCode, createdBody)
	}
	appObj := createdBody["app"].(map[string]any)
	appID := appObj["id"].(string)
	signingSecret := createdBody["signingSecret"].(string)

	// Workspace owner installs app
	resp, installBody := doHuman("owner-tok", "csrf-owner", "POST", "/api/v1/workspaces/"+ws.ID+"/apps", map[string]any{
		"appId": appID,
	})
	if resp.StatusCode != 201 {
		t.Fatalf("expected 201 installing app, got %d: %+v", resp.StatusCode, installBody)
	}

	// Channel conversation rejects session
	resp, _ = doHuman("member-tok", "csrf-member", "POST", "/api/v1/conversations/"+ch.ID+"/apps/"+appID+"/session", map[string]any{})
	if resp.StatusCode != 403 {
		t.Fatalf("expected 403 on channel app session, got %d", resp.StatusCode)
	}

	// Human member mints group app session
	resp, sessionBody := doHuman("member-tok", "csrf-member", "POST", "/api/v1/conversations/"+group.ID+"/apps/"+appID+"/session", map[string]any{})
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 on group app session, got %d: %+v", resp.StatusCode, sessionBody)
	}
	humanToken := sessionBody["token"].(string)
	claims, err := domain.VerifyMarketplaceContextToken(signingSecret, humanToken, time.Now().UTC())
	if err != nil || claims.UserID != member.ID || claims.ConversationID != group.ID {
		t.Fatalf("human session token verification failed: %v (%+v)", err, claims)
	}

	// Agent principal lists group apps and mints session via Bearer + X-Teamie-Service-Key
	agentReq, _ := http.NewRequest("POST", srv.URL+"/api/v1/conversations/"+group.ID+"/apps/"+appID+"/session", bytes.NewReader([]byte("{}")))
	agentReq.Header.Set("Authorization", "Bearer agent-principal-tok")
	agentReq.Header.Set("X-Teamie-Service-Key", "svc-secret")
	agentReq.Header.Set("Content-Type", "application/json")
	agentResp, err := http.DefaultClient.Do(agentReq)
	if err != nil {
		t.Fatal(err)
	}
	defer agentResp.Body.Close()
	if agentResp.StatusCode != 200 {
		t.Fatalf("expected 200 for agent principal session, got %d", agentResp.StatusCode)
	}
	var agentSessionBody map[string]any
	_ = json.NewDecoder(agentResp.Body).Decode(&agentSessionBody)
	agentClaims, err := domain.VerifyMarketplaceContextToken(signingSecret, agentSessionBody["token"].(string), time.Now().UTC())
	if err != nil || agentClaims.UserID != agent.ID || agentClaims.ConversationID != group.ID {
		t.Fatalf("agent session token verification failed: %v (%+v)", err, agentClaims)
	}
}

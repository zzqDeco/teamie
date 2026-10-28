package control

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/im/kitex_gen/im"
	"testing"
	"time"
)

func databaseURL(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("DATABASE_URL_TEST")
	if raw == "" {
		t.Skip("DATABASE_URL_TEST required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	db, e := pgxpool.New(ctx, raw)
	if e != nil {
		t.Fatal(e)
	}
	schema := "control_test_" + strings.ReplaceAll(id(), "-", "")
	if _, e = db.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); e != nil {
		db.Close()
		t.Fatal(e)
	}
	t.Cleanup(func() { _, _ = db.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); db.Close() })
	u, e := url.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

type fakePrincipal struct {
	revision         int64
	enabled, deleted bool
}
type authority struct {
	users               map[string]*im.User
	members             map[string][]string
	principals          map[string]fakePrincipal
	membershipRevisions map[string]int64
	failProvision       bool
	failMemberships     bool
}

func (f *authority) Validate(_ context.Context, token string) (*im.User, string, error) {
	u := f.users[token]
	if u == nil {
		return nil, "", errAPI(401, "unauthenticated", "Sign in")
	}
	return u, "csrf", nil
}
func (f *authority) Users(_ context.Context, ids []string) (map[string]*im.User, error) {
	out := map[string]*im.User{}
	for _, id := range ids {
		if u := f.users[id]; u != nil {
			out[id] = u
		}
	}
	return out, nil
}
func (f *authority) Workspaces(_ context.Context, user string) ([]string, error) {
	return append([]string{}, f.members[user]...), nil
}
func (f *authority) Participants(_ context.Context, workspace, user string) ([]string, error) {
	if !contains(f.members[user], workspace) {
		return nil, errAPI(404, "not_found", "Workspace unavailable")
	}
	out := []string{}
	for id, workspaces := range f.members {
		if contains(workspaces, workspace) {
			out = append(out, id)
		}
	}
	return out, nil
}
func (f *authority) Provision(_ context.Context, a Agent, revision int64, enabled, deleted bool) error {
	if f.failProvision {
		return fmt.Errorf("IM offline")
	}
	old := f.principals[a.ID]
	if revision < old.revision {
		return nil
	}
	if revision == old.revision && (old.enabled != enabled || old.deleted != deleted) {
		return fmt.Errorf("revision conflict")
	}
	if old.deleted && !deleted {
		return fmt.Errorf("deleted")
	}
	f.principals[a.ID] = fakePrincipal{revision, enabled, deleted}
	return nil
}
func (f *authority) Memberships(_ context.Context, a Agent, ids []string) ([]string, error) {
	if f.failMemberships {
		return nil, fmt.Errorf("chat offline")
	}
	if a.MembershipRevision <= f.membershipRevisions[a.ID] {
		return append([]string{}, f.members[a.ID]...), nil
	}
	for _, id := range ids {
		if !contains(f.members[a.OwnerID], id) {
			return nil, fmt.Errorf("owner lost workspace")
		}
	}
	f.members[a.ID] = append([]string{}, ids...)
	f.membershipRevisions[a.ID] = a.MembershipRevision
	return append([]string{}, ids...), nil
}
func (f *authority) Ping(context.Context) error { return nil }
func contains(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
func ptr[T any](v T) *T { return &v }
func flags(t *testing.T, path string, enabled bool) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"revision": "test", "flags": map[string]bool{"agents.enabled": enabled, "agents.create": true, "agents.update": true, "agents.delete": true, "agents.discover": true, "agents.runtime.v3": true, "agents.tasks.execute": true}})
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func setupControl(t *testing.T) (*Store, *authority, string) {
	t.Helper()
	f := &authority{users: map[string]*im.User{"owner": {Id: "owner", DisplayName: "Human Owner", Kind: "human", PlatformRole: "user"}, "admin": {Id: "admin", DisplayName: "Other admin", Kind: "human", PlatformRole: "admin"}}, members: map[string][]string{"owner": {"ws1", "ws2"}, "admin": {"ws1"}}, principals: map[string]fakePrincipal{}, membershipRevisions: map[string]int64{}}
	path := filepath.Join(t.TempDir(), "flags.json")
	flags(t, path, true)
	s, e := Open(context.Background(), databaseURL(t), f, Flags{Path: path})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.DB.Close)
	s.RuntimeToken = "runtime-secret"
	return s, f, path
}
func createAgent(t *testing.T, s *Store, workspaces []string) Agent {
	t.Helper()
	a, e := s.Create(context.Background(), "owner", Changes{Name: ptr("Assistant"), WorkspaceIDs: &workspaces})
	if e != nil {
		t.Fatal(e)
	}
	return a
}

func TestOwnerIsolationVisibilityAndReconciliation(t *testing.T) {
	ctx := context.Background()
	s, f, path := setupControl(t)
	if _, e := s.Create(ctx, "owner", Changes{Name: ptr("Outsider"), WorkspaceIDs: ptr([]string{"outside"})}); e == nil {
		t.Fatal("foreign workspace admitted")
	}
	private := createAgent(t, s, []string{})
	if len(private.WorkspaceIDs) != 0 || !s.definition(private).Enabled {
		t.Fatal("empty visibility must be a valid private agent")
	}
	a := createAgent(t, s, []string{"ws2", "ws1", "ws1"})
	if a.ID != a.PrincipalID || a.SessionID != "agent:"+a.ID || !f.principals[a.ID].enabled || a.AppliedRevision != a.Revision {
		t.Fatal("not atomically reconciled to ordinary identity", a)
	}
	if _, e := s.Owned(ctx, "admin", a.ID); e == nil {
		t.Fatal("platform admin bypassed ownership")
	}
	if _, e := s.Update(ctx, "admin", a.ID, Changes{Name: ptr("Hijack")}); e == nil {
		t.Fatal("platform admin changed owner's agent")
	}
	if e := s.Delete(ctx, "admin", a.ID); e == nil {
		t.Fatal("platform admin deleted owner's agent")
	}
	if got, e := s.Discover(ctx, "ws1", "admin"); e != nil || len(got) != 1 {
		t.Fatal("workspace discovery", got, e)
	}
	decorated, e := s.decorate(ctx, []Agent{a})
	if e != nil || decorated[0].OwnerName != "Human Owner" {
		t.Fatal("owner name not loaded through identity", e)
	}
	// A moderator removes an existing delegated membership. Background runs and
	// unrelated profile edits must not turn a removal into an automatic join.
	f.members[a.ID] = []string{"ws2"}
	if got, e := s.Discover(ctx, "ws1", "admin"); e != nil || len(got) != 0 {
		t.Fatal("configured visibility overrode current membership", e)
	}
	if e = s.Reconcile(ctx, a.ID); e != nil {
		t.Fatal(e)
	}
	a, e = s.Update(ctx, "owner", a.ID, Changes{Name: ptr("Renamed")})
	if e != nil {
		t.Fatal(e)
	}
	if contains(a.WorkspaceIDs, "ws1") || contains(f.members[a.ID], "ws1") {
		t.Fatal("profile update recreated moderated membership")
	}
	a, e = s.Update(ctx, "owner", a.ID, Changes{WorkspaceIDs: ptr([]string{"ws1", "ws2"})})
	if e != nil || !contains(f.members[a.ID], "ws1") {
		t.Fatal("explicit owner visibility change did not grant", e)
	}
	f.members["owner"] = []string{"ws1"}
	if e = s.Reconcile(ctx, a.ID); e != nil {
		t.Fatal(e)
	}
	a, e = s.Owned(ctx, "owner", a.ID)
	if e != nil || contains(a.WorkspaceIDs, "ws2") || contains(f.members[a.ID], "ws2") {
		t.Fatal("owner membership loss failed to revoke", e)
	}
	flags(t, path, false)
	if e = s.Reconcile(ctx, a.ID); e != nil {
		t.Fatal(e)
	}
	a, e = s.Owned(ctx, "owner", a.ID)
	if e != nil || s.definition(a).Enabled || f.principals[a.ID].enabled {
		t.Fatal("global flag failed closed", e)
	}
	if list, e := s.ListOwned(ctx, "owner"); e != nil || len(list) != 2 {
		t.Fatal("disabled feature hid persisted profiles", e)
	}
	if _, e = s.Create(ctx, "owner", Changes{Name: ptr("Blocked"), WorkspaceIDs: ptr([]string{})}); e == nil {
		t.Fatal("disabled global feature accepted creation")
	}
}

func TestDurablePendingDisableDeleteAndRuntimeState(t *testing.T) {
	ctx := context.Background()
	s, f, _ := setupControl(t)
	f.failProvision = true
	a, e := s.Create(ctx, "owner", Changes{Name: ptr("Assistant"), WorkspaceIDs: ptr([]string{"ws1"})})
	if e == nil {
		t.Fatal("create succeeded while identity offline")
	}
	if a.ID == "" || s.definition(a).Enabled || a.AppliedRevision == a.Revision {
		t.Fatal("unapplied principal advertised enabled", a)
	}
	f.failProvision = false
	// A new process can recover from durable desired state without an in-memory queue.
	restarted := &Store{DB: s.DB, IM: f, Flags: s.Flags, RuntimeToken: s.RuntimeToken}
	if e := restarted.Reconcile(ctx, a.ID); e != nil {
		t.Fatal(e)
	}
	a, e = s.Owned(ctx, "owner", a.ID)
	if e != nil || !s.definition(a).Enabled {
		t.Fatal("pending creation not recovered", e)
	}
	rpc := &RPC{Store: s}
	if _, e := rpc.ListAgents(ctx, &agent.AgentListRequest{ServiceToken: "wrong"}); e == nil {
		t.Fatal("unauthenticated runtime list")
	}
	if ack, e := rpc.ReportRuntime(ctx, &agent.RuntimeReport{ServiceToken: s.RuntimeToken, AgentId: a.ID, RuntimeState: "running", SessionId: "foreign"}); e != nil || ack.Ok {
		t.Fatal("foreign runtime session accepted", e)
	}
	active := time.Now().UTC().Format(time.RFC3339Nano)
	raw := "provider secret should never leave runtime"
	if ack, e := rpc.ReportRuntime(ctx, &agent.RuntimeReport{ServiceToken: s.RuntimeToken, AgentId: a.ID, RuntimeState: "waiting_configuration", SessionId: "agent:" + a.ID, LastActiveAt: &active, LastError: &raw}); e != nil || !ack.Ok {
		t.Fatal("runtime state", e)
	}
	a, e = s.Owned(ctx, "owner", a.ID)
	if e != nil || a.RuntimeState != "waiting_configuration" || a.LastError != "runtime_error" || a.LastActiveAt == nil {
		t.Fatal("safe runtime state not durable", a, e)
	}
	f.failProvision = true
	if _, e = s.Update(ctx, "owner", a.ID, Changes{Enabled: ptr(false)}); e == nil {
		t.Fatal("revocation acknowledged while identity offline")
	}
	a, e = s.Owned(ctx, "owner", a.ID)
	if e != nil || s.definition(a).Enabled || a.Enabled {
		t.Fatal("disable desired state not durable", e)
	}
	f.failProvision = false
	if e = s.Reconcile(ctx, a.ID); e != nil || f.principals[a.ID].enabled {
		t.Fatal("disable retry did not revoke principal", e)
	}
	if e = s.Delete(ctx, "owner", a.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Owned(ctx, "owner", a.ID); e == nil {
		t.Fatal("deleted profile visible")
	}
	list, e := rpc.ListAgents(ctx, &agent.AgentListRequest{ServiceToken: s.RuntimeToken})
	if e != nil || len(list.Agents) != 1 || list.Agents[0].DeletedAt == nil || list.Agents[0].Enabled {
		t.Fatal("runtime cannot observe deletion", list, e)
	}
	if !f.principals[a.ID].deleted || len(f.members[a.ID]) != 0 {
		t.Fatal("deleted identity retained authority")
	}
}

func TestHumanOnlyHTTPStrictSettingsAndCSRF(t *testing.T) {
	s, f, _ := setupControl(t)
	h := (&Handler{Store: s, Origin: "https://teamie.example"}).Routes()
	call := func(method, path, body, token, csrf, origin, bearer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "teamie_session", Value: token})
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Origin", origin)
		if bearer != "" {
			r.Header.Set("Authorization", bearer)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	valid := `{"name":"Assistant","workspaceIds":[]}`
	if w := call("POST", "/api/v1/agents", valid, "owner", "", "https://teamie.example", ""); w.Code != 403 {
		t.Fatal("missing CSRF", w.Code)
	}
	if w := call("POST", "/api/v1/agents", valid, "owner", "csrf", "https://evil.example", ""); w.Code != 403 {
		t.Fatal("foreign origin", w.Code)
	}
	if w := call("POST", "/api/v1/agents", `{"name":"Assistant","workspaceIds":[],"prompt":"bypass"}`, "owner", "csrf", "https://teamie.example", ""); w.Code != 400 {
		t.Fatal("runtime settings reached owner API", w.Code, w.Body.String())
	}
	if w := call("POST", "/api/v1/agents", valid, "owner", "csrf", "https://teamie.example", "Bearer agent-token"); w.Code != 403 {
		t.Fatal("bearer impersonation", w.Code)
	}
	f.users["machine"] = &im.User{Id: "machine", Kind: "agent"}
	if w := call("GET", "/api/v1/agents", "", "machine", "", "", ""); w.Code != 403 {
		t.Fatal("machine cookie reached owner API", w.Code)
	}
	w := call("POST", "/api/v1/agents", valid, "owner", "csrf", "https://teamie.example", "")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var out struct {
		Agent Agent `json:"agent"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil || out.Agent.OwnerName != "Human Owner" || out.Agent.PrincipalID != out.Agent.ID {
		t.Fatal("public DTO", e, w.Body.String())
	}
	if w := call("GET", "/api/v1/agents/"+out.Agent.ID, "", "admin", "", "", ""); w.Code != 404 {
		t.Fatal("foreign owner read", w.Code)
	}
}

func TestFeatureFlagsDefaultFailClosed(t *testing.T) {
	if (Flags{}).Enabled("agents.enabled") {
		t.Fatal("default enabled")
	}
	path := filepath.Join(t.TempDir(), "flags.json")
	if e := os.WriteFile(path, []byte(`{"revision":"v1","flags":{"im.dm.create":true}}`), 0600); e != nil {
		t.Fatal(e)
	}
	if (Flags{Path: path}).Enabled("agents.create") {
		t.Fatal("missing v1 capability enabled agents")
	}
	if e := os.WriteFile(path, []byte(`not-json`), 0600); e != nil {
		t.Fatal(e)
	}
	if (Flags{Path: path}).Enabled("agents.enabled") {
		t.Fatal("malformed config enabled")
	}
}

func TestCreateAndUpdateSurfaceReconcileFailuresAndIdempotentRetry(t *testing.T) {
	ctx := context.Background()
	s, f, _ := setupControl(t)
	h := (&Handler{Store: s, Origin: "https://teamie.example"}).Routes()

	callHTTP := func(method, path, body, idempotencyKey string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "teamie_session", Value: "owner"})
		r.Header.Set("X-CSRF-Token", "csrf")
		r.Header.Set("Origin", "https://teamie.example")
		if idempotencyKey != "" {
			r.Header.Set("Idempotency-Key", idempotencyKey)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// 1. Create failure when Identity ProvisionPrincipal fails, followed by idempotent retry via Idempotency-Key.
	f.failProvision = true
	w := callHTTP("POST", "/api/v1/agents", `{"name":"KeyedAgent","workspaceIds":["ws1"]}`, "create-key-1")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when Identity fails during create, got %d: %s", w.Code, w.Body.String())
	}
	f.failProvision = false
	w = callHTTP("POST", "/api/v1/agents", `{"name":"KeyedAgent","workspaceIds":["ws1"]}`, "create-key-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 on create retry after Identity recovers, got %d: %s", w.Code, w.Body.String())
	}
	var keyedOut struct {
		Agent Agent `json:"agent"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &keyedOut); err != nil {
		t.Fatal(err)
	}
	// Repeating the same Idempotency-Key after 201 returns the same Agent without duplicating.
	w = callHTTP("POST", "/api/v1/agents", `{"name":"KeyedAgent","workspaceIds":["ws1"]}`, "create-key-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 on idempotent replay, got %d: %s", w.Code, w.Body.String())
	}
	var replayOut struct {
		Agent Agent `json:"agent"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &replayOut); err != nil || replayOut.Agent.ID != keyedOut.Agent.ID {
		t.Fatalf("idempotent replay returned different agent: %v vs %v", replayOut.Agent.ID, keyedOut.Agent.ID)
	}
	// Reusing the same Idempotency-Key with a conflicting payload is rejected with 409.
	if w := callHTTP("POST", "/api/v1/agents", `{"name":"DifferentName","workspaceIds":["ws1"]}`, "create-key-1"); w.Code != http.StatusConflict {
		t.Fatalf("expected 409 on conflicting idempotency key reuse, got %d: %s", w.Code, w.Body.String())
	}

	// 2. Create failure when Chat membership sync fails (without header), followed by retry without duplicating.
	f.failMemberships = true
	pendingChat, err := s.Create(ctx, "owner", Changes{Name: ptr("ChatSyncedAgent"), WorkspaceIDs: ptr([]string{"ws1"})})
	if err == nil {
		t.Fatal("expected create error when Chat membership sync fails")
	}
	if pendingChat.ID == "" || pendingChat.AppliedRevision != 0 || f.principals[pendingChat.ID].enabled {
		t.Fatalf("unexpected state after Chat membership failure: %+v principal=%+v", pendingChat, f.principals[pendingChat.ID])
	}
	f.failMemberships = false
	recoveredChat, err := s.Create(ctx, "owner", Changes{Name: ptr("ChatSyncedAgent"), WorkspaceIDs: ptr([]string{"ws1"})})
	if err != nil {
		t.Fatalf("expected retry of pending create to succeed: %v", err)
	}
	if recoveredChat.ID != pendingChat.ID || recoveredChat.AppliedRevision != recoveredChat.Revision || !f.principals[recoveredChat.ID].enabled {
		t.Fatalf("retry did not reuse and reconcile pending agent: got %+v want ID %s", recoveredChat, pendingChat.ID)
	}
	ownedList, err := s.ListOwned(ctx, "owner")
	if err != nil || len(ownedList) != 2 {
		t.Fatalf("expected exactly 2 owned agents without duplicates, got %d (%v)", len(ownedList), err)
	}

	// 3. Ordinary Update (rename, workspace change, re-enable) fails with 503 when Identity or Chat fails.
	f.failProvision = true
	if w := callHTTP("PATCH", "/api/v1/agents/"+recoveredChat.ID, `{"name":"RenamedWhileOffline"}`, ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when Identity fails during rename update, got %d: %s", w.Code, w.Body.String())
	}
	stored, err := s.Owned(ctx, "owner", recoveredChat.ID)
	if err != nil || stored.AppliedRevision == stored.Revision || stored.RuntimeState != "error" {
		t.Fatalf("expected unapplied revision after failed rename update: %+v err=%v", stored, err)
	}
	f.failProvision = false
	if w := callHTTP("PATCH", "/api/v1/agents/"+recoveredChat.ID, `{"name":"RenamedWhileOffline"}`, ""); w.Code != http.StatusOK {
		t.Fatalf("expected 200 when retrying rename update, got %d: %s", w.Code, w.Body.String())
	}
	stored, err = s.Owned(ctx, "owner", recoveredChat.ID)
	if err != nil || stored.AppliedRevision != stored.Revision || stored.Name != "RenamedWhileOffline" {
		t.Fatalf("rename update did not converge on retry: %+v err=%v", stored, err)
	}

	f.failMemberships = true
	if w := callHTTP("PATCH", "/api/v1/agents/"+recoveredChat.ID, `{"workspaceIds":["ws1","ws2"]}`, ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when Chat fails during workspace update, got %d: %s", w.Code, w.Body.String())
	}
	stored, err = s.Owned(ctx, "owner", recoveredChat.ID)
	if err != nil || stored.AppliedRevision == stored.Revision || stored.AppliedMembershipRevision == stored.MembershipRevision {
		t.Fatalf("expected unapplied membership revision after Chat failure: %+v err=%v", stored, err)
	}
	f.failMemberships = false
	if w := callHTTP("PATCH", "/api/v1/agents/"+recoveredChat.ID, `{"workspaceIds":["ws1","ws2"]}`, ""); w.Code != http.StatusOK {
		t.Fatalf("expected 200 when retrying workspace update, got %d: %s", w.Code, w.Body.String())
	}
	stored, err = s.Owned(ctx, "owner", recoveredChat.ID)
	if err != nil || stored.AppliedRevision != stored.Revision || stored.AppliedMembershipRevision != stored.MembershipRevision || !contains(f.members[recoveredChat.ID], "ws2") {
		t.Fatalf("workspace update did not converge on retry: %+v members=%v err=%v", stored, f.members[recoveredChat.ID], err)
	}
}

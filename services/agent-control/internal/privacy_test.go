package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"teamie/contracts/gen/im/kitex_gen/im"
)

func publicRequest(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.AddCookie(&http.Cookie{Name: "teamie_session", Value: token})
	}
	r.Header.Set("Origin", "https://teamie.test")
	r.Header.Set("X-CSRF-Token", "csrf")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func requirePublicProfile(t *testing.T, profile map[string]any, workspaces []string) {
	t.Helper()
	fields := []string{"id", "principalId", "ownerId", "ownerName", "name", "description", "workspaceIds", "enabled", "createdAt", "updatedAt"}
	expectedLen := len(fields)
	if _, hasModelID := profile["modelId"]; hasModelID {
		expectedLen++
	}
	if len(profile) != expectedLen {
		t.Fatalf("unexpected public profile fields: %v", profile)
	}
	for _, field := range fields {
		if _, ok := profile[field]; !ok {
			t.Fatalf("missing public field %s", field)
		}
	}
	if profile["id"] != profile["principalId"] || profile["ownerId"] != "owner" || profile["ownerName"] != "Human Owner" {
		t.Fatalf("public attribution lost: %v", profile)
	}
	want := make([]any, len(workspaces))
	for n, id := range workspaces {
		want[n] = id
	}
	if !reflect.DeepEqual(profile["workspaceIds"], want) {
		t.Fatalf("workspace disclosure: got %v, want %v", profile["workspaceIds"], want)
	}
}

func responseProfile(t *testing.T, w *httptest.ResponseRecorder, status int, workspaces []string) map[string]any {
	t.Helper()
	if w.Code != status {
		t.Fatalf("profile response %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Agent map[string]any `json:"agent"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	requirePublicProfile(t, out.Agent, workspaces)
	return out.Agent
}

func responseProfiles(t *testing.T, w *httptest.ResponseRecorder, workspaces []string) []map[string]any {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("profiles response %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Agents == nil {
		t.Fatalf("invalid profiles response: %v %s", err, w.Body.String())
	}
	for _, profile := range out.Agents {
		requirePublicProfile(t, profile, workspaces)
	}
	return out.Agents
}

func requireNoPrivateData(t *testing.T, body string, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if strings.Contains(body, value) {
			t.Fatalf("private fixture value reached browser response: %q", value)
		}
	}
}

func TestSingleProfileHTTPDoesNotSerializeInternalAgent(t *testing.T) {
	// Include deleted and runtime metadata even though ordinary owner reads
	// exclude tombstones. The response projection must reject every such field.
	now := time.Now().UTC()
	f := &authority{users: map[string]*im.User{"owner": {DisplayName: "Human Owner"}}}
	h := &Handler{Store: &Store{IM: f}}
	a := Agent{
		ID: "public-id", PrincipalID: "public-id", OwnerID: "owner", Name: "Assistant",
		WorkspaceIDs: []string{}, Enabled: true, CreatedAt: now, UpdatedAt: now,
		Revision: 749, MembershipRevision: 81, AppliedRevision: 749, AppliedMembershipRevision: 81,
		RuntimeState: "private-runtime-state", SessionID: "private-cross-conversation-session",
		LastActiveAt: &now, DeletedAt: &now,
		LastError: "raw-provider-error and a private task description",
	}
	w := httptest.NewRecorder()
	if err := h.one(w, httptest.NewRequest("GET", "/api/v1/agents/public-id", nil), a, 200); err != nil {
		t.Fatal(err)
	}
	responseProfile(t, w, 200, []string{})
	requireNoPrivateData(t, w.Body.String(), a.RuntimeState, a.SessionID, a.LastError, `"revision"`, `"membershipRevision"`, `"appliedRevision"`)
}

func TestRetiredRuntimeHTTPRoutesHaveNoDependencies(t *testing.T) {
	// No Store or runtime client exists. Matching a handler which attempts IM,
	// database or RuntimeAccess RPC work would panic instead of returning 404.
	h := (&Handler{}).Routes()
	routes := []struct{ method, suffix string }{
		{"GET", "/runtime"},
		{"GET", "/runs"}, {"GET", "/runs/private-run"},
		{"GET", "/tasks"}, {"GET", "/tasks/private-task"},
		{"GET", "/events"}, {"GET", "/events/stream"},
		{"POST", "/runs/private-run/cancel"}, {"POST", "/runs/private-run/retry"},
		{"POST", "/tasks/private-task/cancel"},
	}
	for _, token := range []string{"", "owner", "admin", "peer", "agent-cookie"} {
		for _, route := range routes {
			t.Run(token+route.method+route.suffix, func(t *testing.T) {
				r := httptest.NewRequest(route.method, "/api/v1/agents/private-agent"+route.suffix+"?cursor=-1&limit=101&ownerId=owner", strings.NewReader(`{"description":"private-task-description"}`))
				if token != "" {
					r.AddCookie(&http.Cookie{Name: "teamie_session", Value: token})
				}
				r.Header.Set("Last-Event-ID", "private-event")
				r.Header.Set("Accept", "text/event-stream")
				if token == "agent-cookie" {
					r.Header.Set("Authorization", "Bearer private-agent-token")
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != http.StatusNotFound || w.Header().Get("Content-Type") != "application/json" || w.Flushed {
					t.Fatalf("retired route response: %d %v", w.Code, w.Header())
				}
				requireNoPrivateData(t, w.Body.String(), "private-agent", "private-run", "private-task", "private-event")
				var out struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Error.Code != "not_found" {
					t.Fatalf("invalid 404 response: %s", w.Body.String())
				}
			})
		}
	}
}

type unavailableAuthority struct{ Authority }

func (unavailableAuthority) Validate(context.Context, string) (*im.User, string, error) {
	return nil, "", errors.New("raw provider error: private-session and cross-conversation-task")
}

func TestPublicHTTPErrorDoesNotExposeDependencyDetails(t *testing.T) {
	h := (&Handler{Store: &Store{IM: unavailableAuthority{}}}).Routes()
	w := publicRequest(h, "GET", "/api/v1/agents", "owner", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal(w.Code)
	}
	requireNoPrivateData(t, w.Body.String(), "raw provider error", "private-session", "cross-conversation-task")
	if !strings.Contains(w.Body.String(), `"code":"control_unavailable"`) {
		t.Fatal(w.Body.String())
	}
}

func TestPublicHTTPProfilesPreserveOwnershipAndScopeDiscovery(t *testing.T) {
	s, f, path := setupControl(t)
	f.users["peer"] = &im.User{Id: "peer", Kind: "human", PlatformRole: "user"}
	f.users["outsider"] = &im.User{Id: "outsider", Kind: "human", PlatformRole: "user"}
	f.members["peer"] = []string{"ws1"}
	h := (&Handler{Store: s, Origin: "https://teamie.test"}).Routes()
	w := publicRequest(h, "POST", "/api/v1/agents", "owner", `{"name":"Assistant","description":"Public profile","workspaceIds":["ws1","ws2"]}`)
	created := responseProfile(t, w, 201, []string{"ws1", "ws2"})
	id := created["id"].(string)
	profilePath := "/api/v1/agents/" + id
	privateError := "private raw error with cross-conversation task description"
	if _, err := s.DB.Exec(context.Background(), "UPDATE agents SET runtime_state='running',last_error=$2,last_active_at=now() WHERE id=$1", id, privateError); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{profilePath, "/api/v1/agents"} {
		w := publicRequest(h, "GET", endpoint, "owner", "")
		if endpoint == profilePath {
			responseProfile(t, w, 200, []string{"ws1", "ws2"})
		} else if got := responseProfiles(t, w, []string{"ws1", "ws2"}); len(got) != 1 {
			t.Fatal("owner listing", got)
		}
		requireNoPrivateData(t, w.Body.String(), privateError, "agent:"+id)
	}
	for _, token := range []string{"owner", "admin", "peer"} {
		w := publicRequest(h, "GET", "/api/v1/workspaces/ws1/agents", token, "")
		if got := responseProfiles(t, w, []string{"ws1"}); len(got) != 1 {
			t.Fatal("shared workspace discovery", got)
		}
		requireNoPrivateData(t, w.Body.String(), "ws2", privateError, "agent:"+id)
	}
	for _, token := range []string{"admin", "peer", "outsider"} {
		for _, method := range []string{"GET", "PATCH", "DELETE"} {
			w := publicRequest(h, method, profilePath, token, `{"name":"Takeover"}`)
			if w.Code != http.StatusNotFound {
				t.Fatalf("%s %s bypassed ownership: %d", token, method, w.Code)
			}
		}
		if got := responseProfiles(t, publicRequest(h, "GET", "/api/v1/agents", token, ""), []string{}); len(got) != 0 {
			t.Fatal("another user's profile appeared in owner listing")
		}
	}
	for _, token := range []string{"admin", "peer", "outsider"} {
		if w := publicRequest(h, "GET", "/api/v1/workspaces/ws2/agents", token, ""); w.Code != http.StatusNotFound {
			t.Fatalf("%s discovered private workspace: %d", token, w.Code)
		}
	}
	updated := responseProfile(t, publicRequest(h, "PATCH", profilePath, "owner", `{"description":"Updated public profile"}`), 200, []string{"ws1", "ws2"})
	if updated["description"] != "Updated public profile" {
		t.Fatal(updated)
	}
	flags(t, path, false)
	responseProfile(t, publicRequest(h, "GET", profilePath, "owner", ""), 200, []string{"ws1", "ws2"})
	if got := responseProfiles(t, publicRequest(h, "GET", "/api/v1/workspaces/ws1/agents", "peer", ""), []string{"ws1"}); len(got) != 1 {
		t.Fatal("disabled writes hid persisted profile")
	}
	disabled := responseProfile(t, publicRequest(h, "PATCH", profilePath, "owner", `{"enabled":false}`), 200, []string{"ws1", "ws2"})
	if disabled["enabled"] != false {
		t.Fatal(disabled)
	}
	if w := publicRequest(h, "DELETE", profilePath, "owner", ""); w.Code != 204 || w.Body.Len() != 0 {
		t.Fatalf("delete response: %d %s", w.Code, w.Body.String())
	}
	if w := publicRequest(h, "GET", profilePath, "owner", ""); w.Code != http.StatusNotFound {
		t.Fatal("deleted profile readable", w.Code)
	}
	if got := responseProfiles(t, publicRequest(h, "GET", "/api/v1/agents", "owner", ""), []string{}); len(got) != 0 {
		t.Fatal("deleted profile listed", got)
	}
	if got := responseProfiles(t, publicRequest(h, "GET", "/api/v1/workspaces/ws1/agents", "peer", ""), []string{}); len(got) != 0 {
		t.Fatal("deleted profile discoverable", got)
	}
}

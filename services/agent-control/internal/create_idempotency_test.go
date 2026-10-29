package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func createRequest(h http.Handler, owner, body, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/v1/agents", strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "teamie_session", Value: owner})
	r.Header.Set("Origin", "https://teamie.test")
	r.Header.Set("X-CSRF-Token", "csrf")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestCreateRetryAfterBackgroundReconcileKeepsIdentity(t *testing.T) {
	for _, failure := range []string{"identity", "chat"} {
		t.Run(failure, func(t *testing.T) {
			s, f, _ := setupControl(t)
			h := (&Handler{Store: s, Origin: "https://teamie.test"}).Routes()
			body := `{"name":"Retryable Agent","workspaceIds":["ws1"]}`
			f.failProvision = failure == "identity"
			f.failMemberships = failure == "chat"
			if w := createRequest(h, "owner", body, "stable-intent"); w.Code != 503 {
				t.Fatalf("first: %d %s", w.Code, w.Body)
			}
			owned, err := s.ListOwned(context.Background(), "owner")
			if err != nil || len(owned) != 1 {
				t.Fatalf("pending: %v %v", owned, err)
			}
			original := owned[0].ID
			f.failProvision = false
			f.failMemberships = false
			if err = s.Reconcile(context.Background(), original); err != nil {
				t.Fatal(err)
			}
			for range 2 { // Also represents a successful response lost before the client saw it.
				w := createRequest(h, "owner", body, "stable-intent")
				if w.Code != 201 {
					t.Fatalf("retry: %d %s", w.Code, w.Body)
				}
				var out struct {
					Agent Agent `json:"agent"`
				}
				if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Agent.ID != original {
					t.Fatalf("identity changed: %v %v", out, err)
				}
			}
			owned, err = s.ListOwned(context.Background(), "owner")
			if err != nil || len(owned) != 1 {
				t.Fatalf("duplicate agents: %v %v", owned, err)
			}
		})
	}
}

func TestCreateKeyBindsImmutableIntentBeforeAndAfterReconcile(t *testing.T) {
	ctx := context.Background()
	for _, pending := range []bool{true, false} {
		t.Run(map[bool]string{true: "pending", false: "applied"}[pending], func(t *testing.T) {
			s, f, _ := setupControl(t)
			h := (&Handler{Store: s, Origin: "https://teamie.test"}).Routes()
			original := `{"name":"Original Agent","description":"Original description","modelId":"model-one","workspaceIds":["ws1"],"enabled":true}`
			f.failProvision = pending
			w := createRequest(h, "owner", original, "immutable-intent")
			if (pending && w.Code != 503) || (!pending && w.Code != 201) {
				t.Fatalf("first: %d %s", w.Code, w.Body)
			}
			before, _ := s.ListOwned(ctx, "owner")
			if len(before) != 1 {
				t.Fatal(before)
			}
			f.failProvision = false
			for _, body := range []string{
				strings.Replace(original, "Original Agent", "Changed Agent", 1),
				strings.Replace(original, "Original description", "Changed description", 1),
				strings.Replace(original, "model-one", "model-two", 1),
				strings.Replace(original, "ws1", "ws2", 1),
				strings.Replace(original, `"enabled":true`, `"enabled":false`, 1),
			} {
				if w = createRequest(h, "owner", body, "immutable-intent"); w.Code != 409 {
					t.Fatalf("conflict: %d %s", w.Code, w.Body)
				}
			}
			after, err := s.Owned(ctx, "owner", before[0].ID)
			if err != nil || after.Name != before[0].Name || after.Description != before[0].Description || after.ModelID != before[0].ModelID || !same(after.WorkspaceIDs, before[0].WorkspaceIDs) || after.Enabled != before[0].Enabled || after.Revision != before[0].Revision || after.CreationHash != before[0].CreationHash {
				t.Fatalf("conflict mutated saved intent: %v", err)
			}
		})
	}
}

func TestCreationIdentitySurvivesEditsAndDeletion(t *testing.T) {
	ctx := context.Background()
	s, _, _ := setupControl(t)
	h := (&Handler{Store: s, Origin: "https://teamie.test"}).Routes()
	body := `{"name":"Original","workspaceIds":["ws1","ws2"]}`
	w := createRequest(h, "owner", body, "original-key")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	original, _ := s.ListOwned(ctx, "owner")
	changed, err := s.Update(ctx, "owner", original[0].ID, Changes{Name: ptr("Edited"), WorkspaceIDs: ptr([]string{"ws2"}), Enabled: ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	// Omitted defaults and reordered/deduplicated workspaces normalize to the same original intent.
	w = createRequest(h, "owner", `{"name":" Original ","description":"","modelId":"","workspaceIds":["ws2","ws1","ws1"],"enabled":true}`, "original-key")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var replay struct {
		Agent Agent `json:"agent"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &replay); err != nil {
		t.Fatal(err)
	}
	if replay.Agent.ID != changed.ID || replay.Agent.Name != "Edited" || replay.Agent.Enabled || !same(replay.Agent.WorkspaceIDs, []string{"ws2"}) {
		t.Fatal("create retry overwrote subsequent edits")
	}
	if err = s.Delete(ctx, "owner", changed.ID); err != nil {
		t.Fatal(err)
	}
	if w = createRequest(h, "owner", body, "original-key"); w.Code != 404 {
		t.Fatalf("deleted create resurrected: %d", w.Code)
	}
	// A deliberate new intent with an identical name is still permitted.
	if w = createRequest(h, "owner", body, "new-key"); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if w = createRequest(h, "admin", `{"name":"Admin owned","workspaceIds":["ws1"]}`, "new-key"); w.Code != 201 {
		t.Fatal("keys must be owner-scoped", w.Code, w.Body)
	}
}

func TestCreateRequiresBoundedRequestIdentity(t *testing.T) {
	s, _, _ := setupControl(t)
	h := (&Handler{Store: s, Origin: "https://teamie.test"}).Routes()
	for _, key := range []string{"", "  ", strings.Repeat("x", 129)} {
		if w := createRequest(h, "owner", `{"name":"No identity","workspaceIds":[]}`, key); w.Code != 400 {
			t.Fatal(w.Code, w.Body)
		}
	}
	owned, err := s.ListOwned(context.Background(), "owner")
	if err != nil || len(owned) != 0 {
		t.Fatal("invalid key persisted an Agent", err)
	}
}

// The disposable authority models separate IM services; protect its in-memory
// maps while HTTP requests validate membership concurrently with reconciliation.
type concurrentAuthority struct {
	Authority
	mu sync.Mutex
}

func (a *concurrentAuthority) Workspaces(ctx context.Context, id string) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Authority.Workspaces(ctx, id)
}
func (a *concurrentAuthority) Participants(ctx context.Context, ws, id string) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Authority.Participants(ctx, ws, id)
}
func (a *concurrentAuthority) Provision(ctx context.Context, agent Agent, revision int64, enabled, deleted bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Authority.Provision(ctx, agent, revision, enabled, deleted)
}
func (a *concurrentAuthority) Memberships(ctx context.Context, agent Agent, ids []string) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Authority.Memberships(ctx, agent, ids)
}

func TestConcurrentCreateRetriesCommitOneAgent(t *testing.T) {
	s, f, _ := setupControl(t)
	s.IM = &concurrentAuthority{Authority: f}
	h := (&Handler{Store: s, Origin: "https://teamie.test"}).Routes()
	const clients = 12
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, clients)
	for range clients {
		go func() {
			<-start
			results <- createRequest(h, "owner", `{"name":"One Agent","workspaceIds":["ws1"]}`, "concurrent-key")
		}()
	}
	close(start)
	var original string
	for range clients {
		w := <-results
		if w.Code != 201 {
			t.Errorf("concurrent create: %d %s", w.Code, w.Body)
			continue
		}
		var out struct {
			Agent Agent `json:"agent"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if original == "" {
			original = out.Agent.ID
		}
		if original != out.Agent.ID {
			t.Error("concurrent keys returned different identities")
		}
	}
	owned, err := s.ListOwned(context.Background(), "owner")
	if err != nil || len(owned) != 1 || owned[0].ID != original {
		t.Fatalf("duplicate concurrent creates: %v %v", owned, err)
	}
}

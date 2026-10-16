package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"teamie/agent-protocol/kitex_gen/agent"
)

type runtimeFunc func(context.Context, string, *agent.RuntimeRequest) (*agent.RuntimeResponse, error)

func (f runtimeFunc) Call(ctx context.Context, operation string, req *agent.RuntimeRequest) (*agent.RuntimeResponse, error) {
	return f(ctx, operation, req)
}
func runtimeReply(data string) *agent.RuntimeResponse {
	return &agent.RuntimeResponse{Ok: true, DataJson: &data}
}

func TestRuntimeProxyUsesCurrentHumanOwnerAndCSRF(t *testing.T) {
	s, _, path := setupControl(t)
	a := createAgent(t, s, []string{"ws1"})
	calls := 0
	proxy := runtimeFunc(func(_ context.Context, op string, req *agent.RuntimeRequest) (*agent.RuntimeResponse, error) {
		calls++
		if req.OwnerId != "owner" || req.AgentId != a.ID || req.ServiceToken != s.RuntimeToken {
			t.Fatalf("untrusted attribution: %#v", req)
		}
		if op == "State" {
			return runtimeReply(`{"state":"idle","sessionId":"agent:test","pendingInputs":0,"runningTasks":0}`), nil
		}
		return runtimeReply(`{"ok":true}`), nil
	})
	h := (&Handler{Store: s, Origin: "https://teamie.test", Runtime: proxy}).Routes()
	call := func(method, suffix, cookie, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/agents/"+a.ID+suffix, strings.NewReader(`{}`))
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "teamie_session", Value: cookie})
		}
		r.Header.Set("Origin", "https://teamie.test")
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if got := call("GET", "/runtime", "admin", ""); got.Code != 404 || calls != 0 {
		t.Fatalf("admin bypass: %d %d", got.Code, calls)
	}
	if got := call("GET", "/runtime", "", ""); got.Code != 401 || calls != 0 {
		t.Fatalf("anonymous bypass: %d %d", got.Code, calls)
	}
	if got := call("GET", "/runtime?ownerId=admin", "owner", ""); got.Code != 200 || calls != 1 {
		t.Fatalf("owner read: %d %s", got.Code, got.Body.String())
	}
	if got := call("POST", "/runs/run-1/cancel", "owner", ""); got.Code != 403 || calls != 1 {
		t.Fatalf("missing CSRF accepted: %d %d", got.Code, calls)
	}
	if got := call("POST", "/runs/run-1/cancel", "owner", "csrf"); got.Code != 200 {
		t.Fatalf("cancel: %d %s", got.Code, got.Body.String())
	}
	flags(t, path, false)
	if got := call("GET", "/runtime", "owner", ""); got.Code != 200 {
		t.Fatal("write flag hid persisted runtime state")
	}
	if got := call("POST", "/runs/run-1/retry", "owner", "csrf"); got.Code != 403 {
		t.Fatal("disabled runtime accepted retry")
	}
	if got := call("POST", "/tasks/task-1/cancel", "owner", "csrf"); got.Code != 200 {
		t.Fatal("write flag prevented stopping task")
	}
}

func TestRuntimeEventStreamReplaysAndUsesReconnectCursor(t *testing.T) {
	s, _, _ := setupControl(t)
	a := createAgent(t, s, nil)
	calls := 0
	proxy := runtimeFunc(func(_ context.Context, op string, req *agent.RuntimeRequest) (*agent.RuntimeResponse, error) {
		if op != "Events" {
			t.Fatal(op)
		}
		calls++
		if calls == 1 {
			if req.GetCursor() != "5" {
				t.Fatalf("Last-Event-ID did not supersede URL: %s", req.GetCursor())
			}
			return runtimeReply(`{"events":[{"id":"6","type":"run.started","runId":"r1","status":"running","createdAt":"2026-10-16T00:00:00Z"}],"cursor":"6","hasMore":true}`), nil
		}
		if req.GetCursor() != "6" {
			t.Fatal("stream did not advance from committed event")
		}
		return &agent.RuntimeResponse{Ok: false, StatusCode: ptr(int32(503)), ErrorCode: ptr("runtime_unavailable"), ErrorMessage: ptr("private-provider-payload")}, nil
	})
	h := (&Handler{Store: s, Runtime: proxy}).Routes()
	r := httptest.NewRequest("GET", "/api/v1/agents/"+a.ID+"/events/stream?cursor=0", nil)
	r.AddCookie(&http.Cookie{Name: "teamie_session", Value: "owner"})
	r.Header.Set("Last-Event-ID", "5")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/event-stream" || !w.Flushed {
		t.Fatalf("stream not flushed: %#v", w.Result())
	}
	if !strings.Contains(w.Body.String(), "id: 6\nevent: runtime\n") || !strings.Contains(w.Body.String(), "event: unavailable") || strings.Contains(w.Body.String(), "private-provider") {
		t.Fatal(w.Body.String())
	}
	for _, cursor := range []string{"-1", "1e2", "01", "9223372036854775808"} {
		r := httptest.NewRequest("GET", "/api/v1/agents/"+a.ID+"/events?cursor="+cursor, nil)
		r.AddCookie(&http.Cookie{Name: "teamie_session", Value: "owner"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("cursor %s: %d", cursor, w.Code)
		}
	}
}

func TestRuntimeStreamStopsWhenHumanSessionIsRevoked(t *testing.T) {
	s, authority, _ := setupControl(t)
	a := createAgent(t, s, nil)
	calls := 0
	h := (&Handler{Store: s, Runtime: runtimeFunc(func(_ context.Context, _ string, _ *agent.RuntimeRequest) (*agent.RuntimeResponse, error) {
		calls++
		delete(authority.users, "owner")
		return runtimeReply(`{"events":[{"id":"1","type":"run.completed","createdAt":"2026-10-16T00:00:00Z"}],"cursor":"1","hasMore":true}`), nil
	})}).Routes()
	r := httptest.NewRequest("GET", "/api/v1/agents/"+a.ID+"/events/stream", nil)
	r.AddCookie(&http.Cookie{Name: "teamie_session", Value: "owner"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if calls != 1 {
		t.Fatal("revoked human session continued tailing events")
	}
}

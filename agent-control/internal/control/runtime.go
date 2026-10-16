package control

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/kitex/client"
	"teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/kitex_gen/agent/agentruntimeservice"
	"teamie/backend/kitex_gen/im"
)

// RuntimeAccess transports owner-authorized operations; runtime remains the
// authority for its data. Neither the proxy nor the browser reads Session data.
type RuntimeAccess interface {
	Call(context.Context, string, *agent.RuntimeRequest) (*agent.RuntimeResponse, error)
}

type runtimeClient struct{ client agentruntimeservice.Client }

func NewRuntime(addr string) (RuntimeAccess, error) {
	c, err := agentruntimeservice.NewClient("teamie.agent-runtime", client.WithHostPorts(addr), client.WithRPCTimeout(5*time.Second), client.WithConnectTimeout(time.Second))
	return &runtimeClient{client: c}, err
}

func (c *runtimeClient) Call(ctx context.Context, operation string, req *agent.RuntimeRequest) (*agent.RuntimeResponse, error) {
	switch operation {
	case "State":
		return c.client.State(ctx, req)
	case "Runs":
		return c.client.Runs(ctx, req)
	case "Run":
		return c.client.Run(ctx, req)
	case "Tasks":
		return c.client.Tasks(ctx, req)
	case "Task":
		return c.client.Task(ctx, req)
	case "Events":
		return c.client.Events(ctx, req)
	case "CancelRun":
		return c.client.CancelRun(ctx, req)
	case "CancelTask":
		return c.client.CancelTask(ctx, req)
	case "RetryRun":
		return c.client.RetryRun(ctx, req)
	default:
		return nil, errAPI(400, "invalid_request", "Unknown runtime operation")
	}
}

func runtimeCursor(value string) (string, error) {
	if value == "" {
		return "0", nil
	}
	if len(value) > 19 {
		return "", errAPI(400, "invalid_cursor", "Invalid event cursor")
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != value {
		return "", errAPI(400, "invalid_cursor", "Invalid event cursor")
	}
	return value, nil
}

func runtimeLimit(value string) (int32, error) {
	if value == "" {
		return 50, nil
	}
	n, err := strconv.ParseInt(value, 10, 32)
	if err != nil || n < 1 || n > 100 {
		return 0, errAPI(400, "invalid_query", "Limit must be between 1 and 100")
	}
	return int32(n), nil
}

func (h *Handler) runtimeRequest(r *http.Request, user *im.User) (*agent.RuntimeRequest, error) {
	id := r.PathValue("id")
	if _, err := h.Store.Owned(r.Context(), user.Id, id); err != nil {
		return nil, err
	}
	limit, err := runtimeLimit(r.URL.Query().Get("limit"))
	if err != nil {
		return nil, err
	}
	cursor := r.URL.Query().Get("cursor")
	if strings.Contains(r.URL.Path, "/events") {
		last := r.Header.Get("Last-Event-ID")
		// EventSource retains its original URL across reconnects. Its newer
		// Last-Event-ID therefore takes precedence over the initial query cursor.
		if last != "" {
			cursor = last
		}
		cursor, err = runtimeCursor(cursor)
		if err != nil {
			return nil, err
		}
	}
	resource := r.PathValue("resourceId")
	return &agent.RuntimeRequest{ServiceToken: h.Store.RuntimeToken, AgentId: id, OwnerId: user.Id, ResourceId: &resource, Cursor: &cursor, Limit: &limit}, nil
}

func (h *Handler) callRuntime(ctx context.Context, operation string, req *agent.RuntimeRequest) (json.RawMessage, error) {
	if h.Runtime == nil {
		return nil, errAPI(503, "runtime_unavailable", "Agent runtime is unavailable")
	}
	response, err := h.Runtime.Call(ctx, operation, req)
	if err != nil || response == nil {
		return nil, errAPI(503, "runtime_unavailable", "Agent runtime is unavailable")
	}
	if !response.Ok {
		status := int(response.GetStatusCode())
		if status < 400 || status > 599 {
			status = 503
		}
		code := response.GetErrorCode()
		if code == "" {
			code = "runtime_unavailable"
		}
		return nil, errAPI(status, code, "Agent operation could not be completed")
	}
	data := json.RawMessage(response.GetDataJson())
	if len(data) == 0 || len(data) > 1<<20 || !json.Valid(data) || data[0] != '{' {
		return nil, errAPI(503, "runtime_unavailable", "Invalid runtime response")
	}
	return data, nil
}

func (h *Handler) runtimeRoutes(m *http.ServeMux) {
	for _, route := range []struct{ pattern, operation string }{
		{"GET /api/v1/agents/{id}/runtime", "State"},
		{"GET /api/v1/agents/{id}/runs", "Runs"},
		{"GET /api/v1/agents/{id}/runs/{resourceId}", "Run"},
		{"GET /api/v1/agents/{id}/tasks", "Tasks"},
		{"GET /api/v1/agents/{id}/tasks/{resourceId}", "Task"},
		{"GET /api/v1/agents/{id}/events", "Events"},
		{"POST /api/v1/agents/{id}/runs/{resourceId}/cancel", "CancelRun"},
		{"POST /api/v1/agents/{id}/tasks/{resourceId}/cancel", "CancelTask"},
		{"POST /api/v1/agents/{id}/runs/{resourceId}/retry", "RetryRun"},
	} {
		m.HandleFunc(route.pattern, h.auth(func(w http.ResponseWriter, r *http.Request, u *im.User) error {
			req, err := h.runtimeRequest(r, u)
			if err != nil {
				return err
			}
			if r.Method == http.MethodPost {
				var body struct{}
				if err := decode(w, r, &body); err != nil {
					return err
				}
				if route.operation == "RetryRun" {
					if err := h.Store.flag("agents.enabled"); err != nil {
						return err
					}
					if err := h.Store.flag("agents.runtime.v3"); err != nil {
						return err
					}
				}
			}
			data, err := h.callRuntime(r.Context(), route.operation, req)
			if err != nil {
				return err
			}
			if r.Method == http.MethodPost {
				write(w, 200, map[string]bool{"ok": true})
				return nil
			}
			write(w, 200, data)
			return nil
		}))
	}
	m.HandleFunc("GET /api/v1/agents/{id}/events/stream", h.auth(h.streamRuntime))
}

type runtimeEvent struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	RunID     string `json:"runId,omitempty"`
	TaskID    string `json:"taskId,omitempty"`
	ToolName  string `json:"toolName,omitempty"`
	Status    string `json:"status,omitempty"`
	CreatedAt string `json:"createdAt"`
	ErrorCode string `json:"errorCode,omitempty"`
}
type runtimeEventPage struct {
	Events  []runtimeEvent `json:"events"`
	Cursor  string         `json:"cursor"`
	HasMore bool           `json:"hasMore"`
}

func (h *Handler) streamRuntime(w http.ResponseWriter, r *http.Request, user *im.User) error {
	req, err := h.runtimeRequest(r, user)
	if err != nil {
		return err
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errAPI(503, "stream_unavailable", "Event streaming is unavailable")
	}
	load := func() (runtimeEventPage, error) {
		data, err := h.callRuntime(r.Context(), "Events", req)
		var page runtimeEventPage
		if err == nil {
			err = json.Unmarshal(data, &page)
		}
		if err == nil {
			var next string
			next, err = runtimeCursor(page.Cursor)
			page.Cursor = next
			if err == nil && len(page.Events) > int(req.GetLimit()) {
				err = fmt.Errorf("oversized event page")
			}
			if err == nil && page.HasMore && len(page.Events) == 0 {
				err = fmt.Errorf("empty nonterminal event page")
			}
			if err == nil {
				previous, _ := strconv.ParseInt(req.GetCursor(), 10, 64)
				for _, event := range page.Events {
					id, e := runtimeCursor(event.ID)
					position, _ := strconv.ParseInt(id, 10, 64)
					if e != nil || position <= previous {
						err = fmt.Errorf("unordered event page")
						break
					}
					previous = position
				}
				position, _ := strconv.ParseInt(next, 10, 64)
				if position < previous {
					err = fmt.Errorf("event cursor regressed")
				}
			}
		}
		return page, err
	}
	page, err := load()
	if err != nil {
		return errAPI(503, "runtime_unavailable", "Agent events are unavailable")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	if _, err := io.WriteString(w, ": connected\n\n"); err != nil {
		return nil
	}
	flusher.Flush()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for _, event := range page.Events {
			data, _ := json.Marshal(event)
			if _, err := fmt.Fprintf(w, "id: %s\nevent: runtime\ndata: %s\n\n", event.ID, data); err != nil {
				return nil
			}
		}
		if len(page.Events) > 0 {
			flusher.Flush()
		}
		req.Cursor = &page.Cursor
		if !page.HasMore {
			select {
			case <-r.Context().Done():
				return nil
			case <-ticker.C:
			}
		}
		// A connected stream never outlives the human's current authorization.
		cookie, err := r.Cookie("teamie_session")
		if err != nil {
			return nil
		}
		current, _, err := h.Store.IM.Validate(r.Context(), cookie.Value)
		if err != nil || current == nil || current.Id != user.Id || current.Kind != "human" {
			return nil
		}
		if _, err := h.Store.Owned(r.Context(), user.Id, req.AgentId); err != nil {
			return nil
		}
		page, err = load()
		if err != nil {
			_, _ = io.WriteString(w, "event: unavailable\ndata: {\"errorCode\":\"runtime_unavailable\"}\n\n")
			flusher.Flush()
			return nil
		}
		if len(page.Events) == 0 {
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return nil
			}
			flusher.Flush()
		}
	}
}

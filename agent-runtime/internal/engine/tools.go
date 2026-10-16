package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/kitex_gen/agent/sandboxservice"
	"teamie/backend/kitex_gen/im"
	"teamie/backend/kitex_gen/im/identityservice"
)

type APIError struct {
	Status int
	Code   string
}

func (e *APIError) Error() string { return "im: " + e.Code }

type IM struct {
	BaseURL, ServiceKey, AgentID string
	Identity                     identityservice.Client
	HTTP                         *http.Client
	mu                           sync.Mutex
	token                        string
	expiry                       time.Time
}

func (imc *IM) credential(ctx context.Context) (string, error) {
	imc.mu.Lock()
	defer imc.mu.Unlock()
	if imc.token != "" && time.Until(imc.expiry) > time.Minute {
		return imc.token, nil
	}
	r, e := imc.Identity.IssuePrincipalToken(ctx, &im.PrincipalTokenRequest{ServiceToken: imc.ServiceKey, PrincipalId: imc.AgentID})
	if e != nil {
		return "", errors.New("identity_unavailable")
	}
	if !r.Ok {
		return "", &APIError{Status: 403, Code: r.GetErrorCode()}
	}
	expiry, e := time.Parse(time.RFC3339Nano, r.ExpiresAt)
	if e != nil {
		return "", errors.New("invalid_principal_expiry")
	}
	imc.token = r.Token
	imc.expiry = expiry
	return imc.token, nil
}
func (imc *IM) Call(ctx context.Context, method, path string, body any, out any) error {
	token, e := imc.credential(ctx)
	if e != nil {
		return e
	}
	var data []byte
	if body != nil {
		data, e = json.Marshal(body)
		if e != nil {
			return e
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(imc.BaseURL, "/")+path, bytes.NewReader(data))
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Teamie-Service-Key", imc.ServiceKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := imc.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, e := client.Do(req)
	if e != nil {
		return errors.New("im_unavailable")
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024+1))
	if e != nil || len(raw) > 2*1024*1024 {
		return errors.New("im_response_limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &failure)
		code := failure.Error.Code
		if code == "" {
			code = "im_request_failed"
		}
		if response.StatusCode == 401 {
			imc.mu.Lock()
			if imc.token == token {
				imc.token = ""
			}
			imc.mu.Unlock()
		}
		return &APIError{Status: response.StatusCode, Code: code}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type Conversation struct {
	ID          string   `json:"id"`
	WorkspaceID string   `json:"workspaceId"`
	Type        string   `json:"type"`
	Name        string   `json:"name"`
	MemberIDs   []string `json:"memberIds"`
}
type Message struct {
	ID             string  `json:"id"`
	ConversationID string  `json:"conversationId"`
	SenderID       string  `json:"senderId"`
	SenderName     string  `json:"senderName"`
	SenderKind     string  `json:"senderKind"`
	Body           string  `json:"body"`
	Seq            int64   `json:"seq"`
	CreatedAt      string  `json:"createdAt"`
	RetractedAt    *string `json:"retractedAt,omitempty"`
}
type Bootstrap struct {
	Conversations []Conversation `json:"conversations"`
	SyncCursor    string         `json:"syncCursor"`
}

func (imc *IM) Bootstrap(ctx context.Context) (*Bootstrap, error) {
	var b Bootstrap
	e := imc.Call(ctx, http.MethodGet, "/api/v1/bootstrap", nil, &b)
	return &b, e
}
func validUUID(id string) bool { _, e := uuid.Parse(id); return e == nil }
func messagePath(c, m string) (string, error) {
	if !validUUID(c) || !validUUID(m) {
		return "", &APIError{Status: 400, Code: "invalid_id"}
	}
	return "/api/v1/conversations/" + c + "/messages/" + m, nil
}
func (imc *IM) Message(ctx context.Context, c, m string) (*Message, error) {
	path, e := messagePath(c, m)
	if e != nil {
		return nil, e
	}
	var out struct {
		Message Message `json:"message"`
	}
	e = imc.Call(ctx, http.MethodGet, path, nil, &out)
	return &out.Message, e
}

type Tools struct {
	Store        *Store
	Lease        *Lease
	IM           *IM
	Sandbox      sandboxservice.Client
	SandboxToken string
	Authorize    func(context.Context) error
}
type Observation struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}

func observeError(e error) *Observation {
	var api *APIError
	if errors.As(e, &api) {
		return &Observation{Error: api.Code}
	}
	return &Observation{Error: "service_unavailable"}
}

// Tool receipts share the model's stable call ID. No arbitrary model output reaches IM.
func (t *Tools) execute(ctx context.Context, name string, args any, fn func(context.Context, string) (*Observation, error)) (*Observation, error) {
	run, e := CurrentRun(ctx)
	if e != nil {
		return nil, e
	}
	if t.Authorize != nil {
		if err := t.Authorize(ctx); err != nil {
			return nil, err
		}
	}
	if e = t.Store.CheckLease(ctx, t.Lease); e != nil {
		return nil, e
	}
	callID := compose.GetToolCallID(ctx)
	payload, e := json.Marshal(args)
	if e != nil {
		return nil, e
	}
	cached, e := t.Store.ToolIntent(ctx, t.Lease, run, callID, name, payload)
	if e != nil {
		return nil, e
	}
	if cached != nil {
		var out Observation
		if e = json.Unmarshal(cached, &out); e != nil {
			return nil, e
		}
		return &out, nil
	}
	out, e := fn(ctx, "tool-"+digest([]byte(run.AgentID+"/"+run.ID+"/"+callID)))
	if e != nil {
		return nil, e
	}
	b, e := json.Marshal(out)
	if e != nil {
		return nil, e
	}
	if e = t.Store.ToolResult(ctx, t.Lease, run, callID, b); e != nil {
		return nil, e
	}
	return out, nil
}

type listConversationsArgs struct {
	WorkspaceID string `json:"workspaceId,omitempty" jsonschema:"description=Optional team ID; omit to list all conversations visible to this Agent"`
}
type listMessagesArgs struct {
	ConversationID string `json:"conversationId" jsonschema:"required,description=Explicit destination conversation ID"`
	BeforeSeq      int64  `json:"beforeSeq,omitempty" jsonschema:"description=Exclusive older-page cursor; zero starts at latest"`
	Limit          int    `json:"limit,omitempty" jsonschema:"description=Page size 1 to 50"`
	Keyword        string `json:"keyword,omitempty" jsonschema:"description=Optional text filter within this page; page further to search older history"`
}
type getMessageArgs struct {
	ConversationID string `json:"conversationId" jsonschema:"required"`
	MessageID      string `json:"messageId" jsonschema:"required"`
}
type sendArgs struct {
	ConversationID string `json:"conversationId" jsonschema:"required,description=Explicit destination; always identify the intended conversation from source IDs"`
	Body           string `json:"body" jsonschema:"required,description=Public message text"`
	ReplyToID      string `json:"replyToId,omitempty" jsonschema:"description=Optional message in the same conversation to reply to"`
}
type execArgs struct {
	Command        string `json:"command" jsonschema:"required,description=Shell command in persistent private workspace; python3 available; no network or host access"`
	TimeoutSeconds int32  `json:"timeoutSeconds,omitempty" jsonschema:"description=Time limit 1 to 60 seconds; default 30"`
}

func (t *Tools) Build() ([]tool.BaseTool, error) {
	a, e := utils.InferTool("im_list_conversations", "List the conversations currently accessible to your own IM identity. This never adds membership.", func(ctx context.Context, args *listConversationsArgs) (*Observation, error) {
		return t.execute(ctx, "im_list_conversations", args, func(ctx context.Context, _ string) (*Observation, error) {
			if args.WorkspaceID != "" && !validUUID(args.WorkspaceID) {
				return &Observation{Error: "invalid_id"}, nil
			}
			path := "/api/v1/bootstrap"
			var b Bootstrap
			if e := t.IM.Call(ctx, http.MethodGet, path, nil, &b); e != nil {
				return observeError(e), nil
			}
			out := []Conversation{}
			for _, c := range b.Conversations {
				if args.WorkspaceID == "" || c.WorkspaceID == args.WorkspaceID {
					out = append(out, c)
				}
			}
			return &Observation{OK: true, Data: out}, nil
		})
	})
	if e != nil {
		return nil, e
	}
	b, e := utils.InferTool("im_list_messages", "Read or search an older page of history under your normal IM membership. Use nextBeforeSeq to continue. Messages include source and sender IDs.", func(ctx context.Context, args *listMessagesArgs) (*Observation, error) {
		return t.execute(ctx, "im_list_messages", args, func(ctx context.Context, _ string) (*Observation, error) {
			if !validUUID(args.ConversationID) || args.BeforeSeq < 0 || args.Limit < 0 || args.Limit > 50 || len(args.Keyword) > 256 {
				return &Observation{Error: "invalid_arguments"}, nil
			}
			limit := args.Limit
			if limit == 0 {
				limit = 20
			}
			q := url.Values{"limit": {strconv.Itoa(limit)}, "beforeSeq": {strconv.FormatInt(args.BeforeSeq, 10)}}
			var page struct {
				Messages []Message `json:"messages"`
				HasMore  bool      `json:"hasMore"`
			}
			if e := t.IM.Call(ctx, http.MethodGet, "/api/v1/conversations/"+args.ConversationID+"/messages?"+q.Encode(), nil, &page); e != nil {
				return observeError(e), nil
			}
			out, cursor, more := historyPage(page.Messages, page.HasMore, args.Keyword)
			return &Observation{OK: true, Data: map[string]any{"messages": out, "hasMore": more, "nextBeforeSeq": cursor}}, nil

		})
	})
	if e != nil {
		return nil, e
	}
	c, e := utils.InferTool("im_get_message", "Read one message by its source conversation and message IDs. A retracted message has no body.", func(ctx context.Context, args *getMessageArgs) (*Observation, error) {
		return t.execute(ctx, "im_get_message", args, func(ctx context.Context, _ string) (*Observation, error) {
			m, e := t.IM.Message(ctx, args.ConversationID, args.MessageID)
			if e != nil {
				return observeError(e), nil
			}
			return &Observation{OK: true, Data: m}, nil
		})
	})
	if e != nil {
		return nil, e
	}
	d, e := utils.InferTool("im_send_message", "The ONLY way to speak or reply in IM. Specify a destination conversation explicitly. Your ordinary final text is private runtime output and is never sent to chat.", func(ctx context.Context, args *sendArgs) (*Observation, error) {
		return t.execute(ctx, "im_send_message", args, func(ctx context.Context, id string) (*Observation, error) {
			if !validUUID(args.ConversationID) || (args.ReplyToID != "" && !validUUID(args.ReplyToID)) || strings.TrimSpace(args.Body) == "" || len([]rune(args.Body)) > 8000 {
				return &Observation{Error: "invalid_arguments"}, nil
			}
			body := map[string]any{"clientMessageId": id, "body": args.Body}
			if args.ReplyToID != "" {
				body["replyToId"] = args.ReplyToID
			}
			var out struct {
				Message Message `json:"message"`
			}
			if e := t.IM.Call(ctx, http.MethodPost, "/api/v1/conversations/"+args.ConversationID+"/messages", body, &out); e != nil {
				var ae *APIError
				if errors.As(e, &ae) && ae.Status < 500 {
					return observeError(e), nil
				}
				return nil, &ModelError{Code: "im_unavailable", Retry: true}
			}
			return &Observation{OK: true, Data: out.Message}, nil
		})
	})
	if e != nil {
		return nil, e
	}
	f, e := utils.InferTool("workspace_execute", "Execute shell/Python work in your persistent Agent workspace through the isolated Sandbox service. Files persist per Agent. No network; limits apply. Inspect an interrupted execution before choosing a new action.", func(ctx context.Context, args *execArgs) (*Observation, error) {
		return t.execute(ctx, "workspace_execute", args, func(ctx context.Context, id string) (*Observation, error) {
			if strings.TrimSpace(args.Command) == "" || len(args.Command) > 16384 || args.TimeoutSeconds < 0 || args.TimeoutSeconds > 60 {
				return &Observation{Error: "invalid_arguments"}, nil
			}
			run, _ := CurrentRun(ctx)
			r, e := t.Sandbox.Exec(ctx, &wire.SandboxExecRequest{ServiceToken: t.SandboxToken, AgentId: run.AgentID, RunId: run.ID, RequestId: id, Command: args.Command, TimeoutSeconds: args.TimeoutSeconds})
			if e != nil {
				return nil, &ModelError{Code: "sandbox_unavailable", Retry: true}
			}
			if r.GetErrorCode() == "sandbox_busy" {
				return nil, &ModelError{Code: "sandbox_busy", Retry: true}
			}
			return &Observation{OK: r.Ok, Error: r.GetErrorCode(), Data: map[string]any{"exitCode": r.ExitCode, "stdout": r.Stdout, "stderr": r.Stderr, "truncated": r.Truncated}}, nil
		})
	})
	if e != nil {
		return nil, e
	}
	return []tool.BaseTool{a, b, c, d, f}, nil
}

// Bound observations while retaining a correct older-page cursor. Examine newest
// messages first; a filtered empty page must still advance over examined history.
func historyPage(messages []Message, hasMore bool, keyword string) ([]Message, int64, bool) {
	out := []Message{}
	used, examined := 0, 0
	var cursor int64
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		b, _ := json.Marshal(m)
		if examined > 0 && used+len(b) > 49152 {
			break
		}
		used += len(b)
		examined++
		cursor = m.Seq
		if keyword == "" || strings.Contains(strings.ToLower(m.Body), strings.ToLower(keyword)) {
			out = append(out, m)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, cursor, hasMore || examined < len(messages)
}

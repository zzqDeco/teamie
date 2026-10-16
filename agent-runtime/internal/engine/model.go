package engine

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/jackc/pgx/v5"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/kitex_gen/agent/modelgatewayservice"
)

type ModelError struct {
	Code  string
	Retry bool
}

func (e *ModelError) Error() string { return "model gateway: " + e.Code }

type GatewayModel struct {
	Client                  modelgatewayservice.Client
	Token, OwnerID, AgentID string
	Store                   *Store
	Lease                   *Lease
	Tools                   []*schema.ToolInfo
	Authorize               func(context.Context) error
}

func (m *GatewayModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	copy := *m
	copy.Tools = append([]*schema.ToolInfo(nil), tools...)
	return &copy, nil
}

type chatMessage struct {
	Role       string            `json:"role"`
	Content    string            `json:"content"`
	Name       string            `json:"name,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	ToolCalls  []schema.ToolCall `json:"tool_calls,omitempty"`
}
type chatTool struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}
type toolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

func chatRequest(messages []*schema.Message, tools []*schema.ToolInfo, opts *model.Options) ([]byte, error) {
	input := make([]chatMessage, 0, len(messages))
	for _, msg := range messages {
		if msg != nil {
			input = append(input, chatMessage{Role: string(msg.Role), Content: msg.Content, Name: msg.Name, ToolCallID: msg.ToolCallID, ToolCalls: msg.ToolCalls})
		}
	}
	toolDefs := []chatTool{}
	for _, info := range tools {
		params := json.RawMessage(`{"type":"object","properties":{}}`)
		if info.ParamsOneOf != nil {
			js, e := info.ToJSONSchema()
			if e != nil {
				return nil, e
			}
			b, e := json.Marshal(js)
			if e != nil {
				return nil, e
			}
			params = b
		}
		toolDefs = append(toolDefs, chatTool{Type: "function", Function: toolFunction{Name: info.Name, Description: info.Desc, Parameters: params}})
	}
	maxTokens := 2048
	if opts.MaxTokens != nil && *opts.MaxTokens > 0 && *opts.MaxTokens <= 4096 {
		maxTokens = *opts.MaxTokens
	}
	req := map[string]any{"messages": input, "max_tokens": maxTokens, "stream": false}
	if len(toolDefs) > 0 {
		req["tools"] = toolDefs
		req["tool_choice"] = "auto"
		req["parallel_tool_calls"] = false
	}
	if opts.ToolChoice != nil {
		switch *opts.ToolChoice {
		case schema.ToolChoiceForbidden:
			req["tool_choice"] = "none"
		case schema.ToolChoiceForced:
			req["tool_choice"] = "required"
		}
	}
	return json.Marshal(req)
}
func parseCompletion(b []byte) (*schema.Message, error) {
	var response struct {
		Choices []struct {
			Message      chatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
			Total      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(b, &response) != nil || len(response.Choices) == 0 {
		return nil, &ModelError{Code: "invalid_upstream_response", Retry: true}
	}
	c := response.Choices[0]
	if len(c.Message.Content) > 65536 || len(c.Message.ToolCalls) > 16 {
		return nil, &ModelError{Code: "upstream_response_limit", Retry: false}
	}
	seen := map[string]bool{}
	for _, call := range c.Message.ToolCalls {
		if call.ID == "" || seen[call.ID] || call.Function.Name == "" || len(call.Function.Arguments) > 65536 {
			return nil, &ModelError{Code: "invalid_tool_call", Retry: false}
		}
		seen[call.ID] = true
	}
	return &schema.Message{Role: schema.Assistant, Content: c.Message.Content, ToolCalls: c.Message.ToolCalls, ResponseMeta: &schema.ResponseMeta{FinishReason: c.FinishReason, Usage: &schema.TokenUsage{PromptTokens: response.Usage.Prompt, CompletionTokens: response.Usage.Completion, TotalTokens: response.Usage.Total}}}, nil
}
func (m *GatewayModel) Generate(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.Message, error) {
	run, e := CurrentRun(ctx)
	if e != nil {
		return nil, e
	}
	if m.Authorize != nil {
		if err := m.Authorize(ctx); err != nil {
			return nil, err
		}
	}
	if e = m.Store.CheckLease(ctx, m.Lease); e != nil {
		return nil, e
	}
	opts := model.GetCommonOptions(&model.Options{Tools: m.Tools}, options...)
	request, e := chatRequest(messages, opts.Tools, opts)
	if e != nil {
		return nil, e
	}
	hash := digest(request)
	step := int(run.Step.Add(1))
	cached, e := m.Store.CachedModel(ctx, run, step, hash)
	if e == nil {
		var result schema.Message
		if e = json.Unmarshal(cached, &result); e != nil {
			return nil, e
		}
		return &result, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	response, e := m.Client.Chat(ctx, &wire.GatewayChatRequest{ServiceToken: m.Token, OwnerId: m.OwnerID, AgentId: m.AgentID, SessionId: "agent:" + m.AgentID, RunId: executionID(run), RequestJson: string(request)})
	if e != nil {
		return nil, &ModelError{Code: "gateway_unavailable", Retry: true}
	}
	if !response.Ok {
		code := response.GetErrorCode()
		if code == "" {
			code = "gateway_error"
		}
		return nil, &ModelError{Code: code, Retry: code != "invalid_request" && code != "request_too_large"}
	}
	result, e := parseCompletion([]byte(response.GetResponseJson()))
	if e != nil {
		return nil, e
	}
	b, e := json.Marshal(result)
	if e != nil {
		return nil, e
	}
	if e = m.Store.SaveModel(ctx, m.Lease, run, step, hash, b); e != nil {
		return nil, e
	}
	return result, nil
}
func (m *GatewayModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	r, e := m.Generate(ctx, input, opts...)
	if e != nil {
		return nil, e
	}
	return schema.StreamReaderFromArray([]*schema.Message{r}), nil
}
func SafeError(err error) string {
	var m *ModelError
	if errors.As(err, &m) {
		return m.Code
	}
	if errors.Is(err, ErrLeaseLost) {
		return "lease_lost"
	}
	if errors.Is(err, ErrReplayConflict) {
		return "replay_conflict"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	return "execution_failed"
}
func retryError(err error) bool {
	var cancelled *adk.CancelError
	if errors.As(err, &cancelled) {
		return true
	}
	var m *ModelError
	if errors.As(err, &m) {
		return m.Retry
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrLeaseLost)
}

var _ model.ToolCallingChatModel = (*GatewayModel)(nil)

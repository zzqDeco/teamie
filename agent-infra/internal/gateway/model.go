package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"teamie/agent-infra/internal/api"
	"teamie/agent-protocol/modeltransport"
)

// The gateway is the only provider codec. Runtime sends native blocks and
// request-scoped tool definitions; credentials and model policy stay here.
func (p Provider) nativeModel(ctx context.Context, c Config, key, raw string, streaming bool) (model.AgenticModel, []*schema.AgenticMessage, []model.Option, error) {
	if len(raw) > MaxRequestBytes {
		return nil, nil, nil, api.Err(413, "request_too_large", "Model request is too large")
	}
	var envelope modeltransport.Request
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, nil, nil, api.Err(400, "invalid_request", "Invalid model request envelope")
	}
	if envelope.Protocol != modeltransport.Version {
		return nil, nil, nil, api.Err(400, "protocol_unsupported", "Unsupported model transport version")
	}
	if envelope.Stream != streaming {
		return nil, nil, nil, api.Err(400, "invalid_request", "Model transport mode does not match")
	}
	var input []*schema.AgenticMessage
	var options model.Options
	if (&schema.HumanReadableSerializer{}).Unmarshal(envelope.Messages, &input) != nil || len(input) < 1 || len(input) > 128 || json.Unmarshal(envelope.Options, &options) != nil {
		return nil, nil, nil, api.Err(400, "invalid_request", "Invalid model input")
	}
	for _, message := range input {
		if message == nil || (message.Role != schema.AgenticRoleTypeSystem && message.Role != schema.AgenticRoleTypeUser && message.Role != schema.AgenticRoleTypeAssistant) {
			return nil, nil, nil, api.Err(400, "invalid_request", "Invalid message role")
		}
	}
	if len(options.Tools) > 32 || len(options.DeferredTools) > 0 || options.ToolSearchTool != nil {
		return nil, nil, nil, api.Err(400, "invalid_request", "Unsupported tool manifest")
	}
	seen := map[string]bool{}
	for _, tool := range options.Tools {
		if tool == nil || tool.Name == "" || seen[tool.Name] {
			return nil, nil, nil, api.Err(400, "invalid_request", "Invalid tool manifest")
		}
		seen[tool.Name] = true
	}
	if options.AgenticToolChoice == nil {
		choice := schema.ToolChoiceAllowed
		if options.ToolChoice != nil {
			choice = *options.ToolChoice
		}
		options.AgenticToolChoice = &schema.AgenticToolChoice{Type: choice}
	}
	choice := options.AgenticToolChoice
	if choice.Type != schema.ToolChoiceAllowed && choice.Type != schema.ToolChoiceForbidden && choice.Type != schema.ToolChoiceForced {
		return nil, nil, nil, api.Err(400, "invalid_request", "Invalid tool choice")
	}
	var named []*schema.AllowedTool
	if choice.Allowed != nil {
		named = append(named, choice.Allowed.Tools...)
	}
	if choice.Forced != nil {
		named = append(named, choice.Forced.Tools...)
	}
	for _, tool := range named {
		if tool == nil || !seen[tool.FunctionName] || tool.MCPTool != nil || tool.ServerTool != nil {
			return nil, nil, nil, api.Err(400, "invalid_request", "Unknown function tool choice")
		}
	}
	limit := 2048
	if options.MaxTokens != nil {
		if *options.MaxTokens < 1 {
			return nil, nil, nil, api.Err(400, "invalid_request", "Invalid output token limit")
		}
		limit = *options.MaxTokens
	}
	if limit > 4096 {
		limit = 4096
	}
	if options.Temperature != nil && (*options.Temperature < 0 || *options.Temperature > 2) || options.TopP != nil && (*options.TopP <= 0 || *options.TopP > 1) || len(options.Stop) > 16 {
		return nil, nil, nil, api.Err(400, "invalid_request", "Invalid generation options")
	}
	if _, err := endpoint(c.BaseURL); err != nil {
		return nil, nil, nil, err
	}
	client := p.HTTP
	if client == nil {
		client = NewHTTPClient()
	}
	httpClient := *client
	// Both generation modes follow the owning turn's cancellation, including
	// while waiting for the provider's first response headers.
	httpClient.Timeout = 0
	transport := httpClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	httpClient.Transport = &providerBoundary{next: transport, key: key, config: c, streaming: streaming}
	chat, err := agenticopenai.NewChatModel(ctx, &agenticopenai.ChatConfig{APIKey: key, BaseURL: c.BaseURL, Model: c.Model, HTTPClient: &httpClient, MaxCompletionTokens: &limit, ExtraFields: map[string]any{"parallel_tool_calls": false, "n": 1}})
	if err != nil {
		return nil, nil, nil, api.Err(503, "gateway_unavailable", "Model adapter could not be initialized")
	}
	opts := []model.Option{model.WithTools(options.Tools), model.WithAgenticToolChoice(choice)}
	if options.Temperature != nil {
		opts = append(opts, model.WithTemperature(*options.Temperature))
	}
	if options.TopP != nil {
		opts = append(opts, model.WithTopP(*options.TopP))
	}
	if options.Stop != nil {
		opts = append(opts, model.WithStop(options.Stop))
	}
	return chat, input, opts, nil
}

func providerError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return streamError(ctx, "")
	}
	var safe *api.Error
	if errors.As(err, &safe) {
		return safe
	}
	return api.Err(502, "invalid_upstream_response", "Provider returned an invalid model response")
}

func (p Provider) Chat(ctx context.Context, c Config, key, raw string) (string, int64, int64, error) {
	if ctx.Err() != nil {
		return "", 0, 0, streamError(ctx, "")
	}
	chat, input, opts, err := p.nativeModel(ctx, c, key, raw, false)
	if err != nil {
		return "", 0, 0, err
	}
	message, err := chat.Generate(ctx, input, opts...)
	if err != nil {
		return "", 0, 0, providerError(ctx, err)
	}
	body, err := (&schema.HumanReadableSerializer{}).Marshal(message)
	if err != nil || len(body) > MaxResponseBytes {
		return "", 0, 0, api.Err(502, "invalid_upstream_response", "Provider response exceeded limits")
	}
	var prompt, completion int64
	if message.ResponseMeta != nil && message.ResponseMeta.TokenUsage != nil {
		prompt = int64(message.ResponseMeta.TokenUsage.PromptTokens)
		completion = int64(message.ResponseMeta.TokenUsage.CompletionTokens)
	}
	return string(body), prompt, completion, nil
}

// The official extension emits actual incremental reasoning, text and function
// blocks. Closing this function always cancels its upstream context before
// closing the official reader, including when the downstream write fails.
func (p Provider) Stream(ctx context.Context, c Config, key, raw string, emit func(string) error) (prompt, completion int64, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	chat, input, opts, err := p.nativeModel(ctx, c, key, raw, true)
	if err != nil {
		return 0, 0, err
	}
	reader, err := chat.Stream(ctx, input, opts...)
	if err != nil {
		return 0, 0, providerError(ctx, err)
	}
	defer reader.Close()
	for {
		message, e := reader.Recv()
		if errors.Is(e, io.EOF) {
			return prompt, completion, nil
		}
		if e != nil {
			return prompt, completion, providerError(ctx, e)
		}
		if message == nil {
			continue
		}
		if message.ResponseMeta != nil && message.ResponseMeta.TokenUsage != nil {
			prompt = int64(message.ResponseMeta.TokenUsage.PromptTokens)
			completion = int64(message.ResponseMeta.TokenUsage.CompletionTokens)
		}
		b, e := (&schema.HumanReadableSerializer{}).Marshal(message)
		if e != nil || len(b) > MaxResponseBytes {
			return prompt, completion, api.Err(502, "invalid_upstream_response", "Provider response exceeded limits")
		}
		if e = emit(string(b)); e != nil {
			return prompt, completion, api.Err(499, "client_interrupted", "Stream consumer disconnected")
		}
	}
}

// The only implementation-specific HTTP behavior here is Teamie's policy:
// model selection, bounded bodies, SSRF-safe client, sanitized failures and
// credential suppression. OpenAI serialization and stream conversion are
// owned by the official extension.
type providerBoundary struct {
	next      http.RoundTripper
	key       string
	config    Config
	streaming bool
}

func (g *providerBoundary) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.Body == nil || request.Method != http.MethodPost || request.URL.String() != strings.TrimRight(g.config.BaseURL, "/")+"/chat/completions" || request.Header.Get("Authorization") != "Bearer "+g.key {
		return nil, api.Err(400, "invalid_request", "Invalid provider request boundary")
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, MaxRequestBytes+1))
	request.Body.Close()
	if err != nil || len(raw) > MaxRequestBytes {
		return nil, api.Err(413, "request_too_large", "Model request is too large")
	}
	payload, err := normalizeMode(string(raw), g.config.Model, g.streaming)
	if err != nil {
		return nil, err
	}
	request = request.Clone(request.Context())
	request.Body = io.NopCloser(bytes.NewReader(payload))
	request.ContentLength = int64(len(payload))
	request.GetBody = nil
	response, err := g.next.RoundTrip(request)
	if err != nil {
		if request.Context().Err() != nil {
			return nil, streamError(request.Context(), "")
		}
		return nil, api.Err(502, "upstream_unavailable", "Provider could not be reached")
	}
	if response.StatusCode != 200 {
		response.Body.Close()
		if response.StatusCode == 429 {
			return nil, api.Err(429, "upstream_rate_limited", "Provider rejected the request")
		}
		if g.streaming {
			return nil, api.Err(502, "upstream_http_error", "Provider rejected the request")
		}
		return nil, api.Err(502, "upstream_error", "Provider rejected the request")
	}
	if g.streaming {
		return guardStreamingResponse(response, g.key, request.Context())
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	response.Body.Close()
	if err != nil || len(body) > MaxResponseBytes {
		return nil, api.Err(502, "invalid_upstream_response", "Provider response exceeded limits")
	}
	if err = guardUnary(body, g.key); err != nil {
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	return response, nil
}

func guardUnary(body []byte, key string) error {
	var result struct {
		Object  string          `json:"object"`
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Message struct {
				Role string `json:"role"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     int64 `json:"prompt_tokens"`
			Completion int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &result) != nil {
		return api.Err(502, "invalid_upstream_response", "Invalid chat completion response")
	}
	if len(result.Error) > 0 && string(result.Error) != "null" {
		return api.Err(502, "upstream_in_stream_error", "Provider reported a model error in its response")
	}
	if result.Object != "chat.completion" || len(result.Choices) != 1 || result.Choices[0].Message.Role != "assistant" || result.Usage.Prompt < 0 || result.Usage.Completion < 0 {
		return api.Err(502, "invalid_upstream_response", "Invalid chat completion response")
	}
	return guardSingleResponse(string(body), key)
}

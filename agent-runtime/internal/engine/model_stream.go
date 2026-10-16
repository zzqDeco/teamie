package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/jackc/pgx/v5"
)

func (m *GatewayModel) Stream(ctx context.Context, input []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	run, err := CurrentRun(ctx)
	if err != nil {
		return nil, err
	}
	if m.Authorize != nil {
		if err = m.Authorize(ctx); err != nil {
			return nil, err
		}
	}
	if err = m.Store.CheckLease(ctx, m.Lease); err != nil {
		return nil, err
	}
	if err = run.Front.acquire(ctx); err != nil {
		return nil, err
	}
	opts := model.GetCommonOptions(&model.Options{Tools: m.Tools}, options...)
	m.capOutput(opts)
	request, err := chatRequest(input, opts.Tools, opts)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	_ = json.Unmarshal(request, &object)
	object["stream"] = true
	object["stream_options"] = map[string]any{"include_usage": true}
	request, _ = json.Marshal(object)
	hash := digest(request)
	step := int(run.Step.Add(1))
	cached, err := m.Store.CachedModel(ctx, run, step, hash)
	if err == nil {
		var result schema.Message
		if err = json.Unmarshal(cached, &result); err != nil {
			return nil, err
		}
		return schema.StreamReaderFromArray([]*schema.Message{&result}), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if m.StreamURL == "" {
		return nil, &ModelError{Code: "gateway_stream_unavailable", Retry: true}
	}
	body, _ := json.Marshal(map[string]string{"ownerId": m.OwnerID, "agentId": m.AgentID, "sessionId": "agent:" + m.AgentID, "runId": executionID(run), "requestJson": string(request)})
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, m.StreamURL, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+m.Token)
	req.Header.Set("Content-Type", "application/json")
	client := m.HTTP
	if client == nil {
		client = gatewayStreamHTTP
	}
	response, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, &ModelError{Code: "gateway_unavailable", Retry: true}
	}
	if response.StatusCode != 200 {
		response.Body.Close()
		cancel()
		return nil, &ModelError{Code: "gateway_unavailable", Retry: true}
	}
	reader, writer := schema.Pipe[*schema.Message](8)
	go func() {
		defer cancel()
		defer response.Body.Close()
		defer writer.Close()
		scanner := bufio.NewScanner(io.LimitReader(response.Body, 8<<20+1))
		scanner.Buffer(make([]byte, 4096), 512<<10)
		chunks := []*schema.Message{}
		total := 0
		done := false
		for scanner.Scan() {
			total += len(scanner.Bytes())
			if total > 8<<20 {
				writer.Send(nil, &ModelError{Code: "upstream_response_limit"})
				return
			}
			var frame struct {
				Type         string `json:"type"`
				ResponseJSON string `json:"responseJson"`
				ErrorCode    string `json:"errorCode"`
			}
			if json.Unmarshal(scanner.Bytes(), &frame) != nil {
				writer.Send(nil, &ModelError{Code: "invalid_upstream_response", Retry: true})
				return
			}
			switch frame.Type {
			case "chunk":
				message, e := streamChunk([]byte(frame.ResponseJSON))
				if e != nil {
					writer.Send(nil, e)
					return
				}
				if message == nil {
					continue
				}
				chunks = append(chunks, message)
				if writer.Send(message, nil) {
					return
				}
			case "error":
				code := safeGatewayCode(frame.ErrorCode)
				writer.Send(nil, &ModelError{Code: code, Retry: code != "invalid_request"})
				return
			case "done":
				done = true
			default:
				writer.Send(nil, &ModelError{Code: "invalid_upstream_response", Retry: true})
				return
			}
			if done {
				break
			}
		}
		if !done {
			writer.Send(nil, &ModelError{Code: "gateway_stream_interrupted", Retry: true})
			return
		}
		if err := scanner.Err(); err != nil {
			writer.Send(nil, &ModelError{Code: "gateway_stream_interrupted", Retry: true})
			return
		}
		if len(chunks) == 0 {
			writer.Send(nil, &ModelError{Code: "invalid_upstream_response", Retry: true})
			return
		}
		result, err := schema.ConcatMessages(chunks)
		if err != nil {
			writer.Send(nil, &ModelError{Code: "invalid_upstream_response", Retry: true})
			return
		}
		if len(result.Content) > 65536 || len(result.ToolCalls) > 16 {
			writer.Send(nil, &ModelError{Code: "upstream_response_limit"})
			return
		}
		seen := map[string]bool{}
		for _, call := range result.ToolCalls {
			if call.ID == "" || seen[call.ID] || call.Function.Name == "" || len(call.Function.Arguments) > 65536 {
				writer.Send(nil, &ModelError{Code: "invalid_tool_call"})
				return
			}
			seen[call.ID] = true
		}
		b, err := json.Marshal(result)
		if err == nil {
			err = m.Store.SaveModel(ctx, m.Lease, run, step, hash, b)
		}
		if err != nil {
			writer.Send(nil, err)
		}
	}()
	return reader, nil
}
func streamChunk(b []byte) (*schema.Message, error) {
	var r struct {
		Choices []struct {
			Delta        chatMessage `json:"delta"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
			Total      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(b, &r) != nil {
		return nil, &ModelError{Code: "invalid_upstream_response", Retry: true}
	}
	m := &schema.Message{}
	if len(r.Choices) > 0 {
		c := r.Choices[0]
		m.Role = schema.Assistant
		m.Content = c.Delta.Content
		m.ToolCalls = c.Delta.ToolCalls
		if c.FinishReason != "" {
			m.ResponseMeta = &schema.ResponseMeta{FinishReason: c.FinishReason}
		}
	}
	if r.Usage != nil {
		if m.ResponseMeta == nil {
			m.ResponseMeta = &schema.ResponseMeta{}
		}
		m.ResponseMeta.Usage = &schema.TokenUsage{PromptTokens: r.Usage.Prompt, CompletionTokens: r.Usage.Completion, TotalTokens: r.Usage.Total}
	}
	return m, nil
}
func safeGatewayCode(code string) string {
	switch code {
	case "model_unconfigured", "model_disabled", "gateway_unavailable", "upstream_timeout", "upstream_error", "invalid_request", "request_too_large", "upstream_response_limit", "invalid_upstream_response", "run_cancelled":
		return code
	}
	return "gateway_error"
}

var gatewayStreamHTTP = &http.Client{Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ResponseHeaderTimeout: 15 * time.Second, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 10 * time.Second, MaxIdleConns: 8, MaxIdleConnsPerHost: 2}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

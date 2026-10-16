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
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/jackc/pgx/v5"
)

func (m *GatewayModel) Stream(ctx context.Context, input []*schema.AgenticMessage, options ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
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
	opts := model.GetCommonOptions(nil, options...)
	m.capOutput(opts)
	request, err := agenticRequest(input, opts, true)
	if err != nil {
		return nil, err
	}
	hash := modelRequestHash(request)
	step := int(run.Step.Add(1))
	cached, err := m.cachedModel(ctx, run, step, hash, input, opts, true)
	if err == nil {
		result, e := decodeAgenticMessage(cached)
		if e != nil {
			return nil, e
		}
		return schema.StreamReaderFromArray([]*schema.AgenticMessage{result}), nil
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
	reader, writer := schema.Pipe[*schema.AgenticMessage](8)
	output, bridgeDone := cancelStreamOnClose(reader, cancel)
	go func() {
		defer cancel()
		defer bridgeDone()
		defer response.Body.Close()
		defer writer.Close()
		scanner := bufio.NewScanner(io.LimitReader(response.Body, 8<<20+1))
		scanner.Buffer(make([]byte, 4096), 512<<10)
		chunks := []*schema.AgenticMessage{}
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
				message, e := decodeAgenticMessage([]byte(frame.ResponseJSON))
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
		result, err := schema.ConcatAgenticMessages(chunks)
		if err != nil {
			writer.Send(nil, &ModelError{Code: "invalid_upstream_response", Retry: true})
			return
		}
		if err = validateModelResult(result); err != nil {
			writer.Send(nil, err)
			return
		}
		b, err := (&schema.HumanReadableSerializer{}).Marshal(result)
		if err == nil {
			err = m.Store.SaveModel(ctx, m.Lease, run, step, hash, b)
		}
		if err != nil {
			writer.Send(nil, err)
		}
	}()
	return output, nil
}
func safeGatewayCode(code string) string {
	switch code {
	case "model_unconfigured", "model_disabled", "gateway_unavailable", "upstream_unavailable", "upstream_timeout", "upstream_error", "invalid_request", "request_too_large", "upstream_response_limit", "invalid_upstream_response", "run_cancelled", "gateway_busy", "upstream_rate_limited", "upstream_http_error", "upstream_in_stream_error", "upstream_interrupted", "client_interrupted", "canceled", "protocol_unsupported":
		return code
	}
	return "gateway_error"
}

var gatewayStreamHTTP = &http.Client{Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ResponseHeaderTimeout: 15 * time.Second, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 10 * time.Second, MaxIdleConns: 8, MaxIdleConnsPerHost: 2}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// Eino .35's Pipe has no Close hook. A private pulse source makes consumer Close
// observable even while the HTTP producer is waiting on a silent upstream. No
// pulse becomes a model chunk; completion and cancellation join this watcher.
func cancelStreamOnClose(reader *schema.StreamReader[*schema.AgenticMessage], cancel context.CancelFunc) (*schema.StreamReader[*schema.AgenticMessage], func()) {
	reader.SetAutomaticClose()
	pulse, writer := schema.Pipe[*schema.AgenticMessage](0)
	pulse.SetAutomaticClose()
	stopped, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		defer writer.Close()
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopped:
				return
			case <-ticker.C:
				if writer.Send(nil, nil) {
					cancel()
					return
				}
			}
		}
	}()
	merged := schema.MergeStreamReaders([]*schema.StreamReader[*schema.AgenticMessage]{reader, pulse})
	out := schema.StreamReaderWithConvert(merged, func(m *schema.AgenticMessage) (*schema.AgenticMessage, error) {
		if m == nil {
			return nil, schema.ErrNoValue
		}
		return m, nil
	})
	var once sync.Once
	return out, func() { once.Do(func() { close(stopped); pulse.Close(); <-joined }) }
}

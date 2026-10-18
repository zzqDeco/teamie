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
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/jackc/pgx/v5"
	"teamie/agent-protocol/modeltransport"
)

func (m *GatewayModel) Stream(ctx context.Context, input []*schema.AgenticMessage, options ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	run, err := CurrentRun(ctx)
	if err != nil {
		return nil, err
	}
	release, err := run.Front.modelPermit(ctx)
	if err != nil {
		return nil, err
	}
	handedOff := false
	defer func() {
		if !handedOff {
			release()
		}
	}()
	if m.Authorize != nil {
		if err = m.Authorize(ctx); err != nil {
			return nil, err
		}
	}
	if err = m.Store.CheckLease(ctx, m.Lease); err != nil {
		return nil, err
	}
	opts := model.GetCommonOptions(nil, options...)
	if err = m.capRunOutput(run, opts); err != nil {
		return nil, err
	}
	request, err := agenticRequestForRun(run, input, opts, true)
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
	if err = m.cachedRecovery(ctx, run, step, hash, true); err != nil {
		return nil, err
	}
	if err = m.checkContextRequest(ctx, run, step, hash, request, true); err != nil {
		return nil, err
	}
	if m.StreamURL == "" {
		return nil, &ModelError{Code: "gateway_stream_unavailable", Retry: true}
	}
	body, _ := json.Marshal(modeltransport.CallRequest{OwnerID: m.OwnerID, AgentID: m.AgentID, SessionID: "agent:" + m.AgentID, RunID: executionID(run), RequestJSON: string(request)})
	requestCtx, cancel := context.WithCancel(ctx)
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
		failure := readGatewayHTTPFailure(response)
		response.Body.Close()
		cancel()
		return nil, m.gatewayFailure(ctx, run, step, hash, true, failure.Code)
	}
	reader, writer := schema.Pipe[*schema.AgenticMessage](8)
	output, bridgeDone := cancelStreamOnClose(reader, cancel)
	handedOff = true
	go func() {
		defer release()
		defer cancel()
		defer bridgeDone()
		defer response.Body.Close()
		defer writer.Close()
		scanner := bufio.NewScanner(io.LimitReader(response.Body, modeltransport.MaxHTTPStreamBytes+1))
		scanner.Buffer(make([]byte, 4096), modeltransport.MaxHTTPStreamFrameBytes)
		chunks := &modelStreamAccumulator{}
		requestID := ""
		total := 0
		done := false
		for scanner.Scan() {
			total += len(scanner.Bytes())
			if total > modeltransport.MaxHTTPStreamBytes {
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
				message = streamRequestIDDelta(message, &requestID)
				if e = chunks.add(message); e != nil {
					writer.Send(nil, e)
					return
				}
				if writer.Send(message, nil) {
					return
				}
			case "error":
				code := safeGatewayCode(frame.ErrorCode)
				if isContextOverflow(code) && chunks.count > 0 {
					// An accepted partial stream is not a pure rejected request.
					// Never replay it as a side-effect-free overflow decision.
					writer.Send(nil, &ModelError{Code: "upstream_in_stream_error"})
				} else {
					writer.Send(nil, m.gatewayFailure(ctx, run, step, hash, true, code))
				}
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
		if chunks.count == 0 {
			writer.Send(nil, &ModelError{Code: "invalid_upstream_response", Retry: true})
			return
		}
		result, err := chunks.result()
		if err != nil {
			writer.Send(nil, &ModelError{Code: "invalid_upstream_response", Retry: true})
			return
		}
		if err = validateModelResult(result, modelResultByteLimit(run)); err != nil {
			writer.Send(nil, err)
			return
		}
		b, err := (&schema.HumanReadableSerializer{}).Marshal(result)
		if err == nil && len(b) > modeltransport.MaxResponseBytes {
			err = &ModelError{Code: "upstream_response_limit"}
		}
		if err == nil {
			err = m.Store.SaveModel(ctx, m.Lease, run, step, hash, b)
		}
		if err != nil {
			writer.Send(nil, err)
		}
	}()
	return output, nil
}

// Fold bounded batches so a long response does not retain one native object
// (including transport metadata) for every provider token until completion.
// Native concatenation preserves reasoning/tool block order and identities.
type modelStreamAccumulator struct {
	pending  []*schema.AgenticMessage
	segments []*schema.AgenticMessage
	count    int
}

func (a *modelStreamAccumulator) add(message *schema.AgenticMessage) error {
	a.pending = append(a.pending, message)
	a.count++
	if len(a.pending) >= 128 {
		return a.flush()
	}
	return nil
}

func (a *modelStreamAccumulator) flush() error {
	if len(a.pending) == 0 {
		return nil
	}
	segment, err := schema.ConcatAgenticMessages(a.pending)
	if err != nil {
		return &ModelError{Code: "invalid_upstream_response", Retry: true}
	}
	// Native concatenation finalizes streaming block indices. Restore those
	// indices on intermediate batches so later deltas still join the same block
	// instead of becoming separate text/reasoning/tool calls.
	indices := map[int]bool{}
	for _, message := range a.pending {
		for _, block := range message.ContentBlocks {
			if block != nil && block.StreamingMeta != nil {
				indices[block.StreamingMeta.Index] = true
			}
		}
	}
	if len(indices) > 0 {
		ordered := make([]int, 0, len(indices))
		for index := range indices {
			ordered = append(ordered, index)
		}
		sort.Ints(ordered)
		if len(ordered) != len(segment.ContentBlocks) {
			return &ModelError{Code: "invalid_upstream_response", Retry: true}
		}
		copy := *segment
		copy.ContentBlocks = make([]*schema.ContentBlock, len(ordered))
		for i, block := range segment.ContentBlocks {
			cloned := *block
			cloned.StreamingMeta = &schema.StreamingMeta{Index: ordered[i]}
			copy.ContentBlocks[i] = &cloned
		}
		segment = &copy
	}
	a.segments = append(a.segments, segment)
	a.pending = nil
	return nil
}

func (a *modelStreamAccumulator) result() (*schema.AgenticMessage, error) {
	if err := a.flush(); err != nil {
		return nil, err
	}
	return schema.ConcatAgenticMessages(a.segments)
}

// The OpenAI extension repeats this response header on every chunk. Eino's
// generic Extra concatenation treats strings as text deltas; retain the header
// once so it does not grow with token count. Other metadata and native content
// remain untouched. Cached complete receipts bypass this live-stream adapter.
func streamRequestIDDelta(message *schema.AgenticMessage, previous *string) *schema.AgenticMessage {
	id, ok := message.Extra["openai-request-id"].(string)
	if !ok || id == "" {
		return message
	}
	if id != *previous {
		*previous = id
		return message
	}
	copy := *message
	copy.Extra = make(map[string]any, len(message.Extra))
	for key, value := range message.Extra {
		if key != "openai-request-id" {
			copy.Extra[key] = value
		}
	}
	return &copy
}

func safeGatewayCode(code string) string {
	switch code {
	case "model_unconfigured", "model_disabled", "gateway_unavailable", "upstream_unavailable", "upstream_timeout", "upstream_error", "invalid_request", "request_too_large", "context_overflow", "upstream_response_limit", "invalid_upstream_response", "run_cancelled", "gateway_busy", "upstream_rate_limited", "upstream_http_error", "upstream_in_stream_error", "upstream_interrupted", "client_interrupted", "canceled", "protocol_unsupported":
		return code
	}
	return "gateway_error"
}

var gatewayStreamHTTP = &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 10 * time.Second, MaxIdleConns: 8, MaxIdleConnsPerHost: 2}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// Eino .35's Pipe has no Close hook. A private pulse source makes consumer Close
// observable even while the HTTP producer is waiting on a silent upstream. No
// pulse becomes a model chunk; completion and cancellation join this watcher.
func cancelStreamOnClose(reader *schema.StreamReader[*schema.AgenticMessage], cancel context.CancelFunc) (*schema.StreamReader[*schema.AgenticMessage], func()) {
	pulse, writer := schema.Pipe[*schema.AgenticMessage](0)
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
	// Pipe automatic-close finalizers attach to the original reader wrapper,
	// which Merge discards while retaining its stream. Set the idempotent-close
	// flags after merging so collecting an unused wrapper cannot end live data.
	merged.SetAutomaticClose()
	// The producer retains the underlying streams, so their own finalizers
	// cannot reclaim an abandoned consumer. Only the returned conversion holds
	// this token: it keeps active reads alive and cancels/closes when abandoned.
	lifetime := &modelStreamLifetime{cancel: cancel, reader: merged}
	runtime.SetFinalizer(lifetime, func(l *modelStreamLifetime) { l.cancel(); l.reader.Close() })
	out := schema.StreamReaderWithConvert(merged, func(m *schema.AgenticMessage) (*schema.AgenticMessage, error) {
		runtime.KeepAlive(lifetime)
		if m == nil {
			return nil, schema.ErrNoValue
		}
		return m, nil
	})
	var once sync.Once
	return out, func() { once.Do(func() { close(stopped); pulse.Close(); <-joined }) }
}

type modelStreamLifetime struct {
	cancel context.CancelFunc
	reader *schema.StreamReader[*schema.AgenticMessage]
}

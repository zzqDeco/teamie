package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go.uber.org/zap"
	"io"
	"net"
	"net/http"
	"runtime"
	"sort"
	"sync"
	"teamie/backend/pkg/observability"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/jackc/pgx/v5"
	"teamie/agent-protocol/modeltransport"
)

func (m *GatewayModel) Stream(ctx context.Context, input []*schema.AgenticMessage, options ...model.Option) (out *schema.StreamReader[*schema.AgenticMessage], failure error) {
	run, err := CurrentRun(ctx)
	if err != nil {
		return nil, err
	}
	ctx, operation := observability.Begin(ctx, "model-call", zap.Bool("custom.model.streaming", true))
	observability.L(ctx).Info("Model call started", zap.String("event.action", "model-call-started"))
	phase, step := "model-slot-wait", 0
	handedOff := false
	defer func() {
		if !handedOff {
			modelCompletion(operation, failure, phase, run, step)
		}
	}()
	release, err := run.Front.modelPermit(ctx)
	if err != nil {
		return nil, err
	}
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
	step = int(run.Step.Add(1))
	phase = "receipt-lookup"
	ctx = observability.WithFields(ctx, zap.Int("custom.model.step", step))
	outputTokens := 0
	if opts.MaxTokens != nil {
		outputTokens = *opts.MaxTokens
	}
	observability.L(ctx).Info("Model request admitted", zap.String("event.action", "model-request-admitted"), zap.Int("custom.model.request_bytes", len(request)), zap.Int("custom.model.input_messages", len(input)), zap.Int("custom.model.output_token_limit", outputTokens))
	cached, err := m.cachedModel(ctx, run, step, hash, input, opts, true)
	if err == nil {
		phase = "receipt-replay"
		result, e := decodeAgenticMessage(cached)
		if e != nil {
			return nil, e
		}
		run.recordModelUsageAnchor(step, request, result)
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
	phase = "gateway-request"
	body, _ := json.Marshal(modeltransport.CallRequest{OwnerID: m.OwnerID, AgentID: m.AgentID, SessionID: "agent:" + m.AgentID, RunID: executionID(run), RequestJSON: string(request), MessageLimit: modelInputMessageLimit(run)})
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
	response, err := observability.Client(client, "teamie.llm-gateway").Do(req)
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
		var streamFailure error
		shape := &streamShape{}
		chunkCount, totalBytes := 0, 0
		receivedDone, receiptSaved := false, false
		stage := "stream-read"
		defer func() {
			fields := append(shape.fields(), zap.Int("custom.model.stream.chunk_count", chunkCount), zap.Int("custom.model.stream.transport_bytes", totalBytes), zap.Bool("custom.model.stream.done_received", receivedDone), zap.Bool("custom.model.receipt_saved", receiptSaved))
			modelCompletion(operation, streamFailure, stage, run, step, fields...)
		}()
		fail := func(err error) { streamFailure = err; writer.Send(nil, err) }
		defer release()
		defer cancel()
		defer bridgeDone()
		defer response.Body.Close()
		defer writer.Close()
		scanner := bufio.NewScanner(io.LimitReader(response.Body, modeltransport.MaxHTTPStreamBytes+1))
		scanner.Buffer(make([]byte, 4096), modeltransport.MaxHTTPStreamFrameBytes)
		chunks := &modelStreamAccumulator{}
		requestID := ""
		anchorAttached := false
		total := 0
		done := false
		for scanner.Scan() {
			total += len(scanner.Bytes())
			totalBytes = total
			if total > modeltransport.MaxHTTPStreamBytes {
				fail(&ModelError{Code: "upstream_response_limit"})
				return
			}
			var frame struct {
				Type         string `json:"type"`
				ResponseJSON string `json:"responseJson"`
				ErrorCode    string `json:"errorCode"`
			}
			stage = "frame-decode"
			if decodeErr := json.Unmarshal(scanner.Bytes(), &frame); decodeErr != nil {
				fail(modelDiagnostic(stage, "invalid_transport_frame", decodeErr))
				return
			}
			switch frame.Type {
			case "chunk":
				if len(frame.ResponseJSON) > modeltransport.MaxResponseBytes {
					fail(&ModelError{Code: "upstream_response_limit"})
					return
				}
				stage = "message-decode"
				message, e := decodeAgenticMessage([]byte(frame.ResponseJSON))
				if e != nil {
					fail(e)
					return
				}
				if message == nil {
					continue
				}
				chunkCount++
				shape.observe(message)
				stage = "aggregate-batch"
				message = streamRequestIDDelta(message, &requestID)
				if e = chunks.add(message, len(frame.ResponseJSON)); e != nil {
					fail(e)
					return
				}
				outMsg := message
				if !anchorAttached && message.ResponseMeta != nil && message.ResponseMeta.TokenUsage != nil && message.ResponseMeta.TokenUsage.PromptTokens > 0 {
					if anchor := run.computeRequestUsageAnchor(step, request, message); anchor != nil {
						anchorAttached = true
						cloned := *message
						cloned.Extra = make(map[string]any, len(message.Extra)+1)
						for k, v := range message.Extra {
							cloned.Extra[k] = v
						}
						attachUsageAnchor(&cloned, anchor)
						outMsg = &cloned
					}
				}
				if writer.Send(outMsg, nil) {
					streamFailure = observability.Diagnostic("stream_consumer_closed", requestCtx.Err())
					stage = "consumer-closed"
					return
				}
			case "error":
				stage = "gateway-error"
				code := safeGatewayCode(frame.ErrorCode)
				if isContextOverflow(code) && chunks.count > 0 {
					// An accepted partial stream is not a pure rejected request.
					// Never replay it as a side-effect-free overflow decision.
					fail(&ModelError{Code: "upstream_in_stream_error"})
				} else {
					fail(m.gatewayFailure(ctx, run, step, hash, true, code))
				}
				return
			case "done":
				done = true
				receivedDone = true
			default:
				fail(&ModelError{Code: "invalid_upstream_response", Retry: true})
				return
			}
			if done {
				break
			}
		}
		stage = "stream-read"
		if err := scanner.Err(); err != nil {
			reason := "transport_read_failed"
			if errors.Is(err, bufio.ErrTooLong) {
				reason = "transport_frame_limit_exceeded"
			}
			fail(&ModelError{Code: "gateway_stream_interrupted", Retry: true, Stage: stage, Reason: reason, Cause: err})
			return
		}
		if !done {
			fail(&ModelError{Code: "gateway_stream_interrupted", Retry: true, Stage: stage, Reason: "terminal_frame_missing"})
			return
		}
		stage = "stream-terminal"
		if chunks.count == 0 {
			fail(modelDiagnostic(stage, "empty_stream", nil))
			return
		}
		stage = "aggregate-final"
		result, err := chunks.result()
		if err != nil {
			fail(concatDiagnostic(stage, err))
			return
		}
		stage = "validate-result"
		if err = validateModelResult(result, modelResultByteLimit(run)); err != nil {
			fail(err)
			return
		}
		stage = "serialize-result"
		b, err := (&schema.HumanReadableSerializer{}).Marshal(result)
		if err == nil && len(b) > modeltransport.MaxResponseBytes {
			err = &ModelError{Code: "upstream_response_limit"}
		}
		if err == nil {
			stage = "persist-receipt"
			err = m.Store.SaveModel(ctx, m.Lease, run, step, hash, b)
			receiptSaved = err == nil
			if receiptSaved {
				run.recordModelUsageAnchor(step, request, result)
			}
		}
		if err != nil {
			fail(err)
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
	// Sum of encoded retained chunks/segments bounds the reconstructed message
	// without serializing an ever-growing response after every token.
	retainedBytes int
}

func (a *modelStreamAccumulator) add(message *schema.AgenticMessage, encodedBytes ...int) error {
	var size int
	if len(encodedBytes) > 0 {
		size = encodedBytes[0]
	} else {
		body, err := (&schema.HumanReadableSerializer{}).Marshal(message)
		if err != nil {
			return modelDiagnostic("aggregate-serialize", "native_serialize_failed", err)
		}
		size = len(body)
	}
	if size > modeltransport.MaxResponseBytes {
		return &ModelError{Code: "upstream_response_limit"}
	}
	a.pending = append(a.pending, message)
	a.count++
	a.retainedBytes += size
	if a.retainedBytes > modeltransport.MaxResponseBytes {
		return a.compact()
	}
	if len(a.pending) >= 128 {
		return a.flush()
	}
	return nil
}

func (a *modelStreamAccumulator) flush() error {
	if len(a.pending) == 0 {
		return nil
	}
	segment, err := concatStreamSegments(a.pending)
	if err != nil {
		return err
	}
	a.segments = append(a.segments, segment)
	a.pending = nil
	return nil
}

// Remove repeated chunk framing once the encoded upper bound reaches the native
// cap, then check the exact reconstructed size before accepting another delta.
// At most one bounded incoming chunk is added to the retained response first.
func (a *modelStreamAccumulator) compact() error {
	if err := a.flush(); err != nil {
		return err
	}
	final, err := a.concatFinal()
	if err != nil {
		return concatDiagnostic("aggregate-compact", err)
	}
	body, err := (&schema.HumanReadableSerializer{}).Marshal(final)
	if err != nil {
		return modelDiagnostic("aggregate-serialize", "native_serialize_failed", err)
	}
	if len(body) > modeltransport.MaxResponseBytes {
		return &ModelError{Code: "upstream_response_limit"}
	}
	segment, err := restoreStreamIndices(final, a.segments)
	if err != nil {
		return err
	}
	retained, err := (&schema.HumanReadableSerializer{}).Marshal(segment)
	if err != nil {
		return modelDiagnostic("aggregate-serialize", "native_serialize_failed", err)
	}
	a.segments = []*schema.AgenticMessage{segment}
	a.retainedBytes = len(retained)
	return nil
}

func concatStreamSegments(messages []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
	segment, err := schema.ConcatAgenticMessages(messages)
	if err != nil {
		return nil, concatDiagnostic("aggregate-batch", err)
	}
	return restoreStreamIndices(segment, messages)
}

func restoreStreamIndices(segment *schema.AgenticMessage, messages []*schema.AgenticMessage) (*schema.AgenticMessage, error) {
	// Native concatenation finalizes streaming block indices. Restore those
	// indices on intermediate batches so later deltas still join the same block
	// instead of becoming separate text/reasoning/tool calls.
	indices := map[int]bool{}
	for _, message := range messages {
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
			failure := modelDiagnostic("restore-block-indices", "block_index_count_mismatch", nil)
			failure.ExpectedBlocks = len(ordered)
			failure.ActualBlocks = len(segment.ContentBlocks)
			return nil, failure
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
	return segment, nil
}

func (a *modelStreamAccumulator) result() (*schema.AgenticMessage, error) {
	if err := a.flush(); err != nil {
		return nil, err
	}
	return a.concatFinal()
}

func (a *modelStreamAccumulator) concatFinal() (*schema.AgenticMessage, error) {
	segments := a.segments
	if len(segments) == 1 && a.count > 1 {
		// Eino's one-element fast path would retain the intermediate indices.
		segments = []*schema.AgenticMessage{segments[0], {}}
	}
	return schema.ConcatAgenticMessages(segments)
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

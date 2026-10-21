package engine

import (
	"context"
	"encoding/json"
	"errors"
	"go.uber.org/zap"
	"net"
	"net/http"
	"strings"
	"teamie/backend/pkg/observability"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/kitex_gen/agent/modelgatewayservice"
	"teamie/agent-protocol/modeltransport"
)

type ModelError struct {
	Code  string
	Retry bool
	// Private diagnostics never change Error(), public safe codes, hashes or
	// durable model/tool receipts. The native cause remains available internally.
	Stage, Reason                string
	ConfigID                     string
	ExpectedBlocks, ActualBlocks int
	Cause                        error
}

func (e *ModelError) Error() string { return "model gateway: " + e.Code }
func (e *ModelError) Unwrap() error { return e.Cause }
func (e *ModelError) DiagnosticMessage() string {
	if e.Stage == "" {
		return e.Code
	}
	return e.Code + " at " + e.Stage + ": " + e.Reason
}

type GatewayModel struct {
	Client                  modelgatewayservice.Client
	Token, OwnerID, AgentID string
	StreamURL               string
	ContextWindow           func() int
	ModelConfigID           func() string
	HTTP                    *http.Client
	Store                   *Store
	Lease                   *Lease
	Authorize               func(context.Context) error
}

func safeGatewayPing(ctx context.Context, client modelgatewayservice.Client, token string) (resp *wire.PingResponse, err error) {
	if client == nil {
		return nil, errors.New("nil gateway client")
	}
	defer func() {
		if r := recover(); r != nil {
			resp = nil
			err = errors.New("gateway ping unimplemented")
		}
	}()
	return client.Ping(ctx, &wire.PingRequest{ServiceToken: &token})
}

func (m *GatewayModel) liveModelConfigID(ctx context.Context) (string, bool) {
	if m == nil {
		return "", false
	}
	hasRPC := m.Client != nil
	if gh, ok := m.Client.(*gatewayHTTPClient); ok && gh != nil && gh.Client == nil {
		hasRPC = false
	}
	if hasRPC {
		pc, cancel := context.WithTimeout(ctx, 2*time.Second)
		ping, err := safeGatewayPing(pc, m.Client, m.Token)
		cancel()
		if err == nil && ping != nil && ping.Ready && ping.GetConfigured() && ping.GetEnabled() {
			if id := strings.TrimSpace(ping.GetModelConfigId()); id != "" {
				return id, true
			}
		}
		return "", false
	}
	if m.ModelConfigID != nil {
		if id := strings.TrimSpace(m.ModelConfigID()); id != "" {
			return id, true
		}
	}
	return "", false
}

func (m *GatewayModel) CurrentModelAdmissionState(ctx context.Context) (string, string, bool, error) {
	if m == nil {
		return "", "", false, nil
	}
	if run, err := CurrentRun(ctx); err == nil && run != nil && m.Store != nil {
		nextStep := int(run.Step.Load()) + 1
		id, mode, recorded, err := m.Store.RecordedModelStepAdmission(ctx, run, nextStep)
		if err != nil {
			return "", "", false, err
		}
		if recorded {
			return id, mode, id != "", nil
		}
	}
	id, ok := m.liveModelConfigID(ctx)
	return id, "", ok, nil
}

func (m *GatewayModel) CurrentModelConfigID(ctx context.Context) (string, bool) {
	id, _, ok, err := m.CurrentModelAdmissionState(ctx)
	if err != nil {
		return "", false
	}
	return id, ok
}

func (m *GatewayModel) Generate(ctx context.Context, messages []*schema.AgenticMessage, options ...model.Option) (_ *schema.AgenticMessage, failure error) {
	run, e := CurrentRun(ctx)
	if e != nil {
		return nil, e
	}
	ctx, operation := observability.Begin(ctx, "model-call", zap.Bool("custom.model.streaming", false))
	phase, step := "model-slot-wait", 0
	defer func() { modelCompletion(operation, failure, phase, run, step) }()
	observability.L(ctx).Info("Model call started", zap.String("event.action", "model-call-started"))
	release, e := run.Front.modelPermit(ctx)
	if e != nil {
		return nil, e
	}
	defer release()
	if m.Authorize != nil {
		if err := m.Authorize(ctx); err != nil {
			return nil, err
		}
	}
	if e = m.Store.CheckLease(ctx, m.Lease); e != nil {
		return nil, e
	}
	opts := model.GetCommonOptions(nil, options...)
	if e = m.capRunOutput(run, opts); e != nil {
		return nil, e
	}
	request, e := agenticRequestForRun(run, messages, opts, false)
	if e != nil {
		return nil, e
	}
	hash := modelRequestHash(request)
	step = int(run.Step.Add(1))
	phase = "receipt-lookup"
	ctx = observability.WithFields(ctx, zap.Int("custom.model.step", step))
	outputTokens := 0
	if opts.MaxTokens != nil {
		outputTokens = *opts.MaxTokens
	}
	observability.L(ctx).Info("Model request admitted", zap.String("event.action", "model-request-admitted"), zap.Int("custom.model.request_bytes", len(request)), zap.Int("custom.model.input_messages", len(messages)), zap.Int("custom.model.output_token_limit", outputTokens))
	cached, e := m.cachedModel(ctx, run, step, hash, messages, opts, false)
	if e == nil {
		phase = "receipt-replay"
		run.consumeAdmissionConfig(false)
		result, decodeErr := decodeAgenticMessage(cached)
		if decodeErr == nil {
			replayedConfigID, _ := extractRecordedModelConfigID(result)
			replayedAdm, ok := extractRecordedAdmissionMetadata(result)
			if ok {
				run.recordModelUsageAnchor(step, request, result, replayedConfigID, replayedAdm)
			} else {
				run.recordModelUsageAnchor(step, request, result, replayedConfigID)
			}
		}
		return result, decodeErr
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	if e = m.cachedRecovery(ctx, run, step, hash, false); e != nil {
		run.consumeAdmissionConfig(false)
		return nil, e
	}
	adm, e := m.checkContextRequest(ctx, run, step, hash, request, false)
	if e != nil {
		return nil, e
	}
	reqConfigID := adm.ConfigID
	phase = "gateway-request"
	response, e := m.Client.Chat(withExpectedModelConfigID(ctx, reqConfigID), &wire.GatewayChatRequest{ServiceToken: m.Token, OwnerId: m.OwnerID, AgentId: m.AgentID, SessionId: "agent:" + m.AgentID, RunId: executionID(run), RequestJson: string(request)})
	if e != nil {
		var gatewayError *ModelError
		if errors.As(e, &gatewayError) {
			errConfigID := strings.TrimSpace(gatewayError.ConfigID)
			if errConfigID == "" {
				errConfigID = reqConfigID
			}
			return nil, m.gatewayFailure(ctx, run, step, hash, false, gatewayError.Code, errConfigID, adm)
		}
		return nil, &ModelError{Code: "gateway_unavailable", Retry: true}
	}
	if !response.Ok {
		code := safeGatewayCode(response.GetErrorCode())
		if code == "" {
			code = "gateway_error"
		}
		errConfigID := strings.TrimSpace(response.GetModelConfigId())
		if errConfigID == "" {
			errConfigID = reqConfigID
		}
		return nil, m.gatewayFailure(ctx, run, step, hash, false, code, errConfigID, adm)
	}
	if len(response.GetResponseJson()) > modeltransport.MaxResponseBytes {
		return nil, &ModelError{Code: "upstream_response_limit"}
	}
	phase = "message-decode"
	result, e := decodeAgenticMessage([]byte(response.GetResponseJson()))
	if e != nil {
		return nil, e
	}
	phase = "validate-result"
	if e = validateModelResult(result, modelResultByteLimit(run)); e != nil {
		return nil, e
	}
	respConfigID := strings.TrimSpace(response.GetModelConfigId())
	if respConfigID != "" && reqConfigID != "" && respConfigID != reqConfigID {
		return nil, &ModelError{Code: "gateway_unavailable", Retry: true}
	}
	if respConfigID == "" {
		respConfigID = reqConfigID
	}
	run.recordModelUsageAnchor(step, request, result, respConfigID, adm)
	phase = "serialize-result"
	b, e := (&schema.HumanReadableSerializer{}).Marshal(result)
	if e != nil {
		return nil, e
	}
	if len(b) > modeltransport.MaxResponseBytes {
		return nil, &ModelError{Code: "upstream_response_limit"}
	}
	phase = "persist-receipt"
	if e = m.Store.SaveModel(ctx, m.Lease, run, step, hash, b); e != nil {
		return nil, e
	}
	return result, nil
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
	if errors.Is(err, ErrReplayConflict) || errors.Is(err, adk.ErrInvalidEventID) || errors.Is(err, adk.ErrDuplicateEventID) {
		return false
	}
	if errors.Is(err, adk.ErrSessionEventPersistence) {
		return true
	}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		code := databaseError.Code
		return strings.HasPrefix(code, "08") || strings.HasPrefix(code, "53") || code == "40001" || code == "40P01" || code == "57P01" || code == "57P02" || code == "57P03"
	}
	var networkError net.Error
	if errors.As(err, &networkError) || pgconn.SafeToRetry(err) || pgconn.Timeout(err) {
		return true
	}

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

var _ model.AgenticModel = (*GatewayModel)(nil)

func (m *GatewayModel) capRunOutput(run *Run, opts *model.Options) error {
	if contextProfile(run) != contextProfileV1 {
		m.capOutput(opts)
		return nil
	}
	budget, err := ParseContextBudgetSnapshot(run.ContextBudget)
	if err != nil {
		return err
	}
	limit := budget.OutputReserveTokens
	if opts.MaxTokens == nil {
		initial := budget.initialOutputTokens()
		opts.MaxTokens = &initial
	} else if *opts.MaxTokens > limit {
		opts.MaxTokens = &limit
	}
	return nil
}

func (m *GatewayModel) checkContextRequest(ctx context.Context, run *Run, step int, hash string, request []byte, streaming bool) (requestAdmissionState, error) {
	if contextProfile(run) != contextProfileV1 {
		return requestAdmissionState{}, nil
	}
	budget, err := ParseContextBudgetSnapshot(run.ContextBudget)
	if err != nil {
		return requestAdmissionState{}, err
	}
	var configID, entryMode string
	var hasAdmission bool
	if budget.Version >= 7 {
		configID, entryMode, hasAdmission = run.consumeAdmissionConfig(isSummaryRequest(request))
		if !hasAdmission {
			configID, _ = m.liveModelConfigID(ctx)
		}
	}
	anchor := run.getUsageAnchorForConfig(configID)
	measurement, err := budget.measureCalibratedRequest(anchor, request)
	if err != nil {
		return requestAdmissionState{}, err
	}
	if entryMode == "" {
		entryMode = measurement.CounterMode
	}
	adm := requestAdmissionState{
		ConfigID:    strings.TrimSpace(configID),
		CounterMode: entryMode,
		Known:       strings.TrimSpace(configID) != "",
	}
	observability.L(ctx).Info("Context request checked", append([]zap.Field{zap.String("event.action", "context-request-checked"), zap.Bool("custom.context.fits", budget.fits(measurement))}, budget.measurementLogFields(measurement, "")...)...)
	if !budget.fits(measurement) {
		return adm, m.gatewayFailure(ctx, run, step, hash, streaming, "request_too_large", adm.ConfigID, adm)
	}
	return adm, nil
}

func (m *GatewayModel) capOutput(opts *model.Options) {
	if m.ContextWindow == nil {
		return
	}
	limit := m.ContextWindow() / 4
	if limit < 256 {
		limit = 256
	}
	if limit > 4096 {
		limit = 4096
	}
	if opts.MaxTokens == nil {
		if limit < 2048 {
			opts.MaxTokens = &limit
		}
	} else if *opts.MaxTokens > limit {
		opts.MaxTokens = &limit
	}
}

func agenticRequestForRun(run *Run, messages []*schema.AgenticMessage, opts *model.Options, streaming bool) ([]byte, error) {
	if contextProfile(run) == contextProfileV1 {
		budget, err := ParseContextBudgetSnapshot(run.ContextBudget)
		if err != nil {
			return nil, err
		}
		if budget.Version >= 6 {
			return agenticRequest(messages, opts, streaming, modeltransport.MaxOutputTokens)
		}
	}
	return agenticRequest(messages, opts, streaming)
}

func modelResultByteLimit(run *Run) int {
	if contextProfile(run) == contextProfileV1 {
		budget, err := ParseContextBudgetSnapshot(run.ContextBudget)
		if err == nil && budget.Version >= 6 {
			return modeltransport.MaxResponseBytes
		}
	}
	return 65536
}

func modelInputMessageLimit(run *Run) int {
	if contextProfile(run) == contextProfileV1 {
		budget, err := ParseContextBudgetSnapshot(run.ContextBudget)
		if err == nil && budget.Version >= 6 {
			return modeltransport.MaxInputMessages
		}
	}
	// Omit the field so historical private HTTP envelopes remain unchanged.
	return 0
}

// The default preserves the exact encoding and cap of historical Run requests.
// Only the new frozen policy opts into the larger output ceiling.
func agenticRequest(messages []*schema.AgenticMessage, opts *model.Options, streaming bool, outputLimits ...int) ([]byte, error) {
	limit := 4096
	if len(outputLimits) > 0 {
		limit = min(outputLimits[0], modeltransport.MaxOutputTokens)
	}
	if opts.MaxTokens == nil {
		n := 2048
		opts.MaxTokens = &n
	}
	if *opts.MaxTokens <= 0 {
		return nil, &ModelError{Code: "invalid_request"}
	}
	if *opts.MaxTokens > limit {
		n := limit
		opts.MaxTokens = &n
	}
	if opts.AgenticToolChoice == nil {
		choice := schema.ToolChoiceAllowed
		if opts.ToolChoice != nil {
			choice = *opts.ToolChoice
		}
		opts.AgenticToolChoice = &schema.AgenticToolChoice{Type: choice}
	}
	// Model selection belongs solely to the administrator's gateway.
	opts.Model = nil
	messagesJSON, err := (&schema.HumanReadableSerializer{}).Marshal(messages)
	if err != nil {
		return nil, err
	}
	optionsJSON, err := json.Marshal(opts)
	if err != nil {
		return nil, err
	}
	return json.Marshal(modeltransport.Request{Protocol: modeltransport.Version, Messages: messagesJSON, Options: optionsJSON, Stream: streaming})
}

// Native Eino assigns fresh internal message IDs when restarting a turn. They
// are persisted for Session mutation targets but do not affect provider input.
// Keep them in the transport/audit; exclude only this known ephemeral key from
// the receipt identity. Source IDs live in the message body and remain hashed.
func modelRequestHash(raw []byte) string {
	var envelope modeltransport.Request
	if json.Unmarshal(raw, &envelope) != nil {
		return digest(raw)
	}
	value, wrapper := serializedValue(envelope.Messages)
	var messages []map[string]json.RawMessage
	if json.Unmarshal(value, &messages) != nil {
		return digest(raw)
	}
	for _, message := range messages {
		var extra map[string]json.RawMessage
		if json.Unmarshal(message["extra"], &extra) == nil {
			delete(extra, "_eino_msg_id")
			delete(extra, contextUsageAnchorExtraKey)
			if len(extra) == 0 {
				delete(message, "extra")
			} else {
				message["extra"], _ = json.Marshal(extra)
			}
		}
	}
	value, _ = json.Marshal(messages)
	if wrapper != nil {
		wrapper["value"] = value
		value, _ = json.Marshal(wrapper)
	}
	envelope.Messages = value
	b, _ := json.Marshal(envelope)
	return digest(b)
}

func (m *GatewayModel) cachedModel(ctx context.Context, run *Run, step int, hash string, messages []*schema.AgenticMessage, opts *model.Options, streaming bool) ([]byte, error) {
	if contextProfile(run) == contextProfileV1 {
		return m.Store.CachedModel(ctx, run, step, hash)
	}
	b, err := m.exactCachedModel(ctx, run, step, hash, messages, opts, streaming)
	if !errors.Is(err, ErrReplayConflict) {
		return b, err
	}
	candidates, err := m.Store.observedToolReplayInputs(ctx, run, messages)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		options := *opts
		request, err := agenticRequest(candidate, &options, streaming)
		if err != nil {
			return nil, err
		}
		b, err = m.exactCachedModel(ctx, run, step, modelRequestHash(request), candidate, &options, streaming)
		if !errors.Is(err, ErrReplayConflict) {
			return b, err
		}
	}
	return nil, ErrReplayConflict
}

func (m *GatewayModel) exactCachedModel(ctx context.Context, run *Run, step int, hash string, messages []*schema.AgenticMessage, opts *model.Options, streaming bool) ([]byte, error) {
	b, err := m.Store.CachedModel(ctx, run, step, hash)
	if !errors.Is(err, ErrReplayConflict) {
		return b, err
	}
	legacy, convertErr := legacyMessages(messages)
	if convertErr != nil {
		return nil, err
	}
	oldOpts := *opts
	if oldOpts.AgenticToolChoice != nil {
		c := oldOpts.AgenticToolChoice
		if c.Allowed != nil || c.Forced != nil {
			return nil, err
		}
		oldOpts.ToolChoice = &c.Type
	}
	raw, convertErr := legacyChatRequest(legacy, oldOpts.Tools, &oldOpts)
	if convertErr != nil {
		return nil, err
	}
	if streaming {
		var o map[string]any
		_ = json.Unmarshal(raw, &o)
		o["stream"] = true
		o["stream_options"] = map[string]any{"include_usage": true}
		raw, _ = json.Marshal(o)
	}
	b, err = m.Store.CachedModel(ctx, run, step, digest(raw))
	if err != nil {
		return nil, err
	}
	// Never permit a native-format receipt to bypass its request identity.
	if !isLegacyMessage(b) {
		return nil, ErrReplayConflict
	}
	return b, nil
}

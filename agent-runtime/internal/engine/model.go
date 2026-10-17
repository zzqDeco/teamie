package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

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
}

func (e *ModelError) Error() string { return "model gateway: " + e.Code }

type GatewayModel struct {
	Client                  modelgatewayservice.Client
	Token, OwnerID, AgentID string
	StreamURL               string
	ContextWindow           func() int
	HTTP                    *http.Client
	Store                   *Store
	Lease                   *Lease
	Authorize               func(context.Context) error
}

func (m *GatewayModel) Generate(ctx context.Context, messages []*schema.AgenticMessage, options ...model.Option) (*schema.AgenticMessage, error) {
	run, e := CurrentRun(ctx)
	if e != nil {
		return nil, e
	}
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
	request, e := agenticRequest(messages, opts, false)
	if e != nil {
		return nil, e
	}
	hash := modelRequestHash(request)
	step := int(run.Step.Add(1))
	cached, e := m.cachedModel(ctx, run, step, hash, messages, opts, false)
	if e == nil {
		return decodeAgenticMessage(cached)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	if e = m.cachedRecovery(ctx, run, step, hash, false); e != nil {
		return nil, e
	}
	if e = m.checkContextRequest(ctx, run, step, hash, request, false); e != nil {
		return nil, e
	}
	response, e := m.Client.Chat(ctx, &wire.GatewayChatRequest{ServiceToken: m.Token, OwnerId: m.OwnerID, AgentId: m.AgentID, SessionId: "agent:" + m.AgentID, RunId: executionID(run), RequestJson: string(request)})
	if e != nil {
		var gatewayError *ModelError
		if errors.As(e, &gatewayError) {
			return nil, m.gatewayFailure(ctx, run, step, hash, false, gatewayError.Code)
		}
		return nil, &ModelError{Code: "gateway_unavailable", Retry: true}
	}
	if !response.Ok {
		code := safeGatewayCode(response.GetErrorCode())
		if code == "" {
			code = "gateway_error"
		}
		return nil, m.gatewayFailure(ctx, run, step, hash, false, code)
	}
	result, e := decodeAgenticMessage([]byte(response.GetResponseJson()))
	if e != nil {
		return nil, e
	}
	if e = validateModelResult(result); e != nil {
		return nil, e
	}
	b, e := (&schema.HumanReadableSerializer{}).Marshal(result)
	if e != nil {
		return nil, e
	}
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

func (m *GatewayModel) checkContextRequest(ctx context.Context, run *Run, step int, hash string, request []byte, streaming bool) error {
	if contextProfile(run) != contextProfileV1 {
		return nil
	}
	budget, err := ParseContextBudgetSnapshot(run.ContextBudget)
	if err != nil {
		return err
	}
	measurement, err := measureContextRequest(request)
	if err != nil {
		return err
	}
	if !budget.fits(measurement) {
		return m.gatewayFailure(ctx, run, step, hash, streaming, "request_too_large")
	}
	return nil
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

func agenticRequest(messages []*schema.AgenticMessage, opts *model.Options, streaming bool) ([]byte, error) {
	if opts.MaxTokens == nil {
		n := 2048
		opts.MaxTokens = &n
	}
	if *opts.MaxTokens <= 0 {
		return nil, &ModelError{Code: "invalid_request"}
	}
	if *opts.MaxTokens > 4096 {
		n := 4096
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

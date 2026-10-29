package app

import (
	"context"
	"strings"
	"time"

	"teamie/services/llm-gateway/internal/domain"
)

// CallOutcome records the result and diagnostic telemetry of a unary model call.
type CallOutcome struct {
	Result           domain.ChatCallResult
	Phase            string
	PromptTokens     int64
	CompletionTokens int64
	Failure          error
}

// StreamOutcome records the diagnostic telemetry of a streaming model call.
type StreamOutcome struct {
	Phase              string
	PromptTokens       int64
	CompletionTokens   int64
	ChunkCount         int
	TransportBytes     int
	Failure            error
	TerminalWriteError error
}

func failedChatResult(err error, configID string) domain.ChatCallResult {
	de := domain.AsError(err)
	return domain.ChatCallResult{
		OK:            false,
		ErrorCode:     de.Code,
		ErrorMessage:  de.Message,
		StatusCode:    de.Status,
		ModelConfigID: configID,
	}
}

// CallChat executes a unary model inference call with slot admission, cancellation tracking, and usage auditing.
func (s *Service) CallChat(ctx context.Context, owner, agentID, sessionID, runID, raw string, messageLimits ...int) CallOutcome {
	s.ensureInit()
	outcome := CallOutcome{Phase: "admission"}
	fail := func(err error, cfgID string) CallOutcome {
		outcome.Failure = err
		outcome.Result = failedChatResult(err, cfgID)
		return outcome
	}
	unlockAdmission := s.lockModelAdmission()
	defer unlockAdmission()
	settings, err := s.Store.Load(ctx)
	if err != nil {
		return fail(err, "")
	}
	var c domain.ResolvedModel
	if StrictModelSelection(ctx) {
		exact, ok := settings.ResolveExactModel(RequestedModelID(ctx))
		if !ok {
			return fail(domain.Err(404, "not_found", "Model profile not found"), "")
		}
		c = exact
	} else {
		c = settings.ResolveModel(RequestedModelID(ctx))
	}
	if !c.Configured() {
		return fail(domain.Err(409, "model_unconfigured", "An administrator must configure the model gateway"), "")
	}
	if !c.Enabled {
		return fail(domain.Err(409, "model_disabled", "The model gateway is disabled"), "")
	}
	configID := c.CountingConfigID()
	if expected := ExpectedModelConfigID(ctx); expected != "" && expected != configID {
		return fail(domain.Err(409, "gateway_unavailable", "Model gateway configuration changed"), configID)
	}
	release, ok := s.AcquireSlot()
	if !ok {
		return fail(domain.Err(429, "gateway_busy", "The model gateway is busy"), configID)
	}
	defer release()
	key, err := s.Cipher.Decrypt(c.CipherKey)
	if err != nil {
		return fail(domain.ErrWithCause(503, "credential_unavailable", "Saved model credential could not be decrypted", err), configID)
	}
	callCtx, cleanup := s.RegisterRunCancel(ctx, agentID, runID, c.ID)
	defer cleanup()
	unlockAdmission()

	start := time.Now()
	outcome.Phase = "provider-generate"
	body, prompt, completion, err := s.Model.Chat(callCtx, c, key, raw, messageLimits...)
	outcome.PromptTokens = prompt
	outcome.CompletionTokens = completion
	status := "success"
	if err != nil {
		outcome.Failure = err
		outcome.Result = failedChatResult(err, configID)
		status = outcome.Result.ErrorCode
	} else {
		outcome.Result = domain.ChatCallResult{
			OK:            true,
			ResponseJSON:  body,
			StatusCode:    200,
			ModelConfigID: configID,
		}
		outcome.Phase = "usage-persist"
	}
	auditCtx, auditCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer auditCancel()
	if auditErr := s.Store.Record(auditCtx, domain.Usage{
		OwnerID:          owner,
		AgentID:          agentID,
		SessionID:        sessionID,
		RunID:            runID,
		ModelID:          c.ID,
		Status:           status,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		LatencyMS:        time.Since(start).Milliseconds(),
	}); auditErr != nil {
		outcome.Phase = "usage-persist"
		return fail(domain.ErrWithCause(503, "usage_unavailable", "Model usage could not be recorded", auditErr), configID)
	}
	return outcome
}

// StreamChat executes an NDJSON streaming model inference call with slot admission, cancellation tracking, and usage auditing.
func (s *Service) StreamChat(ctx context.Context, owner, agentID, sessionID, runID, expectedConfigID, raw string, onConfigID func(string), emit func(domain.StreamEvent) error, messageLimits ...int) StreamOutcome {
	s.ensureInit()
	unlockAdmission := s.lockModelAdmission()
	defer unlockAdmission()
	outcome := StreamOutcome{Phase: "admission"}
	var configID string
	writeEvent := func(ev domain.StreamEvent) error {
		if ev.Type == "chunk" {
			outcome.ChunkCount++
			outcome.TransportBytes += len(ev.ResponseJSON)
		}
		return emit(ev)
	}
	terminal := func(err error) StreamOutcome {
		unlockAdmission() // Response writes must not block catalog changes, including admission errors.
		outcome.Failure = err
		if err == nil {
			if wErr := writeEvent(domain.StreamEvent{Type: "done", ModelConfigID: configID}); wErr != nil {
				outcome.TerminalWriteError = wErr
			}
			return outcome
		}
		de := domain.AsError(err)
		_ = writeEvent(domain.StreamEvent{
			Type:          "error",
			ErrorCode:     de.Code,
			ErrorMessage:  de.Message,
			StatusCode:    de.Status,
			ModelConfigID: configID,
		})
		return outcome
	}
	settings, err := s.Store.Load(ctx)
	if err != nil {
		return terminal(err)
	}
	var c domain.ResolvedModel
	if StrictModelSelection(ctx) {
		exact, ok := settings.ResolveExactModel(RequestedModelID(ctx))
		if !ok {
			return terminal(domain.Err(404, "not_found", "Model profile not found"))
		}
		c = exact
	} else {
		c = settings.ResolveModel(RequestedModelID(ctx))
	}
	if !c.Configured() {
		return terminal(domain.Err(409, "model_unconfigured", "An administrator must configure the model gateway"))
	}
	if !c.Enabled {
		return terminal(domain.Err(409, "model_disabled", "The model gateway is disabled"))
	}
	configID = c.CountingConfigID()
	if configID != "" && onConfigID != nil {
		onConfigID(configID)
	}
	if expected := strings.TrimSpace(expectedConfigID); expected != "" && expected != configID {
		return terminal(domain.Err(409, "gateway_unavailable", "Model gateway configuration changed"))
	}
	release, ok := s.AcquireSlot()
	if !ok {
		return terminal(domain.Err(429, "gateway_busy", "The model gateway is busy"))
	}
	defer release()
	key, err := s.Cipher.Decrypt(c.CipherKey)
	if err != nil {
		return terminal(domain.ErrWithCause(503, "credential_unavailable", "Saved model credential could not be decrypted", err))
	}
	callCtx, cleanup := s.RegisterRunCancel(ctx, agentID, runID, c.ID)
	defer cleanup()
	unlockAdmission()

	start := time.Now()
	outcome.Phase = "provider-stream"
	prompt, completion, err := s.Model.Stream(callCtx, c, key, raw, func(chunk string) error {
		return writeEvent(domain.StreamEvent{Type: "chunk", ResponseJSON: chunk})
	}, messageLimits...)
	outcome.PromptTokens = prompt
	outcome.CompletionTokens = completion
	status := "success"
	if err != nil {
		status = domain.AsError(err).Code
	} else {
		outcome.Phase = "usage-persist"
	}
	auditCtx, auditCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer auditCancel()
	if auditErr := s.Store.Record(auditCtx, domain.Usage{
		OwnerID:          owner,
		AgentID:          agentID,
		SessionID:        sessionID,
		RunID:            runID,
		ModelID:          c.ID,
		Status:           status,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		LatencyMS:        time.Since(start).Milliseconds(),
	}); auditErr != nil {
		outcome.Phase = "usage-persist"
		return terminal(domain.ErrWithCause(503, "usage_unavailable", "Model usage could not be recorded", auditErr))
	}
	if err == nil {
		outcome.Phase = "terminal-write"
	}
	return terminal(err)
}

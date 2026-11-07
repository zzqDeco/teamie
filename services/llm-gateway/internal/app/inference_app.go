package app

import (
	"context"
	"errors"
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
	key, err := s.Cipher.Decrypt(c.CipherKey)
	if err != nil {
		return fail(domain.ErrWithCause(503, "credential_unavailable", "Saved model credential could not be decrypted", err), configID)
	}
	callCtx, cleanup := s.RegisterRunCancel(ctx, agentID, runID, c.ID, c.ProviderID)
	defer cleanup()
	unlockAdmission()

	start := time.Now()
	outcome.Phase = "provider-generate"
	providerKey := providerWorkerKey(c)
	var (
		body               string
		prompt, completion int64
	)
	for attempt := 0; ; attempt++ {
		var early *CallOutcome
		var retryBackoff time.Duration
		var retryWake <-chan struct{}
		shouldRetry := false
		done := func() bool {
			releaseWorker, wErr := s.acquireProviderWorker(callCtx, providerKey)
			if wErr != nil {
				var de *domain.Error
				if attempt == 0 && errors.As(wErr, &de) && de.Code == "gateway_busy" {
					outcome.Phase = "admission"
					res := fail(wErr, configID)
					early = &res
					return true
				}
				err = wErr
				return true
			}
			defer releaseWorker()

			dispatchedAt, cErr := s.waitProviderCooldown(callCtx, providerKey)
			if cErr != nil {
				err = cErr
				return true
			}

			releaseSlot, ok := s.AcquireSlot()
			if !ok {
				busyErr := domain.Err(429, "gateway_busy", "The model gateway is busy")
				if attempt == 0 {
					outcome.Phase = "admission"
					res := fail(busyErr, configID)
					early = &res
					return true
				}
				err = busyErr
				return true
			}
			defer releaseSlot()

			outcome.Phase = "provider-generate"
			body, prompt, completion, err = s.Model.Chat(callCtx, c, key, raw, messageLimits...)
			if err == nil {
				s.recordProviderSuccess(callCtx, providerKey, dispatchedAt)
			} else if isRetryableUpstreamRateLimit(err) {
				if backoff, wake, retry := s.recordRateLimitAndPlanRetry(callCtx, providerKey, dispatchedAt, err, attempt); retry {
					retryBackoff = backoff
					retryWake = wake
					shouldRetry = true
				}
			}
			return false
		}()
		if early != nil {
			return *early
		}
		if done || !shouldRetry {
			break
		}
		if sErr := sleepWithContextOrWake(callCtx, retryBackoff, retryWake); sErr != nil {
			err = sErr
			break
		}
	}
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
	key, err := s.Cipher.Decrypt(c.CipherKey)
	if err != nil {
		return terminal(domain.ErrWithCause(503, "credential_unavailable", "Saved model credential could not be decrypted", err))
	}
	callCtx, cleanup := s.RegisterRunCancel(ctx, agentID, runID, c.ID, c.ProviderID)
	defer cleanup()
	unlockAdmission()

	start := time.Now()
	outcome.Phase = "provider-stream"
	providerKey := providerWorkerKey(c)
	var prompt, completion int64
	for attempt := 0; ; attempt++ {
		var early *StreamOutcome
		var retryBackoff time.Duration
		var retryWake <-chan struct{}
		shouldRetry := false
		done := func() bool {
			releaseWorker, wErr := s.acquireProviderWorker(callCtx, providerKey)
			if wErr != nil {
				var de *domain.Error
				if attempt == 0 && errors.As(wErr, &de) && de.Code == "gateway_busy" {
					outcome.Phase = "admission"
					res := terminal(wErr)
					early = &res
					return true
				}
				err = wErr
				return true
			}
			defer releaseWorker()

			dispatchedAt, cErr := s.waitProviderCooldown(callCtx, providerKey)
			if cErr != nil {
				err = cErr
				return true
			}

			releaseSlot, ok := s.AcquireSlot()
			if !ok {
				busyErr := domain.Err(429, "gateway_busy", "The model gateway is busy")
				if attempt == 0 {
					outcome.Phase = "admission"
					res := terminal(busyErr)
					early = &res
					return true
				}
				err = busyErr
				return true
			}
			defer releaseSlot()

			outcome.Phase = "provider-stream"
			prompt, completion, err = s.Model.Stream(callCtx, c, key, raw, func(chunk string) error {
				return writeEvent(domain.StreamEvent{Type: "chunk", ResponseJSON: chunk})
			}, messageLimits...)
			if err == nil {
				s.recordProviderSuccess(callCtx, providerKey, dispatchedAt)
			} else if outcome.ChunkCount == 0 && isRetryableUpstreamRateLimit(err) {
				if backoff, wake, retry := s.recordRateLimitAndPlanRetry(callCtx, providerKey, dispatchedAt, err, attempt); retry {
					retryBackoff = backoff
					retryWake = wake
					shouldRetry = true
				}
			}
			return false
		}()
		if early != nil {
			return *early
		}
		if done || !shouldRetry {
			break
		}
		if sErr := sleepWithContextOrWake(callCtx, retryBackoff, retryWake); sErr != nil {
			err = sErr
			break
		}
	}
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

package engine

import (
	"encoding/json"

	"teamie/agent-runtime/internal/engine/domain"
	"teamie/agent-runtime/internal/engine/infra/eino"
)

const contextProfileLegacy = domain.ContextProfileLegacy
const contextProfileV1 = domain.ContextProfileV1

func contextProfile(run *Run) string {
	if run == nil || run.ContextProfile == "" {
		return contextProfileLegacy
	}
	return run.ContextProfile
}

func modelRetryObservation(err error) bool {
	return eino.IsWillRetryError(err)
}

func canonicalContextPolicy(profile string, raw json.RawMessage) (string, json.RawMessage, error) {
	return domain.CanonicalContextPolicy(profile, raw)
}

// InputSource is a reference, never an inferred destination or an ACL grant.
// Source bodies remain in the ordinary IM history and private Session events.
type InputSource = domain.InputSource

func inputSources(inputs []Input) ([]InputSource, bool) {
	return domain.ExtractInputSources(toDomainInputs(inputs))
}

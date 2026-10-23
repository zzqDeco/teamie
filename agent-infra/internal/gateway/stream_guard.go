package gateway

import (
	"teamie/agent-infra/internal/gateway/domain"
)

func credentialResponseError() error {
	return toAPIError(domain.CredentialResponseError())
}

type credentialStreamGuard struct {
	inner *domain.CredentialStreamGuard
}

func newCredentialStreamGuard(key string) *credentialStreamGuard {
	return &credentialStreamGuard{inner: domain.NewCredentialStreamGuard(key)}
}

func decodedCredential(value any, key string) bool {
	return domain.DecodedCredential(value, key)
}

func guardSingleResponse(raw, key string) error {
	return toAPIError(domain.GuardSingleResponse(raw, key))
}

func (g *credentialStreamGuard) walkDelta(path string, value any, owner uint64) error {
	return toAPIError(g.inner.WalkDelta(path, value, owner))
}

func (g *credentialStreamGuard) push(raw string, emit func(string) error) error {
	return toAPIError(g.inner.Push(raw, emit))
}

func (g *credentialStreamGuard) finish(emit func(string) error) error {
	return toAPIError(g.inner.Finish(emit))
}

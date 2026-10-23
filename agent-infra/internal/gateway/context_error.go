package gateway

import (
	"teamie/agent-infra/internal/gateway/domain"
)

func providerContextError(raw []byte) error {
	return toAPIError(domain.ProviderContextError(raw))
}

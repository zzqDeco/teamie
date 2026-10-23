package gateway

import (
	"errors"

	"teamie/agent-infra/internal/api"
	"teamie/agent-infra/internal/gateway/domain"
	"teamie/agent-infra/internal/gateway/infra/crypto"
)

type Cipher = domain.Cipher

func NewCipher(master string) (Cipher, error) {
	return crypto.NewCipher(master)
}

func toAPIError(err error) error {
	if err == nil {
		return nil
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		return err
	}
	var de *domain.Error
	if errors.As(err, &de) {
		return &api.Error{Status: de.Status, Code: de.Code, Message: de.Message, Cause: de.Cause}
	}
	return err
}

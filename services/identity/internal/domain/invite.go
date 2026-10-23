package domain

import (
	"crypto/subtle"
	"time"
)

// RegistrationInvite represents an administrator-issued single-use registration code.
type RegistrationInvite struct {
	ID        string     `json:"id"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt time.Time  `json:"expiresAt"`
	UsedAt    *time.Time `json:"usedAt,omitempty"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
	UsedBy    *string    `json:"usedBy,omitempty"`
}

// EvaluateBootstrapAdmission evaluates whether a registration attempt is a
// bootstrap administrator admission or requires an ordinary 64-hex-char invite.
func EvaluateBootstrapAdmission(initialized bool, code, bootstrapCode string) (role string, isBootstrap bool, err error) {
	bootstrap := subtle.ConstantTimeCompare([]byte(code), []byte(bootstrapCode)) == 1
	if !initialized {
		if !bootstrap {
			return "", false, ErrInvalidInvite()
		}
		return "admin", true, nil
	}
	if bootstrap || len(code) != 64 {
		return "", false, ErrInvalidInvite()
	}
	return "user", false, nil
}

package domain

import "time"

const (
	// HumanSessionTTL is the lifetime of a browser human session.
	HumanSessionTTL = 7 * 24 * time.Hour
	// PrincipalTokenTTL is the lifetime of a machine principal bearer token.
	PrincipalTokenTTL = 15 * time.Minute
)

// Session represents an authenticated browser or service principal session.
type Session struct {
	User             User   `json:"user"`
	CSRFToken        string `json:"csrfToken"`
	ServicePrincipal bool   `json:"-"`
}

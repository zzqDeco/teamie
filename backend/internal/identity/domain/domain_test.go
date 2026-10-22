package domain

import (
	"testing"
)

func TestDomainEmailAndDisplayNameInvariants(t *testing.T) {
	email, err := NormalizeEmail("  Alice@Example.Invalid ")
	if err != nil || email != "alice@example.invalid" {
		t.Fatalf("expected normalized email, got %q (%v)", email, err)
	}
	if _, err := NormalizeEmail("not-an-email"); err == nil {
		t.Fatal("expected invalid email error")
	}

	name, err := ValidateDisplayName("  Teamie Admin  ")
	if err != nil || name != "Teamie Admin" {
		t.Fatalf("expected trimmed name, got %q (%v)", name, err)
	}
	if _, err := ValidateDisplayName("   "); err == nil {
		t.Fatal("expected empty display name rejection")
	}
}

func TestDomainBootstrapAdmissionInvariants(t *testing.T) {
	role, bootstrap, err := EvaluateBootstrapAdmission(false, DefaultBootstrapInviteCode, DefaultBootstrapInviteCode)
	if err != nil || !bootstrap || role != "admin" {
		t.Fatalf("expected bootstrap admin admission, got role=%q bootstrap=%v err=%v", role, bootstrap, err)
	}
	if _, _, err := EvaluateBootstrapAdmission(true, DefaultBootstrapInviteCode, DefaultBootstrapInviteCode); err == nil {
		t.Fatal("bootstrap code must be rejected after initial account creation")
	}
}

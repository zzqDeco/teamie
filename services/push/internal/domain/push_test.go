package domain_test

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"teamie/services/push/internal/domain"
)

func generateTestVAPID(t *testing.T) (pub, priv string) {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
		base64.RawURLEncoding.EncodeToString(key.Bytes())
}

func TestValidateEndpointURL(t *testing.T) {
	valid := []string{
		"https://fcm.googleapis.com/fcm/send/abc123",
		"https://updates.push.services.mozilla.com/wpush/v2/gAAAAAB",
		"https://web.push.apple.com/QBb123",
	}
	for _, raw := range valid {
		norm, host, err := domain.ValidateEndpointURL(raw, true)
		if err != nil {
			t.Fatalf("expected %q valid, got error: %v", raw, err)
		}
		if norm != raw || host == "" {
			t.Fatalf("unexpected normalization (%q, %q)", norm, host)
		}
	}

	invalid := []string{
		"",
		"http://fcm.googleapis.com/fcm/send/abc",
		"https://user:pass@fcm.googleapis.com/fcm/send/abc",
		"https://fcm.googleapis.com/fcm/send/abc#frag",
		"https://localhost/push",
		"https://127.0.0.1/push",
		"https://[::1]/push",
		"https://10.0.0.1/push",
		"https://192.168.1.5/push",
		"https://169.254.169.254/latest/meta-data",
		"https://100.64.0.1/push",
		"https://chat:8082/internal",
		"https://postgres:5432/",
		"https://push:8089/",
		"https://foo.internal/push",
		"https://bar.local/push",
	}
	for _, raw := range invalid {
		if _, _, err := domain.ValidateEndpointURL(raw, true); err == nil {
			t.Fatalf("expected %q to be rejected under public HTTPS policy", raw)
		}
	}
}

func TestValidateSubscriptionKeysAndVAPID(t *testing.T) {
	pub, priv := generateTestVAPID(t)
	otherPub, _ := generateTestVAPID(t)

	authBytes := make([]byte, 16)
	_, _ = rand.Read(authBytes)
	authB64 := base64.RawURLEncoding.EncodeToString(authBytes)

	if _, err := domain.ValidateP256DH(pub); err != nil {
		t.Fatalf("valid p256dh rejected: %v", err)
	}
	if _, err := domain.ValidateP256DH("not-a-key"); err == nil {
		t.Fatal("invalid p256dh accepted")
	}
	if _, err := domain.ValidateAuthSecret(authB64); err != nil {
		t.Fatalf("valid auth rejected: %v", err)
	}
	if _, err := domain.ValidateAuthSecret(base64.RawURLEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("short auth accepted")
	}

	// Empty VAPID is allowed (service stays inactive).
	cfg, err := domain.ValidateVAPIDConfig("", "", "")
	if err != nil || cfg.Configured() {
		t.Fatalf("expected empty VAPID unconfigured without error, got cfg=%+v err=%v", cfg, err)
	}
	// Partial or mismatched VAPID fails closed.
	if _, err := domain.ValidateVAPIDConfig(pub, "", "mailto:ops@example.com"); err == nil {
		t.Fatal("partial VAPID should fail")
	}
	if _, err := domain.ValidateVAPIDConfig(otherPub, priv, "mailto:ops@example.com"); err == nil {
		t.Fatal("mismatched VAPID pair should fail")
	}
	if _, err := domain.ValidateVAPIDConfig(pub, priv, "http://insecure.example.com"); err == nil {
		t.Fatal("non-https/mailto VAPID subject should fail")
	}
	cfg, err = domain.ValidateVAPIDConfig(pub, priv, "mailto:ops@example.com")
	if err != nil || !cfg.Configured() || cfg.PublicKey != pub {
		t.Fatalf("valid VAPID rejected: cfg=%+v err=%v", cfg, err)
	}
}

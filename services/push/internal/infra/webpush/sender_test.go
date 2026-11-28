package webpush

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"teamie/services/push/internal/domain"
)

// decryptAES128GCM independently implements RFC 8291 / RFC 8188 user-agent decryption for tests.
func decryptAES128GCM(t *testing.T, body []byte, uaPriv *ecdh.PrivateKey, authSecret []byte) []byte {
	t.Helper()
	if len(body) < 86+16 {
		t.Fatalf("ciphertext too short: %d", len(body))
	}
	salt := body[0:16]
	rs := binary.BigEndian.Uint32(body[16:20])
	if rs != defaultRecordSize {
		t.Fatalf("unexpected record size %d", rs)
	}
	idLen := int(body[20])
	if idLen != 65 {
		t.Fatalf("unexpected key id length %d", idLen)
	}
	asPubBytes := body[21:86]
	ciphertext := body[86:]

	asPub, err := ecdh.P256().NewPublicKey(asPubBytes)
	if err != nil {
		t.Fatalf("parse asPub: %v", err)
	}
	ecdhSecret, err := uaPriv.ECDH(asPub)
	if err != nil {
		t.Fatalf("ecdh: %v", err)
	}

	uaPubBytes := uaPriv.PublicKey().Bytes()
	keyInfo := make([]byte, 0, len("WebPush: info")+1+65+65)
	keyInfo = append(keyInfo, "WebPush: info"...)
	keyInfo = append(keyInfo, 0x00)
	keyInfo = append(keyInfo, uaPubBytes...)
	keyInfo = append(keyInfo, asPubBytes...)

	ikm, err := hkdf.Key(sha256.New, ecdhSecret, authSecret, string(keyInfo), 32)
	if err != nil {
		t.Fatal(err)
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		t.Fatal(err)
	}

	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	padded, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatalf("aes128gcm decrypt failed: %v", err)
	}
	if len(padded) == 0 || padded[len(padded)-1] != 0x02 {
		t.Fatalf("missing RFC 8188 final delimiter 0x02")
	}
	return padded[:len(padded)-1]
}

func TestEncryptAES128GCM_RFC8291AppendixAVector(t *testing.T) {
	// Official RFC 8291 Section 5 test vector.
	uaPrivBytes, _ := base64.RawURLEncoding.DecodeString("q1dXqw3bbcSq36acEl0xL_L28_8m_V-k_k5V5l9p_sM")
	_ = uaPrivBytes
	// Exact RFC 8291 Section 5 byte values:
	uaPubB64 := "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	asPrivB64 := "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	authB64 := "BTBZMqHH6r4Tts7J_aSIgg"
	saltB64 := "DGv6ra1nlYgDCS1FRnbzlw"
	expectedB64 := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"

	asPrivBytes, err := base64.RawURLEncoding.DecodeString(asPrivB64)
	if err != nil {
		t.Fatal(err)
	}
	asPriv, err := ecdh.P256().NewPrivateKey(asPrivBytes)
	if err != nil {
		t.Fatal(err)
	}
	salt, err := base64.RawURLEncoding.DecodeString(saltB64)
	if err != nil {
		t.Fatal(err)
	}

	got, err := EncryptAES128GCM([]byte("When I grow up, I want to be a watermelon"), uaPubB64, authB64, salt, asPriv)
	if err != nil {
		t.Fatalf("encrypt RFC 8291 vector failed: %v", err)
	}
	if base64.RawURLEncoding.EncodeToString(got) != expectedB64 {
		t.Fatalf("RFC 8291 vector mismatch:\ngot:  %s\nwant: %s", base64.RawURLEncoding.EncodeToString(got), expectedB64)
	}
}

func TestSender_EndToEndEncryptionVAPIDRedirectAndGone(t *testing.T) {
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSecret := make([]byte, 16)
	_, _ = rand.Read(authSecret)

	vapidPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	vapidPubBytes := vapidPriv.PublicKey().Bytes()
	vapidCfg := domain.VAPIDConfig{
		PublicKey:  base64.RawURLEncoding.EncodeToString(vapidPubBytes),
		PrivateKey: base64.RawURLEncoding.EncodeToString(vapidPriv.Bytes()),
		Subject:    "mailto:push@teamie.example",
	}

	var capturedAuth, capturedEncoding string
	var capturedBody []byte
	statusCode := http.StatusCreated
	var redirectTo string

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if redirectTo != "" {
			http.Redirect(w, r, redirectTo, http.StatusFound)
			return
		}
		capturedAuth = r.Header.Get("Authorization")
		capturedEncoding = r.Header.Get("Content-Encoding")
		capturedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(statusCode)
	}))
	defer srv.Close()

	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	fixedNow := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	sender := &Sender{
		RequirePublicHTTPS: false,
		HTTPClient:         client,
		Now:                func() time.Time { return fixedNow },
	}
	sub := domain.PushSubscription{
		ID:           "sub-1",
		UserID:       "u1",
		EndpointHash: domain.HashString(srv.URL + "/push/1"),
		Endpoint:     srv.URL + "/push/1",
		P256DH:       base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes()),
		Auth:         base64.RawURLEncoding.EncodeToString(authSecret),
	}
	payload := domain.PushWakePayload{
		Type:           "message.wake",
		WorkspaceID:    "w1",
		ConversationID: "c1",
		MessageID:      "m1",
		URL:            "/app/w/w1/c/c1",
	}

	// 1. Successful delivery + RFC 8291 decryption + ES256 VAPID verification.
	res, err := sender.Send(context.Background(), sub, vapidCfg, payload)
	if err != nil || res.StatusCode != http.StatusCreated || res.Gone {
		t.Fatalf("unexpected delivery result: res=%+v err=%v", res, err)
	}
	if capturedEncoding != "aes128gcm" {
		t.Fatalf("expected Content-Encoding aes128gcm, got %q", capturedEncoding)
	}
	decrypted := decryptAES128GCM(t, capturedBody, uaPriv, authSecret)
	var decoded domain.PushWakePayload
	if err := json.Unmarshal(decrypted, &decoded); err != nil {
		t.Fatalf("unmarshal decrypted payload: %v", err)
	}
	if decoded != payload {
		t.Fatalf("decrypted payload mismatch: got %+v want %+v", decoded, payload)
	}

	// Verify VAPID ES256 signature and claims.
	if !strings.HasPrefix(capturedAuth, "vapid t=") {
		t.Fatalf("unexpected Authorization header: %q", capturedAuth)
	}
	parts := strings.Split(strings.TrimPrefix(capturedAuth, "vapid "), ", ")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "t=") || parts[1] != "k="+vapidCfg.PublicKey {
		t.Fatalf("malformed VAPID header: %q", capturedAuth)
	}
	jwtParts := strings.Split(strings.TrimPrefix(parts[0], "t="), ".")
	if len(jwtParts) != 3 {
		t.Fatalf("invalid VAPID JWT parts")
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(jwtParts[2])
	if err != nil || len(sigBytes) != 64 {
		t.Fatalf("invalid ES256 signature length")
	}
	rInt := new(big.Int).SetBytes(sigBytes[:32])
	sInt := new(big.Int).SetBytes(sigBytes[32:64])
	ecdsaPub := &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(vapidPubBytes[1:33]),
		Y:     new(big.Int).SetBytes(vapidPubBytes[33:65]),
	}
	digest := sha256.Sum256([]byte(jwtParts[0] + "." + jwtParts[1]))
	if !ecdsa.Verify(ecdsaPub, digest[:], rInt, sInt) {
		t.Fatal("VAPID ES256 signature verification failed")
	}
	claimsRaw, _ := base64.RawURLEncoding.DecodeString(jwtParts[1])
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Aud != srv.URL || claims.Sub != vapidCfg.Subject || claims.Exp != fixedNow.Add(12*time.Hour).Unix() {
		t.Fatalf("unexpected VAPID claims: %+v", claims)
	}

	// 2. 410 Gone marks subscription for removal.
	statusCode = http.StatusGone
	res, err = sender.Send(context.Background(), sub, vapidCfg, payload)
	if err != nil || !res.Gone || res.StatusCode != http.StatusGone {
		t.Fatalf("expected Gone on 410, got res=%+v err=%v", res, err)
	}

	// 3. Redirects are blocked and never followed.
	redirectTo = "http://127.0.0.1:8082/internal"
	res, err = sender.Send(context.Background(), sub, vapidCfg, payload)
	if err != nil || res.StatusCode != http.StatusFound || res.Reason != "redirect_blocked" {
		t.Fatalf("expected redirect_blocked, got res=%+v err=%v", res, err)
	}

	// 4. DNS-pinned SSRF dialer blocks non-public resolved IPs even for public-looking hostnames.
	ssrfSender := &Sender{
		RequirePublicHTTPS: true,
		Resolver: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
	}
	sub.Endpoint = "https://push-relay.example.com/send/abc"
	res, err = ssrfSender.Send(context.Background(), sub, vapidCfg, payload)
	if err == nil || res.Reason != "transport_error" {
		t.Fatalf("expected SSRF dial failure for private IP resolution, got res=%+v err=%v", res, err)
	}
}

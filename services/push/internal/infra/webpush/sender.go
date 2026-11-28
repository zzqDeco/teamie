// Package webpush implements RFC 8291 (aes128gcm) payload encryption,
// RFC 8292 (VAPID ES256) request signing, and DNS-pinned SSRF-safe outbound HTTP delivery.
package webpush

import (
	"bytes"
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
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"go.uber.org/zap"
	"teamie/pkg/observability"
	"teamie/services/push/internal/domain"
)

const (
	defaultRecordSize  uint32 = 4096
	defaultTTLSeconds         = 86400
	defaultVAPIDExpiry        = 12 * time.Hour
	maxRelayDrainBytes        = 4096
)

// Sender implements domain.WebPushSender with RFC 8291/8292 crypto and DNS-pinned SSRF protection.
type Sender struct {
	RequirePublicHTTPS bool
	Timeout            time.Duration
	Resolver           func(ctx context.Context, host string) ([]netip.Addr, error)
	Now                func() time.Time
	HTTPClient         *http.Client // Optional override for unit tests with custom TLS roots
}

func (s *Sender) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Sender) timeout() time.Duration {
	if s != nil && s.Timeout > 0 {
		return s.Timeout
	}
	return 8 * time.Second
}

func (s *Sender) resolveHost(ctx context.Context, host string) ([]netip.Addr, error) {
	if s != nil && s.Resolver != nil {
		return s.Resolver(ctx, host)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.Unmap())
	}
	return out, nil
}

func (s *Sender) httpClient() *http.Client {
	if s != nil && s.HTTPClient != nil {
		return s.HTTPClient
	}
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: -1,
	}
	tr := &http.Transport{
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 6 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if !s.RequirePublicHTTPS {
				return dialer.DialContext(ctx, network, addr)
			}
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := s.resolveHost(ctx, host)
			if err != nil || len(ips) == 0 {
				return nil, fmt.Errorf("failed to resolve push relay host")
			}
			for _, ip := range ips {
				if !domain.IsPublicTargetIP(ip) {
					return nil, fmt.Errorf("push relay resolved to non-public IP")
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
	}
	return &http.Client{
		Transport: tr,
		Timeout:   s.timeout(),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Send encrypts payload per RFC 8291 (aes128gcm), signs a VAPID ES256 header per RFC 8292,
// and delivers it to sub.Endpoint without following redirects or logging endpoints/secrets.
func (s *Sender) Send(ctx context.Context, sub domain.PushSubscription, vapid domain.VAPIDConfig, payload domain.PushWakePayload) (domain.DeliveryResult, error) {
	started := time.Now()
	if _, _, err := domain.ValidateEndpointURL(sub.Endpoint, s.RequirePublicHTTPS); err != nil {
		return domain.DeliveryResult{Reason: "invalid_endpoint"}, err
	}
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return domain.DeliveryResult{Reason: "encode_failed"}, err
	}
	ciphertext, err := EncryptAES128GCM(plaintext, sub.P256DH, sub.Auth, nil, nil)
	if err != nil {
		return domain.DeliveryResult{Reason: "encrypt_failed"}, err
	}
	authHeader, err := BuildVAPIDAuthorization(sub.Endpoint, vapid, s.now())
	if err != nil {
		return domain.DeliveryResult{Reason: "vapid_sign_failed"}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(ciphertext))
	if err != nil {
		return domain.DeliveryResult{Reason: "request_build_failed"}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", strconv.Itoa(defaultTTLSeconds))
	req.Header.Set("Urgency", "normal")
	req.Header.Set("Authorization", authHeader)

	resp, err := s.httpClient().Do(req)
	if err != nil {
		observability.L(ctx).Info(
			"Web push delivery failed",
			zap.String("event.action", "push-deliver"),
			zap.String("event.outcome", "failure"),
			zap.Int64("event.duration", time.Since(started).Nanoseconds()),
			zap.String("custom.push.endpoint_fp", domain.EndpointFingerprint(sub.EndpointHash)),
			zap.String("custom.push.reason", "transport_error"),
		)
		return domain.DeliveryResult{Reason: "transport_error"}, err
	}
	defer func() {
		_, _ = io.CopyN(io.Discard, resp.Body, maxRelayDrainBytes)
		_ = resp.Body.Close()
	}()

	res := domain.DeliveryResult{StatusCode: resp.StatusCode}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		res.Reason = "ok"
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		res.Gone = true
		res.Reason = "subscription_gone"
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		res.Reason = "redirect_blocked"
	case resp.StatusCode == http.StatusTooManyRequests:
		res.Reason = "rate_limited"
	default:
		res.Reason = "relay_status_" + strconv.Itoa(resp.StatusCode)
	}

	outcome := "failure"
	if res.Reason == "ok" {
		outcome = "success"
	}
	observability.L(ctx).Info(
		"Web push delivery completed",
		zap.String("event.action", "push-deliver"),
		zap.String("event.outcome", outcome),
		zap.Int64("event.duration", time.Since(started).Nanoseconds()),
		zap.String("custom.push.endpoint_fp", domain.EndpointFingerprint(sub.EndpointHash)),
		zap.Int("http.response.status_code", resp.StatusCode),
		zap.String("custom.push.reason", res.Reason),
	)
	return res, nil
}

// EncryptAES128GCM encrypts plaintext using RFC 8291 / RFC 8188 aes128gcm content encoding.
// Optional salt and ephemeralPriv overrides exist solely for deterministic RFC 8291 test vectors.
func EncryptAES128GCM(plaintext []byte, uaPublicB64, authSecretB64 string, salt []byte, ephemeralPriv *ecdh.PrivateKey) ([]byte, error) {
	uaPubBytes, err := domain.DecodeBase64URL(uaPublicB64)
	if err != nil || len(uaPubBytes) != 65 || uaPubBytes[0] != 0x04 {
		return nil, fmt.Errorf("invalid subscriber p256dh public key")
	}
	uaPubKey, err := ecdh.P256().NewPublicKey(uaPubBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid subscriber P-256 curve point: %w", err)
	}
	authSecret, err := domain.DecodeBase64URL(authSecretB64)
	if err != nil || len(authSecret) != 16 {
		return nil, fmt.Errorf("invalid subscriber auth secret")
	}

	asPriv := ephemeralPriv
	if asPriv == nil {
		asPriv, err = ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate ephemeral P-256 key: %w", err)
		}
	}
	asPubBytes := asPriv.PublicKey().Bytes()
	if len(asPubBytes) != 65 {
		return nil, fmt.Errorf("unexpected ephemeral public key length %d", len(asPubBytes))
	}

	ecdhSecret, err := asPriv.ECDH(uaPubKey)
	if err != nil {
		return nil, fmt.Errorf("ecdh key agreement failed: %w", err)
	}

	// RFC 8291 Section 3.3:
	// key_info = "WebPush: info" || 0x00 || ua_public || as_public
	keyInfo := make([]byte, 0, len("WebPush: info")+1+65+65)
	keyInfo = append(keyInfo, "WebPush: info"...)
	keyInfo = append(keyInfo, 0x00)
	keyInfo = append(keyInfo, uaPubBytes...)
	keyInfo = append(keyInfo, asPubBytes...)

	ikm, err := hkdf.Key(sha256.New, ecdhSecret, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, fmt.Errorf("derive RFC 8291 IKM: %w", err)
	}

	if len(salt) == 0 {
		salt = make([]byte, 16)
		if _, err = rand.Read(salt); err != nil {
			return nil, fmt.Errorf("generate aes128gcm salt: %w", err)
		}
	} else if len(salt) != 16 {
		return nil, fmt.Errorf("aes128gcm salt must be 16 bytes")
	}

	// RFC 8291 Section 3.4: CEK (16 bytes) and NONCE (12 bytes)
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, fmt.Errorf("derive aes128gcm CEK: %w", err)
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, fmt.Errorf("derive aes128gcm nonce: %w", err)
	}

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, fmt.Errorf("init aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("init aes-gcm: %w", err)
	}

	// RFC 8188 Section 2: final plaintext record terminates with delimiter 0x02.
	record := make([]byte, len(plaintext)+1)
	copy(record, plaintext)
	record[len(plaintext)] = 0x02

	if uint64(len(record)+gcm.Overhead()) > uint64(defaultRecordSize) {
		return nil, fmt.Errorf("push payload exceeds single aes128gcm record limit")
	}
	sealed := gcm.Seal(nil, nonce, record, nil)

	// RFC 8188 header: salt(16) || rs(4) || idlen(1=65) || keyid(65)
	out := make([]byte, 16+4+1+65+len(sealed))
	copy(out[0:16], salt)
	binary.BigEndian.PutUint32(out[16:20], defaultRecordSize)
	out[20] = 65
	copy(out[21:86], asPubBytes)
	copy(out[86:], sealed)
	return out, nil
}

// BuildVAPIDAuthorization builds an RFC 8292 Authorization header ("vapid t=<jwt>, k=<pub>").
func BuildVAPIDAuthorization(endpoint string, vapid domain.VAPIDConfig, now time.Time) (string, error) {
	aud, err := domain.CanonicalAudience(endpoint)
	if err != nil {
		return "", err
	}
	pubBytes, err := domain.DecodeBase64URL(vapid.PublicKey)
	if err != nil || len(pubBytes) != 65 || pubBytes[0] != 0x04 {
		return "", fmt.Errorf("invalid VAPID public key")
	}
	privBytes, err := domain.DecodeBase64URL(vapid.PrivateKey)
	if err != nil || len(privBytes) != 32 {
		return "", fmt.Errorf("invalid VAPID private key")
	}
	ecdsaPriv := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(pubBytes[1:33]),
			Y:     new(big.Int).SetBytes(pubBytes[33:65]),
		},
		D: new(big.Int).SetBytes(privBytes),
	}

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claimsBytes, err := json.Marshal(struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}{
		Aud: aud,
		Exp: now.UTC().Add(defaultVAPIDExpiry).Unix(),
		Sub: vapid.Subject,
	})
	if err != nil {
		return "", err
	}
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(claimsBytes)
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, ecdsaPriv, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[0:32])
	s.FillBytes(sig[32:64])
	jwtToken := unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
	return "vapid t=" + jwtToken + ", k=" + base64.RawURLEncoding.EncodeToString(pubBytes), nil
}

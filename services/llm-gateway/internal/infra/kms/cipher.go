// Package kms implements the AES-GCM credential Cipher outbound port for the Model Gateway
// on top of teamie/pkg/crypto.
package kms

import (
	pkgcrypto "teamie/pkg/crypto"
	"teamie/services/llm-gateway/internal/domain"
)

var _ domain.Cipher = Cipher{}

// Cipher encrypts and decrypts stored provider credentials using AES-GCM.
type Cipher struct {
	inner pkgcrypto.AESGCMCipher
}

// NewCipher initializes an AES-256-GCM Cipher from a 64-character hex master key.
func NewCipher(master string) (Cipher, error) {
	c, err := pkgcrypto.NewAESGCMCipher(master, "teamie:model-gateway:v1")
	if err != nil {
		return Cipher{}, err
	}
	return Cipher{inner: c}, nil
}

// Encrypt encrypts a plaintext credential string with a fresh random nonce.
func (c Cipher) Encrypt(key string) ([]byte, error) {
	return c.inner.Encrypt(key)
}

// Decrypt decrypts a nonce-prefixed ciphertext credential.
func (c Cipher) Decrypt(data []byte) (string, error) {
	return c.inner.Decrypt(data)
}

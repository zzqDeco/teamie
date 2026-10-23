// Package kms implements the AES-GCM credential Cipher outbound port for the Model Gateway.
package kms

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"

	"teamie/services/llm-gateway/internal/domain"
)

var _ domain.Cipher = Cipher{}

// Cipher encrypts and decrypts stored provider credentials using AES-GCM.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher initializes an AES-256-GCM Cipher from a 64-character hex master key.
func NewCipher(master string) (Cipher, error) {
	key, err := hex.DecodeString(master)
	if err != nil || len(key) != 32 {
		return Cipher{}, errors.New("GATEWAY_MASTER_KEY must be 64 hexadecimal characters")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return Cipher{}, err
	}
	a, err := cipher.NewGCM(block)
	return Cipher{aead: a}, err
}

// Encrypt encrypts a plaintext credential string with a fresh random nonce.
func (c Cipher) Encrypt(key string) ([]byte, error) {
	if key == "" {
		return nil, nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, []byte(key), []byte("teamie:model-gateway:v1")), nil
}

// Decrypt decrypts a nonce-prefixed ciphertext credential.
func (c Cipher) Decrypt(data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	n := c.aead.NonceSize()
	if len(data) < n {
		return "", errors.New("invalid encrypted model credential")
	}
	plain, err := c.aead.Open(nil, data[:n], data[n:], []byte("teamie:model-gateway:v1"))
	return string(plain), err
}

package gateway

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
)

type Cipher struct{ aead cipher.AEAD }

func NewCipher(master string) (Cipher, error) {
	key, e := hex.DecodeString(master)
	if e != nil || len(key) != 32 {
		return Cipher{}, errors.New("GATEWAY_MASTER_KEY must be 64 hexadecimal characters")
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return Cipher{}, e
	}
	a, e := cipher.NewGCM(block)
	return Cipher{a}, e
}
func (c Cipher) Encrypt(key string) ([]byte, error) {
	if key == "" {
		return nil, nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, e := io.ReadFull(rand.Reader, nonce); e != nil {
		return nil, e
	}
	return c.aead.Seal(nonce, nonce, []byte(key), []byte("teamie:model-gateway:v1")), nil
}
func (c Cipher) Decrypt(data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	n := c.aead.NonceSize()
	if len(data) < n {
		return "", errors.New("invalid encrypted model credential")
	}
	plain, e := c.aead.Open(nil, data[:n], data[n:], []byte("teamie:model-gateway:v1"))
	return string(plain), e
}

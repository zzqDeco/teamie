// Package crypto provides shared cryptographic primitives across Teamie services,
// including AES-256-GCM authenticated encryption, bcrypt password hashing,
// SHA-256 token hashing, constant-time secret comparison, and UUIDv4 generation.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/bcrypt"
)

const DummyBcryptHash = "$2a$12$C6UzMDM.H6dfI/f/IKcEe.yr3q8mSrfHGAvQ8N13wZdG.DRZ5V6jW"

// BcryptHasher implements password hashing and comparison using bcrypt cost 12.
type BcryptHasher struct{}

func (BcryptHasher) Hash(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (BcryptHasher) Compare(hashed, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hashed), []byte(password))
}

func (BcryptHasher) DummyHash() string {
	return DummyBcryptHash
}

// TokenGen provides cryptographic random ID, hex token, and SHA-256 digest generation.
type TokenGen struct{}

func (TokenGen) NewID() string {
	return NewID()
}

func (TokenGen) RandomHex(n int) string {
	return RandomHex(n)
}

func (TokenGen) HashToken(token string) string {
	return SHA256Hex(token)
}

// NewID generates a cryptographic random UUIDv4 string.
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// RandomHex generates n random bytes encoded as a 2n-character lowercase hex string.
func RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// SHA256Hex computes the lowercase hex-encoded SHA-256 digest of v.
func SHA256Hex(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}

// EqualSecret compares two secrets of at least 24 bytes in constant time using SHA-256.
func EqualSecret(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return len(a) >= 24 && len(b) >= 24 && subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

// ValidServiceKey compares two non-empty service tokens in constant time.
func ValidServiceKey(expected, provided string) bool {
	return expected != "" && provided != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

// AESGCMCipher encrypts and decrypts credentials using AES-256-GCM with associated data.
type AESGCMCipher struct {
	aead cipher.AEAD
	aad  []byte
}

// NewAESGCMCipher initializes an AES-256-GCM cipher from a 64-character hex master key.
func NewAESGCMCipher(masterHex string, aad string) (AESGCMCipher, error) {
	key, err := hex.DecodeString(masterHex)
	if err != nil || len(key) != 32 {
		return AESGCMCipher{}, errors.New("GATEWAY_MASTER_KEY must be 64 hexadecimal characters")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return AESGCMCipher{}, err
	}
	a, err := cipher.NewGCM(block)
	if err != nil {
		return AESGCMCipher{}, err
	}
	return AESGCMCipher{aead: a, aad: []byte(aad)}, nil
}

// Encrypt encrypts plaintext with a fresh random nonce.
func (c AESGCMCipher) Encrypt(plaintext string) ([]byte, error) {
	if plaintext == "" {
		return nil, nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, []byte(plaintext), c.aad), nil
}

// Decrypt decrypts nonce-prefixed ciphertext.
func (c AESGCMCipher) Decrypt(ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	n := c.aead.NonceSize()
	if len(ciphertext) < n {
		return "", errors.New("invalid encrypted model credential")
	}
	plain, err := c.aead.Open(nil, ciphertext[:n], ciphertext[n:], c.aad)
	return string(plain), err
}

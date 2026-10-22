// Package crypto implements cryptographic password hashing and token generation
// ports for the Identity bounded context.
package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

const dummyBcryptHash = "$2a$12$C6UzMDM.H6dfI/f/IKcEe.yr3q8mSrfHGAvQ8N13wZdG.DRZ5V6jW"

// BcryptHasher implements domain.PasswordHasher using bcrypt cost 12.
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
	return dummyBcryptHash
}

// TokenGen implements domain.TokenGenerator using crypto/rand and SHA-256.
type TokenGen struct{}

func (TokenGen) NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func (TokenGen) RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (TokenGen) HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

package domain

// ValidatePassword enforces the identity password length policy (12+ runes, <=72 bytes).
func ValidatePassword(password string) error {
	if len([]rune(password)) < 12 || len(password) > 72 {
		return ErrInvalidPassword()
	}
	return nil
}

// PasswordHasher abstracts cryptographic password hashing from the domain layer.
type PasswordHasher interface {
	Hash(password string) (string, error)
	Compare(hashed, password string) error
	DummyHash() string
}

// TokenGenerator abstracts CSPRNG identifier, token generation, and SHA-256 hashing.
type TokenGenerator interface {
	NewID() string
	RandomHex(bytes int) string
	HashToken(token string) string
}

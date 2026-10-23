// Package crypto implements cryptographic password hashing and token generation
// ports for the Identity bounded context on top of teamie/pkg/crypto.
package crypto

import (
	pkgcrypto "teamie/pkg/crypto"
)

// BcryptHasher implements domain.PasswordHasher using teamie/pkg/crypto.BcryptHasher.
type BcryptHasher = pkgcrypto.BcryptHasher

// TokenGen implements domain.TokenGenerator using teamie/pkg/crypto.TokenGen.
type TokenGen = pkgcrypto.TokenGen

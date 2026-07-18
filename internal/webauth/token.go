package webauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// tokenBytes is the entropy of every secret this package mints (invite codes,
// session tokens, OAuth state). 32 bytes ≈ 256 bits: far beyond brute-force
// reach, so the sha256-at-rest scheme needs no salt or stretching.
const tokenBytes = 32

// NewToken mints a fresh secret and its at-rest digest. plain is the value
// handed to the user (43 chars, base64url, no padding); hash is the sha256 hex
// digest that is stored — the plaintext never touches disk.
func NewToken() (plain, hash string, err error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("webauth.NewToken: %w", err)
	}
	plain = base64.RawURLEncoding.EncodeToString(buf)
	return plain, HashToken(plain), nil
}

// HashToken returns the sha256 hex digest under which a plaintext token is
// stored and looked up. Deterministic and unsalted by design: the tokens are
// full-entropy random values, not passwords.
func HashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

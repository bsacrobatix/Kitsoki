package webauth

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewToken_ShapeAndUniqueness(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	base64url := regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	for i := 0; i < 32; i++ {
		plain, hash, err := NewToken()
		require.NoError(t, err)
		assert.Regexp(t, base64url, plain)
		assert.Equal(t, HashToken(plain), hash, "returned hash must be the digest of the plaintext")
		assert.Len(t, hash, 64, "sha256 hex")
		assert.False(t, seen[plain], "tokens must not repeat")
		seen[plain] = true
	}
}

func TestHashToken_Deterministic(t *testing.T) {
	t.Parallel()
	assert.Equal(t, HashToken("abc"), HashToken("abc"))
	assert.NotEqual(t, HashToken("abc"), HashToken("abd"))
}

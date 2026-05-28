// Package ulid provides a minimal ULID (Universally Unique Lexicographically
// Sortable Identifier) generator using the existing google/uuid dependency.
//
// A ULID is a 128-bit value encoded as a 26-character Crockford Base32 string.
// For simplicity and to avoid a new module dependency, we generate ULIDs by
// combining a timestamp prefix (48 bits, ms precision) with a UUID-based
// random suffix. The result is lexicographically sortable by creation time.
package ulid

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"
)

// crockford is the Crockford Base32 alphabet (case-insensitive, no I/L/O/U).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var mu sync.Mutex

// New generates a new ULID string.
func New() string {
	mu.Lock()
	defer mu.Unlock()

	// 48-bit timestamp (milliseconds since Unix epoch).
	ms := uint64(time.Now().UnixMilli())

	// 80 bits of cryptographic randomness.
	var random [10]byte
	if _, err := rand.Read(random[:]); err != nil {
		panic(fmt.Sprintf("ulid: rand.Read: %v", err))
	}

	// Encode: 10 chars for timestamp, 16 chars for random = 26 total.
	var buf [26]byte
	encodeCrockford(buf[:10], ms, 10)
	// Combine 10 random bytes into a uint64+uint16 for encoding.
	rnd64 := binary.BigEndian.Uint64(random[:8])
	rnd16 := binary.BigEndian.Uint16(random[8:])
	// Encode 8 bytes (64 bits) → 13 chars, then 2 bytes (16 bits) → 3 chars.
	encodeCrockford(buf[10:23], rnd64, 13)
	encodeCrockford(buf[23:26], uint64(rnd16), 3)

	return string(buf[:])
}

// encodeCrockford encodes v as n Crockford Base32 characters into dst.
// dst must be exactly n bytes.
func encodeCrockford(dst []byte, v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		dst[i] = crockford[v&0x1F]
		v >>= 5
	}
}

// IsValid returns true if s is a valid 26-character ULID.
func IsValid(s string) bool {
	if len(s) != 26 {
		return false
	}
	upper := strings.ToUpper(s)
	for _, c := range upper {
		if !strings.ContainsRune(crockford, c) {
			return false
		}
	}
	return true
}

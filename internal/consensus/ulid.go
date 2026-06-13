package consensus

import (
	"crypto/rand"
	"encoding/binary"
	"strings"
	"time"
)

// crockford is the Crockford base32 alphabet (uppercase).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewRoundID generates a new consensus round ID (ULID format).
func NewRoundID() string { return newULID() }

// newULID generates a monotonic ULID (26-char Crockford base32).
// Format: 10-char timestamp + 16-char random.
func newULID() string {
	now := time.Now().UnixMilli()

	// Encode 48-bit timestamp into 10 Crockford base32 chars.
	var ts [10]byte
	ms := uint64(now)
	ts[9] = crockford[ms&0x1F]
	ms >>= 5
	ts[8] = crockford[ms&0x1F]
	ms >>= 5
	ts[7] = crockford[ms&0x1F]
	ms >>= 5
	ts[6] = crockford[ms&0x1F]
	ms >>= 5
	ts[5] = crockford[ms&0x1F]
	ms >>= 5
	ts[4] = crockford[ms&0x1F]
	ms >>= 5
	ts[3] = crockford[ms&0x1F]
	ms >>= 5
	ts[2] = crockford[ms&0x1F]
	ms >>= 5
	ts[1] = crockford[ms&0x1F]
	ms >>= 5
	ts[0] = crockford[ms&0x1F]

	// 80 bits of random for the suffix (16 base32 chars).
	var rb [10]byte
	if _, err := rand.Read(rb[:]); err != nil {
		// Fallback: use counter-based bytes (test environments without /dev/urandom).
		binary.BigEndian.PutUint64(rb[:8], uint64(time.Now().UnixNano()))
		rb[8] = 0
		rb[9] = 0
	}

	var rnd [16]byte
	// Encode 80 bits (10 bytes) into 16 base32 chars (5 bits each = 80 bits).
	bits := uint64(0)
	bitsLeft := 0
	pos := 0
	for _, b := range rb[:] {
		bits = (bits << 8) | uint64(b)
		bitsLeft += 8
		for bitsLeft >= 5 {
			bitsLeft -= 5
			rnd[pos] = crockford[(bits>>uint(bitsLeft))&0x1F]
			pos++
		}
	}
	for pos < 16 {
		rnd[pos] = '0'
		pos++
	}

	var b strings.Builder
	b.Write(ts[:])
	b.Write(rnd[:])
	return b.String()
}

// isValidULID reports whether s is a valid 26-character Crockford base32 ULID.
func isValidULID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune(crockford, c) {
			return false
		}
	}
	return true
}

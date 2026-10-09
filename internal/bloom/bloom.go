// Package bloom is a small Bloom filter of UUIDs, for sending a project's
// counted users to the edge in a few kilobytes (V4.1 §3.2). A false
// positive lets an uncounted user in; there are no false negatives.
package bloom

import (
	"crypto/sha256"
	"encoding/binary"
	"math"

	"github.com/google/uuid"
)

// header is the encoded filter's prefix: the number of hash functions.
const header = 1

// New is a filter for n items at false-positive rate p, encoded: one byte
// of hash count, then the bit array.
func New(ids []uuid.UUID, p float64) []byte {
	n := max(len(ids), 1)
	m := int(math.Ceil(-float64(n) * math.Log(p) / (math.Ln2 * math.Ln2)))
	m = max((m+7)/8*8, 64)
	k := max(int(math.Round(float64(m)/float64(n)*math.Ln2)), 1)
	k = min(k, 16)
	b := make([]byte, header+m/8)
	b[0] = byte(k)
	for _, id := range ids {
		for _, i := range positions(id, k, m) {
			b[header+i/8] |= 1 << (i % 8)
		}
	}
	return b
}

// Has reports whether id may be in the encoded filter f (false: certainly
// not). An empty or malformed filter has nothing in it.
func Has(f []byte, id uuid.UUID) bool {
	if len(f) <= header || f[0] == 0 {
		return false
	}
	k, m := int(f[0]), (len(f)-header)*8
	for _, i := range positions(id, k, m) {
		if f[header+i/8]&(1<<(i%8)) == 0 {
			return false
		}
	}
	return true
}

// positions are id's k bit positions in m bits (double hashing).
func positions(id uuid.UUID, k, m int) []int {
	h := sha256.Sum256(id[:])
	a, b := binary.BigEndian.Uint64(h[:8]), binary.BigEndian.Uint64(h[8:16])|1
	out := make([]int, k)
	for i := range k {
		out[i] = int((a + uint64(i)*b) % uint64(m))
	}
	return out
}

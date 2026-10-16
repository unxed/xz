package lzma

import (
	"bytes"
	"math/rand"
	"testing"
)

// TestLiteralCodecRoundtrip encodes literals under all combinations of
// "state < 7" (plain) and "state >= 7" (matched), with match bytes that
// agree with the literal for a random number of leading bits, and checks that
// the decoder returns them unchanged.
func TestLiteralCodecRoundtrip(t *testing.T) {
	const n = 50000
	const lc, lp = 3, 0
	rnd := rand.New(rand.NewSource(1))

	lits := make([]byte, n)
	matches := make([]byte, n)
	states := make([]uint32, n)
	litStates := make([]uint32, n)

	var enc, dec literalCodec
	enc.init(lc, lp)
	dec.init(lc, lp)

	var buf bytes.Buffer
	re, err := newRangeEncoder(&buf)
	if err != nil {
		t.Fatalf("newRangeEncoder: %v", err)
	}
	for i := 0; i < n; i++ {
		lits[i] = byte(rnd.Intn(256))
		switch rnd.Intn(4) {
		case 0: // identical to the literal
			matches[i] = lits[i]
		case 1: // differs in one random bit
			matches[i] = lits[i] ^ byte(1<<uint(rnd.Intn(8)))
		case 2: // unrelated
			matches[i] = byte(rnd.Intn(256))
		default: // inverted
			matches[i] = ^lits[i]
		}
		states[i] = uint32(rnd.Intn(12))
		litStates[i] = uint32(rnd.Intn(1 << (lc + lp)))
		if err := enc.Encode(re, lits[i], states[i], matches[i], litStates[i]); err != nil {
			t.Fatalf("Encode(%d): %v", i, err)
		}
	}
	if err := re.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rd, err := newRangeDecoder(&buf)
	if err != nil {
		t.Fatalf("newRangeDecoder: %v", err)
	}
	for i := 0; i < n; i++ {
		got := dec.Decode(rd, states[i], matches[i], litStates[i])
		if rd.err != nil {
			t.Fatalf("literal %d: range decoder error: %v", i, rd.err)
		}
		if got != lits[i] {
			t.Fatalf("literal %d (state %d, match %#02x): got %#02x, want %#02x",
				i, states[i], matches[i], got, lits[i])
		}
	}
}

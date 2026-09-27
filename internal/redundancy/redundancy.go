// Copyright 2014-2026 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package redundancy

import (
	"math"

	"github.com/unxed/xz/internal/hash"
)

// WindowLen is the length, in bytes, of the windows that DuplicateRatio
// hashes when looking for repeated content. It matches the word length
// HashTable4 uses for its own rolling hash, so the signal reflects the
// same granularity of match the real LZMA match finder would see.
const WindowLen = 8

// TableBits controls the size of the table DuplicateRatio uses to spot
// repeated windows: the table has 1<<TableBits entries, one 64-bit hash
// value each. A larger table remembers more distinct earlier windows
// before an unrelated window evicts them, which lowers the chance of
// missing a genuine duplicate, at the cost of more memory; the value
// below is a fixed, modest default (64 KiB) chosen for cheapness rather
// than accuracy, see the package doc comment.
const TableBits = 13

const (
	tableSize = 1 << TableBits
	tableMask = tableSize - 1
)

// Metrics holds the independent signals Analyze computes for a block of
// data. See the package doc comment for what each one is good and bad
// at detecting.
type Metrics struct {
	// Entropy is the order-0 Shannon entropy of the block, in bits per
	// byte (range [0, 8]). Low values indicate a skewed byte
	// distribution (e.g. text, sparse binary data); values close to 8
	// are typical of random or already-compressed/encrypted data.
	Entropy float64

	// DuplicateRatio is the fraction, in [0, 1], of WindowLen-byte
	// windows in the block whose 64-bit rolling hash exactly matches a
	// window hashed earlier in the same block. It is a proxy for how
	// much repeated content a block contains. Two genuinely different
	// windows producing the same 64-bit hash by chance is negligible
	// at realistic block sizes, so this signal does not report
	// spurious duplicates; instead, its known limitation is the
	// opposite one -- for blocks with many more distinct windows than
	// TableBits provides table slots for, an earlier window's hash can
	// be evicted from the table before a genuine repeat of it occurs,
	// causing that duplicate to be missed (a false negative, biasing
	// the ratio down for large, only moderately redundant blocks).
	DuplicateRatio float64
}

// Analyze computes Metrics for data with a single, cheap pass over the
// bytes for each signal. It performs no compression and allocates only
// the fixed-size table DuplicateRatio needs.
func Analyze(data []byte) Metrics {
	return Metrics{
		Entropy:        entropy(data),
		DuplicateRatio: duplicateRatio(data),
	}
}

// EstimateRedundancy combines Entropy and DuplicateRatio into a single
// score in [0, 1] estimating how compressible data is likely to be for
// an LZ-style compressor: values near 0 mean the block looks close to
// incompressible, values near 1 mean the block should compress well.
//
// The combination is the simplest one that respects both signals: it
// takes whichever of the two indicates more redundancy, so that a block
// scores as redundant if either the byte distribution is skewed or
// repeated windows are common, and only scores as non-redundant when
// neither signal fires. The weighting has not been validated against
// real data; callers with more specific needs should use Analyze
// directly instead.
func EstimateRedundancy(data []byte) float64 {
	m := Analyze(data)

	entropyRedundancy := 1 - m.Entropy/8
	if entropyRedundancy < 0 {
		// Guard against floating-point overshoot; Entropy cannot
		// exceed 8 mathematically but rounding could nudge it past.
		entropyRedundancy = 0
	}

	score := entropyRedundancy
	if m.DuplicateRatio > score {
		score = m.DuplicateRatio
	}
	return score
}

// entropy returns the order-0 Shannon entropy of data in bits per byte.
func entropy(data []byte) float64 {
	if len(data) == 0 {
		return 0
	}

	var counts [256]int
	for _, b := range data {
		counts[b]++
	}

	n := float64(len(data))
	var h float64
	for _, c := range counts {
		if c == 0 {
			continue
		}
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}

// duplicateRatio returns the fraction of WindowLen-byte windows in data
// whose 64-bit rolling hash exactly matches an earlier window's hash,
// using a fixed-size table that records the most recently seen hash
// value for each of 1<<TableBits buckets (bucket index = low TableBits
// bits of the hash). See the DuplicateRatio field doc for the false-
// negative behavior this table size trades away.
//
// A hash value of exactly 0 is treated as "bucket not yet written"; a
// genuine window that happens to hash to 0 will therefore never be
// counted as a duplicate and will silently claim its bucket. This is an
// accepted, negligible approximation for a cheap heuristic.
func duplicateRatio(data []byte) float64 {
	if len(data) < WindowLen {
		return 0
	}

	table := make([]uint64, tableSize)
	roller := hash.NewCyclicPoly(WindowLen)

	var windows, dup int
	for i, b := range data {
		h := roller.RollByte(b)
		if i+1 < WindowLen {
			// The rolling hash has not seen a full window yet.
			continue
		}
		windows++
		idx := h & tableMask
		if h != 0 && table[idx] == h {
			dup++
		} else {
			table[idx] = h
		}
	}
	if windows == 0 {
		return 0
	}
	return float64(dup) / float64(windows)
}

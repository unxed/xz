// Copyright 2014-2026 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package redundancy

import (
	"bytes"
	"crypto/rand"
	"math"
	"testing"
)

func TestAnalyze_Empty(t *testing.T) {
	m := Analyze(nil)
	if m.Entropy != 0 {
		t.Errorf("Entropy of empty data = %v, want 0", m.Entropy)
	}
	if m.DuplicateRatio != 0 {
		t.Errorf("DuplicateRatio of empty data = %v, want 0", m.DuplicateRatio)
	}
	if got := EstimateRedundancy(nil); got != 0 {
		t.Errorf("EstimateRedundancy(nil) = %v, want 0", got)
	}
}

func TestEntropy_ConstantByte(t *testing.T) {
	data := bytes.Repeat([]byte{0x42}, 4096)
	m := Analyze(data)
	if m.Entropy != 0 {
		t.Errorf("Entropy of a constant-byte block = %v, want 0", m.Entropy)
	}
}

func TestEntropy_UniformDistribution(t *testing.T) {
	// 256 distinct byte values, each repeated the same number of
	// times, has the maximum possible order-0 entropy: log2(256) = 8.
	data := make([]byte, 0, 256*64)
	for i := 0; i < 64; i++ {
		for b := 0; b < 256; b++ {
			data = append(data, byte(b))
		}
	}
	m := Analyze(data)
	if math.Abs(m.Entropy-8) > 1e-9 {
		t.Errorf("Entropy of a uniform byte distribution = %v, want 8", m.Entropy)
	}
}

func TestEntropy_Ordering(t *testing.T) {
	// Text-like, skewed data must score below high-entropy random
	// data; this is the ordering EstimateRedundancy relies on.
	text := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog. "), 200)

	random := make([]byte, len(text))
	if _, err := rand.Read(random); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	textEntropy := Analyze(text).Entropy
	randomEntropy := Analyze(random).Entropy

	if !(textEntropy < randomEntropy) {
		t.Errorf("text entropy = %v, random entropy = %v; want text < random",
			textEntropy, randomEntropy)
	}
	if randomEntropy < 7.9 {
		t.Errorf("random entropy = %v, want close to 8", randomEntropy)
	}
}

func TestDuplicateRatio_ShorterThanWindow(t *testing.T) {
	data := bytes.Repeat([]byte{0x01}, WindowLen-1)
	if got := duplicateRatio(data); got != 0 {
		t.Errorf("DuplicateRatio of data shorter than WindowLen = %v, want 0", got)
	}
}

func TestDuplicateRatio_RepeatedBlock(t *testing.T) {
	// A block made of the same 37-byte chunk repeated many times is
	// exactly the case order-0 entropy handles badly but that an LZ
	// match finder -- and this metric -- should recognize as highly
	// redundant.
	chunk := []byte("0123456789abcdefghijklmnopqrstuvwxy") // 36 bytes
	chunk = append(chunk, 'Z')                             // 37 bytes, not a divisor of WindowLen
	data := bytes.Repeat(chunk, 200)

	ratio := duplicateRatio(data)
	if ratio < 0.9 {
		t.Errorf("DuplicateRatio of a repeated block = %v, want >= 0.9", ratio)
	}

	// Confirm this is exactly the failure mode entropy alone misses:
	// this repeated-chunk data still has a fairly high byte-frequency
	// entropy despite being trivially compressible.
	e := entropy(data)
	if e < 4 {
		t.Errorf("entropy of the repeated block = %v, want it to stay well above 0 "+
			"(otherwise this test no longer demonstrates entropy's blind spot)", e)
	}
}

func TestDuplicateRatio_RandomData(t *testing.T) {
	data := make([]byte, 4096)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	ratio := duplicateRatio(data)
	if ratio > 0.01 {
		t.Errorf("DuplicateRatio of random data = %v, want close to 0", ratio)
	}
}

func TestEstimateRedundancy_Ordering(t *testing.T) {
	random := make([]byte, 4096)
	if _, err := rand.Read(random); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	repeated := bytes.Repeat([]byte("redundant-chunk-"), 256)
	constant := bytes.Repeat([]byte{0}, 4096)

	scoreRandom := EstimateRedundancy(random)
	scoreRepeated := EstimateRedundancy(repeated)
	scoreConstant := EstimateRedundancy(constant)

	if !(scoreRandom < scoreRepeated) {
		t.Errorf("EstimateRedundancy(random) = %v, EstimateRedundancy(repeated) = %v; "+
			"want random < repeated", scoreRandom, scoreRepeated)
	}
	if !(scoreRandom < scoreConstant) {
		t.Errorf("EstimateRedundancy(random) = %v, EstimateRedundancy(constant) = %v; "+
			"want random < constant", scoreRandom, scoreConstant)
	}
	for name, score := range map[string]float64{
		"random": scoreRandom, "repeated": scoreRepeated, "constant": scoreConstant,
	} {
		if score < 0 || score > 1 {
			t.Errorf("EstimateRedundancy(%s) = %v, want value in [0, 1]", name, score)
		}
	}
}

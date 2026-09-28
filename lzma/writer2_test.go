// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"bytes"
	"io"
	"math/rand"
	"strings"
	"testing"

	"github.com/unxed/xz/internal/randtxt"
)

func TestWriter2(t *testing.T) {
	var buf bytes.Buffer
	w, err := Writer2Config{DictCap: 4096}.NewWriter2(&buf)
	if err != nil {
		t.Fatalf("NewWriter error %s", err)
	}
	n, err := w.Write([]byte{'a'})
	if err != nil {
		t.Fatalf("w.Write([]byte{'a'}) error %s", err)
	}
	if n != 1 {
		t.Fatalf("w.Write([]byte{'a'}) returned %d; want %d", n, 1)
	}
	if err = w.Flush(); err != nil {
		t.Fatalf("w.Flush() error %s", err)
	}
	// check that double Flush doesn't write another chunk
	if err = w.Flush(); err != nil {
		t.Fatalf("w.Flush() error %s", err)
	}
	if err = w.Close(); err != nil {
		t.Fatalf("w.Close() error %s", err)
	}
	p := buf.Bytes()
	want := []byte{1, 0, 0, 'a', 0}
	if !bytes.Equal(p, want) {
		t.Fatalf("bytes written %#v; want %#v", p, want)
	}
}

func TestCycle1(t *testing.T) {
	var buf bytes.Buffer
	w, err := Writer2Config{DictCap: 4096}.NewWriter2(&buf)
	if err != nil {
		t.Fatalf("NewWriter error %s", err)
	}
	n, err := w.Write([]byte{'a'})
	if err != nil {
		t.Fatalf("w.Write([]byte{'a'}) error %s", err)
	}
	if n != 1 {
		t.Fatalf("w.Write([]byte{'a'}) returned %d; want %d", n, 1)
	}
	if err = w.Close(); err != nil {
		t.Fatalf("w.Close() error %s", err)
	}
	r, err := Reader2Config{DictCap: 4096}.NewReader2(&buf)
	if err != nil {
		t.Fatalf("NewReader error %s", err)
	}
	p := make([]byte, 3)
	n, err = r.Read(p)
	t.Logf("n %d error %v", n, err)
}

func TestCycle2(t *testing.T) {
	buf := new(bytes.Buffer)
	w, err := Writer2Config{DictCap: 4096}.NewWriter2(buf)
	if err != nil {
		t.Fatalf("NewWriter error %s", err)
	}
	// const txtlen = 1024
	const txtlen = 2100000
	io.CopyN(buf, randtxt.NewReader(rand.NewSource(42)), txtlen)
	txt := buf.String()
	buf.Reset()
	n, err := io.Copy(w, strings.NewReader(txt))
	if err != nil {
		t.Fatalf("Compressing copy error %s", err)
	}
	if n != txtlen {
		t.Fatalf("Compressing data length %d; want %d", n, txtlen)
	}
	if err = w.Close(); err != nil {
		t.Fatalf("w.Close error %s", err)
	}
	t.Logf("buf.Len() %d", buf.Len())
	r, err := Reader2Config{DictCap: 4096}.NewReader2(buf)
	if err != nil {
		t.Fatalf("NewReader error %s", err)
	}
	out := new(bytes.Buffer)
	n, err = io.Copy(out, r)
	if err != nil {
		t.Fatalf("Decompressing copy error %s after %d bytes", err, n)
	}
	if n != txtlen {
		t.Fatalf("Decompression data length %d; want %d", n, txtlen)
	}
	if txt != out.String() {
		t.Fatal("decompressed data differs from original")
	}
}

func TestWriter2_ParallelCorrectness(t *testing.T) {
	// Генерируем 10 МБ реалистичных текстовых данных
	const size = 10 * 1024 * 1024
	var srcBuf bytes.Buffer
	io.CopyN(&srcBuf, randtxt.NewReader(rand.NewSource(42)), size)
	originalData := srcBuf.Bytes()

	// Хелпер сжатия
	compress := func(concurrency int) ([]byte, error) {
		var out bytes.Buffer
		w, err := Writer2Config{
			DictCap:     1024 * 1024, // Свап-словарь 1 МБ
			Concurrency: concurrency,
		}.NewWriter2(&out)
		if err != nil {
			return nil, err
		}
		_, err = w.Write(originalData)
		if err != nil {
			w.Close()
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}

	// Хелпер распаковки
	decompress := func(compressed []byte) ([]byte, error) {
		r, err := Reader2Config{DictCap: 1024 * 1024}.NewReader2(bytes.NewReader(compressed))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(r)
	}

	// 1. Тест классического однопоточного кодирования
	seqCompressed, err := compress(1)
	if err != nil {
		t.Fatalf("sequential compression failed: %v", err)
	}
	seqDecompressed, err := decompress(seqCompressed)
	if err != nil {
		t.Fatalf("sequential decompression failed: %v", err)
	}
	if !bytes.Equal(originalData, seqDecompressed) {
		t.Error("sequential decompressed data mismatch")
	}

	// 2. Тест нового параллельного кодирования
	parCompressed, err := compress(4)
	if err != nil {
		t.Fatalf("parallel compression failed: %v", err)
	}
	parDecompressed, err := decompress(parCompressed)
	if err != nil {
		t.Fatalf("parallel decompression failed: %v", err)
	}
	if !bytes.Equal(originalData, parDecompressed) {
		t.Error("parallel decompressed data mismatch")
	}

	t.Logf("Sequential compressed size: %.2f MB", float64(len(seqCompressed))/(1024*1024))
	t.Logf("Parallel compressed size:   %.2f MB", float64(len(parCompressed))/(1024*1024))
}
func BenchmarkParallelLZMA2(b *testing.B) {
	const size = 10 * 1024 * 1024
	var srcBuf bytes.Buffer
	io.CopyN(&srcBuf, randtxt.NewReader(rand.NewSource(42)), size)
	originalData := srcBuf.Bytes()

	var out bytes.Buffer
	w, _ := Writer2Config{
		DictCap:     1024 * 1024,
		Concurrency: 4,
	}.NewWriter2(&out)
	w.Write(originalData)
	w.Close()
	compressed := out.Bytes()

	b.Run("DecompressParallel", func(b *testing.B) {
		b.SetBytes(int64(len(originalData)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			r, err := Reader2Config{DictCap: 1024 * 1024}.NewReader2(bytes.NewReader(compressed))
			if err != nil {
				b.Fatal(err)
			}
			io.Copy(io.Discard, r)
			r.Close()
		}
	})
}
func TestWriter2_EntropyDetection(t *testing.T) {
	// Generate 5MB of purely random data
	randData := make([]byte, 5*1024*1024)
	rnd := rand.New(rand.NewSource(42))
	rnd.Read(randData)

	var buf bytes.Buffer
	w, err := Writer2Config{
		DictCap:     1024 * 1024,
		Concurrency: 2,
	}.NewWriter2(&buf)
	if err != nil {
		t.Fatal(err)
	}

	n, err := w.Write(randData)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(randData) {
		t.Fatalf("wrote %d; want %d", n, len(randData))
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Verify size: since it is uncompressible, it should be slightly larger than 5MB due to LZMA2 headers.
	compressedSize := buf.Len()
	if compressedSize < len(randData) {
		t.Errorf("expected uncompressed chunks to be >= original size, got %d", compressedSize)
	}

	// Decompress and verify
	r, err := Reader2Config{DictCap: 1024 * 1024}.NewReader2(&buf)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	decompressed, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(randData, decompressed) {
		t.Fatal("decompressed random data mismatch")
	}
}

func TestWriter2_StateTransitions(t *testing.T) {
	// Block size is dictated by DictCap. Let's force DictCap = 1MB.
	// Block 1 (1MB): Pure random. Should be detected as uncompressible.
	// Block 2 (1MB): Pure zeros. Should be detected as compressible.
	// Block 3 (1MB): Pure random. Should be detected as uncompressible.
	blockSize := 1024 * 1024

	block1 := make([]byte, blockSize)
	rnd := rand.New(rand.NewSource(123))
	rnd.Read(block1)

	block2 := make([]byte, blockSize) // all zeros

	block3 := make([]byte, blockSize)
	rnd.Read(block3)

	payload := append(append(block1, block2...), block3...)

	var buf bytes.Buffer
	w, err := Writer2Config{
		DictCap:     blockSize,
		Concurrency: 2,
	}.NewWriter2(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Total uncompressed = 3MB.
	// Block 1: uncompressed (~1MB)
	// Block 2: highly compressed (should be under 50KB)
	// Block 3: uncompressed (~1MB)
	// Total compressed size should be around ~2MB.
	compressedSize := buf.Len()
	maxExpected := 2*1024*1024 + 100*1024 // 2.1MB
	if compressedSize > maxExpected {
		t.Errorf("compression transition failed: expected size < %d, got %d", maxExpected, compressedSize)
	}

	// Check correctness
	r, err := Reader2Config{DictCap: blockSize}.NewReader2(&buf)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	decompressed, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(payload, decompressed) {
		t.Fatal("decompressed data mismatch")
	}
}

// TestSelectAdaptiveMatcher checks the threshold behavior of the pure
// score-to-algorithm mapping adaptiveMatcher relies on.
func TestSelectAdaptiveMatcher(t *testing.T) {
	cases := []struct {
		score float64
		want  MatchAlgorithm
	}{
		{0, HashTable4},
		{adaptiveEffortThreshold - 0.01, HashTable4},
		{adaptiveEffortThreshold, BinaryTree},
		{adaptiveEffortThreshold + 0.01, BinaryTree},
		{1, BinaryTree},
	}
	for _, c := range cases {
		if got := selectAdaptiveMatcher(c.score); got != c.want {
			t.Errorf("selectAdaptiveMatcher(%v) = %v; want %v", c.score, got, c.want)
		}
	}
}

// TestAdaptiveMatcher_DisabledKeepsConfiguredMatcher makes sure
// Writer2Config.AdaptiveEffort defaults to off and, while off, never
// overrides Matcher regardless of what the block looks like -- the whole
// point of the option being opt-in.
func TestAdaptiveMatcher_DisabledKeepsConfiguredMatcher(t *testing.T) {
	redundant := bytes.Repeat([]byte("redundant-chunk-data-block-"), 3000)
	random := make([]byte, len(redundant))
	rand.New(rand.NewSource(7)).Read(random)

	for _, matcher := range []MatchAlgorithm{HashTable4, BinaryTree} {
		cfg := Writer2Config{Matcher: matcher} // AdaptiveEffort left at its zero value (false)
		for _, data := range [][]byte{redundant, random, nil} {
			if got := adaptiveMatcher(cfg, data); got != matcher {
				t.Errorf("adaptiveMatcher with AdaptiveEffort=false = %v; want configured Matcher %v unchanged", got, matcher)
			}
		}
	}
}

// TestAdaptiveMatcher_RedundantVsRandom demonstrates the actual point of
// part 2 of unxed/zipper#20: with AdaptiveEffort on, adaptiveMatcher (the
// function worker calls for every real, non-minimal block) picks a
// different, real MatchAlgorithm for a highly redundant block than for a
// near-random one, driven by internal/redundancy.EstimateRedundancy.
func TestAdaptiveMatcher_RedundantVsRandom(t *testing.T) {
	// A block built from the same chunk repeated many times: near-random
	// byte frequencies would not flag it, but DuplicateRatio does -- the
	// same case internal/redundancy's own tests use to demonstrate its
	// blind spot for pure entropy (see internal/redundancy's
	// TestDuplicateRatio_RepeatedBlock).
	redundant := bytes.Repeat([]byte("redundant-chunk-data-block-"), 3000)

	// A uniformly random block of the same size: both signals should read
	// low (entropy close to 8 bits/byte, negligible duplicate windows).
	random := make([]byte, len(redundant))
	rand.New(rand.NewSource(7)).Read(random)

	cfg := Writer2Config{AdaptiveEffort: true}

	if got := adaptiveMatcher(cfg, redundant); got != BinaryTree {
		t.Errorf("adaptiveMatcher on a highly redundant block = %v; want BinaryTree (raised effort)", got)
	}
	if got := adaptiveMatcher(cfg, random); got != HashTable4 {
		t.Errorf("adaptiveMatcher on a near-random block = %v; want HashTable4 (lowered effort)", got)
	}
}

// TestWriter2_AdaptiveEffortRoundtrip is the integration-level safety net
// for the wiring in worker(): with AdaptiveEffort on and Concurrency
// pinned to 1, a single worker processes several blocks whose adaptive
// choice alternates between the default primary matcher (HashTable4) and
// the lazily built, cached alternate (BinaryTree) for a run of highly
// redundant blocks -- exercising both the lazy build and its reuse from
// the cache -- and the stream still has to decompress back to the exact
// input.
func TestWriter2_AdaptiveEffortRoundtrip(t *testing.T) {
	blockSize := 1024 * 1024

	redundantBlock := bytes.Repeat([]byte("redundant-chunk-data-block-"), blockSize/27+1)[:blockSize]

	randomBlock := func(seed int64) []byte {
		b := make([]byte, blockSize)
		rand.New(rand.NewSource(seed)).Read(b)
		return b
	}

	var payload []byte
	payload = append(payload, redundantBlock...)
	payload = append(payload, randomBlock(1)...)
	payload = append(payload, redundantBlock...)
	payload = append(payload, randomBlock(2)...)

	var buf bytes.Buffer
	w, err := Writer2Config{
		DictCap:        blockSize,
		Concurrency:    1,
		AdaptiveEffort: true,
	}.NewWriter2(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	if buf.Len() >= len(payload) {
		t.Errorf("compressed size %d not smaller than input %d; the redundant blocks should still compress well under AdaptiveEffort", buf.Len(), len(payload))
	}

	r, err := Reader2Config{DictCap: blockSize}.NewReader2(&buf)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	decompressed, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(payload, decompressed) {
		t.Fatal("decompressed data mismatch with AdaptiveEffort enabled")
	}
}

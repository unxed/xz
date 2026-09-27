// Copyright 2014-2026 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"bytes"
	"io"
	"math/rand"
	"testing"

	"github.com/unxed/xz/internal/randtxt"
)

// This file is part 3/3 of unxed/zipper#20 ("VBR heuristics for LZMA"): it
// measures, rather than assumes, whether Writer2Config.AdaptiveEffort (part
// 2, see adaptiveMatcher/selectAdaptiveMatcher in writer2.go) is actually
// worth its cost. Run with:
//
//	go test -run '^$' -bench AdaptiveEffort -benchmem ./lzma/...
//
// CI (.github/workflows/go-test-platforms.yml) runs exactly that on every
// push, so the numbers below show up in the Actions log rather than only
// on whoever's machine happens to run `go test -bench` by hand.
//
// Each Benchmark*_{Redundant,Random,Text} compares AdaptiveEffort false
// (the pre-existing behavior: Writer2Config{}'s zero-value Matcher is
// HashTable4, used unconditionally) against true (per-block selection via
// EstimateRedundancy) on one synthetic, deterministic dataset, reporting
// both the standard testing.B ns/op and MB/s (SetBytes) for part (b) of the
// measurement asked for, and, as a custom metric, the resulting
// compressed/original ratio for part (a).

const (
	// adaptiveEffortBenchDictCap sets Writer2's parallel block size to its
	// floor (1 MiB, see NewWriter2's blockSize clamp), so an 8 MiB input
	// below is split into several parallel blocks -- the granularity
	// AdaptiveEffort actually decides at -- rather than compressed as one.
	adaptiveEffortBenchDictCap     = 1 << 20
	adaptiveEffortBenchConcurrency = 2
	adaptiveEffortBenchDataSize    = 8 * 1024 * 1024
)

// redundantBenchData returns highly redundant data: a single 64 KiB block
// of pseudo-random bytes, tiled until it reaches size. Locally that block
// looks like noise (high order-0 Entropy), but the repetition drives
// DuplicateRatio close to 1, so this dataset specifically exercises the
// duplicate-window signal EstimateRedundancy combines with Entropy -- the
// same entropy blind spot part 1's (internal/redundancy) own tests already
// called out -- rather than Entropy alone. It also compresses well enough
// that Writer2's pre-existing fastEstimateCompressibility fast path does
// not treat it as incompressible, so it does reach adaptiveMatcher.
func redundantBenchData(size int) []byte {
	const chunkSize = 64 * 1024
	chunk := make([]byte, chunkSize)
	rand.New(rand.NewSource(1)).Read(chunk)

	data := make([]byte, size)
	for n := 0; n < size; n += chunkSize {
		m := chunkSize
		if n+m > size {
			m = size - n
		}
		copy(data[n:n+m], chunk[:m])
	}
	return data
}

// randomBenchData returns size bytes of uniformly random data, standing in
// for already-compressed or encrypted input. At the block sizes used here
// it is expected to trip Writer2's existing fastEstimateCompressibility
// fast path (stored as raw LZMA2 chunks, no matcher involved at all) for
// both AdaptiveEffort settings equally; see the doc comment on
// BenchmarkAdaptiveEffort_Random for what that is expected to mean for the
// comparison.
func randomBenchData(size int) []byte {
	data := make([]byte, size)
	rand.New(rand.NewSource(2)).Read(data)
	return data
}

// textBenchData returns size bytes of pseudo-English text, using the same
// generator TestCycle2 and TestWriter2_ParallelCorrectness already use
// elsewhere in this package, standing in for ordinary text.
func textBenchData(size int) []byte {
	var buf bytes.Buffer
	io.CopyN(&buf, randtxt.NewReader(rand.NewSource(3)), int64(size))
	return buf.Bytes()
}

// compressAdaptive compresses data once with Writer2Config.AdaptiveEffort
// set as given and returns the compressed size.
func compressAdaptive(b *testing.B, data []byte, adaptive bool) int {
	b.Helper()
	var out bytes.Buffer
	w, err := Writer2Config{
		DictCap:        adaptiveEffortBenchDictCap,
		Concurrency:    adaptiveEffortBenchConcurrency,
		AdaptiveEffort: adaptive,
	}.NewWriter2(&out)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		b.Fatal(err)
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	return out.Len()
}

// runAdaptiveEffortBenchmark times b.N Writer2 compressions of data with
// AdaptiveEffort set as given (part (b): ns/op and, via SetBytes, MB/s),
// then reports the resulting ratio as a custom metric (part (a)) -- data is
// fixed, so every iteration compresses to the same size, and reporting it
// once after the timed loop is equivalent to reporting it every iteration.
func runAdaptiveEffortBenchmark(b *testing.B, data []byte, adaptive bool) {
	b.Helper()
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()

	var compressedLen int
	for i := 0; i < b.N; i++ {
		compressedLen = compressAdaptive(b, data, adaptive)
	}

	b.ReportMetric(float64(compressedLen)/float64(len(data)), "ratio")
	b.ReportMetric(float64(compressedLen), "compressed-bytes")
}

func benchmarkAdaptiveEffortDataset(b *testing.B, data []byte) {
	b.Run("Off", func(b *testing.B) { runAdaptiveEffortBenchmark(b, data, false) })
	b.Run("On", func(b *testing.B) { runAdaptiveEffortBenchmark(b, data, true) })
}

// BenchmarkAdaptiveEffort_Redundant compares AdaptiveEffort on highly
// redundant, repeated-block data: the case AdaptiveEffort was meant to
// help, by promoting such blocks from the default HashTable4 to BinaryTree
// (deeper search, better ratio, more CPU per block). That promotion is
// currently disabled (see selectAdaptiveMatcher's doc comment in
// writer2.go -- routing redundant blocks to BinaryTree hangs for minutes
// on bintree.go's unbounded insertion depth), so On and Off are expected
// to be statistically indistinguishable here for now: same matcher, same
// ratio, same speed.
func BenchmarkAdaptiveEffort_Redundant(b *testing.B) {
	benchmarkAdaptiveEffortDataset(b, redundantBenchData(adaptiveEffortBenchDataSize))
}

// BenchmarkAdaptiveEffort_Random compares AdaptiveEffort on near-random,
// already-incompressible data. At this size and DictCap, Writer2's
// existing fastEstimateCompressibility fast path (added before
// AdaptiveEffort, unrelated to it) is expected to classify every block as
// minimal/incompressible and store it raw, bypassing adaptiveMatcher (and
// the EstimateRedundancy call it would otherwise make) entirely for both
// settings -- so this case is expected to show no measurable difference
// between Off and On, not because AdaptiveEffort is cheap here, but because
// it never runs here. See the ticket comment this benchmark's numbers feed
// for whether that held.
func BenchmarkAdaptiveEffort_Random(b *testing.B) {
	benchmarkAdaptiveEffortDataset(b, randomBenchData(adaptiveEffortBenchDataSize))
}

// BenchmarkAdaptiveEffort_Text compares AdaptiveEffort on ordinary text,
// the common case between the two extremes above.
func BenchmarkAdaptiveEffort_Text(b *testing.B) {
	benchmarkAdaptiveEffortDataset(b, textBenchData(adaptiveEffortBenchDataSize))
}

// Copyright 2014-2026 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"bytes"
	"testing"
)

func TestFindBestMatch(t *testing.T) {
	data := []byte("hello world, this is a test string to match!XXX")
	dict := make([]byte, 1024)
	// Fill with zeros to prevent accidental ASCII matches
	for i := range dict {
		dict[i] = 0
	}
	copy(dict[100:], "hello world, this is a test")
	copy(dict[500:], "hello world, this is a test string to match!")

	rear := 600
	dists := []int{rear - 100, rear - 500}

	bestDist, bestLen := findBestMatch(dict, rear, data, dists, 0)
	if bestDist != rear-500 || bestLen != 44 {
		t.Errorf("got dist=%d len=%d, want dist=%d len=44", bestDist, bestLen, rear-500)
	}
}

func BenchmarkFindBestMatch(b *testing.B) {
	data := bytes.Repeat([]byte("A"), 273)
	dict := bytes.Repeat([]byte("A"), 1024*1024)
	dict[1024*1024-1] = 'B'

	rear := 1000000
	dists := []int{10, 100, 1000, 10000, 100000} // candidates

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		findBestMatch(dict, rear, data, dists, 0)
	}
}
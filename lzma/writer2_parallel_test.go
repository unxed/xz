// Copyright 2014-2026 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"bytes"
	"io"
	"math/rand"
	"runtime"
	"testing"
	"time"

	"github.com/ulikunitz/xz/internal/randtxt"
)

func randomText(t testing.TB, seed int64, n int64) []byte {
	var buf bytes.Buffer
	if _, err := io.CopyN(&buf, randtxt.NewReader(rand.NewSource(seed)), n); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func randomBytes(seed int64, n int) []byte {
	p := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(p)
	return p
}

// compressParallel compresses the parts, flushing after each one.
func compressParallel(t *testing.T, c Writer2Config, parts ...[]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := c.NewWriter2(&buf)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range parts {
		// write in pieces that don't align with the blocks
		for len(p) > 0 {
			k := 100000
			if k > len(p) {
				k = len(p)
			}
			if _, err = w.Write(p[:k]); err != nil {
				t.Fatal(err)
			}
			p = p[k:]
		}
		if err = w.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte{1}); err != errClosed {
		t.Fatalf("Write after Close returned %v; want %v", err, errClosed)
	}
	return buf.Bytes()
}

func decompress2(t *testing.T, data []byte) []byte {
	t.Helper()
	r, err := NewReader2(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	p, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("decompressing: %v", err)
	}
	return p
}

func TestParallelWriter2(t *testing.T) {
	text := randomText(t, 1, 5<<20)
	noise := randomBytes(2, 3<<20)
	tests := []struct {
		name  string
		parts [][]byte
	}{
		{"empty", nil},
		{"small", [][]byte{[]byte("abc")}},
		{"text", [][]byte{text}},
		{"noise", [][]byte{noise}},
		{"mixed", [][]byte{text[:2<<20], noise, text[2<<20:]}},
	}
	for _, m := range []MatchAlgorithm{HashTable4, BinaryTree} {
		for _, tc := range tests {
			t.Run(m.String()+"/"+tc.name, func(t *testing.T) {
				if testing.Short() && m == BinaryTree && len(tc.parts) > 0 && len(tc.parts[0]) > 1000 {
					t.Skip("slow in short mode")
				}
				c := Writer2Config{DictCap: 1 << 20, Matcher: m, Workers: 3}
				compressed := compressParallel(t, c, tc.parts...)
				got := decompress2(t, compressed)
				want := bytes.Join(tc.parts, nil)
				if !bytes.Equal(got, want) {
					t.Fatalf("round trip failed: got %d bytes; want %d", len(got), len(want))
				}
			})
		}
	}
}

func TestParallelWriter2Incompressible(t *testing.T) {
	noise := randomBytes(3, 3<<20)
	compressed := compressParallel(t,
		Writer2Config{DictCap: 1 << 20, Workers: 2}, noise)
	// 3 bytes of header per 64 KiB chunk and the EOS chunk
	if want := len(noise) + 3*len(noise)>>16 + 1; len(compressed) != want {
		t.Fatalf("compressed size %d; want %d", len(compressed), want)
	}
	if !bytes.Equal(decompress2(t, compressed), noise) {
		t.Fatal("round trip failed")
	}
}

func TestParallelWriter2StopsWorkers(t *testing.T) {
	before := runtime.NumGoroutine()
	compressParallel(t, Writer2Config{DictCap: 1 << 20, Workers: 4},
		randomText(t, 4, 3<<20))
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines running after Close; had %d before",
				runtime.NumGoroutine(), before)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestIsIncompressible(t *testing.T) {
	var table []uint32
	if isIncompressible(randomText(t, 5, 1<<20), &table) {
		t.Error("text considered incompressible")
	}
	if !isIncompressible(randomBytes(6, 1<<20), &table) {
		t.Error("random bytes considered compressible")
	}
	if isIncompressible(randomBytes(7, 1000), &table) {
		t.Error("small block considered incompressible")
	}
}

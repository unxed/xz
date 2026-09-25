// Copyright 2014-2026 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz_test

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/ulikunitz/xz"
)

func compressBlocks(t *testing.T, data []byte, blockSize int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	cfg := xz.WriterConfig{BlockSize: blockSize}
	w, err := cfg.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readParallel(t *testing.T, data []byte) ([]byte, error) {
	t.Helper()
	pr, err := xz.ReaderConfig{}.NewParallelReader(bytes.NewReader(data),
		int64(len(data)))
	if err != nil {
		return nil, err
	}
	defer pr.Close()
	return io.ReadAll(pr)
}

func TestParallelReaderCorrectness(t *testing.T) {
	orig := bytes.Repeat(
		[]byte("The quick brown fox jumps over the lazy dog. "), 10000)
	compressed := compressBlocks(t, orig, 4096)

	got, err := readParallel(t, compressed)
	if err != nil {
		t.Fatalf("parallel read: %v", err)
	}
	if !bytes.Equal(orig, got) {
		t.Fatal("decompressed data does not match original")
	}
}

func TestParallelReaderMultipleStreams(t *testing.T) {
	a := bytes.Repeat([]byte("stream one "), 2000)
	b := bytes.Repeat([]byte("stream two "), 3000)
	var file []byte
	file = append(file, compressBlocks(t, a, 1024)...)
	file = append(file, 0, 0, 0, 0) // stream padding
	file = append(file, compressBlocks(t, b, 1500)...)

	got, err := readParallel(t, file)
	if err != nil {
		t.Fatalf("parallel read: %v", err)
	}
	want := append(append([]byte{}, a...), b...)
	if !bytes.Equal(want, got) {
		t.Fatal("decompressed data does not match original")
	}
}

func TestParallelReaderCorruptBlock(t *testing.T) {
	orig := bytes.Repeat([]byte("A"), 10000)
	compressed := compressBlocks(t, orig, 1024)
	compressed[100] ^= 0xFF

	if _, err := readParallel(t, compressed); err == nil {
		t.Fatal("expected an error for a corrupted block")
	}
}

func TestParallelReaderEarlyClose(t *testing.T) {
	orig := bytes.Repeat([]byte("B"), 100000)
	compressed := compressBlocks(t, orig, 1024)

	pr, err := xz.ReaderConfig{}.NewParallelReader(
		bytes.NewReader(compressed), int64(len(compressed)))
	if err != nil {
		t.Fatal(err)
	}
	p := make([]byte, 10)
	if _, err = pr.Read(p); err != nil {
		t.Fatal(err)
	}
	if err = pr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err = pr.Read(p); err == nil {
		t.Fatal("Read after Close succeeded")
	}
}

func TestParallelReaderCorpus(t *testing.T) {
	for _, file := range []string{"fox.xz", "fox-check-none.xz"} {
		t.Run(file, func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			r, err := xz.NewReader(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			want, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			got, err := readParallel(t, data)
			if err != nil {
				t.Fatalf("parallel read: %v", err)
			}
			if !bytes.Equal(want, got) {
				t.Fatal("parallel and sequential reader disagree")
			}
		})
	}
}

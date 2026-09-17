// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"bytes"
	"io"
	"runtime"
	"testing"
	"time"
)

// Writing an xz stream starts an LZMA2 writer per block, each with its own
// compression workers. A block that has been written is done with, and so are
// its workers; leaving them running costs a match finder and a dictionary
// apiece for the rest of the program's life.
func TestWriterLeavesNoLZMA2Workers(t *testing.T) {
	waitFor := func(want int) int {
		got := runtime.NumGoroutine()
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
			if got <= want {
				return got
			}
			time.Sleep(10 * time.Millisecond)
			got = runtime.NumGoroutine()
		}
		return got
	}
	before := waitFor(0)

	payload := bytes.Repeat([]byte("The quick brown fox jumps over the lazy dog. "), 1000)
	for i := 0; i < 4; i++ {
		var buf bytes.Buffer
		// A small block size cuts the data into many blocks, each of
		// which gets an LZMA2 writer of its own.
		w, err := WriterConfig{BlockSize: 4096}.NewWriter(&buf)
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		if _, err := w.Write(payload); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		r, err := NewReader(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("read back %d bytes, want %d", len(got), len(payload))
		}
	}

	if got := waitFor(before); got > before {
		t.Errorf("%d goroutines after writing four multi-block streams, %d before", got, before)
	}
}

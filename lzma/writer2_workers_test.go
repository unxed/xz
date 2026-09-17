// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"bytes"
	"io"
	"runtime"
	"testing"
	"time"
)

// waitForGoroutines waits for the number of goroutines to come back down to
// want, and returns what it last saw.
func waitForGoroutines(want int) int {
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

// A closed writer must leave no worker behind. Each worker holds a match
// finder and a dictionary of its own, so workers left running from writers
// that were closed long ago pile up: writing eight small xz streams held on to
// 385 MiB, and the finalizer that was meant to stop them could never run,
// because the workers hold the writer they belong to.
func TestWriter2ClosedLeavesNoWorkers(t *testing.T) {
	before := waitForGoroutines(0)

	payload := bytes.Repeat([]byte("worker lifetime payload\n"), 500)
	for i := 0; i < 8; i++ {
		var buf bytes.Buffer
		w, err := Writer2Config{DictCap: MinDictCap}.NewWriter2(&buf)
		if err != nil {
			t.Fatalf("NewWriter2: %v", err)
		}
		if _, err := w.Write(payload); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	if got := waitForGoroutines(before); got > before {
		t.Errorf("%d goroutines after closing eight writers, %d before", got, before)
	}
}

// Destroy on a closed writer is a no-op rather than a second close of the
// same channel.
func TestWriter2DestroyAfterClose(t *testing.T) {
	var buf bytes.Buffer
	w, err := Writer2Config{DictCap: MinDictCap}.NewWriter2(&buf)
	if err != nil {
		t.Fatalf("NewWriter2: %v", err)
	}
	if _, err := w.Write([]byte("a small stream\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	w.Destroy()
	w.Destroy()
}

// A writer is still reusable after Close: Reset starts the workers the close
// stopped.
func TestWriter2ResetAfterClose(t *testing.T) {
	first := []byte("the first stream of this writer\n")
	second := bytes.Repeat([]byte("the second stream, a longer one\n"), 100)

	var bufA, bufB bytes.Buffer
	w, err := Writer2Config{DictCap: MinDictCap}.NewWriter2(&bufA)
	if err != nil {
		t.Fatalf("NewWriter2: %v", err)
	}
	if _, err := w.Write(first); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.Reset(&bufB); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if _, err := w.Write(second); err != nil {
		t.Fatalf("Write after Reset: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close after Reset: %v", err)
	}

	for _, c := range []struct {
		name string
		buf  *bytes.Buffer
		want []byte
	}{{"first", &bufA, first}, {"second", &bufB, second}} {
		r, err := Reader2Config{DictCap: MinDictCap}.NewReader2(bytes.NewReader(c.buf.Bytes()))
		if err != nil {
			t.Fatalf("%s stream: NewReader2: %v", c.name, err)
		}
		got, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatalf("%s stream: read: %v", c.name, err)
		}
		if !bytes.Equal(got, c.want) {
			t.Errorf("%s stream: read back %d bytes, want %d", c.name, len(got), len(c.want))
		}
	}
}

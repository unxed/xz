// Copyright 2014-2026 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"bytes"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// TestReaderNoReadAhead checks that Reader doesn't consume data that
// follows the LZMA stream in the underlying reader.
func TestReaderNoReadAhead(t *testing.T) {
	orig := readOrigFile(t)
	trailer := []byte("data following the LZMA stream")
	files := []string{
		"a.lzma",
		"a_eos.lzma",
		"a_eos_and_size.lzma",
		"a_lp1_lc2_pb1.lzma",
	}
	wraps := []wrapTest{
		{"io.ByteReader", func(r io.Reader) io.Reader { return r }},
		{"io.Reader", func(r io.Reader) io.Reader {
			return struct{ io.Reader }{r}
		}},
	}
	for _, fn := range files {
		data, err := os.ReadFile(filepath.Join(dirname, fn))
		if err != nil {
			t.Fatalf("ReadFile(%q) error %s", fn, err)
		}
		for _, w := range wraps {
			br := bytes.NewReader(append(data, trailer...))
			r, err := NewReader(w.wrap(br))
			if err != nil {
				t.Fatalf("%s %s: NewReader error %s", fn, w.name, err)
			}
			decoded, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("%s %s: ReadAll error %s", fn, w.name, err)
			}
			if !bytes.Equal(decoded, orig) {
				t.Fatalf("%s %s: decoded data differs from original",
					fn, w.name)
			}
			if br.Len() != len(trailer) {
				t.Errorf("%s %s: %d bytes left in underlying reader;"+
					" want %d", fn, w.name, br.Len(), len(trailer))
			}
		}
	}
}

// TestRangeEncoderLongOutput encodes more bits than fit into the output
// buffer of the range encoder without any call that flushes it, as a
// plain Writer does for a long run of matches.
func TestRangeEncoderLongOutput(t *testing.T) {
	const n = 1 << 20 // 128 KiB of output, twice the buffer
	rnd := rand.New(rand.NewSource(1))
	bits := make([]uint32, n)
	for i := range bits {
		bits[i] = uint32(rnd.Intn(2))
	}

	var buf bytes.Buffer
	e, err := newRangeEncoder(&buf)
	if err != nil {
		t.Fatal(err)
	}
	p := probInit
	for i, b := range bits {
		if i%2 == 0 {
			err = e.DirectEncodeBit(b)
		} else {
			err = e.EncodeBit(b, &p)
		}
		if err != nil {
			t.Fatalf("bit %d: %v", i, err)
		}
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() < n/16 {
		t.Fatalf("output %d bytes; want at least %d", buf.Len(), n/16)
	}

	d, err := newRangeDecoder(&buf)
	if err != nil {
		t.Fatal(err)
	}
	p = probInit
	for i, b := range bits {
		var got uint32
		if i%2 == 0 {
			got = d.DirectDecodeBit()
		} else {
			got = p.Decode(d)
		}
		if got != b {
			t.Fatalf("bit %d decoded as %d; want %d", i, got, b)
		}
	}
	if d.err != nil {
		t.Fatalf("decoder error %v", d.err)
	}
}

// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"bufio"
	"io"
	"io/ioutil"
	"os"
	"testing"
)

func TestDecoder(t *testing.T) {
	filename := "fox.lzma"
	want := "The quick brown fox jumps over the lazy dog.\n"
	for i := 0; i < 2; i++ {
		f, err := os.Open(filename)
		if err != nil {
			t.Fatalf("os.Open(%q) error %s", filename, err)
		}
		p := make([]byte, 13)
		_, err = io.ReadFull(f, p)
		if err != nil {
			t.Fatalf("io.ReadFull error %s", err)
		}
		props, err := PropertiesForCode(p[0])
		if err != nil {
			t.Fatalf("p[0] error %s", err)
		}
		state := newState(props)
		const capacity = 0x800000
		dict, err := newDecoderDict(capacity)
		if err != nil {
			t.Fatalf("newDecoderDict: error %s", err)
		}
		size := int64(-1)
		if i > 0 {
			size = int64(len(want))
		}
		br := bufio.NewReader(f)
		r, err := newDecoder(br, state, dict, size)
		if err != nil {
			t.Fatalf("newDecoder error %s", err)
		}
		bytes, err := ioutil.ReadAll(r)
		if err != nil {
			t.Fatalf("[%d] ReadAll error %s", i, err)
		}
		if err = f.Close(); err != nil {
			t.Fatalf("Close error %s", err)
		}
		got := string(bytes)
		if got != want {
			t.Fatalf("read %q; but want %q", got, want)
		}
	}
}
type chunkReader struct {
	data []byte
	pos  int
	size int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.pos >= len(c.data) {
		return 0, io.EOF
	}
	n := len(p)
	if n > c.size {
		n = c.size
	}
	if c.pos+n > len(c.data) {
		n = len(c.data) - c.pos
	}
	copy(p, c.data[c.pos:c.pos+n])
	c.pos += n
	return n, nil
}

func TestDecoderBCEBoundaries(t *testing.T) {
	t.Parallel()

	lzmaData, err := os.ReadFile("fox.lzma")
	if err != nil {
		t.Fatalf("failed to read fox.lzma: %v", err)
	}

	expected := "The quick brown fox jumps over the lazy dog.\n"

	for chunkSize := 1; chunkSize <= 64; chunkSize++ {
		props, err := PropertiesForCode(lzmaData[0])
		if err != nil {
			t.Fatal(err)
		}
		state := newState(props)
		dict, err := newDecoderDict(MinDictCap)
		if err != nil {
			t.Fatal(err)
		}

		cr := &chunkReader{data: lzmaData[HeaderLen:], size: chunkSize}
		r, err := newDecoder(cr, state, dict, int64(len(expected)))
		if err != nil {
			t.Fatalf("newDecoder failed for chunk size %d: %v", chunkSize, err)
		}

		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("failed to decode at chunk size %d: %v", chunkSize, err)
		}

		if string(got) != expected {
			t.Errorf("chunk size %d: got %q, want %q", chunkSize, string(got), expected)
		}
	}
}

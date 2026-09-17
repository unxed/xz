// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// filterTestPayload builds data that the branch conversion filters have
// something to do on: sequences that look like the call and branch
// instructions each of them rewrites, at addresses that change as the data
// goes on, mixed with bytes that must be left alone. The archives in
// testdata/filters hold this same payload, compressed by the xz tool with one
// filter each, which is what makes them a check of the conversions rather
// than of the decompressor alone.
func filterTestPayload() []byte {
	var buf bytes.Buffer
	state := uint32(0x12345678)
	next := func() uint32 {
		state = state*1664525 + 1013904223
		return state
	}
	instr := make([]byte, 4)
	for block := 0; block < 128; block++ {
		// x86 call and jump, which the x86 filter looks at.
		binary.LittleEndian.PutUint32(instr, next()&0x00ffffff)
		buf.Write([]byte{0xe8})
		buf.Write(instr)
		buf.Write([]byte{0xe9})
		buf.Write(instr)

		// ARM branch with link: the high byte marks it.
		binary.LittleEndian.PutUint32(instr, next()&0x00ffffff|0xeb000000)
		buf.Write(instr)

		// ARM-Thumb branch with link, a pair of halfwords.
		buf.Write([]byte{byte(next()), 0xf1, byte(next()), 0xfa})

		// ARM64 branch with link and an ADRP.
		binary.LittleEndian.PutUint32(instr, 0x94000000|next()&0x0000ffff)
		buf.Write(instr)
		binary.LittleEndian.PutUint32(instr, 0x90000000|next()&0x0000ffe0)
		buf.Write(instr)

		// PowerPC branch with link.
		binary.LittleEndian.PutUint32(instr, next())
		buf.Write([]byte{0x48, instr[0], instr[1], instr[2]&0xfc | 0x01})

		// SPARC call.
		buf.Write([]byte{0x40, byte(next()) & 0x3f, byte(next()), byte(next())})

		// Data the filters must pass through unchanged.
		buf.Write([]byte("plain data between the instructions\n"))
	}
	return buf.Bytes()
}

// TestSimpleFilters reads the archives written with a filter chain of one
// branch conversion filter and LZMA2. Before the chain was supported they
// failed to open at all with "unsupported filter count".
func TestSimpleFilters(t *testing.T) {
	want := filterTestPayload()
	names, err := filepath.Glob(filepath.Join("testdata", "filters", "*.xz"))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no archives in testdata/filters")
	}
	for _, name := range names {
		f, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		r, err := NewReader(f)
		if err != nil {
			f.Close()
			t.Errorf("%s: NewReader: %v", filepath.Base(name), err)
			continue
		}
		got, err := io.ReadAll(r)
		f.Close()
		if err != nil {
			t.Errorf("%s: read: %v", filepath.Base(name), err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: decompressed %d bytes, want the %d bytes of the payload back unchanged",
				filepath.Base(name), len(got), len(want))
		}
	}
}

// The filters that the format defines but this package does not convert have
// to say so by name.
func TestUnsupportedSimpleFilter(t *testing.T) {
	for _, id := range []uint64{ia64FilterID, riscvFilterID} {
		f := simpleFilter{filterID: id}
		if _, err := f.reader(bytes.NewReader(nil), nil); err == nil {
			t.Errorf("the %s filter reports no error", simpleFilterName(id))
		}
	}
}

// TestWriteFilterPayload writes out the payload the archives in
// testdata/filters hold, so they can be made again with the xz tool:
//
//	FILTER_PAYLOAD_PATH=payload.bin go test -run TestWriteFilterPayload
//	for f in x86 arm armthumb arm64 powerpc sparc; do
//		xz -9 --$f --lzma2 -c payload.bin > testdata/filters/$f.xz
//	done
//	xz -9 --delta=dist=4 --lzma2 -c payload.bin > testdata/filters/delta.xz
//
// It does nothing unless the path is given.
func TestWriteFilterPayload(t *testing.T) {
	path := os.Getenv("FILTER_PAYLOAD_PATH")
	if path == "" {
		t.Skip("FILTER_PAYLOAD_PATH not set")
	}
	if err := os.WriteFile(path, filterTestPayload(), 0o600); err != nil {
		t.Fatal(err)
	}
}

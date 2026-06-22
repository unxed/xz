// Copyright 2014-2026 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/ulikunitz/xz"
)

func TestBlockLevelDecompression(t *testing.T) {
	// 1. Create original data composed of 3 distinct segments
	seg1 := bytes.Repeat([]byte("A"), 512)
	seg2 := bytes.Repeat([]byte("B"), 512)
	seg3 := bytes.Repeat([]byte("C"), 512)
	uncompressed := append(append(seg1, seg2...), seg3...)

	// 2. Compress with a block size of 512 to force the creation of 3 independent blocks
	var buf bytes.Buffer
	cfg := xz.WriterConfig{
		BlockSize: 512,
	}
	w, err := cfg.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(uncompressed); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	compressedData := buf.Bytes()
	readerAt := bytes.NewReader(compressedData)

	// 3. Parse block boundaries from the index at the end
	blocks, err := xz.ParseBlocks(readerAt, int64(len(compressedData)))
	if err != nil {
		t.Fatalf("ParseBlocks failed: %v", err)
	}

	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}

	expectedSegs := [][]byte{seg1, seg2, seg3}
	var expectedUncompressedOffset int64

	for i, block := range blocks {
		if block.UncompressedSize != 512 {
			t.Errorf("block %d: expected size 512, got %d", i, block.UncompressedSize)
		}
		if block.UncompressedOffset != expectedUncompressedOffset {
			t.Errorf("block %d: expected uncompressed offset %d, got %d", i, expectedUncompressedOffset, block.UncompressedOffset)
		}
		expectedUncompressedOffset += 512

		// 4. Test standalone block decompression using the parsed metadata
		blockReaderInput := bytes.NewReader(compressedData)
		if _, err := blockReaderInput.Seek(block.Offset, io.SeekStart); err != nil {
			t.Fatalf("failed to seek block %d: %v", i, err)
		}

		rcfg := xz.ReaderConfig{}
		br, err := rcfg.NewBlockReader(blockReaderInput, block.StreamFlags)
		if err != nil {
			t.Fatalf("NewBlockReader failed for block %d: %v", i, err)
		}

		decompressed, err := io.ReadAll(br)
		if err != nil {
			t.Fatalf("failed to read block %d: %v", i, err)
		}

		if !bytes.Equal(decompressed, expectedSegs[i]) {
			t.Errorf("block %d content mismatch", i)
		}
	}
}

func TestParseBlocks_InvalidFile(t *testing.T) {
	invalidData := []byte("not a valid xz file")
	readerAt := bytes.NewReader(invalidData)
	_, err := xz.ParseBlocks(readerAt, int64(len(invalidData)))
	if err == nil {
		t.Error("expected error for invalid file, got nil")
	}
}
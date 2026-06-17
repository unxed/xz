// Copyright 2014-2026 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"errors"
	"io"
)

// Block represents the location and metadata of a single XZ block within a file.
type Block struct {
	Offset             int64 // Absolute offset of the compressed block within the file
	CompressedSize     int64 // Size of the compressed block including block header and padding
	UncompressedOffset int64 // Absolute offset of the block's uncompressed data within the stream
	UncompressedSize   int64 // Uncompressed size of the block data
	StreamFlags        byte  // Checksum type (flags) from the stream header needed to decode the block
}

// ParseBlocks scans an XZ file from the end to extract the boundaries of all blocks.
// It requires an io.ReaderAt and the total size of the file to parse the backward-linked indexes.
// This allows O(1) seeking and parallel decompression of blocks.
func ParseBlocks(r io.ReaderAt, size int64) ([]Block, error) {
	var blocks []Block
	offset := size

	type streamInfo struct {
		records     []record
		flags       byte
		startOffset int64
	}
	var streams []streamInfo

	for offset > 0 {
		// Skip 4-byte zero padding blocks between streams or at the end
		var p [4]byte
		for offset >= 4 {
			if _, err := r.ReadAt(p[:], offset-4); err != nil {
				return nil, err
			}
			if p[0] == 0 && p[1] == 0 && p[2] == 0 && p[3] == 0 {
				offset -= 4
			} else {
				break
			}
		}
		if offset == 0 {
			break
		}

		if offset < footerLen {
			return nil, errors.New("xz: file too small for footer")
		}

		var footBuf [12]byte
		if _, err := r.ReadAt(footBuf[:], offset-footerLen); err != nil {
			return nil, err
		}
		var f footer
		if err := f.UnmarshalBinary(footBuf[:]); err != nil {
			return nil, err
		}

		indexOffset := offset - footerLen - f.indexSize
		if indexOffset < HeaderLen {
			return nil, errors.New("xz: invalid index size")
		}

		sr := io.NewSectionReader(r, indexOffset, f.indexSize)

		// Consume the 1-byte index indicator (0x00) which readIndexBody expects to have already been read
		var indicator [1]byte
		if _, err := sr.Read(indicator[:]); err != nil {
			return nil, err
		}
		if indicator[0] != 0 {
			return nil, errors.New("xz: invalid index indicator")
		}

		records, _, err := readIndexBody(sr, -1)
		if err != nil {
			return nil, err
		}

		var totalPadded int64
		for _, rec := range records {
			totalPadded += (rec.unpaddedSize + 3) &^ 3
		}

		streamStart := indexOffset - totalPadded - HeaderLen
		if streamStart < 0 {
			return nil, errors.New("xz: stream start offset is negative")
		}

		var headBuf [12]byte
		if _, err := r.ReadAt(headBuf[:], streamStart); err != nil {
			return nil, err
		}
		var h header
		if err := h.UnmarshalBinary(headBuf[:]); err != nil {
			return nil, err
		}
		if h.flags != f.flags {
			return nil, errors.New("xz: header and footer flags do not match")
		}

		streams = append(streams, streamInfo{
			records:     records,
			flags:       h.flags,
			startOffset: streamStart,
		})

		offset = streamStart
	}

	var currentUncomp int64
	for i := len(streams) - 1; i >= 0; i-- {
		s := streams[i]
		currentOffset := s.startOffset + int64(HeaderLen)

		for _, rec := range s.records {
			paddedSize := (rec.unpaddedSize + 3) &^ 3
			blocks = append(blocks, Block{
				Offset:             currentOffset,
				CompressedSize:     paddedSize,
				UncompressedOffset: currentUncomp,
				UncompressedSize:   rec.uncompressedSize,
				StreamFlags:        s.flags,
			})
			currentOffset += paddedSize
			currentUncomp += rec.uncompressedSize
		}
	}

	return blocks, nil
}
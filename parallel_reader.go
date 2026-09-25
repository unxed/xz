// Copyright 2014-2026 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"sync"
)

// errParallelReaderClosed is returned by Read after Close.
var errParallelReaderClosed = errors.New("xz: parallel reader closed")

// blockResult holds the decompressed data of a single block.
type blockResult struct {
	data  []byte
	err   error
	ready chan struct{}
}

// ParallelReader decompresses the blocks of an xz file concurrently and
// returns the decompressed data in order. It needs random access to the
// file, because the block boundaries are taken from the indexes at the
// end of each stream (see ParseBlocks).
//
// Speed-up requires a file with multiple blocks, as written for instance
// by "xz -T0" or by a Writer with a small BlockSize. At most twice the
// number of workers blocks are decompressed ahead of the reader; each of
// them is held in memory in full.
type ParallelReader struct {
	blocks  []Block
	results []*blockResult

	current int
	off     int

	// ahead limits the number of blocks decompressed but not yet read.
	ahead chan struct{}
	quit  chan struct{}
	once  sync.Once
	wg    sync.WaitGroup
}

// NewParallelReader creates a ParallelReader for the xz file of the given
// size provided by r. It uses up to runtime.GOMAXPROCS(0) workers. The
// SingleStream option of the configuration is ignored: all streams of the
// file are read.
func (c ReaderConfig) NewParallelReader(r io.ReaderAt, size int64) (*ParallelReader, error) {
	if err := c.Verify(); err != nil {
		return nil, err
	}
	blocks, err := ParseBlocks(r, size)
	if err != nil {
		return nil, err
	}

	workers := runtime.GOMAXPROCS(0)
	if workers > len(blocks) {
		workers = len(blocks)
	}
	if workers < 1 {
		workers = 1
	}

	pr := &ParallelReader{
		blocks:  blocks,
		results: make([]*blockResult, len(blocks)),
		ahead:   make(chan struct{}, 2*workers),
		quit:    make(chan struct{}),
	}
	for i := range pr.results {
		pr.results[i] = &blockResult{ready: make(chan struct{})}
	}

	work := make(chan int)
	for i := 0; i < workers; i++ {
		pr.wg.Add(1)
		go func() {
			defer pr.wg.Done()
			for i := range work {
				res := pr.results[i]
				res.data, res.err = c.decodeBlock(r, pr.blocks[i])
				close(res.ready)
			}
		}()
	}
	pr.wg.Add(1)
	go func() {
		defer pr.wg.Done()
		defer close(work)
		for i := range blocks {
			select {
			case pr.ahead <- struct{}{}:
			case <-pr.quit:
				return
			}
			select {
			case work <- i:
			case <-pr.quit:
				return
			}
		}
	}()

	return pr, nil
}

// decodeBlock decompresses a single block and checks it against the index.
func (c ReaderConfig) decodeBlock(r io.ReaderAt, b Block) ([]byte, error) {
	sr := io.NewSectionReader(r, b.Offset, b.CompressedSize)
	bh, hlen, err := readBlockHeader(sr)
	if err != nil {
		if err == errIndexIndicator || err == io.EOF {
			err = errors.New("xz: block missing at the offset given by the index")
		}
		return nil, err
	}
	newHash, err := newHashFunc(b.StreamFlags)
	if err != nil {
		return nil, err
	}
	br, err := c.newBlockReader(sr, bh, hlen, newHash())
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	// The size comes from the index, so the preallocation is capped; the
	// buffer grows with the data actually decompressed.
	const maxPrealloc = 64 << 20
	if b.UncompressedSize < maxPrealloc {
		buf.Grow(int(b.UncompressedSize))
	} else {
		buf.Grow(maxPrealloc)
	}
	if _, err = io.Copy(&buf, br); err != nil {
		return nil, err
	}
	rec := br.record()
	if rec.uncompressedSize != b.UncompressedSize ||
		(rec.unpaddedSize+3)&^3 != b.CompressedSize {
		return nil, errors.New("xz: block does not match its index record")
	}
	return buf.Bytes(), nil
}

// Read reads decompressed data. It returns io.EOF after the last block.
func (pr *ParallelReader) Read(p []byte) (n int, err error) {
	select {
	case <-pr.quit:
		return 0, errParallelReaderClosed
	default:
	}
	for n < len(p) {
		if pr.current >= len(pr.blocks) {
			if n > 0 {
				return n, nil
			}
			return 0, io.EOF
		}
		res := pr.results[pr.current]
		select {
		case <-res.ready:
		case <-pr.quit:
			return n, errParallelReaderClosed
		}
		if res.err != nil {
			return n, res.err
		}
		k := copy(p[n:], res.data[pr.off:])
		n += k
		pr.off += k
		if pr.off >= len(res.data) {
			res.data = nil
			pr.current++
			pr.off = 0
			<-pr.ahead
			if n > 0 {
				// Return what we have instead of waiting for
				// the next block.
				return n, nil
			}
		}
	}
	return n, nil
}

// Close stops the decompression and waits for the workers to finish. Read
// returns an error after Close.
func (pr *ParallelReader) Close() error {
	pr.once.Do(func() { close(pr.quit) })
	pr.wg.Wait()
	return nil
}

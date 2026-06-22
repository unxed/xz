// Copyright 2014-2026 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"errors"
	"io"
	"runtime"
	"sync"
)

var blockPool = sync.Pool{
	New: func() interface{} {
		return nil
	},
}
type blockResult struct {
	data  []byte
	err   error
	ready chan struct{}
}

// ParallelReader provides multi-core decompression of XZ files.
// It requires the input to implement io.ReaderAt to parse the backward-linked block index.
type ParallelReader struct {
	config ReaderConfig
	r      io.ReaderAt
	size   int64

	blocks  []Block
	results []*blockResult

	currentBlock  int
	currentOffset int

	quit chan struct{}
	wg   sync.WaitGroup
}

// NewParallelReader creates a new ParallelReader that decompresses independent
// blocks concurrently using a worker pool. This provides near-linear scaling
// with available CPU cores.
func (c ReaderConfig) NewParallelReader(r io.ReaderAt, size int64) (*ParallelReader, error) {
	blocks, err := ParseBlocks(r, size)
	if err != nil {
		return nil, err
	}

	pr := &ParallelReader{
		config:  c,
		r:       r,
		size:    size,
		blocks:  blocks,
		results: make([]*blockResult, len(blocks)),
		quit:    make(chan struct{}),
	}

	for i := range blocks {
		pr.results[i] = &blockResult{
			ready: make(chan struct{}),
		}
	}

	// Use available cores, but don't over-provision if there are only a few blocks
	numWorkers := runtime.GOMAXPROCS(0)
	if numWorkers > len(blocks) {
		numWorkers = len(blocks)
	}
	if numWorkers < 1 {
		numWorkers = 1
	}

	// Channel capacity acts as a natural memory bound.
	// At most numWorkers * 2 blocks will be buffered/in-flight ahead of the read head.
	workCh := make(chan int, numWorkers*2)

	for i := 0; i < numWorkers; i++ {
		pr.wg.Add(1)
		go pr.worker(workCh)
	}

	// Feeder goroutine pushes block indices to workers sequentially
	go func() {
		defer close(workCh)
		for i := range blocks {
			select {
			case workCh <- i:
			case <-pr.quit:
				return
			}
		}
	}()

	return pr, nil
}

func (pr *ParallelReader) worker(workCh <-chan int) {
	defer pr.wg.Done()
	for i := range workCh {
		// Check for early abort before starting a heavy decompression task
		select {
		case <-pr.quit:
			res := pr.results[i]
			res.err = errors.New("xz: parallel reader closed")
			close(res.ready)
			continue
		default:
		}

		b := pr.blocks[i]
		sr := io.NewSectionReader(pr.r, b.Offset, b.CompressedSize)
		br, err := pr.config.NewBlockReader(sr, b.StreamFlags)

		var data []byte
		if err == nil {
			if v := blockPool.Get(); v != nil {
				bufSlice := v.([]byte)
				if int64(cap(bufSlice)) >= b.UncompressedSize {
					data = bufSlice[:b.UncompressedSize]
				}
			}
			if data == nil {
				data = make([]byte, b.UncompressedSize)
			}
			_, err = io.ReadFull(br, data)
		}

		if closer, ok := br.(io.Closer); ok {
			closer.Close()
		}

		res := pr.results[i]
		res.data = data
		res.err = err
		close(res.ready)
	}
}

// Read decompressed data sequentially.
func (pr *ParallelReader) Read(p []byte) (int, error) {
	if pr.currentBlock >= len(pr.blocks) {
		return 0, io.EOF
	}

	res := pr.results[pr.currentBlock]

	// Wait for the block to be decompressed, or an abort signal
	select {
	case <-res.ready:
	case <-pr.quit:
		return 0, errors.New("xz: parallel reader closed")
	}

	if res.err != nil {
		return 0, res.err
	}

	n := copy(p, res.data[pr.currentOffset:])
	pr.currentOffset += n

	if pr.currentOffset >= len(res.data) {
		if cap(res.data) > 0 {
			blockPool.Put(res.data)
		}
		res.data = nil
		pr.currentBlock++
		pr.currentOffset = 0
	}

	return n, nil
}

// Close aborts any pending decompression tasks and frees resources.
func (pr *ParallelReader) Close() error {
	select {
	case <-pr.quit:
	default:
		close(pr.quit)
	}
	pr.wg.Wait()
	return nil
}
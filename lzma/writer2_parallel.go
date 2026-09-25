// Copyright 2014-2026 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"bytes"
	"io"
	"sync"
)

// Limits for the size of the blocks compressed by the workers of a
// parallel LZMA2 writer.
const (
	minParallelBlockSize = 1 << 20
	maxParallelBlockSize = 64 << 20
)

// blockJob is a block of input data compressed by a worker into an
// independent LZMA2 chunk sequence.
type blockJob struct {
	data []byte
	out  bytes.Buffer
	err  error
	done chan struct{}
}

// parallelWriter2 compresses blocks of the input concurrently. The
// compressed blocks are written to the underlying writer in order by the
// goroutine calling Write, Flush or Close.
type parallelWriter2 struct {
	w         io.Writer
	config    Writer2Config
	blockSize int

	block []byte
	// queue holds the submitted jobs in input order.
	queue []*blockJob
	jobs  chan *blockJob
	wg    sync.WaitGroup

	err    error
	closed bool
}

func newParallelWriter2(w io.Writer, c Writer2Config) *parallelWriter2 {
	bs := c.DictCap
	if bs < minParallelBlockSize {
		bs = minParallelBlockSize
	}
	if bs > maxParallelBlockSize {
		bs = maxParallelBlockSize
	}
	return &parallelWriter2{w: w, config: c, blockSize: bs}
}

// startWorkers starts the workers if they are not running.
func (p *parallelWriter2) startWorkers() {
	if p.jobs != nil {
		return
	}
	p.jobs = make(chan *blockJob, p.config.Workers)
	for i := 0; i < p.config.Workers; i++ {
		p.wg.Add(1)
		go p.worker()
	}
}

// stopWorkers stops the workers and waits for them to return.
func (p *parallelWriter2) stopWorkers() {
	if p.jobs == nil {
		return
	}
	close(p.jobs)
	p.wg.Wait()
	p.jobs = nil
}

// worker compresses the jobs it receives. It keeps a sequential writer
// and reuses its dictionary and match finder for all jobs.
func (p *parallelWriter2) worker() {
	defer p.wg.Done()
	var (
		sw    *Writer2
		table []uint32
	)
	for job := range p.jobs {
		if isIncompressible(job.data, &table) {
			writeUncompressedChunks(&job.out, job.data)
			close(job.done)
			continue
		}
		var err error
		if sw == nil {
			c := p.config
			c.Workers = 1
			sw, err = c.NewWriter2(&job.out)
		} else {
			err = sw.resetStream(&job.out)
		}
		if err == nil {
			_, err = sw.Write(job.data)
		}
		if err == nil {
			err = sw.Flush()
		}
		if err != nil {
			// Don't reuse a writer in an unknown state.
			sw = nil
		}
		job.err = err
		close(job.done)
	}
}

// submit hands the buffered block over to the workers. It writes the
// oldest compressed blocks out if too many blocks are queued.
func (p *parallelWriter2) submit() error {
	if len(p.block) == 0 {
		return nil
	}
	p.startWorkers()
	for len(p.queue) >= 2*p.config.Workers {
		if err := p.writeOldest(); err != nil {
			return err
		}
	}
	job := &blockJob{data: p.block, done: make(chan struct{})}
	p.block = nil
	p.queue = append(p.queue, job)
	p.jobs <- job
	return nil
}

// writeOldest waits for the oldest job and writes its output.
func (p *parallelWriter2) writeOldest() error {
	job := p.queue[0]
	p.queue[0] = nil
	p.queue = p.queue[1:]
	<-job.done
	if job.err != nil {
		return job.err
	}
	_, err := p.w.Write(job.out.Bytes())
	return err
}

// setErr records the first error and stops the workers.
func (p *parallelWriter2) setErr(err error) error {
	if p.err == nil {
		p.err = err
	}
	p.stopWorkers()
	p.queue = nil
	return p.err
}

// Write buffers p and hands complete blocks over to the workers.
func (p *parallelWriter2) Write(b []byte) (n int, err error) {
	if p.closed {
		return 0, errClosed
	}
	if p.err != nil {
		return 0, p.err
	}
	for len(b) > 0 {
		if p.block == nil {
			p.block = make([]byte, 0, p.blockSize)
		}
		k := p.blockSize - len(p.block)
		if k > len(b) {
			k = len(b)
		}
		p.block = append(p.block, b[:k]...)
		b = b[k:]
		n += k
		if len(p.block) == p.blockSize {
			if err = p.submit(); err != nil {
				return n, p.setErr(err)
			}
		}
	}
	return n, nil
}

// Flush compresses all buffered data and writes it to the underlying
// writer.
func (p *parallelWriter2) Flush() error {
	if p.closed {
		return errClosed
	}
	if p.err != nil {
		return p.err
	}
	if err := p.submit(); err != nil {
		return p.setErr(err)
	}
	for len(p.queue) > 0 {
		if err := p.writeOldest(); err != nil {
			return p.setErr(err)
		}
	}
	return nil
}

// Close flushes the writer, stops the workers and terminates the LZMA2
// stream with an EOS chunk.
func (p *parallelWriter2) Close() error {
	if p.closed {
		return errClosed
	}
	err := p.Flush()
	p.closed = true
	p.stopWorkers()
	if err != nil {
		return err
	}
	_, err = p.w.Write([]byte{0})
	return err
}

// writeUncompressedChunks writes data as uncompressed LZMA2 chunks. The
// first chunk resets the dictionary.
func writeUncompressedChunks(out *bytes.Buffer, data []byte) {
	ctype := cUD
	for len(data) > 0 {
		// The header of an uncompressed chunk has 16 bits for the
		// size.
		n := len(data)
		if n > 1<<16 {
			n = 1 << 16
		}
		h := chunkHeader{ctype: ctype, uncompressed: uint32(n - 1)}
		hdata, err := h.MarshalBinary()
		if err != nil {
			panic(err)
		}
		out.Write(hdata)
		out.Write(data[:n])
		data = data[n:]
		ctype = cU
	}
}

// isIncompressible estimates quickly whether LZMA compression of data is
// hopeless, by counting the bytes covered by matches of at least four
// bytes found with a small hash table. Data with less than 1 % of such
// bytes is considered incompressible. Blocks smaller than 64 KiB are
// always compressed. The table is allocated on first use and reused.
func isIncompressible(data []byte, table *[]uint32) bool {
	if len(data) < 1<<16 {
		return false
	}
	const hashBits = 14
	if *table == nil {
		*table = make([]uint32, 1<<hashBits)
	} else {
		for i := range *table {
			(*table)[i] = 0
		}
	}
	t := *table
	matched := 0
	limit := len(data) / 100
	for i := 0; i+4 <= len(data); {
		h := (uint32(data[i]) | uint32(data[i+1])<<8 |
			uint32(data[i+2])<<16 | uint32(data[i+3])<<24) * 0x9E3779B1
		idx := h >> (32 - hashBits)
		prev := int(t[idx])
		// #nosec G115 -- block sizes are limited to 64 MiB
		t[idx] = uint32(i + 1)
		if prev > 0 {
			q := prev - 1
			if data[q] == data[i] && data[q+1] == data[i+1] &&
				data[q+2] == data[i+2] && data[q+3] == data[i+3] {
				k := 4
				for i+k < len(data) && data[q+k] == data[i+k] {
					k++
				}
				matched += k
				if matched > limit {
					return false
				}
				i += k
				continue
			}
		}
		i += 4
	}
	return true
}

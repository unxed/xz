// Copyright 2014-2026 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"sync"
)

// Writer2Config is used to create a Writer2 using parameters.
type Writer2Config struct {
	// The properties for the encoding. If the it is nil the value
	// {LC: 3, LP: 0, PB: 2} will be chosen.
	Properties *Properties
	// The capacity of the dictionary. If DictCap is zero, the value
	// 8 MiB will be chosen.
	DictCap int
	// Size of the lookahead buffer; value 0 indicates default size
	// 4096
	BufSize int
	// Match algorithm
	Matcher MatchAlgorithm
	// Number of concurrent compression workers. If 0, runtime.GOMAXPROCS(0) is used.
	Concurrency int
}

// fill replaces zero values with default values.
func (c *Writer2Config) fill() {
	if c.Properties == nil {
		c.Properties = &Properties{LC: 3, LP: 0, PB: 2}
	}
	if c.DictCap == 0 {
		c.DictCap = 8 * 1024 * 1024
	}
	if c.BufSize == 0 {
		c.BufSize = 4096
	}
	if c.Concurrency == 0 {
		c.Concurrency = runtime.GOMAXPROCS(0)
	}
}

// Verify checks the Writer2Config for correctness. Zero values will be
// replaced by default values.
func (c *Writer2Config) Verify() error {
	c.fill()
	var err error
	if c == nil {
		return errors.New("lzma: WriterConfig is nil")
	}
	if c.Properties == nil {
		return errors.New("lzma: WriterConfig has no Properties set")
	}
	if err = c.Properties.verify(); err != nil {
		return err
	}
	if !(MinDictCap <= c.DictCap && int64(c.DictCap) <= MaxDictCap) {
		return errors.New("lzma: dictionary capacity is out of range")
	}
	if !(maxMatchLen <= c.BufSize) {
		return errors.New("lzma: lookahead buffer size too small")
	}
	if c.Properties.LC+c.Properties.LP > 4 {
		return errors.New("lzma: sum of lc and lp exceeds 4")
	}
	if err = c.Matcher.verify(); err != nil {
		return err
	}
	return nil
}

type chunkJob struct {
	seq  uint64
	data []byte
	out  *bytes.Buffer
	err  error
}

// Writer2 supports the creation of an LZMA2 stream. It natively supports
// parallel block compression to maximize multi-core CPU utilization.
type Writer2 struct {
	w      io.Writer
	config Writer2Config

	parallel bool
	seqW     *seqWriter2

	// Parallel mode fields
	blockSize int
	inBuf     []byte
	jobs      chan *chunkJob
	outCh     chan *chunkJob
	coordDone chan struct{}
	wg        sync.WaitGroup
	nextSeq   uint64

	err     error
	errLock sync.Mutex

	closed bool
}

// NewWriter2 creates an LZMA2 chunk sequence writer with the default
// parameters and options.
func NewWriter2(lzma2 io.Writer) (w *Writer2, err error) {
	return Writer2Config{}.NewWriter2(lzma2)
}

// NewWriter2 creates a new LZMA2 writer using the given configuration.
func (c Writer2Config) NewWriter2(lzma2 io.Writer) (*Writer2, error) {
	if err := c.Verify(); err != nil {
		return nil, err
	}

	w := &Writer2{
		w:      lzma2,
		config: c,
	}

	if c.Concurrency > 1 {
		w.parallel = true
		w.blockSize = c.DictCap
		if w.blockSize < 1<<20 {
			w.blockSize = 1 << 20
		}
		if w.blockSize > 64<<20 {
			w.blockSize = 64 << 20
		}

		w.jobs = make(chan *chunkJob, c.Concurrency*2)
		w.outCh = make(chan *chunkJob, c.Concurrency*2)
		w.coordDone = make(chan struct{})

		go w.coordinator()

		for i := 0; i < c.Concurrency; i++ {
			w.wg.Add(1)
			go w.worker()
		}
	} else {
		seq, err := newSeqWriter2(lzma2, c)
		if err != nil {
			return nil, err
		}
		w.seqW = seq
	}

	return w, nil
}

func (w *Writer2) getError() error {
	w.errLock.Lock()
	defer w.errLock.Unlock()
	return w.err
}

func (w *Writer2) setError(err error) {
	if err == nil {
		return
	}
	w.errLock.Lock()
	defer w.errLock.Unlock()
	if w.err == nil {
		w.err = err
	}
}

// Write writes data to the LZMA2 stream. Data is buffered and processed in parallel blocks.
func (w *Writer2) Write(p []byte) (n int, err error) {
	if w.closed {
		return 0, errClosed
	}
	if w.parallel {
		if err := w.getError(); err != nil {
			return 0, err
		}
		n = len(p)
		for len(p) > 0 {
			space := w.blockSize - len(w.inBuf)
			if space > len(p) {
				space = len(p)
			}
			w.inBuf = append(w.inBuf, p[:space]...)
			p = p[space:]

			if len(w.inBuf) == w.blockSize {
				w.flushParallelBlock()
				if err := w.getError(); err != nil {
					return n - len(p), err
				}
			}
		}
		return n, nil
	}
	return w.seqW.Write(p)
}

func (w *Writer2) flushParallelBlock() {
	if len(w.inBuf) == 0 {
		return
	}
	job := &chunkJob{
		seq:  w.nextSeq,
		data: w.inBuf,
	}
	w.nextSeq++
	w.inBuf = nil // Hand over ownership to the worker
	w.jobs <- job
}

// Flush writes all buffered data out to the underlying stream.
func (w *Writer2) Flush() error {
	if w.closed {
		return errClosed
	}
	if w.parallel {
		w.flushParallelBlock()
		return w.getError()
	}
	return w.seqW.Flush()
}

// Close terminates the LZMA2 stream with an EOS chunk.
func (w *Writer2) Close() error {
	if w.closed {
		return errClosed
	}
	w.closed = true

	if w.parallel {
		w.flushParallelBlock()
		close(w.jobs)
		w.wg.Wait()
		close(w.outCh)
		<-w.coordDone
		if err := w.getError(); err != nil {
			return err
		}
	} else {
		if err := w.seqW.Flush(); err != nil {
			return err
		}
	}

	// Write zero byte EOS chunk
	_, err := w.w.Write([]byte{0})
	return err
}

func (w *Writer2) coordinator() {
	results := make(map[uint64]*chunkJob)
	writeSeq := uint64(0)

	for job := range w.outCh {
		if job.err != nil {
			w.setError(job.err)
			continue
		}
		results[job.seq] = job

		for {
			if j, ok := results[writeSeq]; ok {
				if w.getError() == nil {
					if _, err := w.w.Write(j.out.Bytes()); err != nil {
						w.setError(err)
					}
				}
				delete(results, writeSeq)
				writeSeq++
			} else {
				break
			}
		}
	}
	close(w.coordDone)
}

func (w *Writer2) worker() {
	defer w.wg.Done()

	startState := newState(*w.config.Properties)

	for job := range w.jobs {
		if w.getError() != nil {
			job.err = errors.New("lzma: parallel compression aborted")
			w.outCh <- job
			continue
		}

		m, err := w.config.Matcher.new(w.config.DictCap)
		if err != nil {
			job.err = err
			w.outCh <- job
			continue
		}
		d, err := newEncoderDict(w.config.DictCap, w.config.BufSize, m)
		if err != nil {
			job.err = err
			w.outCh <- job
			continue
		}

		job.out = new(bytes.Buffer)

		// Use seqWriter2 logically inside memory to generate the LZMA2 chunk sequence for this block
		seqW := &seqWriter2{
			w:      job.out,
			start:  cloneState(startState),
			cstate: start,
			ctype:  start.defaultChunkType(),
		}
		seqW.buf.Grow(maxCompressed)
		seqW.lbw = LimitedByteWriter{BW: &seqW.buf, N: maxCompressed}

		seqW.encoder, err = newEncoder(&seqW.lbw, cloneState(startState), d, 0)

		if err == nil {
			n := 0
			for n < len(job.data) {
				m := maxUncompressed - seqW.written()
				if m <= 0 {
					panic("lzma: maxUncompressed reached")
				}
				var q []byte
				if n+m < len(job.data) {
					q = job.data[n : n+m]
				} else {
					q = job.data[n:]
				}
				k, e := seqW.encoder.Write(q)
				n += k
				if e != nil && e != ErrLimit {
					err = e
					break
				}
				if e == ErrLimit || k == m {
					err = seqW.flushChunk()
					if err != nil {
						break
					}
				}
			}
			if err == nil {
				err = seqW.Flush()
			}
		}

		job.err = err
		w.outCh <- job
	}
}

// errClosed indicates that the writer is closed.
var errClosed = errors.New("lzma: writer closed")

// =========================================================================
// Sequential Engine implementation
// =========================================================================

type seqWriter2 struct {
	w       io.Writer
	start   *state
	encoder *encoder
	cstate  chunkState
	ctype   chunkType
	buf     bytes.Buffer
	lbw     LimitedByteWriter
}

func newSeqWriter2(lzma2 io.Writer, c Writer2Config) (*seqWriter2, error) {
	w := &seqWriter2{
		w:      lzma2,
		start:  newState(*c.Properties),
		cstate: start,
		ctype:  start.defaultChunkType(),
	}
	w.buf.Grow(maxCompressed)
	w.lbw = LimitedByteWriter{BW: &w.buf, N: maxCompressed}
	m, err := c.Matcher.new(c.DictCap)
	if err != nil {
		return nil, err
	}
	d, err := newEncoderDict(c.DictCap, c.BufSize, m)
	if err != nil {
		return nil, err
	}
	w.encoder, err = newEncoder(&w.lbw, cloneState(w.start), d, 0)
	if err != nil {
		return nil, err
	}
	return w, nil
}

func (w *seqWriter2) written() int {
	if w.encoder == nil {
		return 0
	}
	return int(w.encoder.Compressed()) + w.encoder.dict.Buffered()
}

func (w *seqWriter2) Write(p []byte) (n int, err error) {
	for n < len(p) {
		m := maxUncompressed - w.written()
		if m <= 0 {
			panic("lzma: maxUncompressed reached")
		}
		var q []byte
		if n+m < len(p) {
			q = p[n : n+m]
		} else {
			q = p[n:]
		}
		k, err := w.encoder.Write(q)
		n += k
		if err != nil && err != ErrLimit {
			return n, err
		}
		if err == ErrLimit || k == m {
			if err = w.flushChunk(); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

func (w *seqWriter2) writeUncompressedChunk() error {
	u := w.encoder.Compressed()
	if u <= 0 {
		return errors.New("lzma: can't write empty uncompressed chunk")
	}
	if u > maxUncompressed {
		panic("overrun of uncompressed data limit")
	}
	switch w.ctype {
	case cLRND:
		w.ctype = cUD
	default:
		w.ctype = cU
	}
	w.encoder.state = w.start

	header := chunkHeader{
		ctype:        w.ctype,
		uncompressed: uint32(u - 1),
	}
	hdata, err := header.MarshalBinary()
	if err != nil {
		return err
	}
	if _, err = w.w.Write(hdata); err != nil {
		return err
	}
	_, err = w.encoder.dict.CopyN(w.w, int(u))
	return err
}

func (w *seqWriter2) writeCompressedChunk() error {
	if w.ctype == cU || w.ctype == cUD {
		panic("chunk type uncompressed")
	}
	u := w.encoder.Compressed()
	if u <= 0 {
		return errors.New("writeCompressedChunk: empty chunk")
	}
	if u > maxUncompressed {
		panic("overrun of uncompressed data limit")
	}
	c := w.buf.Len()
	if c <= 0 {
		panic("no compressed data")
	}
	if c > maxCompressed {
		panic("overrun of compressed data limit")
	}
	header := chunkHeader{
		ctype:        w.ctype,
		uncompressed: uint32(u - 1),
		compressed:   uint16(c - 1),
		props:        w.encoder.state.Properties,
	}
	hdata, err := header.MarshalBinary()
	if err != nil {
		return err
	}
	if _, err = w.w.Write(hdata); err != nil {
		return err
	}
	_, err = io.Copy(w.w, &w.buf)
	return err
}

func (w *seqWriter2) writeChunk() error {
	u := int(uncompressedHeaderLen + w.encoder.Compressed())
	c := headerLen(w.ctype) + w.buf.Len()
	if u < c {
		return w.writeUncompressedChunk()
	}
	return w.writeCompressedChunk()
}

func (w *seqWriter2) flushChunk() error {
	if w.written() == 0 {
		return nil
	}
	var err error
	if err = w.encoder.Close(); err != nil {
		return err
	}
	if err = w.writeChunk(); err != nil {
		return err
	}
	w.buf.Reset()
	w.lbw.N = maxCompressed
	if err = w.encoder.Reopen(&w.lbw); err != nil {
		return err
	}
	if err = w.cstate.next(w.ctype); err != nil {
		return err
	}
	w.ctype = w.cstate.defaultChunkType()
	w.start = cloneState(w.encoder.state)
	return nil
}

func (w *seqWriter2) Flush() error {
	for w.written() > 0 {
		if err := w.flushChunk(); err != nil {
			return err
		}
	}
	return nil
}

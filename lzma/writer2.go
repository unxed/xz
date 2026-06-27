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
		c.DictCap = 32 * 1024 * 1024
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
	seq   uint64
	data  []byte
	out   *bytes.Buffer
	err   error
	reset bool
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
	pendingWg sync.WaitGroup
	nextSeq   uint64

	err     error
	errLock sync.Mutex

	closed bool
}

var bufPool = sync.Pool{
	New: func() interface{} { return new(bytes.Buffer) },
}
var chunkDataPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 0, 8*1024*1024)
		return &b
	},
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

		runtime.SetFinalizer(w, func(obj *Writer2) {
			obj.Destroy()
		})
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
// Destroy tears down the background worker goroutines. This is primarily
// called automatically via a runtime finalizer during garbage collection.
func (w *Writer2) Destroy() {
	if w.parallel {
		close(w.jobs)
		w.wg.Wait()
		close(w.outCh)
		<-w.coordDone
	}
}

// Reset resets the state of the writer so it can be reused with a new output stream.
// It allows to avoid destroying and recreating dictionaries and worker threads.
func (w *Writer2) Reset(lzma2 io.Writer) error {
	w.errLock.Lock()
	w.err = nil
	w.errLock.Unlock()

	w.w = lzma2
	w.closed = false

	if w.parallel {
		w.inBuf = w.inBuf[:0]
		w.nextSeq = 0
		w.outCh <- &chunkJob{reset: true}
	} else {
		w.seqW.w = lzma2
		w.seqW.cstate = start
		w.seqW.ctype = start.defaultChunkType()
		w.seqW.start.Reset()
		w.seqW.bypass = false

		w.seqW.encoder.state.deepcopy(w.seqW.start)
		w.seqW.encoder.dict.Reset()
		w.seqW.buf.Reset()
		w.seqW.lbw.N = maxCompressed
		w.seqW.encoder.Reopen(&w.seqW.lbw)
	}
	return nil
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
			if w.inBuf == nil {
				ptr := chunkDataPool.Get().(*[]byte)
				w.inBuf = (*ptr)[:0]
			}
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
	w.pendingWg.Add(1)
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
		w.pendingWg.Wait()
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
		if job.reset {
			writeSeq = 0
			for k := range results {
				delete(results, k)
			}
			continue
		}
		if job.err != nil {
			w.setError(job.err)
			if job.out != nil {
				bufPool.Put(job.out)
				job.out = nil
			}
			w.pendingWg.Done()
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
				if j.out != nil {
					bufPool.Put(j.out)
					j.out = nil
				}
				delete(results, writeSeq)
				writeSeq++
				w.pendingWg.Done()
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

	m, err := w.config.Matcher.new(w.config.DictCap)
	if err != nil {
		// Suppress completely, unlikely to ever occur with verified configs
	}

	d, err := newEncoderDict(w.config.DictCap, w.config.BufSize, m)
	if err == nil {
		defer d.Close()
	}

	var seqW *seqWriter2
	if err == nil {
		seqW = &seqWriter2{
			w:      nil,
			start:  cloneState(startState),
			cstate: start,
			ctype:  start.defaultChunkType(),
		}
		seqW.buf.Grow(maxCompressed)
		seqW.lbw = LimitedByteWriter{BW: &seqW.buf, N: maxCompressed}

		seqW.encoder, err = newEncoder(&seqW.lbw, cloneState(startState), d, 0)
	}

	for job := range w.jobs {
		if w.getError() != nil {
			job.err = errors.New("lzma: parallel compression aborted")
			w.outCh <- job
			continue
		}

		if err != nil {
			job.err = err
			w.outCh <- job
			continue
		}

		// Включаем Bypass для всех стандартных уровней сжатия (dictCap <= 32MB)
		if w.config.DictCap <= 32*1024*1024 && isHighlyIncompressible(job.data) {
			job.out = bufPool.Get().(*bytes.Buffer)
			job.out.Reset()
			job.err = writeRawUncompressed(job.out, job.data, true)
			w.outCh <- job
			continue
		}

		job.out = bufPool.Get().(*bytes.Buffer)
		job.out.Reset()

		seqW.w = job.out
		seqW.cstate = start
		seqW.ctype = start.defaultChunkType()
		seqW.start.Reset()

		seqW.encoder.state.deepcopy(seqW.start)
		seqW.encoder.dict.Reset()
		seqW.buf.Reset()
		seqW.lbw.N = maxCompressed
		seqW.encoder.Reopen(&seqW.lbw)

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
				job.err = e
				break
			}
			if e == ErrLimit || k == m {
				job.err = seqW.flushChunk()
				if job.err != nil {
					break
				}
			}
		}
		if job.err == nil {
			job.err = seqW.Flush()
		}

		ptr := &job.data
		chunkDataPool.Put(ptr)

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
	bypass  bool
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
	if w.bypass {
		err = writeRawUncompressed(w.w, p, false)
		return len(p), err
	}
	// Fast-track: упреждающий пропуск для последовательного упаковщика (только при dictCap <= 32MB)
	if w.encoder.dict.capacity <= 32*1024*1024 && w.written() == 0 && isHighlyIncompressible(p) {
		w.bypass = true
		err = writeRawUncompressed(w.w, p, true)
		return len(p), err
	}

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
	if w.bypass {
		return nil
	}
	for w.written() > 0 {
		if err := w.flushChunk(); err != nil {
			return err
		}
	}
	return nil
}

// isHighlyIncompressible оценивает уникальность байт на 4 участках блока.
// Позволяет быстро пропускать тяжело сжимаемые/случайные данные (сжатые/зашифрованные/медиа).
func isHighlyIncompressible(data []byte) bool {
	if len(data) < 4096 {
		return false
	}
	numIncompressibleParts := 0
	samples := [4]int{0, len(data) / 4, len(data) / 2, (len(data) * 3) / 4}

	for _, start := range samples {
		if start+512 > len(data) {
			break
		}
		var freq [256]int
		unique := 0
		for i := 0; i < 512; i++ {
			b := data[start+i]
			if freq[b] == 0 {
				unique++
			}
			freq[b]++
		}
		// Порог загрублен со 190 до 205 уникальных значений из 512.
		// Это гарантирует, что частично сжимаемые Mixed-данные пойдут в кодер,
		// а чистый криптографический/сжатый шум улетит в Bypass.
        // UPD:
		// Порог поднят до 230. 205 все еще ложно срабатывал на многих бинарных файлах,
		// заставляя архиватор думать, что это несжимаемый шум.
		if unique > 230 {
			numIncompressibleParts++
		}
	}
	return numIncompressibleParts >= 2
}

// writeRawUncompressed нарезает несжимаемый блок на стандартные LZMA2-чанки без компрессии.
// Внимание: максимальный размер uncompressed чанка ограничен 16 битами (64 KB), в отличие от сжатых чанков.
func writeRawUncompressed(w io.Writer, data []byte, resetDict bool) error {
	const maxUncompressedChunk = 65536
	n := 0
	first := true
	for n < len(data) {
		chunkSize := len(data) - n
		if chunkSize > maxUncompressedChunk {
			chunkSize = maxUncompressedChunk
		}

		var ctype chunkType
		if first {
			if resetDict {
				ctype = cUD // Чанк без сжатия, сбросить словарь (64 KB)
			} else {
				ctype = cU
			}
			first = false
		} else {
			ctype = cU
		}

		header := chunkHeader{
			ctype:        ctype,
			uncompressed: uint32(chunkSize - 1),
		}
		hdata, err := header.MarshalBinary()
		if err != nil {
			return err
		}
		if _, err := w.Write(hdata); err != nil {
			return err
		}
		if _, err := w.Write(data[n : n+chunkSize]); err != nil {
			return err
		}
		n += chunkSize
	}
	return nil
}

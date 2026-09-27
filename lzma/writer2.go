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

	"github.com/unxed/xz/internal/redundancy"
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
	// AdaptiveEffort enables per-block compression-effort selection: each
	// parallel block (the same granularity at which Writer2 already
	// starts a fresh LZMA2 chunk sequence) is scored with
	// internal/redundancy.EstimateRedundancy before compression, and a
	// highly redundant block is matched with BinaryTree (deeper search,
	// better ratio) while a near-random block is matched with the
	// cheaper HashTable4, overriding Matcher for that one block. Blocks
	// already routed around the matcher entirely by the existing
	// near-incompressible fast path (stored uncompressed) are
	// unaffected.
	//
	// This is deliberately a simple, single-threshold policy (see
	// selectAdaptiveMatcher): the trade-off has not been validated
	// against real-world data yet (unxed/zipper#20, part 3), so it
	// defaults to false, which keeps Matcher fixed for the whole stream
	// exactly as before this option existed.
	AdaptiveEffort bool
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
	seq     uint64
	data    []byte
	out     *bytes.Buffer
	err     error
	reset   bool
	minimal bool
}

var chunkDataPool = sync.Pool{
	New: func() interface{} {
		return nil
	},
}

var outBufPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

var estimateTablePool = sync.Pool{
	New: func() interface{} {
		t := make([]uint32, 1<<14)
		return &t
	},
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

	closed  bool
	stopped bool
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

	w.blockSize = c.DictCap
	if w.blockSize < 1<<20 {
		w.blockSize = 1 << 20
	}
	if w.blockSize > 64<<20 {
		w.blockSize = 64 << 20
	}

	w.start()

	return w, nil
}

// start creates the channels of the writer and the goroutines that work on
// them: one coordinator, which writes the finished chunks out in order, and
// Concurrency workers.
func (w *Writer2) start() {
	w.jobs = make(chan *chunkJob, w.config.Concurrency*2)
	w.outCh = make(chan *chunkJob, w.config.Concurrency*2)
	w.coordDone = make(chan struct{})
	w.stopped = false

	go w.coordinator()

	for i := 0; i < w.config.Concurrency; i++ {
		w.wg.Add(1)
		go w.worker()
	}
}

// stop lets the workers and the coordinator finish and return. A worker holds
// a match finder and a dictionary of its own, which is tens of megabytes at
// the default dictionary capacity, so a writer that is done with has to stop
// them. It is safe to call more than once.
func (w *Writer2) stop() {
	if w.stopped || w.jobs == nil {
		return
	}
	w.stopped = true

	close(w.jobs)
	w.wg.Wait()
	// The workers are the only senders on outCh and they have returned.
	close(w.outCh)
	<-w.coordDone
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

// Destroy tears down the background worker goroutines. Close does that as
// well, so a writer that has been closed needs no Destroy; it is left for a
// writer that is given up on without being closed.
func (w *Writer2) Destroy() {
	w.stop()
}

// Reset resets the state of the writer so it can be reused with a new output stream.
// It allows to avoid destroying and recreating dictionaries and worker threads.
func (w *Writer2) Reset(lzma2 io.Writer) error {
	w.errLock.Lock()
	w.err = nil
	w.errLock.Unlock()

	w.w = lzma2
	w.closed = false

	if cap(w.inBuf) > 0 {
		chunkDataPool.Put(w.inBuf)
	}
	w.inBuf = nil
	w.nextSeq = 0
	if w.stopped {
		// Close stopped the workers; reusing the writer starts them
		// again, which is still cheaper for the caller than building a
		// second writer while the first one is kept around.
		w.start()
	} else {
		w.outCh <- &chunkJob{reset: true}
	}

	return nil
}

// Write writes data to the LZMA2 stream. Data is buffered and processed in parallel blocks.
func (w *Writer2) Write(p []byte) (n int, err error) {
	if w.closed {
		return 0, errClosed
	}
	if err := w.getError(); err != nil {
		return 0, err
	}
	n = len(p)
	for len(p) > 0 {
		if w.inBuf == nil {
			if v := chunkDataPool.Get(); v != nil {
				w.inBuf = v.([]byte)[:0]
			} else {
				w.inBuf = make([]byte, 0, w.blockSize)
			}
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

func (w *Writer2) flushParallelBlock() {
	if len(w.inBuf) == 0 {
		return
	}

	estRatio := fastEstimateCompressibility(w.inBuf)
	isMinimal := estRatio >= 0.99

	job := &chunkJob{
		seq:     w.nextSeq,
		data:    w.inBuf,
		minimal: isMinimal,
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
	w.flushParallelBlock()
	return w.getError()
}

// Close terminates the LZMA2 stream with an EOS chunk.
func (w *Writer2) Close() error {
	if w.closed {
		return errClosed
	}
	w.closed = true

	w.flushParallelBlock()
	w.pendingWg.Wait()
	// Every chunk has been written by now, so the workers are done with
	// whatever the stream ends up being: stop them either way.
	defer w.stop()
	if err := w.getError(); err != nil {
		return err
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
				outBufPool.Put(j.out)
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

// fastEstimateCompressibility estimates the compression ratio of a block.
// It uses a very fast LZ4-style scan to count matching bytes.
// Returns a ratio proxy where >= 0.98 means heavily uncompressible.
func fastEstimateCompressibility(data []byte) float64 {
	if len(data) < 65536 {
		return 0.0
	}
	const hashBits = 14
	tablePtr := estimateTablePool.Get().(*[]uint32)
	table := *tablePtr
	clear(table) // Используем встроенную оптимизированную функцию Go 1.21+
	defer estimateTablePool.Put(tablePtr)

	matchedBytes := 0
	i := 0
	for i < len(data)-4 {
		h := (uint32(data[i]) | uint32(data[i+1])<<8 | uint32(data[i+2])<<16 | uint32(data[i+3])<<24) * 0x9E3779B1
		idx := h >> (32 - hashBits)

		prev := int(table[idx])
		table[idx] = uint32(i + 1)

		if prev > 0 {
			p := prev - 1
			if data[p] == data[i] && data[p+1] == data[i+1] && data[p+2] == data[i+2] && data[p+3] == data[i+3] {
				matchLen := 4
				for i+matchLen < len(data) && data[p+matchLen] == data[i+matchLen] {
					matchLen++
				}
				matchedBytes += matchLen
				i += matchLen

				// Ранний выход, если мы уже точно знаем, что данные неплохо сжимаются
				if matchedBytes > len(data)/64 {
					return 0.5 // Возвращаем коэффициент хорошего сжатия
				}
				continue
			}
		}
		i += 4
	}

	return float64(len(data)-matchedBytes) / float64(len(data))
}

// emitUncompressedLZMA2 directly bypasses the Range Encoder and emits raw LZMA2 Uncompressed chunks.
func emitUncompressedLZMA2(out *bytes.Buffer, data []byte) error {
	first := true
	n := 0
	for n < len(data) {
		m := len(data) - n
		if m > 65536 {
			m = 65536
		}
		header := make([]byte, 3)
		if first {
			header[0] = 1 // cUD (Uncompressed, Reset Dictionary)
			first = false
		} else {
			header[0] = 2 // cU (Uncompressed, Keep Dictionary)
		}
		header[1] = byte((m - 1) >> 8)
		header[2] = byte(m - 1)
		out.Write(header)
		out.Write(data[n : n+m])
		n += m
	}
	return nil
}

// newChunkSeqWriter builds a seqWriter2 (an LZMA2 chunk-sequence encoder
// with its own dictionary and matcher) for one match algorithm, using the
// worker's DictCap/BufSize/Properties. worker keeps at most one of these
// per algorithm it actually ends up using, and resets and reuses it across
// jobs rather than rebuilding it per block.
func (w *Writer2) newChunkSeqWriter(alg MatchAlgorithm, startState *state) (*seqWriter2, error) {
	m, err := alg.new(w.config.DictCap)
	if err != nil {
		return nil, err
	}
	d, err := newEncoderDict(w.config.DictCap, w.config.BufSize, m)
	if err != nil {
		return nil, err
	}

	sw := &seqWriter2{
		start:  cloneState(startState),
		cstate: start,
		ctype:  start.defaultChunkType(),
	}
	sw.buf.Grow(maxCompressed)
	sw.lbw = LimitedByteWriter{BW: &sw.buf, N: maxCompressed}
	sw.encoder, err = newEncoder(&sw.lbw, cloneState(startState), d, 0)
	if err != nil {
		return nil, err
	}
	return sw, nil
}

// compressBlockChunks resets sw for reuse and writes all of data into it as
// one or more LZMA2 chunks, flushing whatever chunk state building up in sw
// out to job.out.
func compressBlockChunks(sw *seqWriter2, job *chunkJob) error {
	job.out.Reset()
	sw.w = job.out
	sw.cstate = start
	sw.ctype = start.defaultChunkType()
	sw.start.Reset()

	sw.encoder.state.deepcopy(sw.start)
	sw.encoder.dict.Reset()
	sw.buf.Reset()
	sw.lbw.N = maxCompressed
	sw.encoder.Reopen(&sw.lbw)

	n := 0
	var jobErr error
	for n < len(job.data) {
		m := maxUncompressed - sw.written()
		if m <= 0 {
			panic("lzma: maxUncompressed reached")
		}
		var q []byte
		if n+m < len(job.data) {
			q = job.data[n : n+m]
		} else {
			q = job.data[n:]
		}
		k, e := sw.encoder.Write(q)
		n += k
		if e != nil && e != ErrLimit {
			jobErr = e
			break
		}
		if e == ErrLimit || k == m {
			jobErr = sw.flushChunk()
			if jobErr != nil {
				break
			}
		}
	}
	if jobErr == nil {
		jobErr = sw.Flush()
	}
	return jobErr
}

// adaptiveEffortThreshold is the internal/redundancy.EstimateRedundancy
// score at or above which adaptiveMatcher picks BinaryTree (deeper search,
// better ratio) instead of HashTable4 (cheap) when AdaptiveEffort is
// enabled. This is a first, deliberately simple cut, not a value tuned
// against real-world data -- see unxed/zipper#20, part 3.
const adaptiveEffortThreshold = 0.5

// selectAdaptiveMatcher maps a redundancy score in [0, 1] to the match
// algorithm adaptiveMatcher should use for a block that scored it.
func selectAdaptiveMatcher(score float64) MatchAlgorithm {
	if score >= adaptiveEffortThreshold {
		return BinaryTree
	}
	return HashTable4
}

// adaptiveMatcher returns the MatchAlgorithm that should compress data: the
// configured Matcher unchanged when cfg.AdaptiveEffort is off (the
// pre-existing, unconditional behavior), or a per-block choice driven by
// internal/redundancy.EstimateRedundancy(data) when it is on.
func adaptiveMatcher(cfg Writer2Config, data []byte) MatchAlgorithm {
	if !cfg.AdaptiveEffort {
		return cfg.Matcher
	}
	return selectAdaptiveMatcher(redundancy.EstimateRedundancy(data))
}

func (w *Writer2) worker() {
	defer w.wg.Done()

	startState := newState(*w.config.Properties)

	// seqWriters caches one seqWriter2 per match algorithm actually used
	// by this worker so far. Off (the default), only w.config.Matcher's
	// entry is ever created, matching the pre-AdaptiveEffort behavior
	// exactly. On, a second entry for the other algorithm is built
	// lazily, the first time some block actually needs it, and then
	// reused like the first.
	seqWriters := make(map[MatchAlgorithm]*seqWriter2, 2)
	seqW, err := w.newChunkSeqWriter(w.config.Matcher, startState)
	if err == nil {
		seqWriters[w.config.Matcher] = seqW
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

		job.out = outBufPool.Get().(*bytes.Buffer)

		if job.minimal {
			job.out.Reset()
			job.err = emitUncompressedLZMA2(job.out, job.data)
		} else {
			alg := adaptiveMatcher(w.config, job.data)
			sw, ok := seqWriters[alg]
			if !ok {
				var buildErr error
				sw, buildErr = w.newChunkSeqWriter(alg, startState)
				if buildErr != nil {
					// Building the alternate matcher failed -- unexpected,
					// since the primary one already succeeded with the
					// same DictCap/BufSize. Fall back to the primary
					// matcher for this one block rather than failing the
					// job over an effort optimization.
					sw = seqWriters[w.config.Matcher]
				} else {
					seqWriters[alg] = sw
				}
			}
			job.err = compressBlockChunks(sw, job)
		}

		if cap(job.data) > 0 {
			chunkDataPool.Put(job.data)
		}
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

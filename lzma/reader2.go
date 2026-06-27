// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"errors"
	"io"
	"bufio"
	"runtime"
	"sync"

	"github.com/unxed/xz/internal/xlog"
)

// Reader2Config stores the parameters for the LZMA2 reader.
// format.
type Reader2Config struct {
	DictCap int
}

// fill converts the zero values of the configuration to the default values.
func (c *Reader2Config) fill() {
	if c.DictCap == 0 {
		c.DictCap = 32 * 1024 * 1024
	}
}

// Verify checks the reader configuration for errors. Zero configuration values
// will be replaced by default values.
func (c *Reader2Config) Verify() error {
	c.fill()
	if !(MinDictCap <= c.DictCap && int64(c.DictCap) <= MaxDictCap) {
		return errors.New("lzma: dictionary capacity is out of range")
	}
	return nil
}

// Reader2 supports the reading of LZMA2 chunk sequences. Note that the
// first chunk should have a dictionary reset and the first compressed
// chunk a properties reset. The chunk sequence may not be terminated by
// an end-of-stream chunk.
type Reader2 struct {
	r   io.Reader
	err error

	dict        *decoderDict
	ur          *uncompressedReader
	decoder     *decoder
	chunkReader io.Reader

	cstate chunkState
}

type seekReaderAt interface {
	io.ReaderAt
	io.Seeker
}

func streamSizeBySeeking(s io.Seeker) (int64, error) {
	curr, err := s.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	size, err := s.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	_, err = s.Seek(curr, io.SeekStart)
	return size, err
}

// NewReader2 creates a reader for an LZMA2 chunk sequence.
func NewReader2(lzma2 io.Reader) (r io.ReadCloser, err error) {
	return Reader2Config{}.NewReader2(lzma2)
}

// NewReader2 creates an LZMA2 reader using the given configuration.
func (c Reader2Config) NewReader2(lzma2 io.Reader) (r io.ReadCloser, err error) {
	if err = c.Verify(); err != nil {
		return nil, err
	}

	// Try parallel decompression if the input is seekable and has multiple independent segments
	if sra, ok := lzma2.(seekReaderAt); ok {
		currentOffset, err := sra.Seek(0, io.SeekCurrent)
		if err == nil {
			size, err := streamSizeBySeeking(sra)
			if err == nil {
				var rAt io.ReaderAt = sra
				streamSize := size
				if currentOffset > 0 {
					rAt = io.NewSectionReader(sra, currentOffset, size-currentOffset)
					streamSize = size - currentOffset
				}

				segments, errSegs := parseSegments(rAt, streamSize)
				if errSegs == nil && len(segments) > 1 {
					var closer io.Closer
					if cl, ok := lzma2.(io.Closer); ok {
						closer = cl
					}
					pr := newParallelReader2(rAt, segments, c, closer)
					return pr, nil
				}
			}
		}
	}

	r2 := &Reader2{r: lzma2, cstate: start}
	r2.dict, err = newDecoderDict(c.DictCap)
	if err != nil {
		return nil, err
	}
	if err = r2.startChunk(); err != nil {
		r2.err = err
	}
	return r2, nil
}

// uncompressed tests whether the chunk type specifies an uncompressed
// chunk.
func uncompressed(ctype chunkType) bool {
	return ctype == cU || ctype == cUD
}

// startChunk parses a new chunk.
func (r *Reader2) startChunk() error {
	r.chunkReader = nil
	header, err := readChunkHeader(r.r)
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	xlog.Debugf("chunk header %v", header)
	if err = r.cstate.next(header.ctype); err != nil {
		return err
	}
	if r.cstate == stop {
		return io.EOF
	}
	if header.ctype == cUD || header.ctype == cLRND {
		r.dict.Reset()
	}
	size := int64(header.uncompressed) + 1
	if uncompressed(header.ctype) {
		if r.ur != nil {
			r.ur.Reopen(r.r, size)
		} else {
			r.ur = newUncompressedReader(r.r, r.dict, size)
		}
		r.chunkReader = r.ur
		return nil
	}
	br := io.LimitReader(r.r, int64(header.compressed)+1)
	if r.decoder == nil {
		state := newState(header.props)
		r.decoder, err = newDecoder(br, state, r.dict, size)
		if err != nil {
			return err
		}
		r.chunkReader = r.decoder
		return nil
	}
	switch header.ctype {
	case cLR:
		r.decoder.State.Reset()
	case cLRN, cLRND:
		r.decoder.State.Properties = header.props
		r.decoder.State.Reset()
	}
	err = r.decoder.Reopen(br, size)
	if err != nil {
		return err
	}
	r.chunkReader = r.decoder
	return nil
}

// Read reads data from the LZMA2 chunk sequence.
func (r *Reader2) Read(p []byte) (n int, err error) {
	if r.err != nil {
		return 0, r.err
	}
	for n < len(p) {
		var k int
		k, err = r.chunkReader.Read(p[n:])
		n += k
		if err != nil {
			if err == io.EOF {
				err = r.startChunk()
				if err == nil {
					continue
				}
			}
			r.err = err
			return n, err
		}
		if k == 0 {
			r.err = errors.New("lzma: Reader2 doesn't get data")
			return n, r.err
		}
	}
	return n, nil
}

// EOS returns whether the LZMA2 stream has been terminated by an
// end-of-stream chunk.
func (r *Reader2) EOS() bool {
	return r.cstate == stop
}
// Close closes the reader and releases the dictionary buffer back to the pool.
func (r *Reader2) Close() error {
	if r.dict != nil {
		r.dict.Close()
		r.dict = nil
	}
	return nil
}

// uncompressedReader is used to read uncompressed chunks.
type uncompressedReader struct {
	lr   io.LimitedReader
	Dict *decoderDict
	eof  bool
	err  error
}

// newUncompressedReader initializes a new uncompressedReader.
func newUncompressedReader(r io.Reader, dict *decoderDict, size int64) *uncompressedReader {
	ur := &uncompressedReader{
		lr:   io.LimitedReader{R: r, N: size},
		Dict: dict,
	}
	return ur
}

// Reopen reinitializes an uncompressed reader.
func (ur *uncompressedReader) Reopen(r io.Reader, size int64) {
	ur.err = nil
	ur.eof = false
	ur.lr = io.LimitedReader{R: r, N: size}
}

// fill reads uncompressed data into the dictionary.
func (ur *uncompressedReader) fill() error {
	if !ur.eof {
		n, err := io.CopyN(ur.Dict, &ur.lr, int64(ur.Dict.Available()))
		if err != io.EOF {
			return err
		}
		ur.eof = true
		if n > 0 {
			return nil
		}
	}
	if ur.lr.N != 0 {
		return io.ErrUnexpectedEOF
	}
	return io.EOF
}

// Read reads uncompressed data from the limited reader.
func (ur *uncompressedReader) Read(p []byte) (n int, err error) {
	if ur.err != nil {
		return 0, ur.err
	}
	for {
		var k int
		k, err = ur.Dict.Read(p[n:])
		n += k
		if n >= len(p) {
			return n, nil
		}
		if err != nil {
			break
		}
		err = ur.fill()
		if err != nil {
			break
		}
	}
	ur.err = err
	return n, err
}
type blockResult struct {
	data  []byte
	err   error
	ready chan struct{}
}

type segmentInfo struct {
	CompOffset   int64
	CompSize     int64
	UncompOffset int64
	UncompSize   int64
}

// parseSegments быстро сканирует сырой LZMA2-поток для нахождения точек сброса словаря
// Это позволяет разбить поток на независимые сегменты для параллельной распаковки
func parseSegments(r io.ReaderAt, size int64) ([]segmentInfo, error) {
	var segments []segmentInfo
	var current segmentInfo
	var inSegment bool

	sr := io.NewSectionReader(r, 0, size)
	br := bufio.NewReaderSize(sr, 64*1024)

	var compOffset int64
	var uncompOffset int64

	for compOffset < size {
		p, err := br.Peek(1)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		control := p[0]
		if control == 0 { // cEOS
			br.Discard(1)
			compOffset++
			if inSegment {
				current.CompSize = compOffset - current.CompOffset
				segments = append(segments, current)
				inSegment = false
			}
			break
		}

		c, err := headerChunkType(control)
		if err != nil {
			return nil, err
		}

		hlen := headerLen(c)
		hdata := make([]byte, hlen)
		if _, err := io.ReadFull(br, hdata); err != nil {
			return nil, err
		}

		var ch chunkHeader
		if err := ch.UnmarshalBinary(hdata); err != nil {
			return nil, err
		}

		isReset := (c == cUD) || (c == cLRND)

		if isReset {
			if inSegment {
				current.CompSize = compOffset - current.CompOffset
				segments = append(segments, current)
			}
			current = segmentInfo{
				CompOffset:   compOffset,
				UncompOffset: uncompOffset,
			}
			inSegment = true
		} else if !inSegment {
			current = segmentInfo{
				CompOffset:   compOffset,
				UncompOffset: uncompOffset,
			}
			inSegment = true
		}

		compOffset += int64(hlen)
		var skip int
		if c != cUD && c != cU && c != cEOS {
			skip = int(ch.compressed) + 1
		} else if c == cUD || c == cU {
			skip = int(ch.uncompressed) + 1
		}

		if skip > 0 {
			if _, err := br.Discard(skip); err != nil {
				return nil, err
			}
			compOffset += int64(skip)
		}

		uncompAdd := int64(ch.uncompressed) + 1
		uncompOffset += uncompAdd
		if inSegment {
			current.UncompSize += uncompAdd
		}
	}

	if inSegment {
		current.CompSize = compOffset - current.CompOffset
		segments = append(segments, current)
	}

	return segments, nil
}

type parallelReader2 struct {
	segments []segmentInfo
	r        io.ReaderAt
	config   Reader2Config

	results []*blockResult
	current int
	offset  int

	quit chan struct{}
	wg   sync.WaitGroup
	c    io.Closer
}

func newParallelReader2(r io.ReaderAt, segments []segmentInfo, config Reader2Config, closer io.Closer) *parallelReader2 {
	pr := &parallelReader2{
		segments: segments,
		r:        r,
		config:   config,
		results:  make([]*blockResult, len(segments)),
		quit:     make(chan struct{}),
		c:        closer,
	}
	for i := range segments {
		pr.results[i] = &blockResult{
			ready: make(chan struct{}),
		}
	}

	numWorkers := runtime.GOMAXPROCS(0)
	if numWorkers > len(segments) {
		numWorkers = len(segments)
	}
	if numWorkers < 1 {
		numWorkers = 1
	}

	workCh := make(chan int, numWorkers*2)

	for i := 0; i < numWorkers; i++ {
		pr.wg.Add(1)
		go pr.worker(workCh)
	}

	go func() {
		defer close(workCh)
		for i := range segments {
			select {
			case workCh <- i:
			case <-pr.quit:
				return
			}
		}
	}()

	return pr
}

func (pr *parallelReader2) worker(workCh <-chan int) {
	defer pr.wg.Done()
	for i := range workCh {
		select {
		case <-pr.quit:
			res := pr.results[i]
			res.err = errors.New("lzma2: parallel reader closed")
			close(res.ready)
			continue
		default:
		}

		seg := pr.segments[i]
		sr := io.NewSectionReader(pr.r, seg.CompOffset, seg.CompSize)
		seqR, err := pr.config.NewReader2(sr)
		var data []byte
		if err == nil {
			// Preallocate exactly UncompSize
			data = make([]byte, seg.UncompSize)
			_, err = io.ReadFull(seqR, data)
			seqR.Close()
			if err == io.ErrUnexpectedEOF || err == io.EOF {
				err = nil // perfectly normal for isolated segments without EOS
			}
		}

		res := pr.results[i]
		res.data = data
		res.err = err
		close(res.ready)
	}
}

func (pr *parallelReader2) Read(p []byte) (int, error) {
	if pr.current >= len(pr.segments) {
		return 0, io.EOF
	}

	res := pr.results[pr.current]

	select {
	case <-res.ready:
	case <-pr.quit:
		return 0, errors.New("lzma2: parallel reader closed")
	}

	if res.err != nil {
		return 0, res.err
	}

	n := copy(p, res.data[pr.offset:])
	pr.offset += n

	if pr.offset >= len(res.data) {
		res.data = nil // free memory
		pr.current++
		pr.offset = 0
	}

	return n, nil
}

func (pr *parallelReader2) Close() error {
	select {
	case <-pr.quit:
	default:
		close(pr.quit)
	}
	pr.wg.Wait()
	if pr.c != nil {
		return pr.c.Close()
	}
	return nil
}

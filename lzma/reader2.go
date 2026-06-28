// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"bytes"
	"errors"
	"io"
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
		c.DictCap = 8 * 1024 * 1024
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

// NewReader2 creates a reader for an LZMA2 chunk sequence.
func NewReader2(lzma2 io.Reader) (io.ReadCloser, error) {
	return Reader2Config{}.NewReader2(lzma2)
}

// NewReader2 creates an LZMA2 reader using the given configuration.
func (c Reader2Config) NewReader2(lzma2 io.Reader) (io.ReadCloser, error) {
	if err := c.Verify(); err != nil {
		return nil, err
	}

	if ra, ok := lzma2.(io.ReaderAt); ok {
		if s, ok := lzma2.(io.Seeker); ok {
			current, err := s.Seek(0, io.SeekCurrent)
			if err == nil {
				end, err := s.Seek(0, io.SeekEnd)
				if err == nil {
					_, err = s.Seek(current, io.SeekStart)
					if err == nil {
						segments, err := parseSegments(ra, current, end)
						if err == nil && len(segments) > 1 {
							return c.newParallelReader2(ra, segments)
						}
					}
				}
			}
		}
	}

	r := &Reader2{r: lzma2, cstate: start}
	var err error
	r.dict, err = newDecoderDict(c.DictCap)
	if err != nil {
		return nil, err
	}
	if err = r.startChunk(); err != nil {
		r.err = err
	}
	return r, nil
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
		r.decoder.State = newState(header.props)
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
type segment struct {
	offset       int64
	compressed   int64
	uncompressed int64
}

func parseSegments(r io.ReaderAt, start, end int64) ([]segment, error) {
	var segments []segment
	var current segment
	offset := start

	for offset < end {
		var head [6]byte
		n, err := r.ReadAt(head[:1], offset)
		if err != nil && err != io.EOF {
			return nil, err
		}
		if n == 0 {
			break
		}

		ctype, err := headerChunkType(head[0])
		if err != nil {
			return nil, err
		}

		hlen := headerLen(ctype)
		if hlen > 1 {
			_, err = r.ReadAt(head[1:hlen], offset+1)
			if err != nil {
				return nil, err
			}
		}

		var ch chunkHeader
		if err := ch.UnmarshalBinary(head[:hlen]); err != nil {
			return nil, err
		}

		if ctype == cEOS {
			current.compressed += 1
			if current.compressed > 0 {
				segments = append(segments, current)
			}
			break
		}

		if ctype == cUD || ctype == cLRND {
			if current.compressed > 0 {
				segments = append(segments, current)
				current = segment{offset: offset}
			} else {
				current.offset = offset
			}
		}

		chunkCompressed := int64(hlen)
		if ctype >= cL {
			chunkCompressed += int64(ch.compressed) + 1
		} else if ctype == cUD || ctype == cU {
			chunkCompressed += int64(ch.uncompressed) + 1
		}

		current.compressed += chunkCompressed
		current.uncompressed += int64(ch.uncompressed) + 1

		offset += chunkCompressed
	}
	return segments, nil
}

type parallelReader2 struct {
	segments []segment
	r        io.ReaderAt
	config   Reader2Config

	results []*chunkResult
	wg      sync.WaitGroup
	quit    chan struct{}

	currentSeg int
	currentOff int
}

type chunkResult struct {
	data  []byte
	err   error
	ready chan struct{}
}

func (c Reader2Config) newParallelReader2(r io.ReaderAt, segments []segment) (io.ReadCloser, error) {
	pr := &parallelReader2{
		segments: segments,
		r:        r,
		config:   c,
		results:  make([]*chunkResult, len(segments)),
		quit:     make(chan struct{}),
	}

	for i := range segments {
		pr.results[i] = &chunkResult{
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

	return pr, nil
}

func (pr *parallelReader2) worker(workCh <-chan int) {
	defer pr.wg.Done()
	for i := range workCh {
		select {
		case <-pr.quit:
			res := pr.results[i]
			res.err = errors.New("lzma: parallel reader closed")
			close(res.ready)
			continue
		default:
		}

		seg := pr.segments[i]
		sr := io.NewSectionReader(pr.r, seg.offset, seg.compressed)

		eosReader := io.MultiReader(sr, bytes.NewReader([]byte{0x00}))

		r2, err := pr.config.NewReader2(eosReader)
		var data []byte
		if err == nil {
			data = make([]byte, seg.uncompressed)
			_, err = io.ReadFull(r2, data)
			if err == nil {
				var discard [1]byte
				_, e := r2.Read(discard[:])
				if e != io.EOF && e != nil {
					err = e
				}
			}
			r2.Close()
		}

		res := pr.results[i]
		res.data = data
		res.err = err
		close(res.ready)
	}
}

func (pr *parallelReader2) Read(p []byte) (n int, err error) {
	if pr.currentSeg >= len(pr.segments) {
		return 0, io.EOF
	}

	res := pr.results[pr.currentSeg]

	select {
	case <-res.ready:
	case <-pr.quit:
		return 0, errors.New("lzma: parallel reader closed")
	}

	if res.err != nil {
		return 0, res.err
	}

	n = copy(p, res.data[pr.currentOff:])
	pr.currentOff += n

	if pr.currentOff >= len(res.data) {
		res.data = nil
		pr.currentSeg++
		pr.currentOff = 0
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
	return nil
}

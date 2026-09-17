// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"errors"
	"io"
)

// rangeEncoder implements range encoding of single bits. The low value can
// overflow therefore we need uint64. The cache value is used to handle
// overflows.
type rangeEncoder struct {
	lbw      *LimitedByteWriter
	nrange   uint32
	low      uint64
	cacheLen int64
	cache    byte
}

// maxInt64 provides the  maximal value of the int64 type
const maxInt64 = 1<<63 - 1

// newRangeEncoder creates a new range encoder.
func newRangeEncoder(bw io.ByteWriter) (re *rangeEncoder, err error) {
	lbw, ok := bw.(*LimitedByteWriter)
	if !ok {
		lbw = &LimitedByteWriter{BW: bw, N: maxInt64}
	}
	return &rangeEncoder{
		lbw:      lbw,
		nrange:   0xffffffff,
		cacheLen: 1}, nil
}

// Available returns the number of bytes that still can be written. The
// method takes the bytes that will be currently written by Close into
// account.
func (e *rangeEncoder) Available() int64 {
	return e.lbw.N - (e.cacheLen + 4)
}

// writeByte writes a single byte to the underlying writer. An error is
// returned if the limit is reached. The written byte will be counted if
// the underlying writer doesn't return an error.
func (e *rangeEncoder) writeByte(c byte) error {
	if e.Available() < 1 {
		return ErrLimit
	}
	return e.lbw.WriteByte(c)
}

// DirectEncodeBit encodes the least-significant bit of b with probability 1/2.
func (e *rangeEncoder) DirectEncodeBit(b uint32) error {
	e.nrange >>= 1
	e.low += uint64(e.nrange) & (0 - (uint64(b) & 1))

	// normalize
	const top = 1 << 24
	if e.nrange >= top {
		return nil
	}
	e.nrange <<= 8
	return e.shiftLow()
}

// EncodeBit encodes the least significant bit of b. The p value will be
// updated by the function depending on the bit encoded.
func (e *rangeEncoder) EncodeBit(b uint32, p *prob) error {
	bound := p.bound(e.nrange)
	if b&1 == 0 {
		e.nrange = bound
		p.inc()
	} else {
		e.low += uint64(bound)
		e.nrange -= bound
		p.dec()
	}

	// normalize
	const top = 1 << 24
	if e.nrange >= top {
		return nil
	}
	e.nrange <<= 8
	return e.shiftLow()
}

// Close writes a complete copy of the low value.
func (e *rangeEncoder) Close() error {
	for i := 0; i < 5; i++ {
		if err := e.shiftLow(); err != nil {
			return err
		}
	}
	return nil
}

// shiftLow shifts the low value for 8 bit. The shifted byte is written into
// the byte writer. The cache value is used to handle overflows.
func (e *rangeEncoder) shiftLow() error {
	if uint32(e.low) < 0xff000000 || (e.low>>32) != 0 {
		tmp := e.cache
		for {
			err := e.writeByte(tmp + byte(e.low>>32))
			if err != nil {
				return err
			}
			tmp = 0xff
			e.cacheLen--
			if e.cacheLen <= 0 {
				if e.cacheLen < 0 {
					panic("negative cacheLen")
				}
				break
			}
		}
		e.cache = byte(uint32(e.low) >> 24)
	}
	e.cacheLen++
	e.low = uint64(uint32(e.low) << 8)
	return nil
}

// rangeDecoder decodes single bits of the range encoding stream.
type rangeDecoder struct {
	nrange uint32
	code   uint32
	// buf[pos:limit] contains the bytes read from r that have not
	// been consumed yet
	pos   int
	limit int
	// maximum number of bytes requested by a single Read call on r
	maxRead int
	r       io.Reader
	// error returned by r together with the buffered bytes
	err error
	buf [4096]byte
}

// newRangeDecoder creates a new range decoder and initializes it with
// init.
func newRangeDecoder(r io.Reader, readAhead bool) (d *rangeDecoder, err error) {
	d = new(rangeDecoder)
	if err = d.init(r, readAhead); err != nil {
		return nil, err
	}
	return d, nil
}

// init initializes the range decoder for reading from r. It reads five
// bytes from the reader and therefore may return an error.
//
// If readAhead is false, the decoder reads only the bytes it consumes,
// so any data following the range-encoded stream stays in r. If
// readAhead is true, the decoder reads data in blocks of up to 4096
// bytes, which avoids a Read call per byte. The caller must then ensure
// that r doesn't provide any data after the end of the stream, for
// instance by using an io.LimitedReader.
func (d *rangeDecoder) init(r io.Reader, readAhead bool) error {
	d.nrange = 0xffffffff
	d.code = 0
	d.pos = 0
	d.limit = 0
	d.maxRead = 1
	if readAhead {
		d.maxRead = len(d.buf)
	}
	d.r = r
	d.err = nil

	if err := d.fill(); err != nil {
		return err
	}
	if d.buf[0] != 0 {
		return errors.New("newRangeDecoder: first byte not zero")
	}
	d.pos = 1

	for i := 0; i < 4; i++ {
		if err := d.updateCode(); err != nil {
			return err
		}
	}

	if d.code >= d.nrange {
		return errors.New("newRangeDecoder: d.code >= d.nrange")
	}

	return nil
}

// maxConsecutiveEmptyReads limits the number of Read calls returning
// neither data nor an error.
const maxConsecutiveEmptyReads = 100

// fill reads new data from r into the buffer. It must only be called if
// all buffered bytes have been consumed. An error is returned if no data
// could be read. The error io.EOF is returned if the reader is
// exhausted.
func (d *rangeDecoder) fill() error {
	if d.err != nil {
		return d.err
	}
	for i := 0; i < maxConsecutiveEmptyReads; i++ {
		n, err := d.r.Read(d.buf[:d.maxRead])
		if n > 0 {
			d.pos, d.limit = 0, n
			d.err = err
			return nil
		}
		if err != nil {
			d.err = err
			return err
		}
	}
	return io.ErrNoProgress
}

// possiblyAtEnd checks whether the decoder may be at the end of the stream.
func (d *rangeDecoder) possiblyAtEnd() bool {
	return d.code == 0
}

// DirectDecodeBit decodes a bit with probability 1/2. The return value b will
// contain the bit at the least-significant position. All other bits will be
// zero.
func (d *rangeDecoder) DirectDecodeBit() (b uint32, err error) {
	d.nrange >>= 1
	d.code -= d.nrange
	t := 0 - (d.code >> 31)
	d.code += d.nrange & t
	b = (t + 1) & 1

	// d.code will stay less then d.nrange

	// normalize
	// assume d.code < d.nrange
	const top = 1 << 24
	if d.nrange >= top {
		return b, nil
	}
	d.nrange <<= 8
	// d.code < d.nrange will be maintained
	return b, d.updateCode()
}

// decodeBit decodes a single bit. The bit will be returned at the
// least-significant position. All other bits will be zero. The probability
// value will be updated.
func (d *rangeDecoder) DecodeBit(p *prob) (b uint32, err error) {
	bound := p.bound(d.nrange)
	if d.code < bound {
		d.nrange = bound
		p.inc()
		b = 0
	} else {
		d.code -= bound
		d.nrange -= bound
		p.dec()
		b = 1
	}
	// normalize
	// assume d.code < d.nrange
	const top = 1 << 24
	if d.nrange >= top {
		return b, nil
	}
	d.nrange <<= 8
	// d.code < d.nrange will be maintained
	return b, d.updateCode()
}

// updateCode reads a new byte into the code.
func (d *rangeDecoder) updateCode() error {
	if d.pos >= d.limit {
		if err := d.fill(); err != nil {
			return err
		}
	}
	d.code = (d.code << 8) | uint32(d.buf[d.pos])
	d.pos++
	return nil
}

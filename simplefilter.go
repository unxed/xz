// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Identifiers of the filters that are applied to the data before the
// compression filter: the delta filter and the branch/call/jump ("BCJ")
// filters, which rewrite the target addresses of machine instructions into a
// form that compresses better. An archive that uses one of them declares two
// filters in its block header, the BCJ filter first and LZMA2 last.
const (
	deltaFilterID    = 0x03
	x86FilterID      = 0x04
	powerPCFilterID  = 0x05
	ia64FilterID     = 0x06
	armFilterID      = 0x07
	armThumbFilterID = 0x08
	sparcFilterID    = 0x09
	arm64FilterID    = 0x0a
	riscvFilterID    = 0x0b

	// maxFilterPropsLen is more room than any filter defined by the format
	// needs; the longest properties in use are the four bytes of a start
	// offset.
	maxFilterPropsLen = 16
)

// simpleFilterName gives the name the xz tools use for a filter, for error
// messages.
func simpleFilterName(id uint64) string {
	switch id {
	case deltaFilterID:
		return "delta"
	case x86FilterID:
		return "x86"
	case powerPCFilterID:
		return "powerpc"
	case ia64FilterID:
		return "ia64"
	case armFilterID:
		return "arm"
	case armThumbFilterID:
		return "armthumb"
	case sparcFilterID:
		return "sparc"
	case arm64FilterID:
		return "arm64"
	case riscvFilterID:
		return "riscv"
	}
	return fmt.Sprintf("%#x", id)
}

// simpleFilter is a branch conversion filter as it appears in a block header:
// its id and the start offset the addresses were converted against, which is
// zero unless the archive says otherwise.
type simpleFilter struct {
	filterID    uint64
	startOffset uint32
}

func (f simpleFilter) String() string {
	return fmt.Sprintf("%s filter start offset %#x", simpleFilterName(f.filterID), f.startOffset)
}

func (f simpleFilter) id() uint64 { return f.filterID }

// last returns false: a branch conversion filter is applied to the
// uncompressed data and is always followed by the compression filter.
func (f simpleFilter) last() bool { return false }

// MarshalBinary converts the filter into its encoded representation.
func (f simpleFilter) MarshalBinary() (data []byte, err error) {
	if f.startOffset == 0 {
		return []byte{byte(f.filterID), 0}, nil
	}
	data = []byte{byte(f.filterID), 4, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(data[2:], f.startOffset)
	return data, nil
}

// UnmarshalBinary reads the filter's id, the length of its properties and the
// properties themselves, as they are stored in a block header.
func (f *simpleFilter) UnmarshalBinary(data []byte) error {
	if len(data) < 2 {
		return errors.New("xz: data for branch conversion filter too short")
	}
	f.filterID = uint64(data[0])
	props := data[2:]
	if len(props) != int(data[1]) {
		return errors.New("xz: wrong property length for branch conversion filter")
	}
	switch len(props) {
	case 0:
		f.startOffset = 0
	case 4:
		f.startOffset = binary.LittleEndian.Uint32(props)
	default:
		return fmt.Errorf("xz: %s filter has %d property bytes, want 0 or 4",
			simpleFilterName(f.filterID), len(props))
	}
	return nil
}

// reader wraps the reader of the filter below it in the chain, undoing the
// branch conversion on the data that comes out of it.
func (f simpleFilter) reader(r io.Reader, c *ReaderConfig) (fr io.Reader, err error) {
	convert, window, err := simpleDecoder(f.filterID)
	if err != nil {
		return nil, err
	}
	return &simpleReader{
		r:       r,
		convert: convert,
		window:  window,
		pos:     f.startOffset,
		buf:     make([]byte, 64*1024),
	}, nil
}

// writeCloser reports that writing these filters is not supported. Reading
// them is what an archive from another tool needs.
func (f simpleFilter) writeCloser(w io.WriteCloser, c *WriterConfig) (fw io.WriteCloser, err error) {
	return nil, fmt.Errorf("xz: writing the %s filter is not supported", simpleFilterName(f.filterID))
}

// deltaFilter is the delta filter, which stores the difference between bytes
// that are distance apart.
type deltaFilter struct {
	distance int
}

func (f deltaFilter) String() string {
	return fmt.Sprintf("delta filter distance %d", f.distance)
}

func (f deltaFilter) id() uint64 { return deltaFilterID }

func (f deltaFilter) last() bool { return false }

func (f deltaFilter) MarshalBinary() (data []byte, err error) {
	if f.distance < 1 || f.distance > 256 {
		return nil, errors.New("xz: delta filter distance out of range")
	}
	// #nosec G115 -- the distance is checked to be 1..256 right above
	return []byte{deltaFilterID, 1, byte(f.distance - 1)}, nil
}

func (f *deltaFilter) UnmarshalBinary(data []byte) error {
	if len(data) != 3 || data[0] != deltaFilterID || data[1] != 1 {
		return errors.New("xz: wrong data for delta filter")
	}
	f.distance = int(data[2]) + 1
	return nil
}

func (f deltaFilter) reader(r io.Reader, c *ReaderConfig) (fr io.Reader, err error) {
	if f.distance < 1 || f.distance > 256 {
		return nil, errors.New("xz: delta filter distance out of range")
	}
	d := &deltaDecoder{distance: f.distance}
	return &simpleReader{
		r:       r,
		convert: d.convert,
		window:  1,
		buf:     make([]byte, 64*1024),
	}, nil
}

func (f deltaFilter) writeCloser(w io.WriteCloser, c *WriterConfig) (fw io.WriteCloser, err error) {
	return nil, errors.New("xz: writing the delta filter is not supported")
}

// simpleReader undoes a branch conversion on the stream of the filter below it
// in the chain. convert rewrites the addresses in buf, which holds the bytes
// at position pos of the filtered data, and returns how many bytes it has
// dealt with; the bytes it left over are kept until more data arrives, since
// an instruction may be cut in half by the end of a read.
type simpleReader struct {
	r       io.Reader
	convert func(pos uint32, buf []byte) int
	window  int
	pos     uint32
	buf     []byte
	off     int
	end     int
	ready   int
	eof     bool
	err     error
}

func (r *simpleReader) Read(p []byte) (n int, err error) {
	for {
		if r.ready > 0 {
			n = copy(p, r.buf[r.off:r.off+r.ready])
			r.off += n
			r.ready -= n
			// #nosec G115 -- the position wraps the same way the addresses in the data do
			r.pos += uint32(n)
			return n, nil
		}
		if r.eof {
			if r.off < r.end {
				// What is left cannot hold an instruction any
				// more, so it is passed through unchanged.
				n = copy(p, r.buf[r.off:r.end])
				r.off += n
				// #nosec G115 -- see above
				r.pos += uint32(n)
				return n, nil
			}
			if r.err != nil {
				return 0, r.err
			}
			return 0, io.EOF
		}

		if r.off > 0 {
			copy(r.buf, r.buf[r.off:r.end])
			r.end -= r.off
			r.off = 0
		}
		if r.end == len(r.buf) {
			return 0, errors.New("xz: branch conversion filter cannot make progress")
		}
		k, err := r.r.Read(r.buf[r.end:])
		r.end += k
		switch {
		case err == io.EOF:
			r.eof = true
		case err != nil:
			r.eof = true
			r.err = err
		}
		if r.end >= r.window {
			r.ready = r.convert(r.pos, r.buf[:r.end])
		}
		if r.ready == 0 && !r.eof && k == 0 && err == nil {
			// A reader that returns nothing without an error is
			// allowed to do so; ask it again.
			continue
		}
	}
}

// simpleDecoder returns the conversion of a filter and the number of bytes one
// conversion looks at.
func simpleDecoder(id uint64) (convert func(pos uint32, buf []byte) int, window int, err error) {
	switch id {
	case x86FilterID:
		x := new(x86Decoder)
		return x.convert, 5, nil
	case powerPCFilterID:
		return powerPCDecode, 4, nil
	case armFilterID:
		return armDecode, 4, nil
	case armThumbFilterID:
		return armThumbDecode, 4, nil
	case sparcFilterID:
		return sparcDecode, 4, nil
	case arm64FilterID:
		return arm64Decode, 4, nil
	}
	return nil, 0, fmt.Errorf("xz: the %s filter is not supported", simpleFilterName(id))
}

// deltaDecoder holds the bytes the delta filter looks back at.
type deltaDecoder struct {
	distance int
	history  [256]byte
	pos      byte
}

func (d *deltaDecoder) convert(pos uint32, buf []byte) int {
	for i := range buf {
		buf[i] += d.history[(byte(d.distance)+d.pos)&0xff]
		d.history[d.pos&0xff] = buf[i]
		d.pos--
	}
	return len(buf)
}

func armDecode(pos uint32, buf []byte) int {
	var i int
	for i = 0; i+4 <= len(buf); i += 4 {
		if buf[i+3] != 0xeb {
			continue
		}
		src := uint32(buf[i+2])<<16 | uint32(buf[i+1])<<8 | uint32(buf[i])
		src <<= 2
		// #nosec G115 -- i is below the length of a buffer of at most 64 KiB
		dest := src - (pos + uint32(i) + 8)
		dest >>= 2
		buf[i+2] = byte(dest >> 16)
		buf[i+1] = byte(dest >> 8)
		buf[i] = byte(dest)
	}
	return i
}

func armThumbDecode(pos uint32, buf []byte) int {
	var i int
	for i = 0; i+4 <= len(buf); i += 2 {
		if buf[i+1]&0xf8 != 0xf0 || buf[i+3]&0xf8 != 0xf8 {
			continue
		}
		src := uint32(buf[i+1]&0x07)<<19 | uint32(buf[i])<<11 |
			uint32(buf[i+3]&0x07)<<8 | uint32(buf[i+2])
		src <<= 1
		// #nosec G115 -- see armDecode
		dest := src - (pos + uint32(i) + 4)
		dest >>= 1
		buf[i+1] = 0xf0 | byte(dest>>19)&0x07
		buf[i] = byte(dest >> 11)
		buf[i+3] = 0xf8 | byte(dest>>8)&0x07
		buf[i+2] = byte(dest)
		i += 2
	}
	return i
}

func arm64Decode(pos uint32, buf []byte) int {
	var i int
	for i = 0; i+4 <= len(buf); i += 4 {
		// #nosec G115 -- see armDecode
		pc := pos + uint32(i)
		instr := binary.LittleEndian.Uint32(buf[i:])
		switch {
		case instr>>26 == 0x25:
			// BL: the whole address is in the instruction.
			src := instr
			instr = 0x94000000
			instr |= (src - (pc >> 2)) & 0x03ffffff
			binary.LittleEndian.PutUint32(buf[i:], instr)
		case instr&0x9f000000 == 0x90000000:
			// ADRP: only addresses within 512 MiB were converted.
			src := (instr>>29)&0x03 | (instr>>3)&0x001ffffc
			if (src+0x00020000)&0x001c0000 != 0 {
				continue
			}
			instr &= 0x9000001f
			dest := src - (pc >> 12)
			instr |= (dest & 0x03) << 29
			instr |= (dest & 0x0003fffc) << 3
			instr |= (0 - (dest & 0x00020000)) & 0x00e00000
			binary.LittleEndian.PutUint32(buf[i:], instr)
		}
	}
	return i
}

func powerPCDecode(pos uint32, buf []byte) int {
	var i int
	for i = 0; i+4 <= len(buf); i += 4 {
		if buf[i]&0xfc != 0x48 || buf[i+3]&0x03 != 0x01 {
			continue
		}
		src := uint32(buf[i]&0x03)<<24 | uint32(buf[i+1])<<16 |
			uint32(buf[i+2])<<8 | uint32(buf[i+3]&0xfc)
		// #nosec G115 -- see armDecode
		dest := src - (pos + uint32(i))
		buf[i] = 0x48 | byte(dest>>24)&0x03
		buf[i+1] = byte(dest >> 16)
		buf[i+2] = byte(dest >> 8)
		buf[i+3] = buf[i+3]&0x03 | byte(dest)
	}
	return i
}

func sparcDecode(pos uint32, buf []byte) int {
	var i int
	for i = 0; i+4 <= len(buf); i += 4 {
		if !((buf[i] == 0x40 && buf[i+1]&0xc0 == 0x00) ||
			(buf[i] == 0x7f && buf[i+1]&0xc0 == 0xc0)) {
			continue
		}
		src := binary.BigEndian.Uint32(buf[i:])
		src <<= 2
		// #nosec G115 -- see armDecode
		dest := src - (pos + uint32(i))
		dest >>= 2
		dest = (0x40000000 - (dest & 0x400000)) | 0x40000000 | (dest & 0x3fffff)
		binary.BigEndian.PutUint32(buf[i:], dest)
	}
	return i
}

// x86Decoder holds the state the x86 filter carries between conversions.
type x86Decoder struct {
	prevMask uint32
	prevPos  uint32
	started  bool
}

func x86TestMSByte(b byte) bool { return b == 0x00 || b == 0xff }

func (x *x86Decoder) convert(pos uint32, buf []byte) int {
	if len(buf) < 5 {
		return 0
	}
	if !x.started {
		x.prevPos = pos - 5
		x.started = true
	}
	var maskToAllowedStatus = [8]bool{true, true, true, false, true, false, false, false}
	var maskToBitNumber = [8]uint32{0, 1, 2, 2, 3, 3, 3, 3}

	if pos-x.prevPos > 5 {
		x.prevPos = pos - 5
	}
	limit := len(buf) - 5
	i := 0
	for i <= limit {
		b := buf[i]
		if b != 0xe8 && b != 0xe9 {
			i++
			continue
		}
		// #nosec G115 -- i is below the length of a buffer of at most 64 KiB
		offset := pos + uint32(i) - x.prevPos
		// #nosec G115 -- see above
		x.prevPos = pos + uint32(i)
		if offset > 5 {
			x.prevMask = 0
		} else {
			for j := uint32(0); j < offset; j++ {
				x.prevMask &= 0x77
				x.prevMask <<= 1
			}
		}
		b = buf[i+4]
		if x86TestMSByte(b) && maskToAllowedStatus[(x.prevMask>>1)&0x07] && x.prevMask>>1 < 0x10 {
			src := uint32(b)<<24 | uint32(buf[i+3])<<16 | uint32(buf[i+2])<<8 | uint32(buf[i+1])
			var dest uint32
			for {
				// #nosec G115 -- see above
				dest = src - (pos + uint32(i) + 5)
				if x.prevMask == 0 {
					break
				}
				k := maskToBitNumber[x.prevMask>>1]
				b = byte(dest >> (24 - k*8))
				if !x86TestMSByte(b) {
					break
				}
				src = dest ^ ((1 << (32 - k*8)) - 1)
			}
			buf[i+4] = byte(^(((dest >> 24) & 1) - 1))
			buf[i+3] = byte(dest >> 16)
			buf[i+2] = byte(dest >> 8)
			buf[i+1] = byte(dest)
			i += 5
			x.prevMask = 0
		} else {
			x.prevMask |= 0x01
			if x86TestMSByte(b) {
				x.prevMask |= 0x10
			}
			i++
		}
	}
	return i
}

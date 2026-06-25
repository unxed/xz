// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import "errors"

// maxPosBits defines the number of bits of the position value that are used to
// to compute the posState value. The value is used to select the tree codec
// for length encoding and decoding.
const maxPosBits = 4

// minMatchLen and maxMatchLen give the minimum and maximum values for
// encoding and decoding length values. minMatchLen is also used as base
// for the encoded length values.
const (
	minMatchLen = 2
	maxMatchLen = minMatchLen + 16 + 256 - 1
)

// lengthCodec support the encoding of the length value.
type lengthCodec struct {
	choice [2]prob
	low    [1 << maxPosBits]treeCodec
	mid    [1 << maxPosBits]treeCodec
	high   treeCodec
}

// deepcopy initializes the lc value as deep copy of the source value.
func (lc *lengthCodec) deepcopy(src *lengthCodec) {
	if lc == src {
		return
	}
	lc.choice = src.choice
	for i := range lc.low {
		lc.low[i].deepcopy(&src.low[i])
	}
	for i := range lc.mid {
		lc.mid[i].deepcopy(&src.mid[i])
	}
	lc.high.deepcopy(&src.high)
}

// init initializes a new length codec.
func (lc *lengthCodec) init() {
	for i := range lc.choice {
		lc.choice[i] = probInit
	}
	for i := range lc.low {
		lc.low[i] = makeTreeCodec(3)
	}
	for i := range lc.mid {
		lc.mid[i] = makeTreeCodec(3)
	}
	lc.high = makeTreeCodec(8)
}

// Encode encodes the length offset. The length offset l can be compute by
// subtracting minMatchLen (2) from the actual length.
//
//	l = length - minMatchLen
func (lc *lengthCodec) Encode(e *rangeEncoder, l uint32, posState uint32,
) (err error) {
	if l > maxMatchLen-minMatchLen {
		return errors.New("lengthCodec.Encode: l out of range")
	}
	if l < 8 {
		if err = lc.choice[0].Encode(e, 0); err != nil {
			return
		}
		return lc.low[posState].Encode(e, l)
	}
	if err = lc.choice[0].Encode(e, 1); err != nil {
		return
	}
	if l < 16 {
		if err = lc.choice[1].Encode(e, 0); err != nil {
			return
		}
		return lc.mid[posState].Encode(e, l-8)
	}
	if err = lc.choice[1].Encode(e, 1); err != nil {
		return
	}
	if err = lc.high.Encode(e, l-16); err != nil {
		return
	}
	return nil
}

// Decode reads the length offset. Add minMatchLen to compute the actual length
// to the length offset l.
func (lc *lengthCodec) Decode(d *rangeDecoder, posState uint32) uint32 {
	nrange := d.nrange
	code := d.code
	pos := d.pos
	limit := d.limit
	buf := &d.buf
	if limit-pos >= 10 {
		val := uint32(lc.choice[0])
		bound := (nrange >> 11) * val
		var bit0 uint32
		if code < bound {
			nrange = bound
			lc.choice[0] = prob(val + (2048-val)>>5)
			bit0 = 0
		} else {
			code -= bound
			nrange -= bound
			lc.choice[0] = prob(val - (val >> 5))
			bit0 = 1
		}
		if nrange < (1 << 24) {
			nrange <<= 8
			code = (code << 8) | uint32(buf[pos])
			pos++
		}

		if bit0 == 0 {
			m := uint32(1)
			probs := lc.low[posState].probs
			for j := 0; j < 3; j++ {
				val := uint32(probs[m])
				bound := (nrange >> 11) * val
				if code < bound {
					nrange = bound
					probs[m] = prob(val + (2048-val)>>5)
					m <<= 1
				} else {
					code -= bound
					nrange -= bound
					probs[m] = prob(val - (val >> 5))
					m = (m << 1) | 1
				}
				if nrange < (1 << 24) {
					nrange <<= 8
					code = (code << 8) | uint32(buf[pos])
					pos++
				}
			}
			d.nrange = nrange; d.code = code; d.pos = pos
			return m - 8
		}

		val = uint32(lc.choice[1])
		bound = (nrange >> 11) * val
		var bit1 uint32
		if code < bound {
			nrange = bound
			lc.choice[1] = prob(val + (2048-val)>>5)
			bit1 = 0
		} else {
			code -= bound
			nrange -= bound
			lc.choice[1] = prob(val - (val >> 5))
			bit1 = 1
		}
		if nrange < (1 << 24) {
			nrange <<= 8
			code = (code << 8) | uint32(buf[pos])
			pos++
		}

		if bit1 == 0 {
			m := uint32(1)
			probs := lc.mid[posState].probs
			for j := 0; j < 3; j++ {
				val := uint32(probs[m])
				bound := (nrange >> 11) * val
				if code < bound {
					nrange = bound
					probs[m] = prob(val + (2048-val)>>5)
					m <<= 1
				} else {
					code -= bound
					nrange -= bound
					probs[m] = prob(val - (val >> 5))
					m = (m << 1) | 1
				}
				if nrange < (1 << 24) {
					nrange <<= 8
					code = (code << 8) | uint32(buf[pos])
					pos++
				}
			}
			d.nrange = nrange; d.code = code; d.pos = pos
			return m - 8 + 8
		}

		m := uint32(1)
		probs := lc.high.probs
		for j := 0; j < 8; j++ {
			val := uint32(probs[m])
			bound := (nrange >> 11) * val
			if code < bound {
				nrange = bound
				probs[m] = prob(val + (2048-val)>>5)
				m <<= 1
			} else {
				code -= bound
				nrange -= bound
				probs[m] = prob(val - (val >> 5))
				m = (m << 1) | 1
			}
			if nrange < (1 << 24) {
				nrange <<= 8
				code = (code << 8) | uint32(buf[pos])
				pos++
			}
		}
		d.nrange = nrange; d.code = code; d.pos = pos
		return m - 256 + 16
	}

	val := uint32(lc.choice[0])
	bound := (nrange >> 11) * val
	var bit0 uint32
	if code < bound {
		nrange = bound
		lc.choice[0] = prob(val + (2048-val)>>5)
		bit0 = 0
	} else {
		code -= bound
		nrange -= bound
		lc.choice[0] = prob(val - (val >> 5))
		bit0 = 1
	}
	if nrange < (1 << 24) {
		nrange <<= 8
		if pos < limit {
			code = (code << 8) | uint32(buf[pos])
			pos++
		} else {
			d.nrange = nrange; d.code = code; d.pos = pos; d.updateCodeSlow()
			nrange = d.nrange; code = d.code; pos = d.pos; limit = d.limit
		}
	}

	if bit0 == 0 {
		m := uint32(1)
		probs := lc.low[posState].probs
		for j := 0; j < 3; j++ {
			val := uint32(probs[m])
			bound := (nrange >> 11) * val
			if code < bound {
				nrange = bound
				probs[m] = prob(val + (2048-val)>>5)
				m <<= 1
			} else {
				code -= bound
				nrange -= bound
				probs[m] = prob(val - (val >> 5))
				m = (m << 1) | 1
			}
			if nrange < (1 << 24) {
				nrange <<= 8
				if pos < limit {
					code = (code << 8) | uint32(buf[pos])
					pos++
				} else {
					d.nrange = nrange; d.code = code; d.pos = pos; d.updateCodeSlow()
					nrange = d.nrange; code = d.code; pos = d.pos; limit = d.limit
				}
			}
		}
		d.nrange = nrange; d.code = code; d.pos = pos
		return m - 8
	}

	val = uint32(lc.choice[1])
	bound = (nrange >> 11) * val
	var bit1 uint32
	if code < bound {
		nrange = bound
		lc.choice[1] = prob(val + (2048-val)>>5)
		bit1 = 0
	} else {
		code -= bound
		nrange -= bound
		lc.choice[1] = prob(val - (val >> 5))
		bit1 = 1
	}
	if nrange < (1 << 24) {
		nrange <<= 8
		if pos < limit {
			code = (code << 8) | uint32(buf[pos])
			pos++
		} else {
			d.nrange = nrange; d.code = code; d.pos = pos; d.updateCodeSlow()
			nrange = d.nrange; code = d.code; pos = d.pos; limit = d.limit
		}
	}

	if bit1 == 0 {
		m := uint32(1)
		probs := lc.mid[posState].probs
		for j := 0; j < 3; j++ {
			val := uint32(probs[m])
			bound := (nrange >> 11) * val
			if code < bound {
				nrange = bound
				probs[m] = prob(val + (2048-val)>>5)
				m <<= 1
			} else {
				code -= bound
				nrange -= bound
				probs[m] = prob(val - (val >> 5))
				m = (m << 1) | 1
			}
			if nrange < (1 << 24) {
				nrange <<= 8
				if pos < limit {
					code = (code << 8) | uint32(buf[pos])
					pos++
				} else {
					d.nrange = nrange; d.code = code; d.pos = pos; d.updateCodeSlow()
					nrange = d.nrange; code = d.code; pos = d.pos; limit = d.limit
				}
			}
		}
		d.nrange = nrange; d.code = code; d.pos = pos
		return m - 8 + 8
	}

	m := uint32(1)
	probs := lc.high.probs
	for j := 0; j < 8; j++ {
		val := uint32(probs[m])
		bound := (nrange >> 11) * val
		if code < bound {
			nrange = bound
			probs[m] = prob(val + (2048-val)>>5)
			m <<= 1
		} else {
			code -= bound
			nrange -= bound
			probs[m] = prob(val - (val >> 5))
			m = (m << 1) | 1
		}
		if nrange < (1 << 24) {
			nrange <<= 8
			if pos < limit {
				code = (code << 8) | uint32(buf[pos])
				pos++
			} else {
				d.nrange = nrange; d.code = code; d.pos = pos; d.updateCodeSlow()
				nrange = d.nrange; code = d.code; pos = d.pos; limit = d.limit
			}
		}
	}
	d.nrange = nrange; d.code = code; d.pos = pos
	return m - 256 + 16
}

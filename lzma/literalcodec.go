// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

// literalCodec supports the encoding of literal. It provides 768 probability
// values per literal state. The upper 512 probabilities are used with the
// context of a match bit.
type literalCodec struct {
	probs []prob
}

// deepcopy initializes literal codec c as a deep copy of the source.
func (c *literalCodec) deepcopy(src *literalCodec) {
	if c == src {
		return
	}
	c.probs = make([]prob, len(src.probs))
	copy(c.probs, src.probs)
}

// init initializes the literal codec.
func (c *literalCodec) init(lc, lp int) {
	switch {
	case !(minLC <= lc && lc <= maxLC):
		panic("lc out of range")
	case !(minLP <= lp && lp <= maxLP):
		panic("lp out of range")
	}
	c.probs = make([]prob, 0x300<<uint(lc+lp))
	for i := range c.probs {
		c.probs[i] = probInit
	}
}

// Encode encodes the byte s using a range encoder as well as the current LZMA
// encoder state, a match byte and the literal state.
func (c *literalCodec) Encode(e *rangeEncoder, s byte,
	state uint32, match byte, litState uint32,
) (err error) {
	k := litState * 0x300
	probs := c.probs[k : k+0x300]
	symbol := uint32(1)
	r := uint32(s)
	if state >= 7 {
		m := uint32(match)
		for {
			matchBit := (m >> 7) & 1
			m <<= 1
			bit := (r >> 7) & 1
			r <<= 1
			i := ((1 + matchBit) << 8) | symbol
			if err = probs[i].Encode(e, bit); err != nil {
				return
			}
			symbol = (symbol << 1) | bit
			if matchBit != bit {
				break
			}
			if symbol >= 0x100 {
				break
			}
		}
	}
	for symbol < 0x100 {
		bit := (r >> 7) & 1
		r <<= 1
		if err = probs[symbol].Encode(e, bit); err != nil {
			return
		}
		symbol = (symbol << 1) | bit
	}
	return nil
}

// Decode decodes a literal byte using the range decoder as well as the LZMA
// state, a match byte, and the literal state.
func (c *literalCodec) Decode(d *rangeDecoder,
	state uint32, match byte, litState uint32,
) byte {
	k := litState * 0x300
	probs := c.probs[k : k+0x300]
	_ = probs[767] // Bounds check elimination hint
	symbol := uint32(1)

	nrange := d.nrange
	code := d.code
	pos := d.pos
	limit := d.limit
	buf := &d.buf
	if limit-pos >= 8 {
		if state >= 7 {
			m := uint32(match)
			for {
				matchBit := (m >> 7) & 1
				m <<= 1
				i := ((1 + matchBit) << 8) | symbol

				val := uint32(probs[i])
				bound := (nrange >> 11) * val
				var bit uint32
				if code < bound {
					nrange = bound
					probs[i] = prob(val + (2048-val)>>5)
					bit = 0
				} else {
					code -= bound
					nrange -= bound
					probs[i] = prob(val - (val >> 5))
					bit = 1
				}
				if nrange < (1 << 24) {
					nrange <<= 8
					code = (code << 8) | uint32(buf[pos])
					pos++
				}

				symbol = (symbol << 1) | bit
				if matchBit != bit {
					break
				}
				if symbol >= 0x100 {
					break
				}
			}
		}
		for symbol < 0x100 {
			val := uint32(probs[symbol])
			bound := (nrange >> 11) * val
			var bit uint32
			if code < bound {
				nrange = bound
				probs[symbol] = prob(val + (2048-val)>>5)
				bit = 0
			} else {
				code -= bound
				nrange -= bound
				probs[symbol] = prob(val - (val >> 5))
				bit = 1
			}
			if nrange < (1 << 24) {
				nrange <<= 8
				code = (code << 8) | uint32(buf[pos])
				pos++
			}
			symbol = (symbol << 1) | bit
		}
		d.nrange = nrange
		d.code = code
		d.pos = pos
		return byte(symbol - 0x100)
	}

	if state >= 7 {
		m := uint32(match)
		for {
			matchBit := (m >> 7) & 1
			m <<= 1
			i := ((1 + matchBit) << 8) | symbol

			val := uint32(probs[i])
			bound := (nrange >> 11) * val
			var bit uint32
			if code < bound {
				nrange = bound
				probs[i] = prob(val + (2048-val)>>5)
				bit = 0
			} else {
				code -= bound
				nrange -= bound
				probs[i] = prob(val - (val >> 5))
				bit = 1
			}
			if nrange < (1 << 24) {
				nrange <<= 8
				if pos < limit {
					code = (code << 8) | uint32(buf[pos])
					pos++
				} else {
					d.nrange = nrange
					d.code = code
					d.pos = pos
					d.updateCodeSlow()
					nrange = d.nrange
					code = d.code
					pos = d.pos
					limit = d.limit
				}
			}

			symbol = (symbol << 1) | bit
			if matchBit != bit {
				break
			}
			if symbol >= 0x100 {
				break
			}
		}
	}
	for symbol < 0x100 {
		val := uint32(probs[symbol])
		bound := (nrange >> 11) * val
		var bit uint32
		if code < bound {
			nrange = bound
			probs[symbol] = prob(val + (2048-val)>>5)
			bit = 0
		} else {
			code -= bound
			nrange -= bound
			probs[symbol] = prob(val - (val >> 5))
			bit = 1
		}
		if nrange < (1 << 24) {
			nrange <<= 8
			if pos < limit {
				code = (code << 8) | uint32(buf[pos])
				pos++
			} else {
				d.nrange = nrange
				d.code = code
				d.pos = pos
				d.updateCodeSlow()
				nrange = d.nrange
				code = d.code
				pos = d.pos
				limit = d.limit
			}
		}
		symbol = (symbol << 1) | bit
	}
	d.nrange = nrange
	d.code = code
	d.pos = pos
	return byte(symbol - 0x100)
}

// minLC and maxLC define the range for LC values.
const (
	minLC = 0
	maxLC = 8
)

// minLC and maxLC define the range for LP values.
const (
	minLP = 0
	maxLP = 4
)

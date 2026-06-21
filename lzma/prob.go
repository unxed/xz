// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

// movebits defines the number of bits used for the updates of probability
// values.
const movebits = 5

// probbits defines the number of bits of a probability value.
const probbits = 11

// probInit defines 0.5 as initial value for prob values.
const probInit prob = 1 << (probbits - 1)

// Type prob represents probabilities. The type can also be used to encode and
// decode single bits.
type prob uint16

// Dec decreases the probability. The decrease is proportional to the
// probability value.
func (p *prob) dec() {
	*p -= *p >> movebits
}

// Inc increases the probability. The Increase is proportional to the
// difference of 1 and the probability value.
func (p *prob) inc() {
	*p += ((1 << probbits) - *p) >> movebits
}

// Computes the new bound for a given range using the probability value.
func (p prob) bound(r uint32) uint32 {
	return (r >> probbits) * uint32(p)
}

// Bits returns 1. One is the number of bits that can be encoded or decoded
// with a single prob value.
func (p prob) Bits() int {
	return 1
}

// Encode encodes the least-significant bit of v. Note that the p value will be
// changed.
func (p *prob) Encode(e *rangeEncoder, v uint32) error {
	return e.EncodeBit(v, p)
}

// Decode decodes a single bit. Note that the p value will change.
func (p *prob) Decode(d *rangeDecoder) uint32 {
	val := uint32(*p)
	bound := (d.nrange >> 11) * val
	if d.code < bound {
		d.nrange = bound
		*p = prob(val + (2048-val)>>5)
		if d.nrange < (1 << 24) {
			d.nrange <<= 8
			if d.pos < d.limit {
				d.code = (d.code << 8) | uint32(d.buf[d.pos])
				d.pos++
			} else {
				d.updateCodeSlow()
			}
		}
		return 0
	}
	d.code -= bound
	d.nrange -= bound
	*p = prob(val - (val >> 5))
	if d.nrange < (1 << 24) {
		d.nrange <<= 8
		if d.pos < d.limit {
			d.code = (d.code << 8) | uint32(d.buf[d.pos])
			d.pos++
		} else {
			d.updateCodeSlow()
		}
	}
	return 1
}

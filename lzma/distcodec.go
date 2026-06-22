// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

// Constants used by the distance codec.
const (
	// minimum supported distance
	minDistance = 1
	// maximum supported distance, value is used for the eos marker.
	maxDistance = 1 << 32
	// number of the supported len states
	lenStates = 4
	// start for the position models
	startPosModel = 4
	// first index with align bits support
	endPosModel = 14
	// bits for the position slots
	posSlotBits = 6
	// number of align bits
	alignBits = 4
)

// distCodec provides encoding and decoding of distance values.
type distCodec struct {
	posSlotCodecs [lenStates]treeCodec
	posModel      [endPosModel - startPosModel]treeReverseCodec
	alignCodec    treeReverseCodec
}

// deepcopy initializes dc as deep copy of the source.
func (dc *distCodec) deepcopy(src *distCodec) {
	if dc == src {
		return
	}
	for i := range dc.posSlotCodecs {
		dc.posSlotCodecs[i].deepcopy(&src.posSlotCodecs[i])
	}
	for i := range dc.posModel {
		dc.posModel[i].deepcopy(&src.posModel[i])
	}
	dc.alignCodec.deepcopy(&src.alignCodec)
}

// newDistCodec creates a new distance codec.
func (dc *distCodec) init() {
	for i := range dc.posSlotCodecs {
		dc.posSlotCodecs[i] = makeTreeCodec(posSlotBits)
	}
	for i := range dc.posModel {
		posSlot := startPosModel + i
		bits := (posSlot >> 1) - 1
		dc.posModel[i] = makeTreeReverseCodec(bits)
	}
	dc.alignCodec = makeTreeReverseCodec(alignBits)
}

// lenState converts the value l to a supported lenState value.
func lenState(l uint32) uint32 {
	if l >= lenStates {
		l = lenStates - 1
	}
	return l
}

// Encode encodes the distance using the parameter l. Dist can have values from
// the full range of uint32 values. To get the distance offset the actual match
// distance has to be decreased by 1. A distance offset of 0xffffffff (eos)
// indicates the end of the stream.
func (dc *distCodec) Encode(e *rangeEncoder, dist uint32, l uint32) (err error) {
	// Compute the posSlot using nlz32
	var posSlot uint32
	var bits uint32
	if dist < startPosModel {
		posSlot = dist
	} else {
		bits = uint32(30 - nlz32(dist))
		posSlot = startPosModel - 2 + (bits << 1)
		posSlot += (dist >> uint(bits)) & 1
	}

	if err = dc.posSlotCodecs[lenState(l)].Encode(e, posSlot); err != nil {
		return
	}

	switch {
	case posSlot < startPosModel:
		return nil
	case posSlot < endPosModel:
		tc := &dc.posModel[posSlot-startPosModel]
		return tc.Encode(dist, e)
	}
	dic := directCodec(bits - alignBits)
	if err = dic.Encode(e, dist>>alignBits); err != nil {
		return
	}
	return dc.alignCodec.Encode(dist, e)
}

// Decode decodes the distance offset using the parameter l. The dist value
// 0xffffffff (eos) indicates the end of the stream. Add one to the distance
// offset to get the actual match distance.
func (dc *distCodec) Decode(d *rangeDecoder, l uint32) uint32 {
	nrange := d.nrange
	code := d.code
	pos := d.pos
	limit := d.limit
	buf := &d.buf

	m := uint32(1)
	probs := dc.posSlotCodecs[lenState(l)].probs
	for j := 0; j < 6; j++ {
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
	posSlot := m - 64

	if posSlot < startPosModel {
		d.nrange = nrange; d.code = code; d.pos = pos
		return posSlot
	}

	bits := (posSlot >> 1) - 1
	dist := (2 | (posSlot & 1)) << bits

	if posSlot < endPosModel {
		probs = dc.posModel[posSlot-startPosModel].probs
		m = 1
		var v uint32
		for j := uint32(0); j < bits; j++ {
			val := uint32(probs[m])
			bound := (nrange >> 11) * val
			var bit uint32
			if code < bound {
				nrange = bound
				probs[m] = prob(val + (2048-val)>>5)
				bit = 0
			} else {
				code -= bound
				nrange -= bound
				probs[m] = prob(val - (val >> 5))
				bit = 1
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
			m = (m << 1) | bit
			v |= bit << j
		}
		dist += v
		d.nrange = nrange; d.code = code; d.pos = pos
		return dist
	}

	directBits := bits - alignBits
	var v uint32
	for j := uint32(0); j < directBits; j++ {
		nrange >>= 1
		code -= nrange
		t := 0 - (code >> 31)
		code += nrange & t
		v = (v << 1) | ((t + 1) & 1)

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
	dist += v << alignBits

	probs = dc.alignCodec.probs
	m = 1
	v = 0
	for j := uint32(0); j < alignBits; j++ {
		val := uint32(probs[m])
		bound := (nrange >> 11) * val
		var bit uint32
		if code < bound {
			nrange = bound
			probs[m] = prob(val + (2048-val)>>5)
			bit = 0
		} else {
			code -= bound
			nrange -= bound
			probs[m] = prob(val - (val >> 5))
			bit = 1
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
		m = (m << 1) | bit
		v |= bit << j
	}
	dist += v

	d.nrange = nrange; d.code = code; d.pos = pos
	return dist
}

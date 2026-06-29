// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/unxed/xz/internal/hash"
)

/* For compression we need to find byte sequences that match the byte
 * sequence at the dictionary head. A hash table is a simple method to
 * provide this capability.
 */

// maxMatches limits the number of matches requested from the Matches
// function. This controls the speed of the overall encoding.
const maxMatches = 16

// shortDists defines the number of short distances supported by the
// implementation.
const shortDists = 8

// The minimum is somehow arbitrary but the maximum is limited by the
// memory requirements of the hash table.
const (
	minTableExponent = 9
	maxTableExponent = 20
)

// newRoller contains the function used to create an instance of the
// hash.Roller.
var newRoller = func(n int) *hash.CyclicPoly { return hash.NewCyclicPoly(n) }

// hashTable stores the hash table including the rolling hash method.
//
// We implement chained hashing into a circular buffer. Each entry in
// the circular buffer stores the delta distance to the next position with a
// word that has the same hash value.
type hashTable struct {
	dict *encoderDict
	// actual hash table
	t []int64
	// circular list data with the offset to the next word
	data  []uint32
	front int
	// mask for computing the index for the hash table
	mask uint64
	// hash offset; initial value is -int64(wordLen)
	hoff int64
	// length of the hashed word
	wordLen int
	// hash roller for computing arbitrary hashes
	hr *hash.CyclicPoly

	// Inlined CyclicPoly state for t.wr
	cpH uint64
	cpP [4]uint64
	cpI int
	cpMask int
	cpShift uint

	// preallocated slices
	p         [maxMatches]int64
    distances [maxMatches + shortDists]int
}

// hashTableExponent derives the hash table exponent from the dictionary
// capacity.
func hashTableExponent(n uint32) int {
	e := 30 - nlz32(n)
	switch {
	case e < minTableExponent:
		e = minTableExponent
	case e > maxTableExponent:
		e = maxTableExponent
	}
	return e
}

// newHashTable creates a new hash table for words of length wordLen
func newHashTable(capacity int, wordLen int) (t *hashTable, err error) {
	if !(0 < capacity) {
		return nil, errors.New(
			"newHashTable: capacity must not be negative")
	}
	exp := hashTableExponent(uint32(capacity))
	if !(1 <= wordLen && wordLen <= 4) {
		return nil, errors.New("newHashTable: " +
			"argument wordLen out of range")
	}
	n := 1 << uint(exp)
	if n <= 0 {
		panic("newHashTable: exponent is too large")
	}
	t = &hashTable{
		t:       make([]int64, n),
		data:    make([]uint32, capacity),
		mask:    (uint64(1) << uint(exp)) - 1,
		hoff:    -int64(wordLen),
		wordLen: wordLen,
		cpMask:  wordLen - 1,
		cpShift: uint(wordLen - 1),
	}
	for i := 0; i < 4; i++ {
		t.cpP[i] = 0
	}
	return t, nil
}

func (t *hashTable) SetDict(d *encoderDict) { t.dict = d }
// Reset clears the hash table and offsets for reuse.
func (t *hashTable) Reset() {
	for i := range t.t {
		t.t[i] = 0
	}
	for i := range t.data {
		t.data[i] = 0
	}
	t.front = 0
	t.hoff = -int64(t.wordLen)
	t.cpH = 0
	t.cpI = 0
	t.cpMask = t.wordLen - 1
	t.cpShift = uint(t.wordLen - 1)
	for i := 0; i < 4; i++ {
		t.cpP[i] = 0
	}
}

// buffered returns the number of bytes that are currently hashed.
func (t *hashTable) buffered() int {
	n := t.hoff + 1
	switch {
	case n <= 0:
		return 0
	case n >= int64(len(t.data)):
		return len(t.data)
	}
	return int(n)
}

// addIndex adds n to an index ensuring that is stays inside the
// circular buffer for the hash chain.
func (t *hashTable) addIndex(i, n int) int {
	i += n - len(t.data)
	if i < 0 {
		i += len(t.data)
	}
	return i
}

// putDelta puts the delta instance at the current front of the circular
// chain buffer.
func (t *hashTable) putDelta(delta uint32) {
	t.data[t.front] = delta
	t.front = t.addIndex(t.front, 1)
}

// putEntry puts a new entry into the hash table. If there is already a
// value stored it is moved into the circular chain buffer.
func (t *hashTable) putEntry(h uint64, pos int64) {
	if pos < 0 {
		return
	}
	i := h & t.mask
	old := t.t[i] - 1
	t.t[i] = pos + 1
	var delta int64
	if old >= 0 {
		delta = pos - old
		if delta > 1<<32-1 || delta > int64(t.buffered()) {
			delta = 0
		}
	}
	t.putDelta(uint32(delta))
}

// WriteByte converts a single byte into a hash and puts them into the hash
// table.
func (t *hashTable) WriteByte(b byte) error {
	y := hash.HashValues[b]
	t.cpH ^= ror(t.cpP[t.cpI], t.cpShift)
	t.cpH = ror(t.cpH, 1) ^ y
	t.cpP[t.cpI] = y
	t.cpI = (t.cpI + 1) & t.cpMask

	t.hoff++
	t.putEntry(t.cpH, t.hoff)
	return nil
}

// Write converts the bytes provided into hash tables and stores the
// abbreviated offsets into the hash table. The method will never return an
// error.
func (t *hashTable) Write(p []byte) (n int, err error) {
	for _, b := range p {
		y := hash.HashValues[b]
		t.cpH ^= ror(t.cpP[t.cpI], t.cpShift)
		t.cpH = ror(t.cpH, 1) ^ y
		t.cpP[t.cpI] = y
		t.cpI = (t.cpI + 1) & t.cpMask

		t.hoff++
		t.putEntry(t.cpH, t.hoff)
	}
	return len(p), nil
}

// getMatches the matches for a specific hash. The functions returns the
// number of positions found.
//
// TODO: Make a getDistances because that we are actually interested in.
func (t *hashTable) getMatches(h uint64, positions []int64) (n int) {
	return getMatches(t.t, t.data, t.front, t.mask, t.hoff, h, positions)
}

// hash computes the rolling hash for the word stored in p. For correct
// results its length must be equal to t.wordLen.
func ror(x uint64, s uint) uint64 {
	return (x >> s) | (x << (64 - s))
}

func (t *hashTable) hash(p []byte) uint64 {
	h := hash.HashValues[p[0]]
	switch t.wordLen {
	case 4:
		h = ror(h, 1) ^ hash.HashValues[p[1]]
		h = ror(h, 1) ^ hash.HashValues[p[2]]
		h = ror(h, 1) ^ hash.HashValues[p[3]]
	case 3:
		h = ror(h, 1) ^ hash.HashValues[p[1]]
		h = ror(h, 1) ^ hash.HashValues[p[2]]
	case 2:
		h = ror(h, 1) ^ hash.HashValues[p[1]]
	}
	return h
}

// Matches fills the positions slice with potential matches. The
// functions returns the number of positions filled into positions. The
// byte slice p must have word length of the hash table.
func (t *hashTable) Matches(p []byte, positions []int64) int {
	if len(p) != t.wordLen {
		panic(fmt.Errorf(
			"byte slice must have length %d", t.wordLen))
	}
	h := t.hash(p)
	return t.getMatches(h, positions)
}

// NextOp identifies the next operation using the hash table.
func (t *hashTable) NextOp(rep [4]uint32) operation {
	data := t.dict.data[:maxMatchLen]
	n, _ := t.dict.buf.Peek(data)
	data = data[:n]
	
	var p []int64
	if n < t.wordLen {
		p = t.p[:0]
	} else {
		p = t.p[:maxMatches]
		k := t.Matches(data[:t.wordLen], p)
		p = p[:k]
	}

	head := t.dict.head
	dists := append(t.distances[:0], 1, 2, 3, 4, 5, 6, 7, 8)
	for _, pos := range p {
		dis := int(head - pos)
		if dis > shortDists {
			dists = append(dists, dis)
		}
	}

	validDists := dists[:0]
	dictLen := t.dict.DictLen()
	for _, dist := range dists {
		if dist <= dictLen {
			validDists = append(validDists, dist)
		}
	}

	if len(data) >= 5 {
		nextIdx := t.hash(data[1:5]) & t.mask
		prefetch(unsafe.Pointer(&t.t[nextIdx]))
	}

	bestDist, bestLen := findBestMatch(t.dict.buf.data, t.dict.buf.rear, data, validDists, rep[0])

	if bestLen == 0 {
		return operation{distance: 0, n: 1, b: data[0]}
	}
	return operation{distance: int64(bestDist), n: bestLen}
}

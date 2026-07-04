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
const maxMatches = 32

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
	minimalMode bool
	litRun      int

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

// SetMinimalMode enables or disables the fast, minimal-compression mode.
func (t *hashTable) SetMinimalMode(minimal bool) { t.minimalMode = minimal }
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
	t.litRun = 0
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

// WriteByte converts a single byte into a hash and puts them into the hash
// table.
func (t *hashTable) WriteByte(b byte) error {
	y := hash.HashValues[b]
	oldP := t.cpP[t.cpI]
	t.cpH ^= (oldP >> t.cpShift) | (oldP << (64 - t.cpShift))
	t.cpH = ((t.cpH >> 1) | (t.cpH << 63)) ^ y
	t.cpP[t.cpI] = y
	t.cpI = (t.cpI + 1) & t.cpMask

	t.hoff++
	if t.hoff >= 0 {
		i := t.cpH & t.mask
		old := t.t[i] - 1
		t.t[i] = t.hoff + 1
		var delta int64
		if old >= 0 {
			delta = t.hoff - old
			if delta >= int64(len(t.data)) {
				delta = 0
			}
		}
		t.data[t.front] = uint32(delta)
		t.front++
		if t.front == len(t.data) {
			t.front = 0
		}
	}
	return nil
}

// Write converts the bytes provided into hash tables and stores the
// abbreviated offsets into the hash table. The method will never return an
// error.
func (t *hashTable) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}
	mask := t.mask
	table := t.t
	data := t.data
	front := t.front
	hoff := t.hoff
	cpH := t.cpH
	cpI := t.cpI
	cpShift := t.cpShift
	cpMask := t.cpMask

	// Разделение цикла (Loop Splitting) для длинных совпадений (>= 4 байт).
	// Позволяет компилятору и процессору идеально предсказывать ветвления (branch prediction),
	// полностью убирая if-проверки из горячего цикла и отключая L3 cache misses для p[1:].
	// Для коротких строк или тестов (wordLen < 4) используется классический полный цикл.
	if t.wordLen == 4 && len(p) >= 4 {
		// Байт 0 (вставляем полностью)
		b := p[0]
		y := hash.HashValues[b]
		oldP := t.cpP[cpI]
		cpH ^= (oldP >> cpShift) | (oldP << (64 - cpShift))
		cpH = ((cpH >> 1) | (cpH << 63)) ^ y
		t.cpP[cpI] = y
		cpI = (cpI + 1) & cpMask

		hoff++
		if hoff >= 0 {
			i := cpH & mask
			old := table[i] - 1
			table[i] = hoff + 1
			var delta int64
			if old >= 0 {
				delta = hoff - old
				if delta >= int64(len(data)) {
					delta = 0
				}
			}
			data[front] = uint32(delta)
			front++
			if front == len(data) {
				front = 0
			}
		}

		// Байты 1..len(p)-1 (только обновляем катящийся хеш, пропуская запись в память)
		for i := 1; i < len(p); i++ {
			b := p[i]
			y := hash.HashValues[b]
			oldP := t.cpP[cpI]
			cpH ^= (oldP >> cpShift) | (oldP << (64 - cpShift))
			cpH = ((cpH >> 1) | (cpH << 63)) ^ y
			t.cpP[cpI] = y
			cpI = (cpI + 1) & cpMask

			hoff++
			if hoff >= 0 {
				front++
				if front == len(data) {
					front = 0
				}
			}
		}
	} else {
		// Классический полный цикл для коротких совпадений и тестов
		for _, b := range p {
			y := hash.HashValues[b]
			oldP := t.cpP[cpI]
			cpH ^= (oldP >> cpShift) | (oldP << (64 - cpShift))
			cpH = ((cpH >> 1) | (cpH << 63)) ^ y
			t.cpP[cpI] = y
			cpI = (cpI + 1) & cpMask

			hoff++
			if hoff >= 0 {
				i := cpH & mask
				old := table[i] - 1
				table[i] = hoff + 1
				var delta int64
				if old >= 0 {
					delta = hoff - old
					if delta >= int64(len(data)) {
						delta = 0
					}
				}
				data[front] = uint32(delta)
				front++
				if front == len(data) {
					front = 0
				}
			}
		}
	}

	t.front = front
	t.hoff = hoff
	t.cpH = cpH
	t.cpI = cpI
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

	var skipMask int
	if t.minimalMode {
		skipMask = 15
	} else if t.litRun >= 256 {
		skipMask = 15
	} else if t.litRun >= 128 {
		skipMask = 7
	} else if t.litRun >= 64 {
		skipMask = 3
	}
	if skipMask > 0 && (t.litRun&skipMask) != 0 {
		t.litRun++
		return operation{distance: 0, n: 1, b: data[0]}
	}

	var p []int64
	numMatches := maxMatches
	if t.minimalMode {
		numMatches = 1
	} else if t.litRun >= 64 {
		numMatches = 1
	} else if t.litRun >= 32 {
		numMatches = 4
	} else if t.litRun >= 16 {
		numMatches = 8
	} else if t.litRun >= 8 {
		numMatches = 16
	}

	if n < t.wordLen {
		p = t.p[:0]
	} else {
		p = t.p[:numMatches]
		h := hash.HashValues[data[0]]
		if t.wordLen == 4 {
			h = ror(h, 1) ^ hash.HashValues[data[1]]
			h = ror(h, 1) ^ hash.HashValues[data[2]]
			h = ror(h, 1) ^ hash.HashValues[data[3]]
		} else if t.wordLen == 3 {
			h = ror(h, 1) ^ hash.HashValues[data[1]]
			h = ror(h, 1) ^ hash.HashValues[data[2]]
		} else if t.wordLen == 2 {
			h = ror(h, 1) ^ hash.HashValues[data[1]]
		}
		k := getMatches(t.t, t.data, t.front, t.mask, t.hoff, h, p)
		p = p[:k]
	}

	head := t.dict.head
	dists := t.distances[:0]
	if t.minimalMode || t.litRun >= 64 {
		// dists остаётся пустым
	} else if t.litRun >= 32 {
		// При среднем litRun отключаем короткие дистанции 1..8, оставляя только ценные rep
		dists = append(dists, int(rep[0]+1), int(rep[1]+1), int(rep[2]+1), int(rep[3]+1))
	} else {
		dists = append(dists, int(rep[0]+1), int(rep[1]+1), int(rep[2]+1), int(rep[3]+1))
		dists = append(dists, 1, 2, 3, 4, 5, 6, 7, 8)
	}
    
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

	if len(data) >= 5 && !(t.minimalMode || t.litRun >= 64) {
		nextIdx := t.hash(data[1:5]) & t.mask
		prefetch(unsafe.Pointer(&t.t[nextIdx]))
	}

	bestDist, bestLen := findBestMatch(t.dict.buf.data, t.dict.buf.rear, data, validDists, rep[0])

	if bestLen == 0 {
		t.litRun++
		return operation{distance: 0, n: 1, b: data[0]}
	}
	t.litRun = 0
	return operation{distance: int64(bestDist), n: bestLen}
}

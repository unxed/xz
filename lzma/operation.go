// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"fmt"
	"unicode"
)

// operation represents an operation on the dictionary during encoding or
// decoding.
type opType uint8

const (
	opTypeLit opType = iota
	opTypeMatch
)

// operation represents an operation on the dictionary during encoding or decoding.
type operation struct {
	distance int64
	length   int32
	lit      byte
	typ      opType
}

func (o operation) Len() int {
	if o.typ == opTypeMatch {
		return int(o.length)
	}
	return 1
}

func (o *operation) String() string {
	if o.typ == opTypeMatch {
		return fmt.Sprintf("M{%d,%d}", o.distance, o.length)
	}
	var c byte
	if unicode.IsPrint(rune(o.lit)) {
		c = o.lit
	} else {
		c = '.'
	}
	return fmt.Sprintf("L{%c/%02x}", c, o.lit)
}

// rep represents a repetition at the given distance and the given length
type match struct {
	distance int64
	n        int
}

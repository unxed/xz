// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"fmt"
	"unicode"
)

type operation struct {
	distance int64
	n        int
	b        byte
}

func (o operation) Len() int {
	return o.n
}

func (o operation) String() string {
	if o.distance == 0 {
		var c byte
		if unicode.IsPrint(rune(o.b)) {
			c = o.b
		} else {
			c = '.'
		}
		return fmt.Sprintf("L{%c/%02x}", c, o.b)
	}
	return fmt.Sprintf("M{%d,%d}", o.distance, o.n)
}

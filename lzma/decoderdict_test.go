// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestNewDecoderDict(t *testing.T) {
	if _, err := newDecoderDict(0); err == nil {
		t.Fatalf("no error for zero dictionary capacity")
	}
	if _, err := newDecoderDict(8); err != nil {
		t.Fatalf("error %s", err)
	}
}

// TestDecoderDictWriteMatch compares the dictionary content produced by
// writeMatch with a byte-by-byte copy. Small capacities force matches
// wrapping around the end of the circular buffer as well as distances up
// to the full dictionary length.
func TestDecoderDictWriteMatch(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, dictCap := range []int{1, 2, 17, 300, 1000} {
		d, err := newDecoderDict(dictCap)
		if err != nil {
			t.Fatalf("newDecoderDict(%d) error %s", dictCap, err)
		}
		var want []byte
		read := 0
		p := make([]byte, dictCap)
		for i := 0; i < 20000; i++ {
			if d.Available() == 0 || rng.Intn(4) == 0 {
				k, err := d.Read(p[:rng.Intn(len(p))+1])
				if err != nil {
					t.Fatalf("Read error %s", err)
				}
				if !bytes.Equal(p[:k], want[read:read+k]) {
					t.Fatalf("dictCap %d: read %d bytes at "+
						"offset %d differ", dictCap, k, read)
				}
				read += k
				continue
			}
			if d.dictLen() == 0 || rng.Intn(3) == 0 {
				c := byte(rng.Intn(256))
				if err = d.WriteByte(c); err != nil {
					t.Fatalf("WriteByte error %s", err)
				}
				want = append(want, c)
				continue
			}
			dist := rng.Intn(d.dictLen()) + 1
			n := maxMatchLen
			if a := d.Available(); a < n {
				n = a
			}
			n = rng.Intn(n) + 1
			if err = d.writeMatch(int64(dist), n); err != nil {
				t.Fatalf("writeMatch(%d, %d) error %s",
					dist, n, err)
			}
			for j := 0; j < n; j++ {
				want = append(want, want[len(want)-dist])
			}
		}
		for {
			k, _ := d.Read(p)
			if k == 0 {
				break
			}
			if !bytes.Equal(p[:k], want[read:read+k]) {
				t.Fatalf("dictCap %d: read %d bytes at offset %d"+
					" differ", dictCap, k, read)
			}
			read += k
		}
		if read != len(want) {
			t.Fatalf("dictCap %d: read %d bytes; want %d",
				dictCap, read, len(want))
		}
	}
}

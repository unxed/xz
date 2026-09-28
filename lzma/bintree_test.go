// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"bytes"
	"io"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/unxed/xz/internal/randtxt"
)

func TestBinTree_Find(t *testing.T) {
	bt, err := newBinTree(30)
	if err != nil {
		t.Fatal(err)
	}
	const s = "Klopp feiert mit Liverpool seinen hoechsten SiegSieg"
	n, err := io.WriteString(bt, s)
	if err != nil {
		t.Fatalf("WriteString error %s", err)
	}
	if n != len(s) {
		t.Fatalf("WriteString returned %d; want %d", n, len(s))
	}

	/* dump info writes the complete tree
	if err = bt.dump(os.Stdout); err != nil {
		t.Fatalf("bt.dump error %s", err)
	}
	*/

	tests := []string{"Sieg", "Sieb", "Simu"}
	for _, c := range tests {
		x := xval([]byte(c))
		a, b := bt.search(bt.root, x)
		t.Logf("%q: a, b == %d, %d", c, a, b)
	}
}

func TestBinTree_PredSucc(t *testing.T) {
	bt, err := newBinTree(30)
	if err != nil {
		t.Fatal(err)
	}
	const s = "Klopp feiert mit Liverpool seinen hoechsten Sieg."
	n, err := io.WriteString(bt, s)
	if err != nil {
		t.Fatalf("WriteString error %s", err)
	}
	if n != len(s) {
		t.Fatalf("WriteString returned %d; want %d", n, len(s))
	}
	for v := bt.min(bt.root); v != null; v = bt.succ(v) {
		t.Log(dumpX(bt.node[v].x))
	}
	t.Log("")
	for v := bt.max(bt.root); v != null; v = bt.pred(v) {
		t.Log(dumpX(bt.node[v].x))
	}
}

func TestBinTree_Cycle(t *testing.T) {
	buf := new(bytes.Buffer)
	w, err := Writer2Config{
		DictCap: 4096,
		Matcher: BinaryTree,
	}.NewWriter2(buf)
	if err != nil {
		t.Fatalf("NewWriter error %s", err)
	}
	// const txtlen = 1024
	const txtlen = 10000
	io.CopyN(buf, randtxt.NewReader(rand.NewSource(42)), txtlen)
	txt := buf.String()
	buf.Reset()
	n, err := io.Copy(w, strings.NewReader(txt))
	if err != nil {
		t.Fatalf("Compressing copy error %s", err)
	}
	if n != txtlen {
		t.Fatalf("Compressing data length %d; want %d", n, txtlen)
	}
	if err = w.Close(); err != nil {
		t.Fatalf("w.Close error %s", err)
	}
	t.Logf("buf.Len() %d", buf.Len())
	r, err := Reader2Config{DictCap: 4096}.NewReader2(buf)
	if err != nil {
		t.Fatalf("NewReader error %s", err)
	}
	out := new(bytes.Buffer)
	n, err = io.Copy(out, r)
	if err != nil {
		t.Fatalf("Decompressing copy error %s after %d bytes", err, n)
	}
	if n != txtlen {
		t.Fatalf("Decompression data length %d; want %d", n, txtlen)
	}
	if txt != out.String() {
		t.Fatal("decompressed data differs from original")
	}
}

// TestBinTree_PeriodicInsertBounded reproduces the degenerate case from
// https://github.com/unxed/zipper/issues/32: a block whose content
// repeats with a short period relative to the 4-byte match-finder word
// (here gcd(wordLen, len(pattern)) == 1) used to collapse almost every
// insertion onto a handful of chains, each insertion/removal costing
// O(current chain length) - an O(n^2)-class cost in the block size that
// hung TestWriter2_AdaptiveEffortRoundtrip for minutes in CI. With
// add's and remove's depth cap (maxTreeDepth), writing this data
// through a small ring buffer - so the buffer wraps, and remove is
// exercised as much as add - must stay fast.
func TestBinTree_PeriodicInsertBounded(t *testing.T) {
	const capacity = 4096
	bt, err := newBinTree(capacity)
	if err != nil {
		t.Fatal(err)
	}
	pattern := []byte("redundant-chunk-data-block-")
	// 28 bytes * 12000 == 336000 bytes; wraps the 4096-node ring
	// buffer about 80 times.
	data := bytes.Repeat(pattern, 12000)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := bt.Write(data); err != nil {
			t.Errorf("bt.Write error %s", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("binTree.Write on periodic data did not return within 30s; " +
			"insertion/removal cost is no longer bounded (see zipper#32)")
	}
}

// TestWriter2_BinaryTreePeriodicRoundtrip is the round-trip counterpart
// of TestBinTree_PeriodicInsertBounded: it compresses the same class of
// periodic, highly redundant input through a real Writer2 configured
// with Matcher: BinaryTree, decompresses it again and checks the
// result matches byte for byte. It both guards against the hang from
// zipper#32 at the Writer2 level (not just the bare binTree) and acts
// as a correctness/round-trip regression check for the now
// depth-capped, no-longer-strictly-ordered tree: match() re-verifies
// every candidate against the real buffer, so losing strict ordering
// beyond maxTreeDepth must never produce wrong output.
func TestWriter2_BinaryTreePeriodicRoundtrip(t *testing.T) {
	pattern := []byte("redundant-chunk-data-block-")
	// 28 bytes * 40000 ~= 1.1 MiB, the same order of magnitude as the
	// block that hung in zipper#32.
	data := bytes.Repeat(pattern, 40000)

	const dictCap = 1 << 20
	buf := new(bytes.Buffer)
	w, err := Writer2Config{
		DictCap: dictCap,
		Matcher: BinaryTree,
	}.NewWriter2(buf)
	if err != nil {
		t.Fatalf("NewWriter2 error %s", err)
	}

	done := make(chan error, 1)
	go func() {
		if _, err := w.Write(data); err != nil {
			done <- err
			return
		}
		done <- w.Close()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("compressing periodic data error %s", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("compressing periodic data with BinaryTree did not return " +
			"within 60s (see zipper#32)")
	}

	r, err := Reader2Config{DictCap: dictCap}.NewReader2(buf)
	if err != nil {
		t.Fatalf("NewReader2 error %s", err)
	}
	out := new(bytes.Buffer)
	n, err := io.Copy(out, r)
	if err != nil {
		t.Fatalf("decompressing copy error %s after %d bytes", err, n)
	}
	if int(n) != len(data) {
		t.Fatalf("decompressed length %d; want %d", n, len(data))
	}
	if !bytes.Equal(out.Bytes(), data) {
		t.Fatal("decompressed data differs from original")
	}
}

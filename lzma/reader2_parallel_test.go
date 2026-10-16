package lzma

import (
	"bytes"
	"io"
	"testing"
	"time"
)

// udSegments builds an LZMA2 stream of n independent segments, each one
// uncompressed chunk with a dictionary reset, and returns it together with
// the expected decoded data.
func udSegments(n, size int) (stream, want []byte) {
	for i := 0; i < n; i++ {
		data := bytes.Repeat([]byte{byte(i)}, size)
		stream = append(stream, 0x01, byte((size-1)>>8), byte(size-1))
		stream = append(stream, data...)
		want = append(want, data...)
	}
	stream = append(stream, 0x00)
	return stream, want
}

func readyCount(pr *parallelReader2) int {
	n := 0
	for _, res := range pr.results {
		select {
		case <-res.ready:
			n++
		default:
		}
	}
	return n
}

func newTestParallelReader2(t *testing.T, stream []byte) *parallelReader2 {
	t.Helper()
	r, err := NewReader2(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	pr, ok := r.(*parallelReader2)
	if !ok {
		t.Fatalf("got %T, want *parallelReader2", r)
	}
	return pr
}

func TestParallelReader2RoundTrip(t *testing.T) {
	stream, want := udSegments(100, 1000)
	pr := newTestParallelReader2(t, stream)
	got, err := io.ReadAll(pr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("decoded data differs: got %d bytes, want %d", len(got), len(want))
	}
	if err := pr.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestParallelReader2ReadAheadBounded checks that workers do not decode the
// whole stream while the reader is idle (unxed/f4#1731: a 7-Zip folder whose
// reader is abandoned kept every core decoding the rest of the folder).
func TestParallelReader2ReadAheadBounded(t *testing.T) {
	const segs = 200
	stream, want := udSegments(segs, 1000)
	pr := newTestParallelReader2(t, stream)
	defer pr.Close()

	if pr.window <= 0 || pr.window >= segs {
		t.Fatalf("window = %d, want 0 < window < %d", pr.window, segs)
	}
	// Let the workers run ahead as far as they are allowed to.
	deadline := time.Now().Add(5 * time.Second)
	for readyCount(pr) < pr.window && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if n := readyCount(pr); n != pr.window {
		t.Fatalf("decoded %d segments ahead of an idle reader, want exactly window %d", n, pr.window)
	}

	// Reading one whole segment lets exactly one more segment start.
	buf := make([]byte, 1000)
	if _, err := io.ReadFull(pr, buf); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for readyCount(pr) < pr.window+1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if n := readyCount(pr); n != pr.window+1 {
		t.Fatalf("after one segment decoded %d, want %d", n, pr.window+1)
	}

	rest, err := io.ReadAll(pr)
	if err != nil {
		t.Fatal(err)
	}
	if got := append(buf, rest...); !bytes.Equal(got, want) {
		t.Fatalf("decoded data differs: got %d bytes, want %d", len(got), len(want))
	}
}

func TestParallelReader2CloseWhileThrottled(t *testing.T) {
	stream, _ := udSegments(200, 1000)
	pr := newTestParallelReader2(t, stream)
	deadline := time.Now().Add(5 * time.Second)
	for readyCount(pr) < pr.window && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	done := make(chan struct{})
	go func() {
		pr.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked while the feeder waited for a slot")
	}
}

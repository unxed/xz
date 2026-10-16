package xz_test

import (
	"bytes"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/unxed/xz"
)

var (
	corpusOnce       sync.Once
	corpusCompressed []byte
	corpusSize       int64
)

func prepareCorpus(b *testing.B) {
	path := os.Getenv("XZ_BENCH_CORPUS")
	if path == "" {
		b.Skip("XZ_BENCH_CORPUS is not set")
	}
	corpusOnce.Do(func() {
		data, err := os.ReadFile(path)
		if err != nil {
			b.Fatalf("reading corpus: %v", err)
		}
		corpusSize = int64(len(data))
		var buf bytes.Buffer
		w, err := xz.NewWriter(&buf)
		if err != nil {
			b.Fatalf("NewWriter: %v", err)
		}
		if _, err := w.Write(data); err != nil {
			b.Fatalf("Write: %v", err)
		}
		if err := w.Close(); err != nil {
			b.Fatalf("Close: %v", err)
		}
		corpusCompressed = buf.Bytes()
	})
}

// BenchmarkDecompressionCorpus measures single-threaded decompression of
// the file named by XZ_BENCH_CORPUS (for instance machine code, whose
// literals and matches are far less predictable than the generated text of
// BenchmarkDecompressionSpeed).
func BenchmarkDecompressionCorpus(b *testing.B) {
	prepareCorpus(b)
	b.SetBytes(corpusSize)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		zr, err := xz.NewReader(bytes.NewReader(corpusCompressed))
		if err != nil {
			b.Fatal(err)
		}
		n, err := io.Copy(io.Discard, zr)
		if err != nil || n != corpusSize {
			b.Fatalf("decoded %d of %d bytes: %v", n, corpusSize, err)
		}
	}
}

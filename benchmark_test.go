package xz_test

import (
    "sync"
	"bytes"
	"io"
	"math/rand"
	"testing"

	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/internal/randtxt"
)

var (
	benchDataOnce sync.Once
	benchCompressedData []byte
	benchOriginalSize int64
)

func prepareBenchData() {
	const size = 20 * 1024 * 1024
	r := io.LimitReader(randtxt.NewReader(rand.NewSource(42)), size)
	data, _ := io.ReadAll(r)
	benchOriginalSize = int64(len(data))
	var buf bytes.Buffer
	w, _ := xz.NewWriter(&buf)
	w.Write(data)
	w.Close()
	benchCompressedData = buf.Bytes()
}

func BenchmarkDecompressionSpeed(b *testing.B) {
	benchDataOnce.Do(prepareBenchData)

	b.ReportAllocs()
	b.SetBytes(benchOriginalSize)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		zr, _ := xz.NewReader(bytes.NewReader(benchCompressedData))
		io.Copy(io.Discard, zr)
	}
}

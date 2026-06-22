package xz_test

import (
	"bytes"
	"fmt"
	"io"
	"testing"
	"time"

	"math/rand"
	"github.com/ulikunitz/xz/internal/randtxt"

	"github.com/ulikunitz/xz"
)

func TestReproduceDecompressionSpeed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow decompression speed test in short mode")
	}
	// Генерируем 20 МБ реалистичных текстовых данных с использованием встроенного триграммного генератора
	const size = 20 * 1024 * 1024

	// Подключаем math/rand и randtxt для генерации реалистичного английского текста
	// var randReader io.Reader
	// randReader инициализируется ниже

	r := io.LimitReader(randtxt.NewReader(rand.NewSource(42)), size)
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("Failed to generate test data: %v", err)
	}

	var compressed bytes.Buffer
	w, err := xz.NewWriter(&compressed)
	if err != nil {
		t.Fatalf("Failed to create writer: %v", err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("Failed to write data: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Failed to close writer: %v", err)
	}

	compressedBytes := compressed.Bytes()
	t.Logf("Compressed size: %.2f MB (Ratio: %.2f%%)",
		float64(len(compressedBytes))/(1024*1024),
		float64(len(compressedBytes))/float64(size)*100)

	// Замеряем время декомпрессии
	start := time.Now()
	input := bytes.NewReader(compressedBytes)
	r, err = xz.NewReader(input)
	if err != nil {
		t.Fatalf("Failed to create reader: %v", err)
	}

	written, err := io.Copy(io.Discard, r)
	if err != nil {
		t.Fatalf("Decompression failed: %v", err)
	}
	elapsed := time.Since(start)

	if written != size {
		t.Errorf("Expected %d bytes, got %d", size, written)
	}

	throughput := float64(size) / (1024 * 1024) / elapsed.Seconds()
	fmt.Printf("\n=== REPRODUCTION RESULTS ===\n")
	fmt.Printf("Decompressed size: %d bytes\n", written)
	fmt.Printf("Time elapsed:      %v\n", elapsed)
	fmt.Printf("Throughput:        %.2f MB/s\n", throughput)
	fmt.Printf("============================\n\n")
}
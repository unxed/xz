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

	"github.com/unxed/xz/internal/randtxt"
)

func TestWriter2(t *testing.T) {
	var buf bytes.Buffer
	w, err := Writer2Config{DictCap: 4096}.NewWriter2(&buf)
	if err != nil {
		t.Fatalf("NewWriter error %s", err)
	}
	n, err := w.Write([]byte{'a'})
	if err != nil {
		t.Fatalf("w.Write([]byte{'a'}) error %s", err)
	}
	if n != 1 {
		t.Fatalf("w.Write([]byte{'a'}) returned %d; want %d", n, 1)
	}
	if err = w.Flush(); err != nil {
		t.Fatalf("w.Flush() error %s", err)
	}
	// check that double Flush doesn't write another chunk
	if err = w.Flush(); err != nil {
		t.Fatalf("w.Flush() error %s", err)
	}
	if err = w.Close(); err != nil {
		t.Fatalf("w.Close() error %s", err)
	}
	p := buf.Bytes()
	want := []byte{1, 0, 0, 'a', 0}
	if !bytes.Equal(p, want) {
		t.Fatalf("bytes written %#v; want %#v", p, want)
	}
}

func TestCycle1(t *testing.T) {
	var buf bytes.Buffer
	w, err := Writer2Config{DictCap: 4096}.NewWriter2(&buf)
	if err != nil {
		t.Fatalf("NewWriter error %s", err)
	}
	n, err := w.Write([]byte{'a'})
	if err != nil {
		t.Fatalf("w.Write([]byte{'a'}) error %s", err)
	}
	if n != 1 {
		t.Fatalf("w.Write([]byte{'a'}) returned %d; want %d", n, 1)
	}
	if err = w.Close(); err != nil {
		t.Fatalf("w.Close() error %s", err)
	}
	r, err := Reader2Config{DictCap: 4096}.NewReader2(&buf)
	if err != nil {
		t.Fatalf("NewReader error %s", err)
	}
	p := make([]byte, 3)
	n, err = r.Read(p)
	t.Logf("n %d error %v", n, err)
}

func TestCycle2(t *testing.T) {
	buf := new(bytes.Buffer)
	w, err := Writer2Config{DictCap: 4096}.NewWriter2(buf)
	if err != nil {
		t.Fatalf("NewWriter error %s", err)
	}
	// const txtlen = 1024
	const txtlen = 2100000
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

func TestWriter2_ParallelCorrectness(t *testing.T) {
	// Генерируем 10 МБ реалистичных текстовых данных
	const size = 10 * 1024 * 1024
	var srcBuf bytes.Buffer
	io.CopyN(&srcBuf, randtxt.NewReader(rand.NewSource(42)), size)
	originalData := srcBuf.Bytes()

	// Хелпер сжатия
	compress := func(concurrency int) ([]byte, error) {
		var out bytes.Buffer
		w, err := Writer2Config{
			DictCap:     1024 * 1024, // Свап-словарь 1 МБ
			Concurrency: concurrency,
		}.NewWriter2(&out)
		if err != nil {
			return nil, err
		}
		_, err = w.Write(originalData)
		if err != nil {
			w.Close()
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}

	// Хелпер распаковки
	decompress := func(compressed []byte) ([]byte, error) {
		r, err := Reader2Config{DictCap: 1024 * 1024}.NewReader2(bytes.NewReader(compressed))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(r)
	}

	// 1. Тест классического однопоточного кодирования
	seqCompressed, err := compress(1)
	if err != nil {
		t.Fatalf("sequential compression failed: %v", err)
	}
	seqDecompressed, err := decompress(seqCompressed)
	if err != nil {
		t.Fatalf("sequential decompression failed: %v", err)
	}
	if !bytes.Equal(originalData, seqDecompressed) {
		t.Error("sequential decompressed data mismatch")
	}

	// 2. Тест нового параллельного кодирования
	parCompressed, err := compress(4)
	if err != nil {
		t.Fatalf("parallel compression failed: %v", err)
	}
	parDecompressed, err := decompress(parCompressed)
	if err != nil {
		t.Fatalf("parallel decompression failed: %v", err)
	}
	if !bytes.Equal(originalData, parDecompressed) {
		t.Error("parallel decompressed data mismatch")
	}

	t.Logf("Sequential compressed size: %.2f MB", float64(len(seqCompressed))/(1024*1024))
	t.Logf("Parallel compressed size:   %.2f MB", float64(len(parCompressed))/(1024*1024))
}
func BenchmarkParallelLZMA2(b *testing.B) {
	const size = 10 * 1024 * 1024
	var srcBuf bytes.Buffer
	io.CopyN(&srcBuf, randtxt.NewReader(rand.NewSource(42)), size)
	originalData := srcBuf.Bytes()

	var out bytes.Buffer
	w, _ := Writer2Config{
		DictCap:     1024 * 1024,
		Concurrency: 4,
	}.NewWriter2(&out)
	w.Write(originalData)
	w.Close()
	compressed := out.Bytes()

	b.Run("DecompressParallel", func(b *testing.B) {
		b.SetBytes(int64(len(originalData)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			r, err := Reader2Config{DictCap: 1024 * 1024}.NewReader2(bytes.NewReader(compressed))
			if err != nil {
				b.Fatal(err)
			}
			io.Copy(io.Discard, r)
			r.Close()
		}
	})
}

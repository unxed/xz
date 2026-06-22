// Copyright 2014-2026 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz_test

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/ulikunitz/xz"
)

func TestParallelReader_Correctness(t *testing.T) {
	// Generate original data
	orig := bytes.Repeat([]byte("The quick brown fox jumps over the lazy dog. "), 10000)

	// Compress with small blocks to force multiple blocks
	var buf bytes.Buffer
	cfg := xz.WriterConfig{BlockSize: 4096}
	w, err := cfg.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(orig); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	// Decompress in parallel
	rAt := bytes.NewReader(buf.Bytes())
	pr, err := xz.ReaderConfig{}.NewParallelReader(rAt, int64(buf.Len()))
	if err != nil {
		t.Fatalf("NewParallelReader failed: %v", err)
	}
	defer pr.Close()

	decompressed, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if !bytes.Equal(orig, decompressed) {
		t.Fatal("decompressed data does not match original")
	}
}

func TestParallelReader_CorruptBlock(t *testing.T) {
	// 1. Создаем оригинальные данные
	orig := bytes.Repeat([]byte("A"), 10000)

	// 2. Сжимаем их маленькими блоками
	var buf bytes.Buffer
	cfg := xz.WriterConfig{BlockSize: 1024}
	w, err := cfg.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(orig)
	w.Close()

	compressed := buf.Bytes()
	// 3. Намеренно повреждаем байты в середине сжатого блока
	if len(compressed) > 100 {
		compressed[100] ^= 0xFF
	}

	rAt := bytes.NewReader(compressed)
	pr, err := xz.ReaderConfig{}.NewParallelReader(rAt, int64(len(compressed)))
	if err != nil {
		// Ошибка на этапе парсинга индексов тоже является корректным поведением
		return
	}
	defer pr.Close()

	// 4. Проверяем, что чтение падает с ошибкой, а не зависает
	_, err = io.ReadAll(pr)
	if err == nil {
		t.Error("expected error due to block corruption, but got nil")
	}
}

func TestParallelReader_EarlyClose(t *testing.T) {
	// 1. Создаем оригинальные данные
	orig := bytes.Repeat([]byte("B"), 10000)

	var buf bytes.Buffer
	cfg := xz.WriterConfig{BlockSize: 1024}
	w, err := cfg.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(orig)
	w.Close()

	rAt := bytes.NewReader(buf.Bytes())
	pr, err := xz.ReaderConfig{}.NewParallelReader(rAt, int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}

	// 2. Считываем только малую часть данных
	p := make([]byte, 10)
	_, _ = pr.Read(p)

	// 3. Закрываем ридер досрочно.
	// Этот вызов должен немедленно разблокировать воркеры и завершить их без утечек горутин.
	err = pr.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}
func TestParallelReader_AgainstExistingCorpus(t *testing.T) {
	// Список тестовых файлов, поставляемых с проектом
	files := []string{
		"fox.xz",
		"fox-check-none.xz",
		"example.xz",
	}

	for _, file := range files {
		t.Run(file, func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("failed to read test file: %v", err)
			}

			// 1. Декодируем оригинальным последовательным декодером
			rSeq, err := xz.NewReader(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("NewReader failed: %v", err)
			}
			seqDec, err := io.ReadAll(rSeq)
			if err != nil {
				t.Fatalf("sequential ReadAll failed: %v", err)
			}

			// 2. Декодируем новым параллельным декодером
			rAt := bytes.NewReader(data)
			pr, err := xz.ReaderConfig{}.NewParallelReader(rAt, int64(len(data)))
			if err != nil {
				t.Fatalf("NewParallelReader failed: %v", err)
			}
			defer pr.Close()

			parDec, err := io.ReadAll(pr)
			if err != nil {
				t.Fatalf("parallel ReadAll failed: %v", err)
			}

			// 3. Сверяем результаты до последнего байта
			if !bytes.Equal(seqDec, parDec) {
				t.Error("decompressed data mismatch between sequential and parallel reader")
			}
		})
	}
}

//go:build !amd64 || purego
// +build !amd64 purego

package lzma

import "unsafe"

func prefetch(addr unsafe.Pointer) {}
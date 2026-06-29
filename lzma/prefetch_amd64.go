//go:build amd64 && !purego
// +build amd64,!purego

package lzma

import "unsafe"

//go:noescape
func prefetch(addr unsafe.Pointer)

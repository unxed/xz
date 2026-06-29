//go:build amd64 && !purego
// +build amd64,!purego

package lzma

//go:noescape
func findBestMatch(dict []byte, rear int, data []byte, dists []int, rep0 uint32) (int, int)
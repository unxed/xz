//go:build amd64 && !purego
// +build amd64,!purego

package lzma

//go:noescape
func getMatches(table []int64, data []uint32, front int, mask uint64, hoff int64, h uint64, positions []int64) int
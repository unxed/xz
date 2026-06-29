//go:build !amd64 || purego
// +build !amd64 purego

package lzma

func getMatches(table []int64, data []uint32, front int, mask uint64, hoff int64, h uint64, positions []int64) int {
	if hoff < 0 || len(positions) == 0 {
		return 0
	}
	buffered := hoff + 1
	limit := int64(len(data))
	if buffered < 0 {
		buffered = 0
	}
	if buffered > limit {
		buffered = limit
	}

	tailPos := hoff + 1 - buffered
	rear := int64(front) - buffered
	if rear >= 0 {
		rear -= int64(len(data))
	}

	pos := table[h&mask] - 1
	delta := pos - tailPos
	n := 0

	for {
		if delta < 0 {
			return n
		}
		positions[n] = tailPos + delta
		n++
		if n >= len(positions) {
			return n
		}
		i := rear + delta
		if i < 0 {
			i += int64(len(data))
		}
		u := data[i]
		if u == 0 {
			return n
		}
		delta -= int64(u)
	}
}
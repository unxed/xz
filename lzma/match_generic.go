//go:build !amd64 || purego
// +build !amd64 purego

package lzma

func findBestMatch(dict []byte, rear int, data []byte, dists []int, rep0 uint32) (int, int) {
	bestLen := 0
	bestDist := 0
	bufSize := len(dict)
	dataLen := len(data)

	for _, dist := range dists {
		i := rear - dist + bestLen
		if i < 0 {
			i += bufSize
		} else if i >= bufSize {
			i -= bufSize
		}
		if dict[i] != data[bestLen] {
			continue
		}

		n := 0
		idx := rear - dist
		if idx < 0 {
			idx += bufSize
		}

		for n < dataLen {
			if dict[idx] != data[n] {
				break
			}
			n++
			idx++
			if idx == bufSize {
				idx = 0
			}
		}

		if n == 0 {
			continue
		}
		if n == 1 && uint32(dist-1) != rep0 {
			continue
		}

		if n > bestLen {
			bestDist = dist
			bestLen = n
			if n == dataLen {
				break
			}
		}
	}
	return bestDist, bestLen
}
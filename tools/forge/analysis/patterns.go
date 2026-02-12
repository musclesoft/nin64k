package analysis

import (
	"forge/parse"
)

func getPatternBreakInfo(pat parse.Pattern) (breakRow int, jumpTarget int) {
	for row := 0; row < 64; row++ {
		r := pat.Rows[row]
		if r.Effect == 0x0B {
			return row, int(r.Param)
		}
		if r.Effect == 0x0D {
			return row, -1
		}
	}
	return 64, -1
}

func findReachableOrders(song parse.ParsedSong, raw []byte) ([]int, map[int]int) {
	addrs := song.Addrs
	rawLen := len(raw)

	visited := make(map[int]bool)
	var orders []int
	order := song.StartOrder

	for len(orders) < 512 {
		if visited[order] {
			break
		}
		visited[order] = true
		orders = append(orders, order)

		var breakRow [3]int
		var jumpTarget [3]int

		for ch := 0; ch < 3; ch++ {
			if addrs.TrackLo[ch]+order >= rawLen || addrs.TrackHi[ch]+order >= rawLen {
				breakRow[ch], jumpTarget[ch] = 64, -1
				continue
			}
			lo := raw[addrs.TrackLo[ch]+order]
			hi := raw[addrs.TrackHi[ch]+order]
			addr := uint16(lo) | uint16(hi)<<8
			srcOff := int(addr) - addrs.BaseAddr
			if srcOff >= 0 && srcOff+192 <= rawLen {
				if pat, ok := song.Patterns[addr]; ok {
					breakRow[ch], jumpTarget[ch] = getPatternBreakInfo(pat)
				} else {
					breakRow[ch], jumpTarget[ch] = 64, -1
				}
			} else {
				breakRow[ch], jumpTarget[ch] = 64, -1
			}
		}

		minBreak := breakRow[0]
		for ch := 1; ch < 3; ch++ {
			if breakRow[ch] < minBreak {
				minBreak = breakRow[ch]
			}
		}

		nextOrder := -1
		for ch := 0; ch < 3; ch++ {
			if breakRow[ch] == minBreak && jumpTarget[ch] >= 0 {
				nextOrder = jumpTarget[ch]
				break
			}
		}
		if nextOrder < 0 {
			nextOrder = order + 1
		}
		if nextOrder >= song.NumOrders {
			break
		}
		order = nextOrder
	}

	orderMap := make(map[int]int)
	for newIdx, oldIdx := range orders {
		orderMap[oldIdx] = newIdx
	}

	return orders, orderMap
}

func computeTruncateLimits(song parse.ParsedSong, reachableOrders []int, raw []byte) map[uint16]int {
	limits := make(map[uint16]int)
	addrs := song.Addrs
	rawLen := len(raw)

	for _, orderIdx := range reachableOrders {
		var breakRow [3]int
		var addrsAtOrder [3]uint16

		for ch := 0; ch < 3; ch++ {
			if addrs.TrackLo[ch]+orderIdx >= rawLen || addrs.TrackHi[ch]+orderIdx >= rawLen {
				breakRow[ch] = 64
				continue
			}
			lo := raw[addrs.TrackLo[ch]+orderIdx]
			hi := raw[addrs.TrackHi[ch]+orderIdx]
			addr := uint16(lo) | uint16(hi)<<8
			addrsAtOrder[ch] = addr

			if pat, ok := song.Patterns[addr]; ok {
				br, _ := getPatternBreakInfo(pat)
				breakRow[ch] = br
			} else {
				breakRow[ch] = 64
			}
		}

		minBreak := breakRow[0]
		for ch := 1; ch < 3; ch++ {
			if breakRow[ch] < minBreak {
				minBreak = breakRow[ch]
			}
		}

		truncateAt := minBreak + 1
		if truncateAt > 64 {
			truncateAt = 64
		}

		for ch := 0; ch < 3; ch++ {
			addr := addrsAtOrder[ch]
			if addr == 0 {
				continue
			}
			if existing, ok := limits[addr]; !ok || truncateAt < existing {
				limits[addr] = truncateAt
			}
		}
	}

	return limits
}

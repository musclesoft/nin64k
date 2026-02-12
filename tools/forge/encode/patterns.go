package encode

var gapCodeToValue = []int{0, 1, 3, 7, 15, 31, 63}

func calculatePatternGap(pat []byte, truncateAfter int) int {
	numRows := len(pat) / 3
	if truncateAfter <= 0 || truncateAfter > numRows {
		truncateAfter = numRows
	}

	for code := 6; code >= 1; code-- {
		gap := gapCodeToValue[code]
		spacing := gap + 1
		if 64%spacing != 0 {
			continue
		}
		numSlots := 64 / spacing
		matches := true
		for slot := 0; slot < numSlots && matches; slot++ {
			startRow := slot * spacing
			for zeroIdx := 1; zeroIdx <= gap && matches; zeroIdx++ {
				rowNum := startRow + zeroIdx
				if rowNum >= truncateAfter {
					break
				}
				off := rowNum * 3
				if pat[off] != 0 || pat[off+1] != 0 || pat[off+2] != 0 {
					matches = false
				}
			}
		}
		if matches {
			return code
		}
	}
	return 0
}

func packPatterns(patterns [][]byte, dict []byte, rowToIdx map[string]int, truncateLimits []int) ([][]byte, []byte, int, int) {
	const primaryMax = 224
	const rleMax = 16
	const rleBase = 0xEF
	const extMarker = 0xFF
	const dictZeroRleMax = 15

	numEntries := len(dict) / 3
	patternPacked := make([][]byte, len(patterns))
	gapCodes := make([]byte, len(patterns))
	primaryCount := 0
	extendedCount := 0

	for i, pat := range patterns {
		numRows := len(pat) / 3
		truncateAfter := numRows
		if i < len(truncateLimits) && truncateLimits[i] > 0 && truncateLimits[i] < truncateAfter {
			truncateAfter = truncateLimits[i]
		}

		gapCode := calculatePatternGap(pat, truncateAfter)
		gapCodes[i] = byte(gapCode)
		gap := gapCodeToValue[gapCode]
		spacing := gap + 1

		var patPacked []byte
		var prevRow [3]byte
		repeatCount := 0
		lastWasDictZero := false
		lastDictZeroPos := -1

		emitRLE := func() {
			if repeatCount == 0 {
				return
			}
			if lastWasDictZero && lastDictZeroPos >= 0 && repeatCount <= dictZeroRleMax {
				patPacked[lastDictZeroPos] = byte(repeatCount)
				lastWasDictZero = false
			} else {
				if lastWasDictZero {
					lastWasDictZero = false
				}
				for repeatCount > 0 {
					emit := repeatCount
					if emit > rleMax {
						emit = rleMax
					}
					patPacked = append(patPacked, byte(rleBase+emit-1))
					repeatCount -= emit
				}
			}
			repeatCount = 0
		}

		for slot := 0; slot*spacing < truncateAfter; slot++ {
			row := slot * spacing
			off := row * 3
			curRow := [3]byte{pat[off], pat[off+1], pat[off+2]}

			if curRow == prevRow {
				repeatCount++
				maxAllowed := rleMax
				if lastWasDictZero && lastDictZeroPos >= 0 {
					maxAllowed = dictZeroRleMax
				}
				if repeatCount >= maxAllowed {
					emitRLE()
				}
			} else {
				emitRLE()

				idx := rowToIdx[string(curRow[:])]
				if idx == 0 && curRow != [3]byte{0, 0, 0} {
					for j := 1; j < numEntries; j++ {
						if dict[j*3] == curRow[0] && dict[j*3+1] == curRow[1] && dict[j*3+2] == curRow[2] {
							idx = j
							break
						}
					}
				}

				if idx < primaryMax {
					if idx == 0 {
						lastDictZeroPos = len(patPacked)
						patPacked = append(patPacked, 0)
						lastWasDictZero = true
					} else {
						patPacked = append(patPacked, byte(idx))
						lastWasDictZero = false
					}
					primaryCount++
				} else {
					patPacked = append(patPacked, extMarker, byte(idx-primaryMax))
					lastWasDictZero = false
					extendedCount++
				}
			}
			prevRow = curRow
		}

		emitRLE()
		patternPacked[i] = patPacked
	}

	return patternPacked, gapCodes, primaryCount, extendedCount
}

func optimizeOverlap(patterns [][]byte) []byte {
	if len(patterns) == 0 {
		return nil
	}

	var packed []byte
	packed = append(packed, patterns[0]...)

	for i := 1; i < len(patterns); i++ {
		pat := patterns[i]
		bestOverlap := 0

		for overlap := len(pat); overlap > 0; overlap-- {
			if overlap > len(packed) {
				continue
			}
			suffix := packed[len(packed)-overlap:]
			prefix := pat[:overlap]
			match := true
			for j := 0; j < overlap; j++ {
				if suffix[j] != prefix[j] {
					match = false
					break
				}
			}
			if match {
				bestOverlap = overlap
				break
			}
		}

		packed = append(packed, pat[bestOverlap:]...)
	}

	return packed
}

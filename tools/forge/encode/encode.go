package encode

import (
	"fmt"

	"forge/transform"
)

func Encode(song transform.TransformedSong) EncodedSong {
	return EncodeWithReorder(song, true)
}

func EncodeWithEquiv(song transform.TransformedSong, songNum int, projectRoot string) EncodedSong {
	return encodeInternal(song, true, songNum, projectRoot)
}

func EncodeWithEquivNoReorder(song transform.TransformedSong, songNum int, projectRoot string) EncodedSong {
	return encodeInternal(song, false, songNum, projectRoot)
}

func EncodeWithReorder(song transform.TransformedSong, doReorder bool) EncodedSong {
	return encodeInternal(song, doReorder, 0, "")
}

func encodeInternal(song transform.TransformedSong, doReorder bool, songNum int, projectRoot string) EncodedSong {
	result := EncodedSong{
		RowToIdx: make(map[string]int),
	}

	numPatterns := len(song.Patterns)

	var reorderMap []int
	if doReorder && numPatterns > 0 {
		reorderMap = OptimizePatternOrder(song)
	}

	patterns := make([][]byte, numPatterns)
	truncateLimits := make([]int, numPatterns)

	usedSlots := make([]bool, numPatterns)
	for oldIdx, pat := range song.Patterns {
		newIdx := oldIdx
		if reorderMap != nil {
			newIdx = reorderMap[oldIdx]
			if newIdx < 0 || newIdx >= numPatterns {
				panic(fmt.Sprintf("encodeInternal: reorderMap[%d] = %d out of bounds (numPatterns=%d)", oldIdx, newIdx, numPatterns))
			}
		}
		if usedSlots[newIdx] {
			panic(fmt.Sprintf("encodeInternal: slot %d used twice (oldIdx=%d)", newIdx, oldIdx))
		}
		usedSlots[newIdx] = true
		patData := make([]byte, 192)
		for row := 0; row < 64; row++ {
			r := pat.Rows[row]
			b0 := (r.Note & 0x7F) | ((r.Effect & 8) << 4)
			b1 := (r.Inst & 0x1F) | ((r.Effect & 7) << 5)
			patData[row*3] = b0
			patData[row*3+1] = b1
			patData[row*3+2] = r.Param
		}
		patterns[newIdx] = patData
		truncateLimits[newIdx] = pat.TruncateAt
	}
	for i, used := range usedSlots {
		if !used {
			panic(fmt.Sprintf("encodeInternal: slot %d not filled (patterns array has gap)", i))
		}
	}

	result.RowDict = buildDictionary(patterns, truncateLimits)

	result.RowToIdx[string([]byte{0, 0, 0})] = 0
	numEntries := len(result.RowDict) / 3
	dictSet := make(map[string]bool)
	dictSet[string([]byte{0, 0, 0})] = true
	for idx := 1; idx < numEntries; idx++ {
		row := string(result.RowDict[idx*3 : idx*3+3])
		result.RowToIdx[row] = idx
		dictSet[row] = true
	}

	for patIdx, pat := range song.Patterns {
		truncateAt := pat.TruncateAt
		if truncateAt <= 0 || truncateAt > 64 {
			truncateAt = 64
		}
		var prevRow [3]byte
		for row := 0; row < truncateAt; row++ {
			r := pat.Rows[row]
			b0 := (r.Note & 0x7F) | ((r.Effect & 8) << 4)
			b1 := (r.Inst & 0x1F) | ((r.Effect & 7) << 5)
			curRow := [3]byte{b0, b1, r.Param}
			if curRow == prevRow || curRow == [3]byte{0, 0, 0} {
				continue
			}
			if !dictSet[string(curRow[:])] {
				panic(fmt.Sprintf("encodeInternal: song.Patterns[%d] row %d (%02X %02X %02X) not in dictionary",
					patIdx, row, curRow[0], curRow[1], curRow[2]))
			}
			prevRow = curRow
		}
	}

	origDict := result.RowDict
	compactDict, oldToNew := compactDictionary(
		result.RowDict, result.RowToIdx, patterns, truncateLimits, nil)
	result.RowDict = compactDict

	numCompact := len(compactDict) / 3
	fmt.Printf("  [dict] song %d: %d entries\n", songNum, numCompact)

	result.RowToIdx = make(map[string]int)
	result.RowToIdx[string([]byte{0, 0, 0})] = 0
	for idx := 1; idx < numCompact; idx++ {
		row := string(compactDict[idx*3 : idx*3+3])
		result.RowToIdx[row] = idx
	}

	numOrig := len(origDict) / 3
	for oldIdx := 1; oldIdx < numOrig; oldIdx++ {
		row := string(origDict[oldIdx*3 : oldIdx*3+3])
		if _, exists := result.RowToIdx[row]; !exists {
			newIdx, hasMapping := oldToNew[oldIdx]
			if hasMapping {
				result.RowToIdx[row] = newIdx
			}
		}
	}

	for patIdx, pat := range song.Patterns {
		truncateAt := pat.TruncateAt
		if truncateAt <= 0 || truncateAt > 64 {
			truncateAt = 64
		}
		var prevRow [3]byte
		for row := 0; row < truncateAt; row++ {
			r := pat.Rows[row]
			b0 := (r.Note & 0x7F) | ((r.Effect & 8) << 4)
			b1 := (r.Inst & 0x1F) | ((r.Effect & 7) << 5)
			curRow := [3]byte{b0, b1, r.Param}
			if curRow == prevRow || curRow == [3]byte{0, 0, 0} {
				continue
			}
			if _, ok := result.RowToIdx[string(curRow[:])]; !ok {
				panic(fmt.Sprintf("encodeInternal AFTER compaction: song.Patterns[%d] row %d (%02X %02X %02X) not in RowToIdx",
					patIdx, row, curRow[0], curRow[1], curRow[2]))
			}
			prevRow = curRow
		}
	}

	canonPatterns, canonTruncate, patternToCanon := deduplicatePatternsWithEquiv(
		patterns, compactDict, result.RowToIdx, truncateLimits, nil)

	canonPackedData, canonGapCodes, primaryCount, extendedCount :=
		packPatternsWithEquiv(canonPatterns, compactDict, result.RowToIdx, canonTruncate, nil)

	result.PrimaryCount = primaryCount
	result.ExtendedCount = extendedCount

	result.PatternData = make([][]byte, len(patterns))
	result.PatternGapCodes = make([]byte, len(patterns))
	for i := range patterns {
		canonIdx := patternToCanon[i]
		result.PatternData[i] = canonPackedData[canonIdx]
		result.PatternGapCodes[i] = canonGapCodes[canonIdx]
	}

	var canonOffsets []uint16
	result.PackedPatterns, canonOffsets = optimizeOverlap(canonPackedData)

	result.PatternOffsets = make([]uint16, len(patterns))
	for i := range patterns {
		canonIdx := patternToCanon[i]
		result.PatternOffsets[i] = canonOffsets[canonIdx]
	}

	result.CanonPatterns = canonPackedData
	result.CanonGapCodes = canonGapCodes
	result.PatternCanon = patternToCanon

	result.TempTranspose, result.TempTrackptr, result.TrackStarts =
		encodeOrdersWithRemap(song, reorderMap)

	result.InstrumentData = encodeInstruments(song.Instruments, song.MaxUsedSlot)

	result.RawPatterns = patterns
	result.RawPatternsEquiv = patterns
	result.TruncateLimits = truncateLimits

	return result
}

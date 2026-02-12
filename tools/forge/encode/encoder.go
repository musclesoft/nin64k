package encode

import (
	"forge/parse"
	"forge/transform"
	"sort"
)

type EncodedSong struct {
	RowDict          []byte
	RowToIdx         map[string]int
	PatternData      [][]byte
	PatternOffsets   []uint16
	PatternGapCodes  []byte
	PackedPatterns   []byte
	PrimaryCount     int
	ExtendedCount    int
	OrderBitstream   []byte
	DeltaTable       []byte
	DeltaBases       []int
	StartConst       int
	TransposeTable   []byte
	TransposeBases   []int
	InstrumentData   []byte
	TrackStarts      [3]byte
	TempTranspose    [3][]byte
	TempTrackptr     [3][]byte
}

func Encode(song transform.TransformedSong) EncodedSong {
	result := EncodedSong{
		RowToIdx: make(map[string]int),
	}

	patterns := make([][]byte, len(song.Patterns))
	truncateLimits := make([]int, len(song.Patterns))

	for i, pat := range song.Patterns {
		patData := make([]byte, 192)
		for row := 0; row < 64; row++ {
			r := pat.Rows[row]
			b0 := (r.Note & 0x7F) | ((r.Effect & 8) << 4)
			b1 := (r.Inst & 0x1F) | ((r.Effect & 7) << 5)
			patData[row*3] = b0
			patData[row*3+1] = b1
			patData[row*3+2] = r.Param
		}
		patterns[i] = patData
		truncateLimits[i] = pat.TruncateAt
	}

	result.RowDict = buildDictionary(patterns, truncateLimits)

	result.RowToIdx[string([]byte{0, 0, 0})] = 0
	numEntries := len(result.RowDict) / 3
	for idx := 1; idx < numEntries; idx++ {
		row := string(result.RowDict[idx*3 : idx*3+3])
		result.RowToIdx[row] = idx
	}

	result.PatternData, result.PatternGapCodes, result.PrimaryCount, result.ExtendedCount =
		packPatterns(patterns, result.RowDict, result.RowToIdx, truncateLimits)

	result.PackedPatterns = optimizeOverlap(result.PatternData)

	result.TempTranspose, result.TempTrackptr, result.TrackStarts =
		encodeOrders(song)

	result.InstrumentData = encodeInstruments(song.Instruments, song.MaxUsedSlot)

	return result
}

func buildDictionary(patterns [][]byte, truncateLimits []int) []byte {
	rowUsage := make(map[string]int)

	for i, pat := range patterns {
		numRows := len(pat) / 3
		truncateAt := numRows
		if i < len(truncateLimits) && truncateLimits[i] > 0 && truncateLimits[i] < truncateAt {
			truncateAt = truncateLimits[i]
		}

		var prevRow [3]byte
		for row := 0; row < truncateAt; row++ {
			off := row * 3
			curRow := [3]byte{pat[off], pat[off+1], pat[off+2]}
			if curRow != prevRow && curRow != [3]byte{0, 0, 0} {
				rowUsage[string(curRow[:])]++
			}
			prevRow = curRow
		}
	}

	type dictEntry struct {
		row   [3]byte
		count int
	}
	var entries []dictEntry
	for rowStr, count := range rowUsage {
		var row [3]byte
		copy(row[:], rowStr)
		entries = append(entries, dictEntry{row, count})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].count > entries[j].count
	})

	dict := make([]byte, (len(entries)+1)*3)
	for i, e := range entries {
		slot := i + 1
		copy(dict[slot*3:], e.row[:])
	}

	return dict
}

func encodeInstruments(instruments []parse.Instrument, maxUsedSlot int) []byte {
	if maxUsedSlot <= 0 {
		return nil
	}

	numInst := maxUsedSlot + 1
	data := make([]byte, numInst*16)

	for i, inst := range instruments {
		if i >= numInst {
			break
		}
		for p := 0; p < 16; p++ {
			idx := p*numInst + i
			var val byte
			switch p {
			case 0:
				val = inst.AD
			case 1:
				val = inst.SR
			case 2:
				val = inst.WaveStart
			case 3:
				val = inst.WaveEnd
			case 4:
				val = inst.WaveLoop
			case 5:
				val = inst.ArpStart
			case 6:
				val = inst.ArpEnd
			case 7:
				val = inst.ArpLoop
			case 8:
				val = inst.PulseWidthLo
			case 9:
				val = inst.PulseWidthHi
			case 10:
				val = inst.PulseSpeed
			case 11:
				val = inst.VibDepthSpeed
			case 12:
				val = inst.VibDelay
			case 13:
				val = inst.FilterStart
			case 14:
				val = inst.FilterEnd
			case 15:
				val = inst.FilterLoop
			}
			data[idx] = val
		}
	}

	return data
}

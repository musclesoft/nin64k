package transform

import (
	"forge/analysis"
	"forge/parse"
	"sort"
)

type TransformedRow struct {
	Note   byte
	Inst   byte
	Effect byte
	Param  byte
}

type TransformedPattern struct {
	OriginalAddr uint16
	CanonicalIdx int
	Rows         []TransformedRow
	TruncateAt   int
}

type TransformedOrder struct {
	PatternIdx int
	Transpose  int8
}

type TransformedSong struct {
	Instruments    []parse.Instrument
	Patterns       []TransformedPattern
	Orders         [3][]TransformedOrder
	WaveTable      []byte
	ArpTable       []byte
	FilterTable    []byte
	EffectRemap    [16]byte
	FSubRemap      map[int]byte
	InstRemap      []int
	PatternRemap   map[uint16]uint16
	TransposeDelta map[uint16]int
	OrderMap       map[int]int
	MaxUsedSlot    int
	PatternOrder   []uint16
}

func Transform(song parse.ParsedSong, anal analysis.SongAnalysis, raw []byte) TransformedSong {
	result := TransformedSong{
		PatternRemap:   make(map[uint16]uint16),
		TransposeDelta: make(map[uint16]int),
		OrderMap:       anal.OrderMap,
	}

	result.EffectRemap, result.FSubRemap = buildEffectRemap(anal)
	result.InstRemap, result.MaxUsedSlot = buildInstRemap(anal, len(song.Instruments))

	canonicalPatterns, transposeDelta := findTransposeEquivalents(song, anal, raw)
	result.TransposeDelta = transposeDelta

	for addr, canonical := range canonicalPatterns {
		result.PatternRemap[addr] = canonical
	}

	var sortedCanonical []uint16
	seen := make(map[uint16]bool)
	for _, canonical := range canonicalPatterns {
		if !seen[canonical] {
			seen[canonical] = true
			sortedCanonical = append(sortedCanonical, canonical)
		}
	}
	sort.Slice(sortedCanonical, func(i, j int) bool {
		return sortedCanonical[i] < sortedCanonical[j]
	})
	result.PatternOrder = sortedCanonical

	addrToIdx := make(map[uint16]int)
	for idx, addr := range sortedCanonical {
		addrToIdx[addr] = idx
	}

	for _, addr := range sortedCanonical {
		pat := song.Patterns[addr]
		truncateAt := 64
		if limit, ok := anal.TruncateLimits[addr]; ok && limit < truncateAt {
			truncateAt = limit
		}

		transformed := TransformedPattern{
			OriginalAddr: addr,
			CanonicalIdx: addrToIdx[addr],
			TruncateAt:   truncateAt,
			Rows:         make([]TransformedRow, 64),
		}

		for row := 0; row < 64; row++ {
			r := pat.Rows[row]
			newNote := r.Note
			if newNote == 0x7F {
				newNote = 0x67
			}

			newB0, newB1, newParam := remapRowBytes(
				encodeB0(r.Note, r.Effect),
				encodeB1(r.Inst, r.Effect),
				r.Param,
				result.EffectRemap,
				result.FSubRemap,
				result.InstRemap,
			)

			transformed.Rows[row] = TransformedRow{
				Note:   newB0 & 0x7F,
				Inst:   newB1 & 0x1F,
				Effect: (newB1 >> 5) | ((newB0 >> 4) & 8),
				Param:  newParam,
			}
		}

		result.Patterns = append(result.Patterns, transformed)
	}

	for ch := 0; ch < 3; ch++ {
		for _, oldOrder := range anal.ReachableOrders {
			if oldOrder >= len(song.Orders[ch]) {
				continue
			}
			entry := song.Orders[ch][oldOrder]
			canonical := result.PatternRemap[entry.PatternAddr]
			delta := result.TransposeDelta[entry.PatternAddr]

			result.Orders[ch] = append(result.Orders[ch], TransformedOrder{
				PatternIdx: addrToIdx[canonical],
				Transpose:  int8(int(entry.Transpose) + delta),
			})
		}
	}

	result.Instruments = remapInstruments(song.Instruments, result.InstRemap, result.MaxUsedSlot)
	result.WaveTable = song.WaveTable
	result.ArpTable = song.ArpTable
	result.FilterTable = song.FilterTable

	return result
}

func encodeB0(note, effect byte) byte {
	return (note & 0x7F) | ((effect & 8) << 4)
}

func encodeB1(inst, effect byte) byte {
	return (inst & 0x1F) | ((effect & 7) << 5)
}

func remapInstruments(instruments []parse.Instrument, instRemap []int, maxUsedSlot int) []parse.Instrument {
	if maxUsedSlot <= 0 {
		return nil
	}

	result := make([]parse.Instrument, maxUsedSlot+1)

	for oldIdx, newIdx := range instRemap {
		if newIdx > 0 && newIdx <= maxUsedSlot && oldIdx < len(instruments) {
			result[newIdx] = instruments[oldIdx]
		}
	}

	return result
}

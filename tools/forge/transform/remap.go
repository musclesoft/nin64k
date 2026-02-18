package transform

import (
	"fmt"
	"sort"

	"forge/analysis"
	"forge/parse"
)

func Transform(song parse.ParsedSong, anal analysis.SongAnalysis, raw []byte, opts TransformOptions) TransformedSong {
	effectRemap, fSubRemap, portaUpEffect, portaDownEffect, tonePortaEffect := BuildGlobalEffectRemap()
	opts.PortaUpEffect = portaUpEffect
	opts.PortaDownEffect = portaDownEffect
	opts.TonePortaEffect = tonePortaEffect
	return TransformWithGlobalEffects(song, anal, raw, effectRemap, fSubRemap, opts)
}

func TransformWithGlobalEffects(song parse.ParsedSong, anal analysis.SongAnalysis, raw []byte, effectRemap [16]byte, fSubRemap map[int]byte, opts TransformOptions) TransformedSong {
	result := TransformedSong{
		PatternRemap:   make(map[uint16]uint16),
		TransposeDelta: make(map[uint16]int),
		OrderMap:       anal.OrderMap,
	}

	result.EffectRemap = effectRemap
	result.FSubRemap = fSubRemap
	numInst := len(song.Instruments)
	result.InstRemap, result.MaxUsedSlot = BuildInstRemap(anal, numInst)

	var canonicalPatterns map[uint16]uint16
	var transposeDelta map[uint16]int
	if opts.TransposeEquiv != nil {
		canonicalPatterns = opts.TransposeEquiv.PatternRemap
		transposeDelta = opts.TransposeEquiv.TransposeDelta
	} else {
		canonicalPatterns, transposeDelta = findTransposeEquivalentsInternal(song, anal, raw)
	}
	result.TransposeDelta = transposeDelta

	for addr, canonical := range canonicalPatterns {
		result.PatternRemap[addr] = canonical
	}

	uniqueCanonical := make(map[uint16]bool)
	for _, canonical := range canonicalPatterns {
		uniqueCanonical[canonical] = true
	}
	var sortedPatterns []uint16
	for addr := range uniqueCanonical {
		sortedPatterns = append(sortedPatterns, addr)
	}
	sort.Slice(sortedPatterns, func(i, j int) bool {
		return sortedPatterns[i] < sortedPatterns[j]
	})
	result.PatternOrder = sortedPatterns

	addrToIdx := make(map[uint16]int)
	for idx, addr := range sortedPatterns {
		addrToIdx[addr] = idx
	}

	for _, addr := range sortedPatterns {
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

			rawB0 := encodeB0(r.Note, r.Effect)
			rawB1 := encodeB1(r.Inst, r.Effect)
			rawB2 := r.Param

			if opts.EquivMap != nil {
				rowHex := fmt.Sprintf("%02x%02x%02x", rawB0, rawB1, rawB2)
				if target, ok := opts.EquivMap[rowHex]; ok {
					fmt.Sscanf(target, "%02x%02x%02x", &rawB0, &rawB1, &rawB2)
				}
			}

			rawNote := rawB0 & 0x7F
			if rawNote == 0x7F {
				rawB0 = (rawB0 & 0x80) | 0x61
			}

			newB0, newB1, newParam := RemapRowBytes(
				rawB0, rawB1, rawB2,
				result.EffectRemap, result.FSubRemap, result.InstRemap,
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

	result.WaveTable = song.WaveTable

	newArpTable, arpRemap, arpValid := deduplicateArpTable(song.Instruments, song.ArpTable)
	newFilterTable, filterRemap, filterValid := deduplicateFilterTable(song.Instruments, song.FilterTable)

	remappedInstruments := make([]parse.Instrument, len(song.Instruments))
	copy(remappedInstruments, song.Instruments)
	if arpRemap != nil {
		applyArpRemap(remappedInstruments, arpRemap, arpValid)
	}
	if filterRemap != nil {
		applyFilterRemap(remappedInstruments, filterRemap, filterValid)
	}

	result.Instruments = remapInstruments(remappedInstruments, result.InstRemap, result.MaxUsedSlot)
	result.ArpTable = newArpTable
	result.FilterTable = newFilterTable

	baselineRows := make(map[string]int)
	for _, pat := range result.Patterns {
		truncateAt := pat.TruncateAt
		if truncateAt <= 0 || truncateAt > 64 {
			truncateAt = 64
		}
		prev := ""
		for row := 0; row < truncateAt; row++ {
			r := pat.Rows[row]
			key := rowKey(r.Note, r.Inst, r.Effect, r.Param)
			if key != prev {
				baselineRows[key]++
				prev = key
			}
		}
	}

	// NOTE: PermanentArp optimization is applied in pipeline/remap.go AFTER
	// OptimizePersistentFXSelective to avoid creating NOPs that incorrectly inherit permarp

	if opts.PersistPorta {
		for _, portaEffect := range []byte{opts.PortaUpEffect, opts.PortaDownEffect} {
			if portaEffect == 0 {
				continue
			}
			origPatterns := DeepCopyPatterns(result.Patterns)
			result.Patterns = OptimizePortaToNOP(result.Patterns, portaEffect, baselineRows)
			if err := VerifyFullSongPorta(origPatterns, result.Patterns, result.Orders, portaEffect); err != nil {
				panic("persistporta verification failed: " + err.Error())
			}
		}
	}

	if opts.PersistTonePorta && opts.TonePortaEffect != 0 {
		result.Patterns = AddNopHardAfterTonePorta(result.Patterns, opts.TonePortaEffect)
		origPatterns := DeepCopyPatterns(result.Patterns)
		result.Patterns = OptimizeTonePortaRuns(result.Patterns, opts.TonePortaEffect, baselineRows)
		if err := VerifyFullSongTonePorta(origPatterns, result.Patterns, result.Orders, opts.TonePortaEffect); err != nil {
			panic("permtoneporta verification failed: " + err.Error())
		}
	}

	if false && opts.OptimizeInst && result.MaxUsedSlot < 31 {
		origPatterns := DeepCopyPatterns(result.Patterns)
		result.Patterns = OptimizeInstrumentReuse(result.Patterns, result.Orders, result.MaxUsedSlot)
		if err := VerifyInstOptTransform(origPatterns, result.Patterns, result.Orders); err != nil {
			panic("instopt verification failed: " + err.Error())
		}
	}

	return result
}

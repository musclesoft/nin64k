package serialize

import (
	"fmt"
	"forge/encode"
	"forge/transform"
)

func Serialize(song transform.TransformedSong, encoded encode.EncodedSong) []byte {
	numPatterns := len(encoded.PatternOffsets)
	patternDataStart := PackedPtrsOffset + numPatterns*2
	totalSize := patternDataStart + len(encoded.PackedPatterns)
	output := make([]byte, totalSize)

	copy(output[InstOffset:], encoded.InstrumentData)

	numOrders := len(song.Orders[0])
	bitstream := encode.PackOrderBitstream(numOrders, encoded.TempTranspose, encoded.TempTrackptr)
	copy(output[BitstreamOffset:], bitstream)

	filterSize := len(song.FilterTable)
	if filterSize > MaxFilterSize {
		filterSize = MaxFilterSize
	}
	copy(output[FilterOffset:], song.FilterTable[:filterSize])

	arpSize := len(song.ArpTable)
	if arpSize > MaxArpSize {
		arpSize = MaxArpSize
	}
	copy(output[ArpOffset:], song.ArpTable[:arpSize])

	output[TransBaseOffset] = 0
	output[DeltaBaseOffset] = 0

	numDictEntries := len(encoded.RowDict) / 3
	for i := 1; i < numDictEntries && i <= DictArraySize; i++ {
		output[RowDictOffset+i-1] = encoded.RowDict[i*3]
		output[RowDictOffset+DictArraySize+i-1] = encoded.RowDict[i*3+1]
		output[RowDictOffset+DictArraySize*2+i-1] = encoded.RowDict[i*3+2]
	}

	for i := 0; i < numPatterns; i++ {
		pOff := encoded.PatternOffsets[i]
		gapCode := byte(0)
		if i < len(encoded.PatternGapCodes) {
			gapCode = encoded.PatternGapCodes[i]
		}
		output[PackedPtrsOffset+i*2] = byte(pOff & 0xFF)
		output[PackedPtrsOffset+i*2+1] = byte(pOff>>8) | (gapCode << 5)
	}

	copy(output[patternDataStart:], encoded.PackedPatterns)

	return output
}

func findLastNonZero(data []byte) int {
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] != 0 {
			return i
		}
	}
	return 0
}

type gap struct {
	start int
	size  int
	used  int
}

var debugGaps = false
var DebugCanon = false

type gapSpec struct {
	start int
	size  int
}

func placePatternDataWithGaps(
	patterns [][]byte,
	gapCodes []byte,
	instSize int,
	bitstreamSize int,
	filterSize int,
	arpSize int,
	numDictEntries int,
	numPatterns int,
) ([]uint16, []byte, map[int][]byte) {
	if len(patterns) == 0 {
		return nil, nil, nil
	}

	instGapStart := InstOffset + instSize
	instGapSize := BitstreamOffset - instGapStart
	if instGapSize < 0 {
		instGapSize = 0
	}

	bitstreamGapStart := BitstreamOffset + bitstreamSize
	bitstreamGapSize := FilterOffset - bitstreamGapStart
	if bitstreamGapSize < 0 {
		bitstreamGapSize = 0
	}

	filterGapStart := FilterOffset + filterSize
	filterGapSize := ArpOffset - filterGapStart
	if filterGapSize < 0 {
		filterGapSize = 0
	}

	arpGapStart := ArpOffset + arpSize
	arpGapSize := TransBaseOffset - arpGapStart
	if arpGapSize < 0 {
		arpGapSize = 0
	}

	dictFreeStart := numDictEntries - 1
	if dictFreeStart < 0 {
		dictFreeStart = 0
	}
	dictFreeSize := DictArraySize - dictFreeStart
	if dictFreeSize < 0 {
		dictFreeSize = 0
	}

	// Gap order matches odin_convert for consistent pattern assignment
	gapSpecs := []gapSpec{
		{instGapStart, instGapSize},
		{filterGapStart, filterGapSize},
		{arpGapStart, arpGapSize},
		{RowDictOffset + dictFreeStart, dictFreeSize},
		{RowDictOffset + DictArraySize + dictFreeStart, dictFreeSize},
		{RowDictOffset + DictArraySize*2 + dictFreeStart, dictFreeSize},
		{bitstreamGapStart, bitstreamGapSize}, // Last, matching odin
	}

	if debugGaps {
		totalGapSize := 0
		for _, g := range gapSpecs {
			totalGapSize += g.size
		}
		fmt.Printf("    [gaps] inst=%d filter=%d arp=%d dict=%d×3 bitstream=%d total=%d\n",
			instGapSize, filterGapSize, arpGapSize, dictFreeSize, bitstreamGapSize, totalGapSize)
	}

	// Try multiple candidate orderings and pick best result
	type result struct {
		offsets []uint16
		blob    []byte
		gapData map[int][]byte
	}

	tryOrdering := func(candidateOrder []int) result {
		gaps := make([]*gap, len(gapSpecs))
		for i, gs := range gapSpecs {
			gaps[i] = &gap{gs.start, gs.size, 0}
		}

		gapAssign := make([]int, len(patterns))
		for i := range gapAssign {
			gapAssign[i] = -1
		}

		for _, idx := range candidateOrder {
			size := len(patterns[idx])
			bestGap := -1
			bestRemaining := int(^uint(0) >> 1)
			for gi, g := range gaps {
				remaining := g.size - g.used
				if size <= remaining && remaining < bestRemaining {
					bestGap = gi
					bestRemaining = remaining
				}
			}
			if bestGap >= 0 {
				gapAssign[idx] = bestGap
				gaps[bestGap].used += size
			}
		}

		inGap := make(map[int]int)
		gapData := make(map[int][]byte)
		for gi, g := range gaps {
			var gapPatterns [][]byte
			var gapPatternIdxs []int
			for i, gapIdx := range gapAssign {
				if gapIdx == gi {
					gapPatterns = append(gapPatterns, patterns[i])
					gapPatternIdxs = append(gapPatternIdxs, i)
				}
			}
			if len(gapPatterns) == 0 {
				continue
			}
			blob, offsets := optimizeOverlapForGaps(gapPatterns)
			if len(blob) <= g.size {
				gapData[g.start] = blob
				for j, patIdx := range gapPatternIdxs {
					inGap[patIdx] = g.start + int(offsets[j])
				}
			} else {
				for _, patIdx := range gapPatternIdxs {
					gapAssign[patIdx] = -1
				}
			}
		}

		var remainingPats [][]byte
		remainingIdx := make(map[int]int)
		for i, p := range patterns {
			if _, ok := inGap[i]; !ok {
				remainingIdx[i] = len(remainingPats)
				remainingPats = append(remainingPats, p)
			}
		}

		mainBlob, remainingOffsets := optimizeOverlapForGaps(remainingPats)
		mainBlobStart := PackedPtrsOffset + numPatterns*2

		finalOffsets := make([]uint16, len(patterns))
		for i := range patterns {
			if gapOff, ok := inGap[i]; ok {
				finalOffsets[i] = uint16(gapOff)
			} else if remIdx, ok := remainingIdx[i]; ok {
				finalOffsets[i] = uint16(mainBlobStart + int(remainingOffsets[remIdx]))
			}
		}

		return result{finalOffsets, mainBlob, gapData}
	}

	// Build candidate orderings
	n := len(patterns)
	sizes := make([]int, n)
	overlapPotential := make([]int, n)
	for i, pi := range patterns {
		sizes[i] = len(pi)
		maxOverlap := 0
		for j, pj := range patterns {
			if i == j {
				continue
			}
			maxLen := sizes[i]
			if len(pj) < maxLen {
				maxLen = len(pj)
			}
			for l := maxLen; l > 0; l-- {
				if string(pi[len(pi)-l:]) == string(pj[:l]) {
					if l > maxOverlap {
						maxOverlap = l
					}
					break
				}
			}
			for l := maxLen; l > 0; l-- {
				if string(pj[len(pj)-l:]) == string(pi[:l]) {
					if l > maxOverlap {
						maxOverlap = l
					}
					break
				}
			}
		}
		overlapPotential[i] = maxOverlap
	}

	// Ordering: low overlap first, then large size (matches odin)
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	for i := 0; i < n-1; i++ {
		for j := i + 1; j < n; j++ {
			oi, oj := overlapPotential[order[i]], overlapPotential[order[j]]
			si, sj := sizes[order[i]], sizes[order[j]]
			if oj < oi || (oj == oi && sj > si) {
				order[i], order[j] = order[j], order[i]
			}
		}
	}

	bestResult := tryOrdering(order)

	if debugGaps {
		fmt.Printf("    [gaps] pats_in_main=%d main_blob=%d\n",
			len(patterns)-len(bestResult.gapData), len(bestResult.blob))
	}

	return bestResult.offsets, bestResult.blob, bestResult.gapData
}

// optimizeOverlapForGaps uses greedy superstring algorithm with multi-trial heuristics
func optimizeOverlapForGaps(patterns [][]byte) ([]byte, []uint16) {
	n := len(patterns)
	if n == 0 {
		return nil, nil
	}

	// Deduplicate patterns first
	canonical := make([]int, n)
	for i := range canonical {
		canonical[i] = i
	}
	for i := 0; i < n; i++ {
		if canonical[i] != i {
			continue
		}
		for j := i + 1; j < n; j++ {
			if canonical[j] != j {
				continue
			}
			if string(patterns[i]) == string(patterns[j]) {
				canonical[j] = i
			}
		}
	}

	var uniquePatterns [][]byte
	origToUnique := make([]int, n)
	for i := 0; i < n; i++ {
		if canonical[i] == i {
			origToUnique[i] = len(uniquePatterns)
			uniquePatterns = append(uniquePatterns, patterns[i])
		} else {
			origToUnique[i] = -1
		}
	}
	for i := 0; i < n; i++ {
		if canonical[i] != i {
			origToUnique[i] = origToUnique[canonical[i]]
		}
	}

	numUnique := len(uniquePatterns)
	if numUnique == 0 {
		return nil, make([]uint16, n)
	}

	// Run baseline greedy
	bestPacked, bestOffsets := greedyOverlapCore(uniquePatterns)

	// Try multiple shuffled orderings
	type result struct {
		packed  []byte
		offsets []int
		seed    int
	}
	numTrials := 64
	results := make(chan result, numTrials)
	bestSeed := 0

	for w := 0; w < numTrials; w++ {
		seed := w + 1
		go func(s int) {
			packed, offs := greedyOverlapShuffle(uniquePatterns, s)
			results <- result{packed, offs, s}
		}(seed)
	}

	// Collect results, keep best (use seed as tiebreaker for determinism)
	for i := 0; i < numTrials; i++ {
		r := <-results
		better := len(r.packed) < len(bestPacked)
		if !better && len(r.packed) == len(bestPacked) && bestSeed > 0 && r.seed < bestSeed {
			// Same length as current best trial, prefer lower seed
			better = true
		}
		if better {
			// Validate offsets
			valid := true
			for j, pat := range uniquePatterns {
				off := r.offsets[j]
				if off < 0 || off+len(pat) > len(r.packed) {
					valid = false
					break
				}
				if string(r.packed[off:off+len(pat)]) != string(pat) {
					valid = false
					break
				}
			}
			if valid {
				bestPacked = r.packed
				bestOffsets = r.offsets
				bestSeed = r.seed
			}
		}
	}

	// Map back to original pattern indices
	offsets := make([]uint16, n)
	for i := 0; i < n; i++ {
		offsets[i] = uint16(bestOffsets[origToUnique[i]])
	}

	return bestPacked, offsets
}

// greedyOverlapCore builds a superstring using greedy merging
func greedyOverlapCore(patterns [][]byte) ([]byte, []int) {
	n := len(patterns)
	if n == 0 {
		return nil, nil
	}

	strings := make([][]byte, n)
	for i := range strings {
		strings[i] = make([]byte, len(patterns[i]))
		copy(strings[i], patterns[i])
	}

	patternOffset := make([]int, n)
	root := make([]int, n)
	for i := range root {
		root[i] = i
	}

	for {
		bestOverlap := 0
		bestI, bestJ := -1, -1

		for i := 0; i < n; i++ {
			if strings[i] == nil {
				continue
			}
			for j := 0; j < n; j++ {
				if i == j || strings[j] == nil {
					continue
				}
				si, sj := strings[i], strings[j]
				maxLen := len(si)
				if len(sj) < maxLen {
					maxLen = len(sj)
				}
				for l := maxLen; l >= 1; l-- {
					if string(si[len(si)-l:]) == string(sj[:l]) {
						if l > bestOverlap {
							bestOverlap = l
							bestI, bestJ = i, j
						}
						break
					}
				}
			}
		}

		if bestOverlap == 0 {
			break
		}

		si := strings[bestI]
		sj := strings[bestJ]
		merged := make([]byte, len(si)+len(sj)-bestOverlap)
		copy(merged, si)
		copy(merged[len(si):], sj[bestOverlap:])
		strings[bestI] = merged

		offsetShift := len(si) - bestOverlap
		for p := 0; p < n; p++ {
			if root[p] == bestJ {
				root[p] = bestI
				patternOffset[p] += offsetShift
			}
		}

		strings[bestJ] = nil
	}

	var packed []byte
	uniqueOffset := make([]int, n)
	for i := 0; i < n; i++ {
		if strings[i] != nil {
			baseOffset := len(packed)
			packed = append(packed, strings[i]...)
			for p := 0; p < n; p++ {
				if root[p] == i {
					uniqueOffset[p] = baseOffset + patternOffset[p]
				}
			}
		}
	}

	return packed, uniqueOffset
}

// greedyOverlapShuffle runs greedy with shuffled pattern order
func greedyOverlapShuffle(patterns [][]byte, seed int) ([]byte, []int) {
	n := len(patterns)

	// Build permutation
	perm := make([]int, n)
	for i := range perm {
		perm[i] = i
	}

	// Simple LCG shuffle
	rng := seed * 1103515245
	for i := n - 1; i > 0; i-- {
		rng = rng*1103515245 + 12345
		j := ((rng >> 16) & 0x7FFF) % (i + 1)
		perm[i], perm[j] = perm[j], perm[i]
	}

	// Reorder patterns
	shuffled := make([][]byte, n)
	for i, p := range perm {
		shuffled[i] = patterns[p]
	}

	// Run greedy
	packed, shuffledOffs := greedyOverlapCore(shuffled)

	// Map offsets back to original indices
	offsets := make([]int, n)
	for i, p := range perm {
		offsets[p] = shuffledOffs[i]
	}

	return packed, offsets
}

func SerializeWithTables(
	song transform.TransformedSong,
	encoded encode.EncodedSong,
	deltaToIdx map[int]byte,
	transposeToIdx map[int8]byte,
	deltaBase int,
	transposeBase int,
) []byte {
	return SerializeWithWaveRemap(song, encoded, deltaToIdx, transposeToIdx, deltaBase, transposeBase, nil, 0)
}

func SerializeWithWaveRemap(
	song transform.TransformedSong,
	encoded encode.EncodedSong,
	deltaToIdx map[int]byte,
	transposeToIdx map[int8]byte,
	deltaBase int,
	transposeBase int,
	waveRemap map[int][3]int,
	startConst int,
) []byte {
	numPatterns := len(encoded.PatternData)
	numOrders := len(song.Orders[0])

	filterSize := len(song.FilterTable)
	if filterSize > MaxFilterSize {
		filterSize = MaxFilterSize
	}
	arpSize := len(song.ArpTable)
	if arpSize > MaxArpSize {
		arpSize = MaxArpSize
	}
	numDictEntries := len(encoded.RowDict) / 3

	// Use gap filling if canonical patterns are available, otherwise fall back
	patternDataStart := PackedPtrsOffset + numPatterns*2

	var finalOffsets []uint16
	var mainBlob []byte
	var gapData map[int][]byte

	if len(encoded.CanonPatterns) > 0 {
		if DebugCanon {
			totalCanonSize := 0
			for _, p := range encoded.CanonPatterns {
				totalCanonSize += len(p)
			}
			fmt.Printf("    [canon] %d patterns, total %d bytes\n", len(encoded.CanonPatterns), totalCanonSize)
		}
		bitstreamSize := numOrders * 4 // PackOrderBitstream creates 4 bytes per order
		instSize := len(encoded.InstrumentData)

		// Place canonical patterns with gap filling
		canonOffsets, blob, gaps := placePatternDataWithGaps(
			encoded.CanonPatterns,
			encoded.CanonGapCodes,
			instSize,
			bitstreamSize,
			filterSize,
			arpSize,
			numDictEntries,
			numPatterns,
		)

		mainBlob = blob
		gapData = gaps

		// Map canonical offsets back to original pattern indices
		finalOffsets = make([]uint16, numPatterns)
		for i := 0; i < numPatterns; i++ {
			canonIdx := encoded.PatternCanon[i]
			finalOffsets[i] = canonOffsets[canonIdx]
		}
	} else {
		// Fallback: no gap filling
		mainBlob = encoded.PackedPatterns
		finalOffsets = make([]uint16, numPatterns)
		for i := 0; i < numPatterns; i++ {
			finalOffsets[i] = uint16(patternDataStart) + encoded.PatternOffsets[i]
		}
	}

	totalSize := patternDataStart + len(mainBlob)
	output := make([]byte, totalSize)

	instData := append([]byte{}, encoded.InstrumentData...)
	if waveRemap != nil && len(instData) > 0 {
		numInst := len(instData) / 16
		for inst := 1; inst <= numInst; inst++ {
			if remap, ok := waveRemap[inst]; ok {
				base := (inst - 1) * 16
				instData[base+2] = byte(remap[0])
				// WaveEnd + 1 (only if < 255)
				if remap[1] < 255 {
					instData[base+3] = byte(remap[1] + 1)
				} else {
					instData[base+3] = byte(remap[1])
				}
				instData[base+4] = byte(remap[2])
			}
		}
	}
	copy(output[InstOffset:], instData)

	var relTranspose [3][]byte
	var relTrackptr [3][]byte

	for ch := 0; ch < 3; ch++ {
		relTranspose[ch] = make([]byte, numOrders)
		relTrackptr[ch] = make([]byte, numOrders)
	}

	for ch := 0; ch < 3; ch++ {
		prevTrackptr := startConst

		for i := 0; i < numOrders && i < len(encoded.TempTrackptr[ch]); i++ {
			absTranspose := int8(encoded.TempTranspose[ch][i])
			absTrackptr := int(encoded.TempTrackptr[ch][i])

			relTranspose[ch][i] = transposeToIdx[absTranspose]

			delta := absTrackptr - prevTrackptr
			if delta > 127 {
				delta -= 256
			} else if delta < -128 {
				delta += 256
			}
			relTrackptr[ch][i] = deltaToIdx[delta]

			prevTrackptr = absTrackptr
		}
	}

	bitstream := encode.PackOrderBitstream(numOrders, relTranspose, relTrackptr)
	copy(output[BitstreamOffset:], bitstream)

	copy(output[FilterOffset:], song.FilterTable[:filterSize])
	copy(output[ArpOffset:], song.ArpTable[:arpSize])

	output[TransBaseOffset] = byte(transposeBase)
	output[DeltaBaseOffset] = byte(deltaBase)

	for i := 1; i < numDictEntries && i <= DictArraySize; i++ {
		output[RowDictOffset+i-1] = encoded.RowDict[i*3]
		output[RowDictOffset+DictArraySize+i-1] = encoded.RowDict[i*3+1]
		output[RowDictOffset+DictArraySize*2+i-1] = encoded.RowDict[i*3+2]
	}

	// Write patterns placed in gaps
	for gapStart, blob := range gapData {
		copy(output[gapStart:], blob)
	}

	// Write pattern pointers (finalOffsets are absolute)
	for i := 0; i < numPatterns; i++ {
		pOff := finalOffsets[i]
		gapCode := byte(0)
		if i < len(encoded.PatternGapCodes) {
			gapCode = encoded.PatternGapCodes[i]
		}
		output[PackedPtrsOffset+i*2] = byte(pOff & 0xFF)
		output[PackedPtrsOffset+i*2+1] = byte(pOff>>8) | (gapCode << 5)
	}

	// Write pattern data
	copy(output[patternDataStart:], mainBlob)

	return output[:findLastNonZero(output)+1]
}

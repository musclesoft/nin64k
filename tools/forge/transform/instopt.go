package transform

import "fmt"

var DebugInstOpt = false

// OptimizeInstrumentReuse is DISABLED.
//
// Analysis showed potential savings of ~12000+ inst->31 conversions across all songs,
// which would reduce dictionary entries by sharing inst=31 for "reload from prevInst".
//
// Implementation challenges:
// 1. Requires inst=31 as special value (conflicts with songs using 31+ instruments)
// 2. Requires player changes (ASM and VP) to handle inst=31 semantics
// 3. Requires tracking prevInst per channel in both players
// 4. Verification complexity to ensure behavior matches
//
// The original inst=0 approach (changing its meaning) was abandoned because:
// - Songs 1,2,3,5 have rows with note>0, inst=0 relying on "skip" behavior
// - Changing inst=0 to mean "reload" would break these songs
// - Analysis showed only 2 rows with actual conflicts (both key-off notes)
//
// Future work could:
// 1. Use inst=31 approach (requires no songs use 31+ instruments)
// 2. Add per-song flag to enable new semantics
// 3. Duplicate conflicted patterns to resolve conflicts
func OptimizeInstrumentReuse(patterns []TransformedPattern, orders [3][]TransformedOrder, maxUsedSlot int) []TransformedPattern {
	// Just run analysis for debugging, don't transform
	AnalyzeInst0Conflicts(patterns, orders)
	return patterns
}

// simulateChannelForInstOpt simulates playthrough for one channel,
// recording the prevInst value seen at each (patternIdx, rowIdx).
func simulateChannelForInstOpt(patterns []TransformedPattern, orders []TransformedOrder, prevInstSets map[string]map[byte]bool) {
	var prevInst byte = 0

	for _, order := range orders {
		patIdx := order.PatternIdx
		if patIdx < 0 || patIdx >= len(patterns) {
			continue
		}

		pat := patterns[patIdx]
		truncateAt := pat.TruncateAt
		if truncateAt <= 0 || truncateAt > 64 {
			truncateAt = 64
		}

		for row := 0; row < truncateAt; row++ {
			key := fmt.Sprintf("%d:%d", patIdx, row)

			// Record prevInst at this position
			if prevInstSets[key] == nil {
				prevInstSets[key] = make(map[byte]bool)
			}
			prevInstSets[key][prevInst] = true

			// Update prevInst based on this row
			r := pat.Rows[row]
			if r.Inst != 0 {
				prevInst = r.Inst
			}
		}
	}
}

// AnalyzeInst0Conflicts checks if original inst=0, note>0 rows are reached with
// consistent prevInst values. For debugging purposes.
func AnalyzeInst0Conflicts(patterns []TransformedPattern, orders [3][]TransformedOrder) {
	prevInstSets := make(map[string]map[byte]bool)
	patternUsage := make(map[int][]string)

	for ch := 0; ch < 3; ch++ {
		simulateChannelForInstOpt(patterns, orders[ch], prevInstSets)
		for orderIdx, order := range orders[ch] {
			patternUsage[order.PatternIdx] = append(patternUsage[order.PatternIdx],
				fmt.Sprintf("ch%d ord%d", ch, orderIdx))
		}
	}

	// Check rows where inst=0 and note>0
	inst0Conflicts := 0
	inst0Single := 0
	for key, prevInsts := range prevInstSets {
		var patIdx, rowIdx int
		fmt.Sscanf(key, "%d:%d", &patIdx, &rowIdx)
		if patIdx >= len(patterns) {
			continue
		}
		row := patterns[patIdx].Rows[rowIdx]

		// Only interested in original inst=0, note>0 rows (excluding key-off 0x61)
		if row.Inst != 0 || row.Note == 0 || row.Note == 0x61 {
			continue
		}

		if len(prevInsts) > 1 {
			inst0Conflicts++
			if DebugInstOpt {
				fmt.Printf("  [instopt] inst=0 conflict at pat%d row%d: note=%02X prevInsts=%v usage=%v\n",
					patIdx, rowIdx, row.Note, prevInsts, patternUsage[patIdx])
			}
		} else {
			inst0Single++
		}
	}

	fmt.Printf("  [instopt] inst=0,note>0 rows: %d single prevInst, %d conflicts\n", inst0Single, inst0Conflicts)
}

// VerifyInstOptTransform verifies that transformed patterns produce the same
// effective instrument sequence as original patterns when played through all orders.
func VerifyInstOptTransform(origPatterns, transPatterns []TransformedPattern, orders [3][]TransformedOrder) error {
	// No-op when optimization is disabled
	return nil
}

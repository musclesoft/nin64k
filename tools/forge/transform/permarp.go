package transform

import (
	"fmt"
	"sort"
)

var DebugPermarp = false

const NopHardParam = PlayerParam0NopHard // Effect 0, param 4 = NOP(HARD) clears permarp

// VerifyFullSongPermarp verifies that transformed patterns produce the same effective arp
// stream as original patterns when played through the entire song order.
// With "ARP always persistent", effect 1 (ARP) sets permarp, and NOP(HARD) clears it.
func VerifyFullSongPermarp(origPatterns, transPatterns []TransformedPattern, orders [3][]TransformedOrder, arpEffect byte) error {
	for ch := 0; ch < 3; ch++ {
		if err := verifyChannelPermarp(origPatterns, transPatterns, orders[ch], arpEffect, ch); err != nil {
			return err
		}
	}
	return nil
}

func verifyChannelPermarp(origPatterns, transPatterns []TransformedPattern, orders []TransformedOrder, arpEffect byte, ch int) error {
	// permarp persists across pattern boundaries for cross-pattern optimization
	// Patterns that need to clear inherited permarp will have NOP(HARD) at their start
	var permarp byte
	for orderIdx, order := range orders {
		patIdx := order.PatternIdx
		if patIdx < 0 || patIdx >= len(origPatterns) || patIdx >= len(transPatterns) {
			continue
		}

		origPat := origPatterns[patIdx]
		transPat := transPatterns[patIdx]

		truncateAt := origPat.TruncateAt
		if truncateAt <= 0 || truncateAt > 64 {
			truncateAt = 64
		}

		for row := 0; row < truncateAt; row++ {
			origRow := origPat.Rows[row]
			transRow := transPat.Rows[row]

			// Original: ARP effect with param != 0 does arp
			var origArp byte
			if origRow.Effect == arpEffect && origRow.Param != 0 {
				origArp = origRow.Param
			}

			// Transformed: ARP sets permarp, NOP continues, NOP(HARD) clears
			var transArp byte
			effect, param := transRow.Effect, transRow.Param

			if effect == arpEffect {
				// ARP always sets permarp
				permarp = param
				if param != 0 {
					transArp = param
				}
			} else if effect == 0 && param == 0 && permarp != 0 {
				// NOP with active permarp: use permarp
				transArp = permarp
			} else if effect != 0 || param != 0 {
				// Any other effect/param clears permarp (including NOP(HARD))
				permarp = 0
			}

			if origArp != transArp {
				return fmt.Errorf("ch%d order%d pat%d row%d: orig arp=%02X, trans arp=%02X (permarp=%02X)",
					ch, orderIdx, patIdx, row, origArp, transArp, permarp)
			}
		}
	}
	return nil
}

// optimizeArpToPermanent converts ARP commands to use "ARP always persistent" semantics.
// Phase 1: Insert NOP(HARD) after every ARP that's followed by NOP (preserves original behavior)
// Phase 2: Convert runs of same-value ARPs: middle ARPs become NOP (optimization)
// Pattern: ARP ARP ARP NOP -> ARP NOP NOP NOP(HARD)
func OptimizeArpToPermanent(patterns []TransformedPattern, arpEffect byte, baselineRows map[string]int) []TransformedPattern {
	result := make([]TransformedPattern, len(patterns))
	nopHardAdded := 0
	runsOptimized := 0

	for i, pat := range patterns {
		result[i], nopHardAdded, runsOptimized = optimizePatternArpPersistent(pat, arpEffect, nopHardAdded, runsOptimized, baselineRows)
	}

	fmt.Printf("[permarp] added %d NOP(HARD), optimized %d runs\n", nopHardAdded, runsOptimized)
	return result
}

func optimizePatternArpPersistent(pat TransformedPattern, arpEffect byte, nopHardAdded, runsOptimized int, baselineRows map[string]int) (TransformedPattern, int, int) {
	newPat := TransformedPattern{
		OriginalAddr: pat.OriginalAddr,
		CanonicalIdx: pat.CanonicalIdx,
		TruncateAt:   pat.TruncateAt,
		Rows:         make([]TransformedRow, len(pat.Rows)),
	}
	copy(newPat.Rows, pat.Rows)

	truncateAt := pat.TruncateAt
	if truncateAt <= 0 || truncateAt > 64 {
		truncateAt = 64
	}

	// Phase 1: Find ARP runs FIRST (before any modifications)
	// A run is CONSECUTIVE ARPs with same value (no NOPs between - those break the run)
	// We track if the run is followed by NOP to know if NOP(HARD) is needed
	type arpRun struct {
		arpRows      []int // rows with actual ARP commands
		followedByNOP bool  // true if last ARP is immediately followed by NOP
		arpValue     byte
	}

	var runs []arpRun
	var currentRun *arpRun

	for row := 0; row < truncateAt; row++ {
		r := pat.Rows[row] // Use ORIGINAL pattern for run detection

		if r.Effect == arpEffect && r.Param != 0 {
			if currentRun == nil {
				runs = append(runs, arpRun{arpRows: []int{row}, arpValue: r.Param})
				currentRun = &runs[len(runs)-1]
			} else if r.Param == currentRun.arpValue {
				currentRun.arpRows = append(currentRun.arpRows, row)
			} else {
				// Different arp value starts new run
				runs = append(runs, arpRun{arpRows: []int{row}, arpValue: r.Param})
				currentRun = &runs[len(runs)-1]
			}
			continue
		}

		// NOP immediately after ARP: mark run as followed by NOP, then end run
		if r.Effect == 0 && r.Param == 0 && currentRun != nil {
			currentRun.followedByNOP = true
		}

		// Any non-ARP ends the run
		currentRun = nil
	}

	// Phase 2: Process each run
	// - Runs with 2+ consecutive ARPs: convert middle ARPs to NOP (optimization)
	// - All runs followed by NOP: add NOP(HARD) to prevent inheritance (correctness)
	for _, run := range runs {
		lastArpRow := run.arpRows[len(run.arpRows)-1]

		// For runs with only 1 ARP, just add NOP(HARD) if followed by NOP
		if len(run.arpRows) < 2 {
			if run.followedByNOP && lastArpRow+1 < truncateAt {
				// Convert the NOP right after ARP to NOP(HARD)
				newPat.Rows[lastArpRow+1].Param = NopHardParam
				nopHardAdded++
			}
			continue
		}

		// Run has 2+ consecutive ARPs - check if optimization is worthwhile
		// Convert middle ARPs to NOP (they'll inherit permarp from first)

		// Count unique ARP entries that would be removed
		arpRowsUnique := make(map[string]bool)
		for _, rowIdx := range run.arpRows[1:] { // Skip first
			r := newPat.Rows[rowIdx]
			key := rowKey(r.Note, r.Inst, arpEffect, run.arpValue)
			arpRowsUnique[key] = true
		}

		// Count NOP entries needed (middle ARPs become NOP)
		nopRowsNeeded := make(map[string]bool)
		for _, rowIdx := range run.arpRows[1:] { // Skip first
			r := newPat.Rows[rowIdx]
			nopKey := rowKey(r.Note, r.Inst, 0, 0)
			nopRowsNeeded[nopKey] = true
		}

		arpRowsRemoved := len(arpRowsUnique)

		// Calculate cost of new NOP entries (only if baseline provided)
		newCost := 0
		if baselineRows != nil {
			for nopKey := range nopRowsNeeded {
				if _, exists := baselineRows[nopKey]; !exists {
					newCost++
				}
			}
		}
		// If no baseline, assume NOPs are free (aggressive optimization)

		netSavings := arpRowsRemoved - newCost

		if DebugPermarp {
			fmt.Printf("  [permarp] pat %d: run at rows %v, arp=$%02X\n",
				pat.CanonicalIdx, run.arpRows, run.arpValue)
			fmt.Printf("    unique ARP=%d, NOPs needed=%d, net=%d\n",
				len(arpRowsUnique), len(nopRowsNeeded), netSavings)
		}

		// Only optimize if we save entries (or break even with baseline)
		if netSavings > 0 || (netSavings == 0 && baselineRows == nil) {
			// Apply conversion: middle and subsequent ARPs become NOP
			for _, rowIdx := range run.arpRows[1:] {
				if DebugPermarp {
					r := newPat.Rows[rowIdx]
					fmt.Printf("    convert row %d: note=%02X inst=%d ARP $%02X -> NOP\n",
						rowIdx, r.Note, r.Inst, run.arpValue)
				}
				newPat.Rows[rowIdx].Effect = 0
				newPat.Rows[rowIdx].Param = 0
			}
			runsOptimized++
		}

		// Add NOP(HARD) after run if followed by NOP
		// NOTE: lastArpRow is the last ARP in ORIGINAL pattern, but after run optimization
		// the middle ARPs became NOP. So the EFFECTIVE last arp is arpRows[0], not lastArpRow.
		// We should add NOP(HARD) at arpRows[0]+1 if that's where the NOP sequence starts.
		if run.followedByNOP {
			// After optimization, only arpRows[0] is still ARP, rest are NOP
			// NOP(HARD) goes after the last EFFECTIVE NOP in the run sequence
			nopHardRow := lastArpRow + 1
			if nopHardRow < truncateAt {
				newPat.Rows[nopHardRow].Param = NopHardParam
				nopHardAdded++
			}
		}
	}

	// Verify transformation
	if !verifyPermarpTransform(pat.Rows, newPat.Rows, arpEffect, truncateAt) {
		if DebugPermarp {
			fmt.Printf("  [permarp] pat %d: verification failed, reverting\n", pat.CanonicalIdx)
		}
		return pat, nopHardAdded, runsOptimized - 1
	}

	// Debug: print when runs are optimized
	if runsOptimized > 0 && DebugPermarp {
		fmt.Printf("  [permarp] pat %d: verification PASSED after %d run optimizations\n", pat.CanonicalIdx, runsOptimized)
	}

	return newPat, nopHardAdded, runsOptimized
}

func rowKey(note, inst, effect, param byte) string {
	return string([]byte{note, inst, effect, param})
}

// verifyPermarpTransform checks that the transformed pattern produces the same
// effective arp sequence as the original when "ARP always persistent" is simulated.
func verifyPermarpTransform(orig, transformed []TransformedRow, arpEffect byte, truncateAt int) bool {
	// Original: ARP effect with param != 0 does arp
	origArp := make([]byte, truncateAt)
	for row := 0; row < truncateAt; row++ {
		if orig[row].Effect == arpEffect && orig[row].Param != 0 {
			origArp[row] = orig[row].Param
		}
	}

	// Transformed: ARP sets permarp, NOP continues, anything else clears
	transArp := make([]byte, truncateAt)
	var permarp byte
	for row := 0; row < truncateAt; row++ {
		r := transformed[row]
		effect, param := r.Effect, r.Param

		if effect == arpEffect {
			// ARP always sets permarp
			permarp = param
			if param != 0 {
				transArp[row] = param
			}
		} else if effect == 0 && param == 0 && permarp != 0 {
			// NOP with active permarp
			transArp[row] = permarp
		} else if effect != 0 || param != 0 {
			// Any other effect/param clears permarp
			permarp = 0
		}
	}

	// Compare
	for row := 0; row < truncateAt; row++ {
		if origArp[row] != transArp[row] {
			if DebugPermarp {
				fmt.Printf("    verify fail row %d: orig arp=%02X, trans arp=%02X\n",
					row, origArp[row], transArp[row])
			}
			return false
		}
	}
	return true
}

// OptimizeCrossPatternArp finds ARP rows that ALWAYS follow an ARP with the same value
// across pattern boundaries, and converts them to NOP.
func OptimizeCrossPatternArp(patterns []TransformedPattern, orders [3][]TransformedOrder, arpEffect byte) ([]TransformedPattern, int) {
	// First pass: count all unique rows
	rowCounts := make(map[string]int)
	for _, pat := range patterns {
		truncateAt := pat.TruncateAt
		if truncateAt <= 0 || truncateAt > 64 {
			truncateAt = 64
		}
		var prevRow TransformedRow
		for row := 0; row < truncateAt; row++ {
			r := pat.Rows[row]
			if r == prevRow {
				prevRow = r
				continue
			}
			key := rowKey(r.Note, r.Inst, r.Effect, r.Param)
			rowCounts[key]++
			prevRow = r
		}
	}

	// Second pass: find candidates by walking through orders for each channel
	type rowLocation struct {
		patIdx int
		rowIdx int
	}
	type candidateInfo struct {
		arpParam  byte
		effRow    string
		nopRow    string
		validUses int
		totalUses int
	}
	candidateMap := make(map[rowLocation]*candidateInfo)

	for ch := 0; ch < 3; ch++ {
		var lastArpParam byte
		var lastWasArp bool

		for _, order := range orders[ch] {
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
				r := pat.Rows[row]

				if r.Effect == arpEffect && r.Param != 0 {
					loc := rowLocation{patIdx, row}
					if candidateMap[loc] == nil {
						candidateMap[loc] = &candidateInfo{
							arpParam: r.Param,
							effRow:   rowKey(r.Note, r.Inst, r.Effect, r.Param),
							nopRow:   rowKey(r.Note, r.Inst, 0, 0),
						}
					}
					candidateMap[loc].totalUses++
					if lastWasArp && r.Param == lastArpParam {
						candidateMap[loc].validUses++
					}
						lastArpParam = r.Param
					lastWasArp = true
				} else if r.Effect == 0 && r.Param == 0 {
					// NOP continues arp state
				} else {
					// Any other effect clears arp state
					lastWasArp = false
				}
			}
		}
	}

	// Only include candidates where ALL uses follow same arp param
	type candidate struct {
		patIdx int
		rowIdx int
		effRow string
		nopRow string
	}
	var candidates []candidate
	for loc, info := range candidateMap {
		if info.validUses == info.totalUses && info.totalUses > 0 {
			candidates = append(candidates, candidate{
				patIdx: loc.patIdx,
				rowIdx: loc.rowIdx,
				effRow: info.effRow,
				nopRow: info.nopRow,
			})
		} else if DebugPermarp && info.totalUses > 0 {
			fmt.Printf("  [crossarp-reject] pat%d row%d: valid=%d total=%d param=%02X\n",
				loc.patIdx, loc.rowIdx, info.validUses, info.totalUses, info.arpParam)
		}
	}

	// Sort candidates for deterministic order
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].patIdx != candidates[j].patIdx {
			return candidates[i].patIdx < candidates[j].patIdx
		}
		return candidates[i].rowIdx < candidates[j].rowIdx
	})

	// Copy patterns
	result := make([]TransformedPattern, len(patterns))
	for i, pat := range patterns {
		result[i] = TransformedPattern{
			OriginalAddr: pat.OriginalAddr,
			CanonicalIdx: pat.CanonicalIdx,
			TruncateAt:   pat.TruncateAt,
			Rows:         make([]TransformedRow, len(pat.Rows)),
		}
		copy(result[i].Rows, pat.Rows)
	}

	// Boundary protection: find patterns that start with NOP and can follow
	// a pattern that ends with active permarp - add NOP(HARD) to clear inherited permarp
	// Track which patterns can end with active permarp
	patternsEndingWithArp := make(map[int]bool)
	for ch := 0; ch < 3; ch++ {
		var lastArpActive bool
		for orderIdx, order := range orders[ch] {
			patIdx := order.PatternIdx
			if patIdx < 0 || patIdx >= len(patterns) {
				continue
			}
			pat := patterns[patIdx]
			truncateAt := pat.TruncateAt
			if truncateAt <= 0 || truncateAt > 64 {
				truncateAt = 64
			}

			// Check if arp is active at end of pattern
			arpActive := lastArpActive
			for row := 0; row < truncateAt; row++ {
				r := pat.Rows[row]
				if r.Effect == arpEffect && r.Param != 0 {
					arpActive = true
				} else if r.Effect == 0 && r.Param == 0 {
					// NOP continues arp state
				} else {
					// Any other effect clears arp
					arpActive = false
				}
			}
			if arpActive {
				patternsEndingWithArp[patIdx] = true
			}
			lastArpActive = arpActive
			_ = orderIdx
		}
	}

	// Find patterns whose first row is NOP and can follow a pattern ending with arp
	patternsNeedingBoundaryProtection := make(map[int]bool)
	for ch := 0; ch < 3; ch++ {
		var prevPatIdx int = -1
		for _, order := range orders[ch] {
			patIdx := order.PatternIdx
			if patIdx < 0 || patIdx >= len(patterns) {
				continue
			}
			pat := patterns[patIdx]

			// Check if first row is NOP (effect=0, param=0)
			if pat.Rows[0].Effect == 0 && pat.Rows[0].Param == 0 {
				// Check if previous pattern could end with active arp
				if prevPatIdx >= 0 && patternsEndingWithArp[prevPatIdx] {
					patternsNeedingBoundaryProtection[patIdx] = true
				}
			}
			prevPatIdx = patIdx
		}
	}

	// Add NOP(HARD) at start of patterns needing boundary protection
	boundaryProtected := 0
	for patIdx := range patternsNeedingBoundaryProtection {
		if result[patIdx].Rows[0].Effect == 0 && result[patIdx].Rows[0].Param == 0 {
			result[patIdx].Rows[0].Param = NopHardParam
			boundaryProtected++
			if DebugPermarp {
				fmt.Printf("  [crossarp] pat %d row 0: NOP -> NOP(HARD) (boundary protection)\n", patIdx)
			}
		}
	}

	maxConversions := -1 // -1=unlimited, 0=disable
	converted := 0
	for _, c := range candidates {
		if maxConversions >= 0 && converted >= maxConversions {
			break
		}
		effCount := rowCounts[c.effRow]
		nopCount := rowCounts[c.nopRow]

		// Skip if this row type is already depleted (all instances already converted)
		if effCount <= 0 {
			continue
		}

		// Skip row 0 conversions - boundary protection runs before conversions,
		// so converting row 0 to NOP could inherit permarp from previous pattern
		if c.rowIdx == 0 {
			continue
		}

		willRemoveEntry := effCount == 1
		nopAlreadyExists := nopCount > 0

		if willRemoveEntry || nopAlreadyExists {
			netChange := 0
			if willRemoveEntry {
				netChange--
			}
			if !nopAlreadyExists {
				netChange++
			}

			if netChange < 0 || (netChange == 0 && nopAlreadyExists) {
				result[c.patIdx].Rows[c.rowIdx].Effect = 0
				result[c.patIdx].Rows[c.rowIdx].Param = 0
				rowCounts[c.effRow]--
				rowCounts[c.nopRow]++
				converted++

				if DebugPermarp {
					fmt.Printf("  [crossarp] pat %d row %d: ARP -> NOP (eff=%d, nop=%d)\n",
						c.patIdx, c.rowIdx, effCount, nopCount)
				}
			}
		}
	}

	if boundaryProtected > 0 {
		fmt.Printf("[permarp] boundary protection: %d patterns\n", boundaryProtected)
	}

	return result, converted
}

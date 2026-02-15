package transform

import "fmt"

var DebugPermarp = false

const NopHardParam = 4 // Effect 0, param 4 = NOP(HARD) clears permarp

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

		// Permarp is cleared on pattern change
		var permarp byte

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
func optimizeArpToPermanent(patterns []TransformedPattern, arpEffect byte, baselineRows map[string]int) []TransformedPattern {
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

	// Phase 1: Insert NOP(HARD) after every ARP followed by NOP
	// This preserves original behavior where ARP only affects one row
	for row := 0; row < truncateAt-1; row++ {
		r := newPat.Rows[row]
		nextRow := &newPat.Rows[row+1]

		// If this row is ARP with param != 0 and next row is NOP
		if r.Effect == arpEffect && r.Param != 0 {
			if nextRow.Effect == 0 && nextRow.Param == 0 {
				// Convert NOP to NOP(HARD)
				nextRow.Param = NopHardParam
				nopHardAdded++
			}
		}
	}

	// Phase 2: Find and optimize ARP runs (consecutive same-value ARPs)
	// Middle ARPs become NOP, which will use permarp from first ARP
	type arpRun struct {
		rows     []int
		arpValue byte
	}

	var runs []arpRun
	var currentRun *arpRun

	for row := 0; row < truncateAt; row++ {
		r := newPat.Rows[row]

		if r.Effect == arpEffect && r.Param != 0 {
			if currentRun == nil {
				runs = append(runs, arpRun{rows: []int{row}, arpValue: r.Param})
				currentRun = &runs[len(runs)-1]
			} else if r.Param == currentRun.arpValue {
				currentRun.rows = append(currentRun.rows, row)
			} else {
				// Different arp value starts new run
				runs = append(runs, arpRun{rows: []int{row}, arpValue: r.Param})
				currentRun = &runs[len(runs)-1]
			}
			continue
		}

		// NOP (effect=0, param=0) can continue a run since permarp persists
		// But NOP(HARD) or any other effect ends the run
		if r.Effect != 0 || r.Param != 0 {
			currentRun = nil
		}
	}

	// Apply optimization to each run with 2+ ARPs
	for _, run := range runs {
		if len(run.rows) < 2 {
			continue
		}

		// Calculate dictionary impact
		// Before: each unique (note,inst,arpEffect,arpValue) is an entry
		// After: first ARP stays, middle ARPs become NOP

		// Count unique ARP entries that would be removed
		arpRowsUnique := make(map[string]bool)
		for _, rowIdx := range run.rows[1:] { // Skip first
			r := newPat.Rows[rowIdx]
			key := rowKey(r.Note, r.Inst, arpEffect, run.arpValue)
			arpRowsUnique[key] = true
		}

		// Count NOP entries needed (middle ARPs become NOP)
		nopRowsNeeded := make(map[string]bool)
		for _, rowIdx := range run.rows[1:] { // Skip first
			r := newPat.Rows[rowIdx]
			nopKey := rowKey(r.Note, r.Inst, 0, 0)
			nopRowsNeeded[nopKey] = true
		}

		// Calculate costs
		arpRowsRemoved := len(arpRowsUnique)
		if arpRowsRemoved <= 0 {
			continue
		}

		// Added: NOP entries not in baseline
		newCost := 0
		for nopKey := range nopRowsNeeded {
			if _, exists := baselineRows[nopKey]; !exists {
				newCost++
			}
		}

		netSavings := arpRowsRemoved - newCost

		if DebugPermarp {
			fmt.Printf("  [permarp] pat %d: run at rows %v, arp=$%02X\n",
				pat.CanonicalIdx, run.rows, run.arpValue)
			fmt.Printf("    unique ARP=%d, NOPs needed=%d, net=%d\n",
				len(arpRowsUnique), len(nopRowsNeeded), netSavings)
		}

		if netSavings <= 0 {
			continue
		}

		// Apply conversion: middle and subsequent ARPs become NOP
		for _, rowIdx := range run.rows[1:] {
			newPat.Rows[rowIdx].Effect = 0
			newPat.Rows[rowIdx].Param = 0
		}

		runsOptimized++
	}

	// Verify transformation
	if !verifyPermarpTransform(pat.Rows, newPat.Rows, arpEffect, truncateAt) {
		if DebugPermarp {
			fmt.Printf("  [permarp] pat %d: verification failed, reverting\n", pat.CanonicalIdx)
		}
		return pat, nopHardAdded, runsOptimized - 1
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

package transform

import "fmt"

var DebugPermTonePorta = false

// NopHardParam is defined in permarp.go

// AddNopHardAfterTonePorta adds NOP(HARD) after any TONEPORTA followed by NOP.
// This is necessary because TONEPORTA always sets permporta, and we need to clear it
// when the original data had TONEPORTA followed by NOP (no intended sliding).
// This MUST be called for all songs when TONEPORTA is used.
func AddNopHardAfterTonePorta(patterns []TransformedPattern, tonePortaEffect byte) []TransformedPattern {
	result := make([]TransformedPattern, len(patterns))
	nopHardAdded := 0
	for i, pat := range patterns {
		result[i], nopHardAdded = addNopHardToPattern(pat, tonePortaEffect, nopHardAdded)
	}
	// Always print NOP(HARD) count for debugging
	fmt.Printf("[permtoneporta] added %d NOP(HARD)\n", nopHardAdded)
	return result
}

func addNopHardToPattern(pat TransformedPattern, tonePortaEffect byte, nopHardAdded int) (TransformedPattern, int) {
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

	// Find ALL TONEPORTAs followed by NOP and add NOP(HARD)
	for row := 0; row < truncateAt-1; row++ {
		r := newPat.Rows[row]
		nextRow := &newPat.Rows[row+1]

		// If this row is TONEPORTA with param != 0 and next row is NOP
		if r.Effect == tonePortaEffect && r.Param != 0 {
			if nextRow.Effect == 0 && nextRow.Param == 0 {
				// Convert NOP to NOP(HARD)
				nextRow.Param = NopHardParam
				nopHardAdded++
			}
		}
	}

	return newPat, nopHardAdded
}

// OptimizeTonePortaRuns converts runs of consecutive same-speed TONEPORTA to use NOP.
// First TONEPORTA stays as TONEPORTA (sets permporta), subsequent same-speed TONEPORTAs
// become NOP (which uses permporta). NOP(HARD) must already be added by AddNopHardAfterTonePorta.
func OptimizeTonePortaRuns(patterns []TransformedPattern, tonePortaEffect byte, baselineRows map[string]int) []TransformedPattern {
	result := make([]TransformedPattern, len(patterns))
	converted := 0
	for i, pat := range patterns {
		result[i], converted = optimizePatternTonePortaRuns(pat, tonePortaEffect, converted, baselineRows)
	}
	if DebugPermTonePorta {
		fmt.Printf("[permtoneporta] converted %d runs\n", converted)
	}
	return result
}

func optimizePatternTonePortaRuns(pat TransformedPattern, tonePortaEffect byte, converted int, baselineRows map[string]int) (TransformedPattern, int) {
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

	// Find runs of consecutive same-speed TONEPORTA commands
	type portaRun struct {
		rows  []int // row indices with TONEPORTA
		speed byte
	}

	var runs []portaRun
	var currentRun *portaRun

	for row := 0; row < truncateAt; row++ {
		r := newPat.Rows[row]

		if r.Effect == tonePortaEffect && r.Param != 0 {
			if currentRun == nil {
				runs = append(runs, portaRun{rows: []int{row}, speed: r.Param})
				currentRun = &runs[len(runs)-1]
			} else if r.Param == currentRun.speed {
				currentRun.rows = append(currentRun.rows, row)
			} else {
				// Different speed starts new run
				runs = append(runs, portaRun{rows: []int{row}, speed: r.Param})
				currentRun = &runs[len(runs)-1]
			}
			continue
		}

		// Non-TONEPORTA or TONEPORTA $00 ends run
		currentRun = nil
	}

	// Apply optimization to each run (convert subsequent TONEPORTA to NOP)
	for _, run := range runs {
		if len(run.rows) < 2 {
			continue
		}

		// Calculate dictionary impact
		// Count unique TONEPORTA rows being replaced
		portaRowsUnique := make(map[string]bool)
		for _, rowIdx := range run.rows {
			r := newPat.Rows[rowIdx]
			key := rowKey(r.Note, r.Inst, tonePortaEffect, run.speed)
			portaRowsUnique[key] = true
		}

		// Count NOP entries needed for converted rows
		nopRowsNeeded := make(map[string]bool)
		for i := 1; i < len(run.rows); i++ {
			r := newPat.Rows[run.rows[i]]
			nopKey := rowKey(r.Note, r.Inst, 0, 0)
			nopRowsNeeded[nopKey] = true
		}

		// Calculate costs
		// Removed: all unique TONEPORTA rows except first
		portaRowsRemoved := len(portaRowsUnique) - 1
		if portaRowsRemoved <= 0 {
			continue // All rows have same note/inst, nothing to save
		}

		// Added: NOP entries not in baseline
		newCost := 0
		for nopKey := range nopRowsNeeded {
			if _, exists := baselineRows[nopKey]; !exists {
				newCost++
			}
		}

		netSavings := portaRowsRemoved - newCost

		if DebugPermTonePorta {
			fmt.Printf("  [permtoneporta] pat %d: run at rows %v, speed=$%02X\n",
				pat.CanonicalIdx, run.rows, run.speed)
			fmt.Printf("    unique PORTA rows=%d, NOPs needed=%d\n",
				len(portaRowsUnique), len(nopRowsNeeded))
			fmt.Printf("    removed=%d, newCost=%d, net=%d\n",
				portaRowsRemoved, newCost, netSavings)
		}

		if netSavings <= 0 {
			continue
		}

		// Apply conversion:
		// - First TONEPORTA stays as TONEPORTA (sets permporta)
		// - Remaining TONEPORTA become NOP (use permporta)
		for i := 1; i < len(run.rows); i++ {
			rowIdx := run.rows[i]
			newPat.Rows[rowIdx].Effect = 0
			newPat.Rows[rowIdx].Param = 0
		}

		converted++
	}

	// Verify transformation
	if !verifyTonePortaTransform(pat.Rows, newPat.Rows, tonePortaEffect, truncateAt) {
		if DebugPermTonePorta {
			fmt.Printf("  [permtoneporta] pat %d: verification failed, reverting\n", pat.CanonicalIdx)
		}
		return pat, converted - 1
	}

	return newPat, converted
}

// verifyTonePortaTransform checks that the transformed pattern produces the same
// effective porta behavior as the original when permporta semantics are applied.
func verifyTonePortaTransform(orig, transformed []TransformedRow, tonePortaEffect byte, truncateAt int) bool {
	// Compute effective porta speed for original
	// TONEPORTA sets permporta AND does porta, NOP with permporta uses it, NOP(HARD) clears it
	origPorta := make([]byte, truncateAt)
	var origPermporta byte
	for row := 0; row < truncateAt; row++ {
		r := orig[row]
		if r.Effect == tonePortaEffect {
			origPermporta = r.Param
			if r.Param != 0 {
				origPorta[row] = r.Param
			}
		} else if r.Effect == 0 {
			if r.Param == 0 && origPermporta != 0 {
				origPorta[row] = origPermporta
			} else if r.Param != 0 {
				origPermporta = 0
			}
		} else {
			origPermporta = 0
		}
	}

	// Compute effective porta speed for transformed (same logic)
	transPorta := make([]byte, truncateAt)
	var transPermporta byte
	for row := 0; row < truncateAt; row++ {
		r := transformed[row]
		if r.Effect == tonePortaEffect {
			transPermporta = r.Param
			if r.Param != 0 {
				transPorta[row] = r.Param
			}
		} else if r.Effect == 0 {
			if r.Param == 0 && transPermporta != 0 {
				transPorta[row] = transPermporta
			} else if r.Param != 0 {
				transPermporta = 0
			}
		} else {
			transPermporta = 0
		}
	}

	// Compare
	for row := 0; row < truncateAt; row++ {
		if origPorta[row] != transPorta[row] {
			if DebugPermTonePorta {
				fmt.Printf("    verify fail row %d: orig porta=%02X, trans porta=%02X\n",
					row, origPorta[row], transPorta[row])
			}
			return false
		}
	}
	return true
}

// VerifyFullSongTonePorta verifies transformed patterns match original behavior across all orders.
func VerifyFullSongTonePorta(origPatterns, transPatterns []TransformedPattern, orders [3][]TransformedOrder, tonePortaEffect byte) error {
	for ch := 0; ch < 3; ch++ {
		if err := verifyChannelTonePorta(origPatterns, transPatterns, orders[ch], tonePortaEffect, ch); err != nil {
			return err
		}
	}
	return nil
}

func verifyChannelTonePorta(origPatterns, transPatterns []TransformedPattern, orders []TransformedOrder, tonePortaEffect byte, ch int) error {
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

		// Simulate both original and transformed
		var origPermporta, transPermporta byte
		for row := 0; row < truncateAt; row++ {
			origRow := origPat.Rows[row]
			transRow := transPat.Rows[row]

			// Original effective porta
			var origPorta byte
			if origRow.Effect == tonePortaEffect {
				origPermporta = origRow.Param
				if origRow.Param != 0 {
					origPorta = origRow.Param
				}
			} else if origRow.Effect == 0 {
				if origRow.Param == 0 && origPermporta != 0 {
					origPorta = origPermporta
				} else if origRow.Param != 0 {
					origPermporta = 0
				}
			} else {
				origPermporta = 0
			}

			// Transformed effective porta
			var transPorta byte
			if transRow.Effect == tonePortaEffect {
				transPermporta = transRow.Param
				if transRow.Param != 0 {
					transPorta = transRow.Param
				}
			} else if transRow.Effect == 0 {
				if transRow.Param == 0 && transPermporta != 0 {
					transPorta = transPermporta
				} else if transRow.Param != 0 {
					transPermporta = 0
				}
			} else {
				transPermporta = 0
			}

			if origPorta != transPorta {
				return fmt.Errorf("ch%d order%d pat%d row%d: orig porta=%02X, trans porta=%02X",
					ch, orderIdx, patIdx, row, origPorta, transPorta)
			}
		}

		// Clear permporta at pattern boundary
		origPermporta = 0
		transPermporta = 0
	}
	return nil
}

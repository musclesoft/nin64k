package transform

import "fmt"

var DebugPersistPorta = false

// OptimizePortaToNOP converts runs of consecutive same-speed PORTA UP/DOWN to use NOP.
// First PORTA stays as PORTA (sets permporta), subsequent same-speed PORTAs become NOP.
// This works because PORTA already persists through NOP in the player.
func OptimizePortaToNOP(patterns []TransformedPattern, portaEffect byte, baselineRows map[string]int) []TransformedPattern {
	result := make([]TransformedPattern, len(patterns))
	converted := 0
	for i, pat := range patterns {
		result[i], converted = optimizePatternPortaToNOP(pat, portaEffect, converted, baselineRows)
	}
	if DebugPersistPorta {
		fmt.Printf("[persistporta] effect %d: converted %d runs\n", portaEffect, converted)
	}
	return result
}

func optimizePatternPortaToNOP(pat TransformedPattern, portaEffect byte, converted int, baselineRows map[string]int) (TransformedPattern, int) {
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

	// Find runs of consecutive same-speed PORTA commands
	type portaRun struct {
		rows  []int
		speed byte
	}

	var runs []portaRun
	var currentRun *portaRun

	for row := 0; row < truncateAt; row++ {
		r := newPat.Rows[row]

		if r.Effect == portaEffect && r.Param != 0 {
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

		// NOP continues run (porta persists), but NOP(HARD) or other effects end it
		if r.Effect != 0 || r.Param != 0 {
			currentRun = nil
		}
	}

	// Apply optimization to each run
	for _, run := range runs {
		if len(run.rows) < 2 {
			continue
		}

		// Calculate dictionary impact
		portaRowsUnique := make(map[string]bool)
		for _, rowIdx := range run.rows {
			r := newPat.Rows[rowIdx]
			key := rowKey(r.Note, r.Inst, portaEffect, run.speed)
			portaRowsUnique[key] = true
		}

		nopRowsNeeded := make(map[string]bool)
		for i := 1; i < len(run.rows); i++ {
			r := newPat.Rows[run.rows[i]]
			nopKey := rowKey(r.Note, r.Inst, 0, 0)
			nopRowsNeeded[nopKey] = true
		}

		portaRowsRemoved := len(portaRowsUnique) - 1
		if portaRowsRemoved <= 0 {
			continue
		}

		newCost := 0
		for nopKey := range nopRowsNeeded {
			if _, exists := baselineRows[nopKey]; !exists {
				newCost++
			}
		}

		netSavings := portaRowsRemoved - newCost

		if DebugPersistPorta {
			fmt.Printf("  [persistporta] pat %d: run at rows %v, speed=$%02X\n",
				pat.CanonicalIdx, run.rows, run.speed)
			fmt.Printf("    unique PORTA=%d, NOPs needed=%d, net=%d\n",
				len(portaRowsUnique), len(nopRowsNeeded), netSavings)
		}

		if netSavings <= 0 {
			continue
		}

		// Apply: first PORTA stays, rest become NOP
		for i := 1; i < len(run.rows); i++ {
			rowIdx := run.rows[i]
			newPat.Rows[rowIdx].Effect = 0
			newPat.Rows[rowIdx].Param = 0
		}

		converted++
	}

	// Verify transformation
	if !verifyPortaTransform(pat.Rows, newPat.Rows, portaEffect, truncateAt) {
		if DebugPersistPorta {
			fmt.Printf("  [persistporta] pat %d: verification failed, reverting\n", pat.CanonicalIdx)
		}
		return pat, converted - 1
	}

	return newPat, converted
}

func verifyPortaTransform(orig, transformed []TransformedRow, portaEffect byte, truncateAt int) bool {
	// Original: PORTA with param != 0 does slide
	origSlide := make([]byte, truncateAt)
	var origActive byte
	for row := 0; row < truncateAt; row++ {
		r := orig[row]
		if r.Effect == portaEffect && r.Param != 0 {
			origActive = r.Param
			origSlide[row] = r.Param
		} else if r.Effect == 0 && r.Param == 0 && origActive != 0 {
			origSlide[row] = origActive
		} else if r.Effect != 0 || r.Param != 0 {
			origActive = 0
		}
	}

	// Transformed: same logic
	transSlide := make([]byte, truncateAt)
	var transActive byte
	for row := 0; row < truncateAt; row++ {
		r := transformed[row]
		if r.Effect == portaEffect && r.Param != 0 {
			transActive = r.Param
			transSlide[row] = r.Param
		} else if r.Effect == 0 && r.Param == 0 && transActive != 0 {
			transSlide[row] = transActive
		} else if r.Effect != 0 || r.Param != 0 {
			transActive = 0
		}
	}

	for row := 0; row < truncateAt; row++ {
		if origSlide[row] != transSlide[row] {
			if DebugPersistPorta {
				fmt.Printf("    verify fail row %d: orig=%02X, trans=%02X\n",
					row, origSlide[row], transSlide[row])
			}
			return false
		}
	}
	return true
}

// VerifyFullSongPorta verifies transformed patterns match original behavior across all orders.
func VerifyFullSongPorta(origPatterns, transPatterns []TransformedPattern, orders [3][]TransformedOrder, portaEffect byte) error {
	for ch := 0; ch < 3; ch++ {
		if err := verifyChannelPorta(origPatterns, transPatterns, orders[ch], portaEffect, ch); err != nil {
			return err
		}
	}
	return nil
}

func verifyChannelPorta(origPatterns, transPatterns []TransformedPattern, orders []TransformedOrder, portaEffect byte, ch int) error {
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

		// Slide state persists within a pattern but NOT across patterns
		// (new note triggers reset slide state - but let's be conservative and check per-pattern)
		var slideActive byte

		for row := 0; row < truncateAt; row++ {
			origRow := origPat.Rows[row]

			// Compute original effective slide
			var origSlide byte
			if origRow.Effect == portaEffect && origRow.Param != 0 {
				origSlide = origRow.Param
			} else if origRow.Effect == 0 && origRow.Param == 0 {
				origSlide = slideActive // NOP continues slide
			}

			// Update slide state
			if origRow.Effect == portaEffect && origRow.Param != 0 {
				slideActive = origRow.Param
			} else if origRow.Effect != 0 || origRow.Param != 0 {
				slideActive = 0
			}

			transRow := transPat.Rows[row]

			// Compute transformed effective slide (same logic)
			var transSlide byte
			var transSlideActive byte
			if transRow.Effect == portaEffect && transRow.Param != 0 {
				transSlide = transRow.Param
				transSlideActive = transRow.Param
			} else if transRow.Effect == 0 && transRow.Param == 0 {
				transSlide = slideActive
			} else {
				transSlideActive = 0
			}
			_ = transSlideActive

			if origSlide != transSlide {
				return fmt.Errorf("ch%d order%d pat%d row%d: orig slide=%02X, trans slide=%02X",
					ch, orderIdx, patIdx, row, origSlide, transSlide)
			}
		}
	}
	return nil
}

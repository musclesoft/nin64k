package encode

import (
	"fmt"
	"strings"
)

func deduplicatePatternsWithEquiv(
	patterns [][]byte,
	dict []byte,
	rowToIdx map[string]int,
	truncateLimits []int,
	equivMap map[int]int,
) ([][]byte, []int, []int) {
	n := len(patterns)
	if n == 0 {
		return nil, nil, nil
	}

	getSignature := func(pat []byte, truncateAt int) string {
		var sig strings.Builder
		numRows := len(pat) / 3
		if truncateAt > 0 && truncateAt < numRows {
			numRows = truncateAt
		}
		for row := 0; row < numRows; row++ {
			off := row * 3
			rowBytes := string(pat[off : off+3])
			idx := rowToIdx[rowBytes]
			if equivMap != nil {
				if mappedIdx, ok := equivMap[idx]; ok {
					idx = mappedIdx
				}
			}
			sig.WriteString(fmt.Sprintf("%d,", idx))
		}
		return sig.String()
	}

	sigToCanon := make(map[string]int)
	patternToCanon := make([]int, n)

	for i, pat := range patterns {
		truncateAt := 64
		if i < len(truncateLimits) && truncateLimits[i] > 0 {
			truncateAt = truncateLimits[i]
		}
		sig := getSignature(pat, truncateAt)
		if canon, exists := sigToCanon[sig]; exists {
			patternToCanon[i] = canon
		} else {
			sigToCanon[sig] = i
			patternToCanon[i] = i
		}
	}

	var canonPatterns [][]byte
	var canonTruncate []int
	oldToNew := make(map[int]int)

	for i, pat := range patterns {
		if patternToCanon[i] == i {
			oldToNew[i] = len(canonPatterns)
			canonPatterns = append(canonPatterns, pat)
			if i < len(truncateLimits) {
				canonTruncate = append(canonTruncate, truncateLimits[i])
			} else {
				canonTruncate = append(canonTruncate, 64)
			}
		}
	}

	finalMapping := make([]int, n)
	for i := range patterns {
		canonOldIdx := patternToCanon[i]
		finalMapping[i] = oldToNew[canonOldIdx]
	}

	dedupCount := n - len(canonPatterns)
	if dedupCount > 0 {
		fmt.Printf("  [equiv-dedup] Deduplicated %d patterns before packing (%d → %d)\n",
			dedupCount, n, len(canonPatterns))
	}

	return canonPatterns, canonTruncate, finalMapping
}

func findEquivEquivPatterns(
	packedPatterns [][]byte,
	gapCodes []byte,
	truncateLimits []int,
) ([][]byte, []byte, []int, []int) {
	n := len(packedPatterns)
	if n == 0 {
		return nil, nil, nil, nil
	}

	sigToCanon := make(map[string]int)
	patternCanon := make([]int, n)

	for i, packed := range packedPatterns {
		var gap byte
		if i < len(gapCodes) {
			gap = gapCodes[i]
		}
		sig := string(append([]byte{gap}, packed...))
		if canon, exists := sigToCanon[sig]; exists {
			patternCanon[i] = canon
		} else {
			sigToCanon[sig] = i
			patternCanon[i] = i
		}
	}

	var canonPatterns [][]byte
	var canonGapCodes []byte
	var canonTruncate []int
	oldToNew := make(map[int]int)

	for i, packed := range packedPatterns {
		if patternCanon[i] == i {
			oldToNew[i] = len(canonPatterns)
			canonPatterns = append(canonPatterns, packed)
			if i < len(gapCodes) {
				canonGapCodes = append(canonGapCodes, gapCodes[i])
			} else {
				canonGapCodes = append(canonGapCodes, 0)
			}
			if i < len(truncateLimits) {
				canonTruncate = append(canonTruncate, truncateLimits[i])
			} else {
				canonTruncate = append(canonTruncate, 64)
			}
		}
	}

	finalCanon := make([]int, n)
	for i := range packedPatterns {
		canonIdx := patternCanon[i]
		finalCanon[i] = oldToNew[canonIdx]
	}

	return canonPatterns, canonGapCodes, canonTruncate, finalCanon
}

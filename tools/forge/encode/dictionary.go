package encode

import (
	"fmt"
	"sort"
)

func buildDictionary(patterns [][]byte, truncateLimits []int) []byte {
	rowUsage := make(map[string]int)
	allRows := make(map[string]bool)

	for _, pat := range patterns {
		numRows := len(pat) / 3
		var prevRow [3]byte
		for row := 0; row < numRows; row++ {
			off := row * 3
			curRow := [3]byte{pat[off], pat[off+1], pat[off+2]}
			if curRow != [3]byte{0, 0, 0} {
				allRows[string(curRow[:])] = true
			}
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
		if entries[i].count != entries[j].count {
			return entries[i].count > entries[j].count
		}
		return string(entries[i].row[:]) < string(entries[j].row[:])
	})

	dict := make([]byte, (len(entries)+1)*3)
	dictSet := make(map[string]bool)
	dictSet[string([]byte{0, 0, 0})] = true
	for i, e := range entries {
		slot := i + 1
		copy(dict[slot*3:], e.row[:])
		dictSet[string(e.row[:])] = true
	}

	for row := range allRows {
		if !dictSet[row] {
			b := []byte(row)
			panic(fmt.Sprintf("buildDictionary: row %02X %02X %02X in patterns but not in dictionary", b[0], b[1], b[2]))
		}
	}

	return dict
}

func compactDictionary(
	dict []byte,
	rowToIdx map[string]int,
	patterns [][]byte,
	truncateLimits []int,
	equivMap map[int]int,
) ([]byte, map[int]int) {
	numEntries := len(dict) / 3
	usedIdx := make(map[int]bool)
	idxCount := make(map[int]int)
	usedIdx[0] = true

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
			if curRow == prevRow {
				continue
			}
			idx := rowToIdx[string(curRow[:])]
			if equivMap != nil {
				if mappedIdx, ok := equivMap[idx]; ok {
					idx = mappedIdx
				}
			}
			usedIdx[idx] = true
			idxCount[idx]++
			prevRow = curRow
		}
	}

	type entry struct {
		row    [3]byte
		oldIdx int
		count  int
	}
	var entries []entry
	for idx := 1; idx < numEntries; idx++ {
		if usedIdx[idx] {
			var row [3]byte
			copy(row[:], dict[idx*3:idx*3+3])
			entries = append(entries, entry{row, idx, idxCount[idx]})
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count != entries[j].count {
			return entries[i].count > entries[j].count
		}
		return string(entries[i].row[:]) < string(entries[j].row[:])
	})

	compactDict := make([]byte, (len(entries)+1)*3)
	oldToNew := make(map[int]int)
	oldToNew[0] = 0

	for i, e := range entries {
		slot := i + 1
		copy(compactDict[slot*3:], e.row[:])
		oldToNew[e.oldIdx] = slot
	}

	changed := true
	for changed {
		changed = false
		for oldIdx := 0; oldIdx < numEntries; oldIdx++ {
			if _, exists := oldToNew[oldIdx]; !exists {
				if equivMap != nil {
					if target, ok := equivMap[oldIdx]; ok {
						if newTarget, ok := oldToNew[target]; ok {
							oldToNew[oldIdx] = newTarget
							changed = true
							continue
						}
					}
				}
			}
		}
	}

	for oldIdx := 0; oldIdx < numEntries; oldIdx++ {
		if _, exists := oldToNew[oldIdx]; !exists {
			oldToNew[oldIdx] = 0
		}
	}

	return compactDict, oldToNew
}

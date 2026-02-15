package encode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type EquivResult struct {
	SongNum      int                 `json:"song"`
	Equiv        map[string][]string `json:"equiv"`
	ExcludedOrig []string            `json:"excluded_orig,omitempty"`
}

var globalEquivCache []EquivResult
var equivCacheLoaded bool

func LoadEquivCache(projectRoot string) []EquivResult {
	if equivCacheLoaded {
		return globalEquivCache
	}
	equivCacheLoaded = true

	cachePath := filepath.Join(projectRoot, "tools/odin_convert/equiv_cache.json")
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil
	}
	var results []EquivResult
	if json.Unmarshal(data, &results) != nil {
		return nil
	}
	globalEquivCache = results
	return results
}

// GetEquivSources returns all equiv source rows for a song (excluding already-excluded ones)
func GetEquivSources(projectRoot string, songNum int) []string {
	cache := LoadEquivCache(projectRoot)
	if cache == nil || songNum < 1 || songNum > len(cache) {
		return nil
	}
	songEquiv := cache[songNum-1]
	excluded := make(map[string]bool)
	for _, e := range songEquiv.ExcludedOrig {
		excluded[e] = true
	}
	var sources []string
	for src := range songEquiv.Equiv {
		if !excluded[src] {
			sources = append(sources, src)
		}
	}
	return sources
}

// GetExcludedOrig returns the excluded_orig entries for a song
func GetExcludedOrig(projectRoot string, songNum int) []string {
	cache := LoadEquivCache(projectRoot)
	if cache == nil || songNum < 1 || songNum > len(cache) {
		return nil
	}
	return cache[songNum-1].ExcludedOrig
}

// OverrideExcluded temporarily overrides excluded entries (nil = use cache defaults)
var OverrideExcluded []string
var UseOverrideExcluded bool

// TestExclusions is set during equiv bisection to test specific exclusions
var TestExclusions []string

// BuildEquivHexMap returns a map of source row hex -> target row hex for use during transformation.
// Works entirely in RAW (original GT) format - no translation.
// Patterns should be in RAW format (3 bytes per row: note|effect_hi, inst|effect_lo, param).
func BuildEquivHexMap(
	projectRoot string,
	songNum int,
	patterns [][]byte,
	truncateLimits []int,
) map[string]string {
	if songNum < 1 || songNum > 9 {
		return nil
	}

	equivCache := LoadEquivCache(projectRoot)
	if equivCache == nil {
		return nil
	}
	songEquiv := equivCache[songNum-1].Equiv
	if len(songEquiv) == 0 {
		return nil
	}

	excludedOrig := make(map[string]bool)
	if UseOverrideExcluded {
		for _, origHex := range OverrideExcluded {
			excludedOrig[origHex] = true
		}
	} else {
		for _, origHex := range equivCache[songNum-1].ExcludedOrig {
			excludedOrig[origHex] = true
		}
	}

	// Build set of rows actually used in patterns (RAW format)
	usedRows := make(map[string]bool)
	usedRows["000000"] = true
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
			if curRow != prevRow {
				rowHex := fmt.Sprintf("%02x%02x%02x", curRow[0], curRow[1], curRow[2])
				usedRows[rowHex] = true
			}
			prevRow = curRow
		}
	}

	// Build equiv rows with options (same strategy as BuildEquivMap)
	type equivRow struct {
		src     string
		options []string
		hasZero bool
	}

	var rows []equivRow
	for src, dsts := range songEquiv {
		if excludedOrig[src] {
			continue
		}
		if !usedRows[src] || src == "000000" {
			continue
		}
		var options []string
		hasZero := false
		for _, dst := range dsts {
			if dst != src {
				options = append(options, dst)
				if dst == "000000" {
					hasZero = true
				}
			}
		}
		if len(options) > 0 {
			rows = append(rows, equivRow{src: src, options: options, hasZero: hasZero})
		}
	}

	// Sort: hasZero first, then fewest options, then by src for determinism
	for i := 0; i < len(rows)-1; i++ {
		for j := i + 1; j < len(rows); j++ {
			swap := false
			if rows[j].hasZero && !rows[i].hasZero {
				swap = true
			} else if rows[j].hasZero == rows[i].hasZero {
				if len(rows[j].options) < len(rows[i].options) {
					swap = true
				} else if len(rows[j].options) == len(rows[i].options) && rows[j].src < rows[i].src {
					swap = true
				}
			}
			if swap {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}

	finalUsed := make(map[string]bool)
	for row := range usedRows {
		finalUsed[row] = true
	}

	result := make(map[string]string)

	// First pass: map all rows that can map to "000000"
	for _, r := range rows {
		if r.hasZero {
			result[r.src] = "000000"
			delete(finalUsed, r.src)
		}
	}

	// Second pass: map remaining rows to targets still in use
	for _, r := range rows {
		if _, mapped := result[r.src]; mapped {
			continue
		}

		bestTarget := ""
		for _, opt := range r.options {
			if finalUsed[opt] && (bestTarget == "" || opt < bestTarget) {
				bestTarget = opt
			}
		}

		if bestTarget != "" {
			result[r.src] = bestTarget
			delete(finalUsed, r.src)
		}
	}

	// Iterative pass: keep trying until stable
	changed := true
	for changed {
		changed = false
		for _, r := range rows {
			if _, mapped := result[r.src]; mapped {
				continue
			}

			bestTarget := ""
			for _, opt := range r.options {
				if finalUsed[opt] && (bestTarget == "" || opt < bestTarget) {
					bestTarget = opt
				}
			}

			if bestTarget != "" {
				result[r.src] = bestTarget
				delete(finalUsed, r.src)
				changed = true
			}
		}
	}

	return result
}

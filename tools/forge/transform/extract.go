package transform

import (
	"sort"

	"forge/analysis"
	"forge/parse"
)

func ExtractRawPatternsAsBytes(song parse.ParsedSong, anal analysis.SongAnalysis, raw []byte) ([][]byte, []int) {
	canonicalPatterns, _ := findTransposeEquivalents(song, anal, raw)

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

	var patterns [][]byte
	var truncateLimits []int

	for _, addr := range sortedPatterns {
		pat := song.Patterns[addr]
		truncateAt := 64
		if limit, ok := anal.TruncateLimits[addr]; ok && limit < truncateAt {
			truncateAt = limit
		}

		patBytes := make([]byte, 64*3)
		for row := 0; row < 64; row++ {
			r := pat.Rows[row]
			patBytes[row*3] = encodeB0(r.Note, r.Effect)
			patBytes[row*3+1] = encodeB1(r.Inst, r.Effect)
			patBytes[row*3+2] = r.Param
		}
		patterns = append(patterns, patBytes)
		truncateLimits = append(truncateLimits, truncateAt)
	}

	return patterns, truncateLimits
}

func ExtractPatternsAsBytes(song parse.ParsedSong, anal analysis.SongAnalysis, raw []byte, effectRemap [16]byte, fSubRemap map[int]byte, instRemap []int) ([][]byte, []int) {
	canonicalPatterns, _ := findTransposeEquivalents(song, anal, raw)

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

	var patterns [][]byte
	var truncateLimits []int

	for _, addr := range sortedPatterns {
		pat := song.Patterns[addr]
		truncateAt := 64
		if limit, ok := anal.TruncateLimits[addr]; ok && limit < truncateAt {
			truncateAt = limit
		}

		patBytes := make([]byte, 64*3)
		for row := 0; row < 64; row++ {
			r := pat.Rows[row]
			newNote := r.Note
			if newNote == 0x7F {
				newNote = 0x61
			}

			b0, b1, b2 := RemapRowBytes(
				encodeB0(newNote, r.Effect),
				encodeB1(r.Inst, r.Effect),
				r.Param,
				effectRemap,
				fSubRemap,
				instRemap,
			)
			patBytes[row*3] = b0
			patBytes[row*3+1] = b1
			patBytes[row*3+2] = b2
		}
		patterns = append(patterns, patBytes)
		truncateLimits = append(truncateLimits, truncateAt)
	}

	return patterns, truncateLimits
}

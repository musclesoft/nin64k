package transform

import (
	"forge/analysis"
	"forge/parse"
	"sort"
)

func findTransposeEquivalents(song parse.ParsedSong, anal analysis.SongAnalysis, raw []byte) (map[uint16]uint16, map[uint16]int) {
	var sortedAddrs []uint16
	for addr := range anal.PatternAddrs {
		sortedAddrs = append(sortedAddrs, addr)
	}
	sort.Slice(sortedAddrs, func(i, j int) bool {
		return sortedAddrs[i] < sortedAddrs[j]
	})

	contentToCanonical := make(map[string]uint16)
	addrToCanonical := make(map[uint16]uint16)
	addrTransposeDelta := make(map[uint16]int)

	for _, addr := range sortedAddrs {
		patData := parse.GetRawPatternData(raw, song.Addrs, addr)
		if patData == nil {
			continue
		}
		content := string(patData)

		if canonical, exists := contentToCanonical[content]; exists {
			addrToCanonical[addr] = canonical
			addrTransposeDelta[addr] = 0
			continue
		}

		found := false
		var canonAddrs []uint16
		for _, ca := range contentToCanonical {
			canonAddrs = append(canonAddrs, ca)
		}
		sort.Slice(canonAddrs, func(i, j int) bool {
			return canonAddrs[i] < canonAddrs[j]
		})

		for _, canonAddr := range canonAddrs {
			canonData := parse.GetRawPatternData(raw, song.Addrs, canonAddr)
			if canonData == nil {
				continue
			}
			if isEquiv, delta := checkTransposeEquiv(canonData, patData); isEquiv && delta != 0 {
				addrToCanonical[addr] = canonAddr
				addrTransposeDelta[addr] = delta
				found = true
				break
			}
		}

		if !found {
			contentToCanonical[content] = addr
			addrToCanonical[addr] = addr
			addrTransposeDelta[addr] = 0
		}
	}

	return addrToCanonical, addrTransposeDelta
}

func checkTransposeEquiv(canonPat, thisPat []byte) (bool, int) {
	transpose := 0
	transposeSet := false

	for row := 0; row < 64; row++ {
		off := row * 3
		noteCanon := canonPat[off] & 0x7F
		noteThis := thisPat[off] & 0x7F

		if (canonPat[off]&0x80) != (thisPat[off]&0x80) ||
			canonPat[off+1] != thisPat[off+1] ||
			canonPat[off+2] != thisPat[off+2] {
			return false, 0
		}

		if noteCanon != 0 || noteThis != 0 {
			if noteCanon == 0 || noteThis == 0 {
				return false, 0
			}
			diff := int(noteThis) - int(noteCanon)
			if !transposeSet {
				transpose = diff
				transposeSet = true
			} else if diff != transpose {
				return false, 0
			}
		}
	}

	return true, transpose
}

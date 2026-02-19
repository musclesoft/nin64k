package analysis

import (
	"forge/parse"
)

type SongAnalysis struct {
	EffectUsage       map[byte]int
	EffectParams      map[byte]map[byte]int
	FSubUsage         map[string]int
	ReachableOrders   []int
	OrderMap          map[int]int
	PatternAddrs      map[uint16]bool
	PatternBreaks     map[uint16]int
	PatternJumps      map[uint16]int
	UsedInstruments   []int
	InstrumentFreq    map[int]int
	FilterTriggerInst map[int]bool
	TruncateLimits    map[uint16]int
	DuplicateOrder    int // Order number that should duplicate DuplicateSource (-1 if none)
	DuplicateSource   int // Source order to duplicate
}

func Analyze(song parse.ParsedSong, raw []byte) SongAnalysis {
	analysis := SongAnalysis{
		EffectUsage:       make(map[byte]int),
		EffectParams:      make(map[byte]map[byte]int),
		FSubUsage:         make(map[string]int),
		PatternAddrs:      make(map[uint16]bool),
		PatternBreaks:     make(map[uint16]int),
		PatternJumps:      make(map[uint16]int),
		InstrumentFreq:    make(map[int]int),
		FilterTriggerInst: make(map[int]bool),
		TruncateLimits:    make(map[uint16]int),
		DuplicateOrder:    -1,
		DuplicateSource:   -1,
	}

	analysis.ReachableOrders, analysis.OrderMap = findReachableOrders(song, raw)

	for _, orderIdx := range analysis.ReachableOrders {
		for ch := 0; ch < 3; ch++ {
			if orderIdx < len(song.Orders[ch]) {
				addr := song.Orders[ch][orderIdx].PatternAddr
				analysis.PatternAddrs[addr] = true
			}
		}
	}

	for addr := range analysis.PatternAddrs {
		if pat, ok := song.Patterns[addr]; ok {
			breakRow, jumpTarget := getPatternBreakInfo(pat)
			analysis.PatternBreaks[addr] = breakRow
			analysis.PatternJumps[addr] = jumpTarget

			analyzePatternEffects(&analysis, pat, len(song.Instruments))
		}
	}

	analysis.UsedInstruments = findUsedInstruments(analysis.InstrumentFreq)
	analysis.TruncateLimits = computeTruncateLimits(song, analysis.ReachableOrders, raw)

	return analysis
}

func analyzePatternEffects(analysis *SongAnalysis, pat parse.Pattern, numInst int) {
	for row := 0; row < 64; row++ {
		r := pat.Rows[row]

		if r.Inst > 0 && int(r.Inst) < numInst {
			analysis.InstrumentFreq[int(r.Inst)]++
		}

		if r.Effect == 0xF && r.Param >= 0xE0 && r.Param < 0xF0 {
			triggerInst := int(r.Param & 0x0F)
			if triggerInst > 0 {
				analysis.FilterTriggerInst[triggerInst] = true
			}
		}

		if r.Effect != 0 {
			analysis.EffectUsage[r.Effect]++
			if analysis.EffectParams[r.Effect] == nil {
				analysis.EffectParams[r.Effect] = make(map[byte]int)
			}
			analysis.EffectParams[r.Effect][r.Param]++
		}

		if r.Effect == 0xF {
			classifyFSubEffect(analysis, r.Param)
		}
	}
}

func classifyFSubEffect(analysis *SongAnalysis, param byte) {
	switch {
	case param < 0x80:
		analysis.FSubUsage["speed"]++
	case param >= 0x80 && param < 0x90:
		analysis.FSubUsage["globalvol"]++
	case param >= 0x90 && param < 0xA0:
		analysis.FSubUsage["filtmode"]++
	case param >= 0xB0 && param < 0xC0:
		analysis.FSubUsage["fineslide"]++
	case param >= 0xE0 && param < 0xF0:
		analysis.FSubUsage["filttrig"]++
	case param >= 0xF0:
		analysis.FSubUsage["hrdrest"]++
	}
}

func findUsedInstruments(freq map[int]int) []int {
	var used []int
	for inst, count := range freq {
		if count > 0 {
			used = append(used, inst)
		}
	}
	return used
}

// AnalyzeWithOrders performs analysis using pre-computed reachable orders.
// This only analyzes patterns that are in the specified order list.
func AnalyzeWithOrders(song parse.ParsedSong, raw []byte, reachableOrders []int) SongAnalysis {
	analysis := SongAnalysis{
		EffectUsage:       make(map[byte]int),
		EffectParams:      make(map[byte]map[byte]int),
		FSubUsage:         make(map[string]int),
		PatternAddrs:      make(map[uint16]bool),
		PatternBreaks:     make(map[uint16]int),
		PatternJumps:      make(map[uint16]int),
		InstrumentFreq:    make(map[int]int),
		FilterTriggerInst: make(map[int]bool),
		TruncateLimits:    make(map[uint16]int),
		DuplicateOrder:    -1,
		DuplicateSource:   -1,
	}

	// Build order map (old index -> new index)
	analysis.ReachableOrders = reachableOrders
	analysis.OrderMap = make(map[int]int)
	for newIdx, oldIdx := range reachableOrders {
		analysis.OrderMap[oldIdx] = newIdx
	}

	// Collect pattern addresses from reachable orders
	for _, orderIdx := range analysis.ReachableOrders {
		for ch := 0; ch < 3; ch++ {
			if orderIdx < len(song.Orders[ch]) {
				addr := song.Orders[ch][orderIdx].PatternAddr
				analysis.PatternAddrs[addr] = true
			}
		}
	}

	// Analyze patterns
	for addr := range analysis.PatternAddrs {
		if pat, ok := song.Patterns[addr]; ok {
			breakRow, jumpTarget := getPatternBreakInfo(pat)
			analysis.PatternBreaks[addr] = breakRow
			analysis.PatternJumps[addr] = jumpTarget

			analyzePatternEffects(&analysis, pat, len(song.Instruments))
		}
	}

	analysis.UsedInstruments = findUsedInstruments(analysis.InstrumentFreq)
	analysis.TruncateLimits = computeTruncateLimits(song, analysis.ReachableOrders, raw)

	return analysis
}


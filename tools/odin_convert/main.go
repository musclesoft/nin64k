package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var projectRoot string

func init() {
	projectRoot = findProjectRoot()
}

func findProjectRoot() string {
	// Try to find project root by looking for known markers
	// Start from executable location, then try working directory
	candidates := []string{}

	// If running via "go run", check relative to source file
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(dir, "../.."))
	}

	// Check working directory and parent directories
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
		candidates = append(candidates, filepath.Join(wd, "../.."))
		for d := wd; d != "/" && d != "."; d = filepath.Dir(d) {
			candidates = append(candidates, d)
		}
	}

	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		// Check for known project structure
		if _, err := os.Stat(filepath.Join(abs, "src/odin_player.inc")); err == nil {
			return abs
		}
	}
	// Fallback: assume running from tools/odin_convert
	return "../.."
}

func projectPath(rel string) string {
	return filepath.Join(projectRoot, rel)
}

func commas(n uint64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}

// Code offsets where table addresses are stored (read address from offset+1)
const (
	codeSongStart   = 0x003B // LDA $xxxx,X - song start table
	codeTranspose0  = 0x00BA // LDA $xxxx,X - transpose table ch0
	codeTrackLo0    = 0x00C0 // LDA $xxxx,X - track lo ptr ch0
	codeTrackHi0    = 0x00C3 // LDY $xxxx,X - track hi ptr ch0
	codeTranspose1  = 0x00CE // LDA $xxxx,X - transpose table ch1
	codeTrackLo1    = 0x00D4 // LDA $xxxx,X - track lo ptr ch1
	codeTrackHi1    = 0x00D7 // LDY $xxxx,X - track hi ptr ch1
	codeTranspose2  = 0x00E2 // LDA $xxxx,X - transpose table ch2
	codeTrackLo2    = 0x00E8 // LDA $xxxx,X - track lo ptr ch2
	codeTrackHi2    = 0x00EB // LDY $xxxx,X - track hi ptr ch2
	codeInstAD      = 0x0520 // LDA $xxxx,Y - instrument AD table
	codeInstSR      = 0x0526 // LDA $xxxx,Y - instrument SR table
	codeWavetable   = 0x025F // LDA $xxxx,Y - wavetable
	codeArptable    = 0x0281 // LDA $xxxx,Y - arptable
	codeFiltertable = 0x015B // LDA $xxxx,Y - filtertable
)

func readWord(data []byte, offset int) uint16 {
	return uint16(data[offset]) | uint16(data[offset+1])<<8
}

func writeWord(data []byte, offset int, val uint16) {
	data[offset] = byte(val)
	data[offset+1] = byte(val >> 8)
}

// SongStats holds analysis results for a song
type SongStats struct {
	BaseAddr       int
	NumInstruments int
	NumOrders      int
	NumPatterns    int
	StartOrder     int
	WavetableSize  int
	ArptableSize   int
	FiltertableSize int
	MaxWaveIdx     int
	MaxArpIdx      int
	MaxFilterIdx   int
}

func analyzeSong(raw []byte) SongStats {
	stats := SongStats{}
	stats.BaseAddr = int(raw[2]) << 8

	// Extract addresses
	songStartOff := int(readWord(raw, codeSongStart)) - stats.BaseAddr
	instADAddr := readWord(raw, codeInstAD)
	instSRAddr := readWord(raw, codeInstSR)
	stats.NumInstruments = int(instSRAddr) - int(instADAddr)
	srcInstOff := int(instADAddr) - stats.BaseAddr

	wavetableOff := int(readWord(raw, codeWavetable)) - stats.BaseAddr
	arptableOff := int(readWord(raw, codeArptable)) - stats.BaseAddr
	filtertableOff := int(readWord(raw, codeFiltertable)) - stats.BaseAddr

	stats.WavetableSize = arptableOff - wavetableOff
	stats.ArptableSize = filtertableOff - arptableOff
	if stats.ArptableSize < 0 {
		stats.ArptableSize = 0
	}
	stats.FiltertableSize = 256
	if filtertableOff+stats.FiltertableSize > len(raw) {
		stats.FiltertableSize = len(raw) - filtertableOff
	}

	// Start order
	stats.StartOrder = int(raw[songStartOff])

	// Calculate numOrders from table layout gap
	transpose0Off := int(readWord(raw, codeTranspose0)) - stats.BaseAddr
	trackLo0Off := int(readWord(raw, codeTrackLo0)) - stats.BaseAddr
	stats.NumOrders = trackLo0Off - transpose0Off
	if stats.NumOrders <= 0 || stats.NumOrders > 255 {
		stats.NumOrders = 255
	}

	// Count unique patterns
	trackLoOff := []int{
		int(readWord(raw, codeTrackLo0)) - stats.BaseAddr,
		int(readWord(raw, codeTrackLo1)) - stats.BaseAddr,
		int(readWord(raw, codeTrackLo2)) - stats.BaseAddr,
	}
	trackHiOff := []int{
		int(readWord(raw, codeTrackHi0)) - stats.BaseAddr,
		int(readWord(raw, codeTrackHi1)) - stats.BaseAddr,
		int(readWord(raw, codeTrackHi2)) - stats.BaseAddr,
	}
	patternSet := make(map[uint16]bool)
	for ch := 0; ch < 3; ch++ {
		for i := 0; i < stats.NumOrders; i++ {
			if trackLoOff[ch]+i < len(raw) && trackHiOff[ch]+i < len(raw) {
				lo := raw[trackLoOff[ch]+i]
				hi := raw[trackHiOff[ch]+i]
				addr := uint16(lo) | uint16(hi)<<8
				srcOff := int(addr) - stats.BaseAddr
				if srcOff >= 0 && srcOff+192 <= len(raw) {
					patternSet[addr] = true
				}
			}
		}
	}
	stats.NumPatterns = len(patternSet)

	// Scan instruments for max table indices
	for inst := 0; inst < stats.NumInstruments; inst++ {
		// Wave indices: params 2,3,4 (start, end, loop)
		for _, param := range []int{2, 3, 4} {
			idx := srcInstOff + param*stats.NumInstruments + inst
			if idx < len(raw) {
				val := int(raw[idx])
				if val > stats.MaxWaveIdx && val < 255 {
					stats.MaxWaveIdx = val
				}
			}
		}
		// Arp indices: params 5,6,7
		for _, param := range []int{5, 6, 7} {
			idx := srcInstOff + param*stats.NumInstruments + inst
			if idx < len(raw) {
				val := int(raw[idx])
				if val > stats.MaxArpIdx && val < 255 {
					stats.MaxArpIdx = val
				}
			}
		}
		// Filter indices: params 13,14,15
		for _, param := range []int{13, 14, 15} {
			idx := srcInstOff + param*stats.NumInstruments + inst
			if idx < len(raw) {
				val := int(raw[idx])
				if val > stats.MaxFilterIdx && val < 255 {
					stats.MaxFilterIdx = val
				}
			}
		}
	}

	return stats
}

func printSongStats(songNum int, stats SongStats) {
	fmt.Printf("Song %d: base=$%04X inst=%d orders=%d pats=%d start=%d\n",
		songNum, stats.BaseAddr, stats.NumInstruments, stats.NumOrders, stats.NumPatterns, stats.StartOrder)
	fmt.Printf("  Tables: wave=%d (max idx %d), arp=%d (max idx %d), filter=%d (max idx %d)\n",
		stats.WavetableSize, stats.MaxWaveIdx, stats.ArptableSize, stats.MaxArpIdx,
		stats.FiltertableSize, stats.MaxFilterIdx)
	// Validation warnings
	if stats.MaxWaveIdx >= stats.WavetableSize {
		fmt.Printf("  WARNING: wave index %d >= table size %d\n", stats.MaxWaveIdx, stats.WavetableSize)
	}
	if stats.MaxArpIdx >= stats.ArptableSize {
		fmt.Printf("  WARNING: arp index %d >= table size %d\n", stats.MaxArpIdx, stats.ArptableSize)
	}
	if stats.MaxFilterIdx >= stats.FiltertableSize {
		fmt.Printf("  WARNING: filter index %d >= table size %d\n", stats.MaxFilterIdx, stats.FiltertableSize)
	}
}

// analyzeEffects returns a bitmap of effects used in patterns (effects 0-15)
// and a bitmap of F sub-effects (high nibble of parameter when effect=F)
func analyzeEffects(raw []byte) (uint16, uint16) {
	baseAddr := int(raw[2]) << 8
	trackLo0Off := int(readWord(raw, codeTrackLo0)) - baseAddr
	trackHi0Off := int(readWord(raw, codeTrackHi0)) - baseAddr
	trackLo1Off := int(readWord(raw, codeTrackLo1)) - baseAddr
	trackHi1Off := int(readWord(raw, codeTrackHi1)) - baseAddr
	trackLo2Off := int(readWord(raw, codeTrackLo2)) - baseAddr
	trackHi2Off := int(readWord(raw, codeTrackHi2)) - baseAddr
	trackLoOff := []int{trackLo0Off, trackLo1Off, trackLo2Off}
	trackHiOff := []int{trackHi0Off, trackHi1Off, trackHi2Off}

	// Collect unique pattern addresses
	patternAddrs := make(map[uint16]bool)
	for order := 0; order < 256; order++ {
		for ch := 0; ch < 3; ch++ {
			if trackLoOff[ch]+order >= len(raw) || trackHiOff[ch]+order >= len(raw) {
				continue
			}
			lo := raw[trackLoOff[ch]+order]
			hi := raw[trackHiOff[ch]+order]
			addr := uint16(lo) | uint16(hi)<<8
			srcOff := int(addr) - baseAddr
			if srcOff >= 0 && srcOff+192 <= len(raw) {
				patternAddrs[addr] = true
			}
		}
	}

	// Scan all patterns for effects
	var usedEffects uint16
	var fSubEffects uint16
	for addr := range patternAddrs {
		srcOff := int(addr) - baseAddr
		for row := 0; row < 64; row++ {
			off := srcOff + row*3
			byte0 := raw[off]
			byte1 := raw[off+1]
			byte2 := raw[off+2]
			effect := (byte1 >> 5) | ((byte0 >> 4) & 8)
			if effect != 0 {
				usedEffects |= 1 << effect
			}
			if effect == 0xF {
				param := byte2
				highNibble := param >> 4
				fSubEffects |= 1 << highNibble
			}
		}
	}
	return usedEffects, fSubEffects
}

// countEffectUsage returns counts of each effect (0-15) across all patterns
func countEffectUsage(raw []byte) [16]int {
	var counts [16]int
	baseAddr := int(raw[2]) << 8
	trackLo0Off := int(readWord(raw, codeTrackLo0)) - baseAddr
	trackHi0Off := int(readWord(raw, codeTrackHi0)) - baseAddr
	trackLo1Off := int(readWord(raw, codeTrackLo1)) - baseAddr
	trackHi1Off := int(readWord(raw, codeTrackHi1)) - baseAddr
	trackLo2Off := int(readWord(raw, codeTrackLo2)) - baseAddr
	trackHi2Off := int(readWord(raw, codeTrackHi2)) - baseAddr
	trackLoOff := []int{trackLo0Off, trackLo1Off, trackLo2Off}
	trackHiOff := []int{trackHi0Off, trackHi1Off, trackHi2Off}

	patternAddrs := make(map[uint16]bool)
	for order := 0; order < 256; order++ {
		for ch := 0; ch < 3; ch++ {
			if trackLoOff[ch]+order >= len(raw) || trackHiOff[ch]+order >= len(raw) {
				continue
			}
			lo := raw[trackLoOff[ch]+order]
			hi := raw[trackHiOff[ch]+order]
			addr := uint16(lo) | uint16(hi)<<8
			srcOff := int(addr) - baseAddr
			if srcOff >= 0 && srcOff+192 <= len(raw) {
				patternAddrs[addr] = true
			}
		}
	}

	for addr := range patternAddrs {
		srcOff := int(addr) - baseAddr
		for row := 0; row < 64; row++ {
			off := srcOff + row*3
			byte0 := raw[off]
			byte1 := raw[off+1]
			effect := (byte1 >> 5) | ((byte0 >> 4) & 8)
			counts[effect]++
		}
	}
	return counts
}

// countEffectParams returns parameter value counts for each effect type (0-15)
// Returns map[effect][param]count
func countEffectParams(raw []byte) map[int]map[int]int {
	result := make(map[int]map[int]int)
	for i := 0; i < 16; i++ {
		result[i] = make(map[int]int)
	}

	baseAddr := int(raw[2]) << 8
	trackLo0Off := int(readWord(raw, codeTrackLo0)) - baseAddr
	trackHi0Off := int(readWord(raw, codeTrackHi0)) - baseAddr
	trackLo1Off := int(readWord(raw, codeTrackLo1)) - baseAddr
	trackHi1Off := int(readWord(raw, codeTrackHi1)) - baseAddr
	trackLo2Off := int(readWord(raw, codeTrackLo2)) - baseAddr
	trackHi2Off := int(readWord(raw, codeTrackHi2)) - baseAddr
	trackLoOff := []int{trackLo0Off, trackLo1Off, trackLo2Off}
	trackHiOff := []int{trackHi0Off, trackHi1Off, trackHi2Off}

	patternAddrs := make(map[uint16]bool)
	for order := 0; order < 256; order++ {
		for ch := 0; ch < 3; ch++ {
			if trackLoOff[ch]+order >= len(raw) || trackHiOff[ch]+order >= len(raw) {
				continue
			}
			lo := raw[trackLoOff[ch]+order]
			hi := raw[trackHiOff[ch]+order]
			addr := uint16(lo) | uint16(hi)<<8
			srcOff := int(addr) - baseAddr
			if srcOff >= 0 && srcOff+192 <= len(raw) {
				patternAddrs[addr] = true
			}
		}
	}

	for addr := range patternAddrs {
		srcOff := int(addr) - baseAddr
		for row := 0; row < 64; row++ {
			off := srcOff + row*3
			byte0 := raw[off]
			byte1 := raw[off+1]
			byte2 := raw[off+2]
			effect := int((byte1 >> 5) | ((byte0 >> 4) & 8))
			if effect != 0 {
				result[effect][int(byte2)]++
			}
		}
	}
	return result
}

// analyzeTableDupes checks for duplicate ranges in wave/arp tables
func analyzeTableDupes(raw []byte) {
	baseAddr := int(raw[2]) << 8
	instADAddr := readWord(raw, codeInstAD)
	instSRAddr := readWord(raw, codeInstSR)
	numInst := int(instSRAddr) - int(instADAddr)
	srcInstOff := int(instADAddr) - baseAddr

	wavetableOff := int(readWord(raw, codeWavetable)) - baseAddr
	arptableOff := int(readWord(raw, codeArptable)) - baseAddr
	filtertableOff := int(readWord(raw, codeFiltertable)) - baseAddr

	// Collect ranges used by instruments
	waveRanges := make(map[string][]int) // content -> list of instruments
	arpRanges := make(map[string][]int)
	filterRanges := make(map[string][]int)

	for inst := 0; inst < numInst; inst++ {
		// Wave range
		waveStart := int(raw[srcInstOff+2*numInst+inst])
		waveEnd := int(raw[srcInstOff+3*numInst+inst])
		if waveStart < 255 && waveEnd < 255 && waveEnd >= waveStart {
			off := wavetableOff + waveStart
			length := waveEnd - waveStart + 1
			if off >= 0 && off+length <= len(raw) {
				content := string(raw[off : off+length])
				waveRanges[content] = append(waveRanges[content], inst)
			}
		}

		// Arp range
		arpStart := int(raw[srcInstOff+5*numInst+inst])
		arpEnd := int(raw[srcInstOff+6*numInst+inst])
		if arpStart < 255 && arpEnd < 255 && arpEnd >= arpStart {
			off := arptableOff + arpStart
			length := arpEnd - arpStart + 1
			if off >= 0 && off+length <= len(raw) {
				content := string(raw[off : off+length])
				arpRanges[content] = append(arpRanges[content], inst)
			}
		}

		// Filter range (params 13=start, 14=end, 15=loop)
		filtStart := int(raw[srcInstOff+13*numInst+inst])
		filtEnd := int(raw[srcInstOff+14*numInst+inst])
		if filtStart < 255 && filtEnd < 255 && filtEnd >= filtStart {
			off := filtertableOff + filtStart
			length := filtEnd - filtStart + 1
			if off >= 0 && off+length <= len(raw) {
				content := string(raw[off : off+length])
				filterRanges[content] = append(filterRanges[content], inst)
			}
		}
	}

	// Check for identical content at different positions
	waveDupeBytes := 0
	for content, insts := range waveRanges {
		if len(insts) > 1 {
			waveDupeBytes += len(content) * (len(insts) - 1)
		}
	}
	arpDupeBytes := 0
	for content, insts := range arpRanges {
		if len(insts) > 1 {
			arpDupeBytes += len(content) * (len(insts) - 1)
		}
	}
	// Calculate filter table actual used size
	filterUsedSize := 0
	for content := range filterRanges {
		filterUsedSize += len(content)
	}
	filterDupeBytes := 0
	for content, insts := range filterRanges {
		if len(insts) > 1 {
			filterDupeBytes += len(content) * (len(insts) - 1)
		}
	}

	fmt.Printf("  Dupe potential: wave=%d, arp=%d, filter=%d (used %d of 256)\n",
		waveDupeBytes, arpDupeBytes, filterDupeBytes, filterUsedSize)
}

// collectPatternContents extracts all unique pattern contents from a song
func collectPatternContents(raw []byte) map[string]bool {
	baseAddr := int(raw[2]) << 8
	trackLo0Off := int(readWord(raw, codeTrackLo0)) - baseAddr
	trackHi0Off := int(readWord(raw, codeTrackHi0)) - baseAddr
	trackLo1Off := int(readWord(raw, codeTrackLo1)) - baseAddr
	trackHi1Off := int(readWord(raw, codeTrackHi1)) - baseAddr
	trackLo2Off := int(readWord(raw, codeTrackLo2)) - baseAddr
	trackHi2Off := int(readWord(raw, codeTrackHi2)) - baseAddr
	trackLoOff := []int{trackLo0Off, trackLo1Off, trackLo2Off}
	trackHiOff := []int{trackHi0Off, trackHi1Off, trackHi2Off}

	// Collect unique pattern contents
	patterns := make(map[string]bool)
	for order := 0; order < 256; order++ {
		for ch := 0; ch < 3; ch++ {
			if trackLoOff[ch]+order >= len(raw) || trackHiOff[ch]+order >= len(raw) {
				continue
			}
			lo := raw[trackLoOff[ch]+order]
			hi := raw[trackHiOff[ch]+order]
			addr := uint16(lo) | uint16(hi)<<8
			srcOff := int(addr) - baseAddr
			if srcOff >= 0 && srcOff+192 <= len(raw) {
				content := string(raw[srcOff : srcOff+192])
				patterns[content] = true
			}
		}
	}
	return patterns
}

// analyzeRowDictCombinations analyzes the note/inst/fx combinations in a row dict
// Returns counts for: nop, note-only, inst-only, fx-only, note+inst, note+fx, inst+fx, note+inst+fx
func analyzeRowDictCombinations(dict []byte) [8]int {
	var counts [8]int
	numEntries := len(dict) / 3
	for i := 0; i < numEntries; i++ {
		off := i * 3
		byte0 := dict[off]     // note (bit 7 = effect bit 3)
		byte1 := dict[off+1]   // inst in bits 0-4, effect bits 0-2 in bits 5-7
		byte2 := dict[off+2]   // param

		note := byte0 & 0x7F
		inst := byte1 & 0x1F
		effect := (byte1 >> 5) | ((byte0 >> 4) & 8)
		param := byte2

		hasNote := note != 0
		hasInst := inst != 0
		hasFx := effect != 0 || (effect == 0 && param != 0 && hasNote)

		// If no effect, param is meaningless
		if effect == 0 {
			hasFx = false
		}

		switch {
		case !hasNote && !hasInst && !hasFx:
			counts[0]++ // nop
		case hasNote && !hasInst && !hasFx:
			counts[1]++ // note-only
		case !hasNote && hasInst && !hasFx:
			counts[2]++ // inst-only
		case !hasNote && !hasInst && hasFx:
			counts[3]++ // fx-only
		case hasNote && hasInst && !hasFx:
			counts[4]++ // note+inst
		case hasNote && !hasInst && hasFx:
			counts[5]++ // note+fx
		case !hasNote && hasInst && hasFx:
			counts[6]++ // inst+fx
		case hasNote && hasInst && hasFx:
			counts[7]++ // note+inst+fx
		}
	}
	return counts
}

// WaveSnippet represents a wavetable snippet used by an instrument
type WaveSnippet struct {
	Content    []byte
	LoopOffset int // offset of loop point within content
}

// GlobalWaveTable holds the combined wavetable and mapping info
type GlobalWaveTable struct {
	Data     []byte                      // the combined wavetable
	Snippets map[string]int              // snippet content -> start offset in Data
	Remap    map[int]map[int][3]int      // [songNum][instNum] -> [newStart, newEnd, newLoop]
}

// collectAllWaveSnippets collects wavetable snippets from all songs
func collectAllWaveSnippets(songData [][]byte) map[string]WaveSnippet {
	snippets := make(map[string]WaveSnippet)

	for _, raw := range songData {
		if raw == nil {
			continue
		}
		baseAddr := int(raw[2]) << 8
		instADAddr := readWord(raw, codeInstAD)
		instSRAddr := readWord(raw, codeInstSR)
		numInst := int(instSRAddr) - int(instADAddr)
		srcInstOff := int(instADAddr) - baseAddr
		waveOff := int(readWord(raw, codeWavetable)) - baseAddr

		for inst := 0; inst < numInst; inst++ {
			start := int(raw[srcInstOff+2*numInst+inst])
			end := int(raw[srcInstOff+3*numInst+inst])
			loop := int(raw[srcInstOff+4*numInst+inst])

			if start >= 255 || end >= 255 || end < start {
				continue
			}

			minIdx, maxIdx := start, end
			if loop < minIdx {
				minIdx = loop
			}
			if loop > maxIdx {
				maxIdx = loop
			}

			off := waveOff + minIdx
			length := maxIdx - minIdx + 1
			if off >= 0 && off+length <= len(raw) {
				content := raw[off : off+length]
				key := string(content)
				if _, exists := snippets[key]; !exists {
					snippets[key] = WaveSnippet{
						Content:    append([]byte{}, content...),
						LoopOffset: loop - minIdx,
					}
				}
			}
		}
	}
	return snippets
}

// buildGlobalWaveTable builds a combined wavetable using greedy superstring algorithm
func buildGlobalWaveTable(songData [][]byte) *GlobalWaveTable {
	snippets := collectAllWaveSnippets(songData)

	// Collect unique contents (sorted for determinism)
	var contents [][]byte
	for _, snip := range snippets {
		contents = append(contents, snip.Content)
	}
	// Sort by length descending, then by content
	sort.Slice(contents, func(i, j int) bool {
		if len(contents[i]) != len(contents[j]) {
			return len(contents[i]) > len(contents[j])
		}
		return string(contents[i]) < string(contents[j])
	})

	// Remove substrings (contents fully contained in other contents)
	var filtered [][]byte
	for i, s := range contents {
		isSubstring := false
		for j, t := range contents {
			if i != j && len(t) >= len(s) && bytes.Contains(t, s) {
				isSubstring = true
				break
			}
		}
		if !isSubstring {
			filtered = append(filtered, s)
		}
	}
	// Sort filtered for determinism
	sort.Slice(filtered, func(i, j int) bool {
		if len(filtered[i]) != len(filtered[j]) {
			return len(filtered[i]) > len(filtered[j])
		}
		return string(filtered[i]) < string(filtered[j])
	})

	// Greedy superstring: repeatedly merge pair with maximum overlap
	current := filtered
	for len(current) > 1 {
		bestI, bestJ := 0, 1
		bestOverlap := 0
		var bestMerged []byte

		for i := 0; i < len(current); i++ {
			for j := 0; j < len(current); j++ {
				if i == j {
					continue
				}
				// Find max overlap where suffix of current[i] == prefix of current[j]
				maxOv := len(current[i])
				if len(current[j]) < maxOv {
					maxOv = len(current[j])
				}
				for ov := maxOv; ov > 0; ov-- {
					if bytes.Equal(current[i][len(current[i])-ov:], current[j][:ov]) {
						if ov > bestOverlap {
							bestOverlap = ov
							bestI, bestJ = i, j
							bestMerged = append(append([]byte{}, current[i]...), current[j][ov:]...)
						}
						break
					}
				}
			}
		}

		if bestMerged == nil {
			// No overlap, concatenate first two
			bestMerged = append(append([]byte{}, current[0]...), current[1]...)
			bestI, bestJ = 0, 1
		}

		var next [][]byte
		for k := range current {
			if k != bestI && k != bestJ {
				next = append(next, current[k])
			}
		}
		next = append(next, bestMerged)
		current = next
	}

	combined := current[0]

	// Build snippet offset map
	snippetOffsets := make(map[string]int)
	for content := range snippets {
		idx := bytes.Index(combined, []byte(content))
		snippetOffsets[content] = idx
	}

	// Build per-song, per-instrument remap
	remap := make(map[int]map[int][3]int)
	for songNum := 1; songNum <= 9; songNum++ {
		raw := songData[songNum-1]
		if raw == nil {
			continue
		}
		remap[songNum] = make(map[int][3]int)

		baseAddr := int(raw[2]) << 8
		instADAddr := readWord(raw, codeInstAD)
		instSRAddr := readWord(raw, codeInstSR)
		numInst := int(instSRAddr) - int(instADAddr)
		srcInstOff := int(instADAddr) - baseAddr
		waveOff := int(readWord(raw, codeWavetable)) - baseAddr

		for inst := 0; inst < numInst; inst++ {
			start := int(raw[srcInstOff+2*numInst+inst])
			end := int(raw[srcInstOff+3*numInst+inst])
			loop := int(raw[srcInstOff+4*numInst+inst])

			if start >= 255 || end >= 255 || end < start {
				remap[songNum][inst] = [3]int{255, 255, 255}
				continue
			}

			minIdx, maxIdx := start, end
			if loop < minIdx {
				minIdx = loop
			}
			if loop > maxIdx {
				maxIdx = loop
			}

			off := waveOff + minIdx
			length := maxIdx - minIdx + 1
			if off >= 0 && off+length <= len(raw) {
				content := string(raw[off : off+length])
				globalOffset := snippetOffsets[content]

				// Remap indices: new = global_offset + (old - min)
				newStart := globalOffset + (start - minIdx)
				newEnd := globalOffset + (end - minIdx)
				newLoop := globalOffset + (loop - minIdx)
				remap[songNum][inst] = [3]int{newStart, newEnd, newLoop}
			} else {
				remap[songNum][inst] = [3]int{255, 255, 255}
			}
		}
	}

	return &GlobalWaveTable{
		Data:     combined,
		Snippets: snippetOffsets,
		Remap:    remap,
	}
}

// writeGlobalWaveTable writes the wavetable as an assembly include file
func writeGlobalWaveTable(gwt *GlobalWaveTable, path string) error {
	var buf bytes.Buffer
	buf.WriteString("; Auto-generated global wavetable - DO NOT EDIT\n")
	buf.WriteString(fmt.Sprintf("; %d bytes\n\n", len(gwt.Data)))
	buf.WriteString("global_wavetable:\n")

	for i := 0; i < len(gwt.Data); i += 16 {
		buf.WriteString("        .byte   ")
		end := i + 16
		if end > len(gwt.Data) {
			end = len(gwt.Data)
		}
		for j := i; j < end; j++ {
			if j > i {
				buf.WriteString(", ")
			}
			buf.WriteString(fmt.Sprintf("$%02X", gwt.Data[j]))
		}
		buf.WriteString("\n")
	}

	return os.WriteFile(path, buf.Bytes(), 0644)
}

// getPatternBreakInfo returns the first row with a break (0x0D) or position jump (0x0B) effect,
// and the jump target if it's a position jump (-1 otherwise). Uses original (pre-remap) effect numbers.
func getPatternBreakInfo(pat []byte) (breakRow int, jumpTarget int) {
	numRows := len(pat) / 3
	for row := 0; row < numRows; row++ {
		off := row * 3
		byte0 := pat[off]
		byte1 := pat[off+1]
		effect := (byte1 >> 5) | ((byte0 >> 4) & 8)
		if effect == 0x0B {
			return row, int(pat[off+2])
		}
		if effect == 0x0D {
			return row, -1
		}
	}
	return 64, -1
}

// remapPatternPositionJumps rewrites position jump targets using the order mapping
func remapPatternPositionJumps(pat []byte, orderMap map[int]int) {
	for row := 0; row < 64; row++ {
		rowOff := row * 3
		byte0 := pat[rowOff]
		byte1 := pat[rowOff+1]
		effect := (byte1 >> 5) | ((byte0 >> 4) & 0x08)
		if effect == 0x0B {
			oldTarget := int(pat[rowOff+2])
			if newTarget, ok := orderMap[oldTarget]; ok {
				pat[rowOff+2] = byte(newTarget)
			}
		}
	}
}

// findReachableOrders finds all orders reachable from startOrder using transitive closure
// Uses cross-channel analysis: only follows jumps that execute before any channel breaks
// Returns: ordered list of reachable order indices, and a map from old order to new order
func findReachableOrders(raw []byte, baseAddr, startOrder, numOrders int,
	trackLoOff, trackHiOff []int) ([]int, map[int]int) {

	rawLen := len(raw)

	// Find reachable orders using BFS with cross-channel break analysis
	reachable := make(map[int]bool)
	queue := []int{startOrder}
	reachable[startOrder] = true

	for len(queue) > 0 {
		order := queue[0]
		queue = queue[1:]

		// Get break info for all 3 channels at this order
		var breakRow [3]int
		var jumpTarget [3]int
		for ch := 0; ch < 3; ch++ {
			lo := raw[trackLoOff[ch]+order]
			hi := raw[trackHiOff[ch]+order]
			addr := uint16(lo) | uint16(hi)<<8
			srcOff := int(addr) - baseAddr
			if srcOff >= 0 && srcOff+192 <= rawLen {
				breakRow[ch], jumpTarget[ch] = getPatternBreakInfo(raw[srcOff : srcOff+192])
			} else {
				breakRow[ch], jumpTarget[ch] = 64, -1
			}
		}

		// Find minimum break row across all channels
		minBreak := breakRow[0]
		for ch := 1; ch < 3; ch++ {
			if breakRow[ch] < minBreak {
				minBreak = breakRow[ch]
			}
		}

		// Follow position jumps only from channels whose break is at the minimum row
		hasJump := false
		for ch := 0; ch < 3; ch++ {
			if breakRow[ch] == minBreak && jumpTarget[ch] >= 0 {
				target := jumpTarget[ch]
				if target < numOrders && !reachable[target] {
					reachable[target] = true
					queue = append(queue, target)
				}
				hasJump = true
			}
		}

		// If no position jump at minBreak, next order is reachable
		if !hasJump {
			nextOrder := order + 1
			if nextOrder < numOrders && !reachable[nextOrder] {
				reachable[nextOrder] = true
				queue = append(queue, nextOrder)
			}
		}
	}

	// Build ordered list: startOrder first, then sequential from start,
	// followed by any earlier orders reached via position jumps
	var orders []int
	for order := startOrder; order < numOrders; order++ {
		if reachable[order] {
			orders = append(orders, order)
		}
	}
	for order := 0; order < startOrder; order++ {
		if reachable[order] {
			orders = append(orders, order)
		}
	}

	orderMap := make(map[int]int)
	for newIdx, oldIdx := range orders {
		orderMap[oldIdx] = newIdx
	}

	return orders, orderMap
}

// ConversionStats holds before/after statistics
type ConversionStats struct {
	OrigOrders        int
	NewOrders         int
	OrigPatterns      int
	UniquePatterns    int
	OrigWaveSize      int
	NewWaveSize       int
	OrigArpSize       int
	NewArpSize        int
	NewFilterSize     int
	PatternDictSize   int
	PatternPackedSize int
	PrimaryIndices    int
	ExtendedIndices   int
}

// PrevSongTables holds table data from previous song for cross-song deduplication
type PrevSongTables struct {
	Arp     []byte
	Filter  []byte
	RowDict []byte
}

func convertToNewFormat(raw []byte, songNum int, prevTables *PrevSongTables, effectRemap [16]byte, fSubRemap map[int]byte, globalWave *GlobalWaveTable) ([]byte, ConversionStats) {
	var stats ConversionStats
	// Detect base address from entry point JMP (offset 0: 4c xx yy -> base is $yy00)
	baseAddr := int(raw[2]) << 8

	// Extract all table addresses from embedded player code
	songStartOff := int(readWord(raw, codeSongStart)) - baseAddr
	srcTranspose0Off := int(readWord(raw, codeTranspose0)) - baseAddr
	srcTranspose1Off := int(readWord(raw, codeTranspose1)) - baseAddr
	srcTranspose2Off := int(readWord(raw, codeTranspose2)) - baseAddr
	trackLo0Off := int(readWord(raw, codeTrackLo0)) - baseAddr
	trackLo1Off := int(readWord(raw, codeTrackLo1)) - baseAddr
	trackLo2Off := int(readWord(raw, codeTrackLo2)) - baseAddr
	trackHi0Off := int(readWord(raw, codeTrackHi0)) - baseAddr
	trackHi1Off := int(readWord(raw, codeTrackHi1)) - baseAddr
	trackHi2Off := int(readWord(raw, codeTrackHi2)) - baseAddr

	// Extract instrument spacing (= number of instruments)
	instADAddr := readWord(raw, codeInstAD)
	instSRAddr := readWord(raw, codeInstSR)
	numInst := int(instSRAddr) - int(instADAddr)
	srcInstOff := int(instADAddr) - baseAddr

	// Extract table offsets from embedded player code
	wavetableOff := int(readWord(raw, codeWavetable)) - baseAddr
	arptableOff := int(readWord(raw, codeArptable)) - baseAddr
	filtertableOff := int(readWord(raw, codeFiltertable)) - baseAddr

	// Track original table sizes
	stats.OrigWaveSize = arptableOff - wavetableOff
	stats.OrigArpSize = filtertableOff - arptableOff
	if stats.OrigArpSize < 0 {
		stats.OrigArpSize = 0
	}

	// Calculate numOrders from table layout gap (transpose0 to trackLo0)
	rawLen := len(raw)
	numOrders := trackLo0Off - srcTranspose0Off
	if numOrders <= 0 || numOrders > 255 {
		numOrders = 255 // Default to max
	}

	// Verify with valid pattern pointers
	trackLoOff := []int{trackLo0Off, trackLo1Off, trackLo2Off}
	trackHiOff := []int{trackHi0Off, trackHi1Off, trackHi2Off}

	// Get start order and find all reachable orders
	startOrder := int(raw[songStartOff])
	reachableOrders, orderMap := findReachableOrders(raw, baseAddr, startOrder, numOrders, trackLoOff, trackHiOff)
	newNumOrders := len(reachableOrders)
	stats.OrigOrders = numOrders
	stats.NewOrders = newNumOrders

	// Collect unique patterns from reachable orders only
	patternAddrs := make(map[uint16]bool)
	for _, oldOrder := range reachableOrders {
		for ch := 0; ch < 3; ch++ {
			lo := raw[trackLoOff[ch]+oldOrder]
			hi := raw[trackHiOff[ch]+oldOrder]
			addr := uint16(lo) | uint16(hi)<<8
			srcOff := int(addr) - baseAddr
			if srcOff >= 0 && srcOff+192 <= rawLen {
				patternAddrs[addr] = true
			}
		}
	}

	// Deduplicate patterns by content (exact match or transpose-equivalent)
	// Sort addresses first for deterministic output
	var sortedPatternAddrs []uint16
	for addr := range patternAddrs {
		sortedPatternAddrs = append(sortedPatternAddrs, addr)
	}
	for i := 0; i < len(sortedPatternAddrs)-1; i++ {
		for j := i + 1; j < len(sortedPatternAddrs); j++ {
			if sortedPatternAddrs[j] < sortedPatternAddrs[i] {
				sortedPatternAddrs[i], sortedPatternAddrs[j] = sortedPatternAddrs[j], sortedPatternAddrs[i]
			}
		}
	}

	// Helper: check if two patterns are transpose-equivalent
	// Returns (isEquiv, transposeOffset) where canonical_note + offset = this_note
	checkTransposeEquiv := func(canonPat, thisPat []byte) (bool, int) {
		transpose := 0
		transposeSet := false
		for row := 0; row < 64; row++ {
			off := row * 3
			noteCanon := canonPat[off] & 0x7F
			noteThis := thisPat[off] & 0x7F
			// Compare inst+effect bits (byte0 bit7 + byte1 + byte2)
			if (canonPat[off]&0x80) != (thisPat[off]&0x80) || canonPat[off+1] != thisPat[off+1] || canonPat[off+2] != thisPat[off+2] {
				return false, 0
			}
			// Check note transpose
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

	contentToCanonical := make(map[string]uint16)
	addrToCanonical := make(map[uint16]uint16)
	addrTransposeDelta := make(map[uint16]int) // transpose delta: canonical_note + delta = original_note
	for _, addr := range sortedPatternAddrs {
		srcOff := int(addr) - baseAddr
		content := string(raw[srcOff : srcOff+192])
		// First check exact match
		if canonical, exists := contentToCanonical[content]; exists {
			addrToCanonical[addr] = canonical
			addrTransposeDelta[addr] = 0
		} else {
			// Check transpose equivalence against existing canonical patterns (in sorted order)
			found := false
			var canonAddrs []uint16
			for _, ca := range contentToCanonical {
				canonAddrs = append(canonAddrs, ca)
			}
			sort.Slice(canonAddrs, func(i, j int) bool { return canonAddrs[i] < canonAddrs[j] })
			for _, canonAddr := range canonAddrs {
				canonOff := int(canonAddr) - baseAddr
				if isEquiv, delta := checkTransposeEquiv(raw[canonOff:canonOff+192], raw[srcOff:srcOff+192]); isEquiv && delta != 0 {
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
	}

	// Rewrite source transpose tables for transpose-equivalent patterns
	// This must happen BEFORE building the dictionary so all patterns use canonical notes
	srcTransposeOffsets := []int{srcTranspose0Off, srcTranspose1Off, srcTranspose2Off}
	transposeRewrites := 0
	for _, oldOrder := range reachableOrders {
		for ch := 0; ch < 3; ch++ {
			lo := raw[trackLoOff[ch]+oldOrder]
			hi := raw[trackHiOff[ch]+oldOrder]
			addr := uint16(lo) | uint16(hi)<<8
			if delta := addrTransposeDelta[addr]; delta != 0 {
				// Add delta to source transpose (canonical_note + delta = original_note)
				// So when playing canonical notes with adjusted transpose, we get original pitch
				oldTrans := raw[srcTransposeOffsets[ch]+oldOrder]
				raw[srcTransposeOffsets[ch]+oldOrder] = byte(int(oldTrans) + delta)
				transposeRewrites++
			}
		}
	}
	if transposeRewrites > 0 {
		fmt.Printf("  Transpose dedup: adjusted %d transpose entries\n", transposeRewrites)
	}

	// Rewrite pattern pointers in raw[] to point to canonical patterns
	// This ensures truncation limit calculations and other reads use canonical data
	pointerRewrites := 0
	for _, oldOrder := range reachableOrders {
		for ch := 0; ch < 3; ch++ {
			lo := raw[trackLoOff[ch]+oldOrder]
			hi := raw[trackHiOff[ch]+oldOrder]
			addr := uint16(lo) | uint16(hi)<<8
			canonical := addrToCanonical[addr]
			if canonical != addr {
				raw[trackLoOff[ch]+oldOrder] = byte(canonical)
				raw[trackHiOff[ch]+oldOrder] = byte(canonical >> 8)
				pointerRewrites++
			}
		}
	}
	if pointerRewrites > 0 {
		fmt.Printf("  Pattern dedup: rewrote %d pattern pointers\n", pointerRewrites)
	}

	// Build unique pattern list (only canonical addresses)
	uniquePatterns := make(map[uint16]bool)
	for _, canonical := range addrToCanonical {
		uniquePatterns[canonical] = true
	}
	patterns := make([]uint16, 0, len(uniquePatterns))
	for addr := range uniquePatterns {
		patterns = append(patterns, addr)
	}
	// Sort patterns by address
	for i := 0; i < len(patterns)-1; i++ {
		for j := i + 1; j < len(patterns); j++ {
			if patterns[j] < patterns[i] {
				patterns[i], patterns[j] = patterns[j], patterns[i]
			}
		}
	}

	// Create address-to-index map (via canonical address)
	canonicalToIndex := make(map[uint16]byte)
	for i, addr := range patterns {
		canonicalToIndex[addr] = byte(i)
	}
	patternIndex := make(map[uint16]byte)
	for addr, canonical := range addrToCanonical {
		patternIndex[addr] = canonicalToIndex[canonical]
	}
	numPatterns := len(patterns)
	stats.OrigPatterns = len(patternAddrs)
	stats.UniquePatterns = numPatterns

	// Full deduplication: group instruments by (full access range content, loop offset)
	wavetableSize := arptableOff - wavetableOff
	arptableSize := filtertableOff - arptableOff
	if arptableSize < 0 {
		arptableSize = 0
	}
	stats.OrigWaveSize = wavetableSize
	stats.OrigArpSize = arptableSize

	// Helper: find content in existing table
	findInTable := func(table []byte, content []byte) int {
		for i := 0; i <= len(table)-len(content); i++ {
			if bytes.Equal(table[i:i+len(content)], content) {
				return i
			}
		}
		return -1
	}

	// Full range info for each instrument
	type fullRange struct {
		min, max   int
		loopOffset int    // loop - min
		content    []byte // content of [min, max]
	}

	// Build wave table with full deduplication
	waveFullRanges := make([]fullRange, numInst)
	for inst := 0; inst < numInst; inst++ {
		start := int(raw[srcInstOff+2*numInst+inst])
		end := int(raw[srcInstOff+3*numInst+inst])
		loop := int(raw[srcInstOff+4*numInst+inst])
		if start >= 255 || end >= 255 || end < start {
			continue
		}
		minIdx, maxIdx := start, end
		if loop < minIdx {
			minIdx = loop
		}
		if loop > maxIdx {
			maxIdx = loop
		}
		off := wavetableOff + minIdx
		length := maxIdx - minIdx + 1
		if off >= 0 && off+length <= len(raw) {
			waveFullRanges[inst] = fullRange{
				min:        minIdx,
				max:        maxIdx,
				loopOffset: loop - minIdx,
				content:    raw[off : off+length],
			}
		}
	}

	// Group by (content, loopOffset) - instruments with same key can share
	type groupKey struct {
		content    string
		loopOffset int
	}
	waveGroups := make(map[groupKey][]int)
	for inst := 0; inst < numInst; inst++ {
		fr := waveFullRanges[inst]
		if len(fr.content) == 0 {
			continue
		}
		key := groupKey{string(fr.content), fr.loopOffset}
		waveGroups[key] = append(waveGroups[key], inst)
	}

	// Sort groups by size (largest first) then by content for determinism
	type sortedGroup struct {
		key   groupKey
		insts []int
	}
	var waveSorted []sortedGroup
	for key, insts := range waveGroups {
		waveSorted = append(waveSorted, sortedGroup{key, insts})
	}
	// Sort by length desc, then by content (wave is now global, no cross-song sharing)
	for i := 0; i < len(waveSorted)-1; i++ {
		for j := i + 1; j < len(waveSorted); j++ {
			li, lj := len(waveSorted[i].key.content), len(waveSorted[j].key.content)
			if lj > li || (lj == li && waveSorted[j].key.content < waveSorted[i].key.content) {
				waveSorted[i], waveSorted[j] = waveSorted[j], waveSorted[i]
			}
		}
	}

	// Build wave remap (only used as fallback if globalWave is nil)
	var newWaveTable []byte
	waveRemap := make([]int, numInst)
	for _, sg := range waveSorted {
		content := []byte(sg.key.content)
		pos := findInTable(newWaveTable, content)
		if pos < 0 {
			pos = len(newWaveTable)
			newWaveTable = append(newWaveTable, content...)
		}
		for _, inst := range sg.insts {
			waveRemap[inst] = pos - waveFullRanges[inst].min
		}
	}

	// Build arp table with full deduplication (same logic)
	arpFullRanges := make([]fullRange, numInst)
	for inst := 0; inst < numInst; inst++ {
		start := int(raw[srcInstOff+5*numInst+inst])
		end := int(raw[srcInstOff+6*numInst+inst])
		loop := int(raw[srcInstOff+7*numInst+inst])
		if start >= 255 || end >= 255 || end < start {
			continue
		}
		minIdx, maxIdx := start, end
		if loop < minIdx {
			minIdx = loop
		}
		if loop > maxIdx {
			maxIdx = loop
		}
		off := arptableOff + minIdx
		length := maxIdx - minIdx + 1
		if off >= 0 && off+length <= len(raw) {
			arpFullRanges[inst] = fullRange{
				min:        minIdx,
				max:        maxIdx,
				loopOffset: loop - minIdx,
				content:    raw[off : off+length],
			}
		}
	}

	arpGroups := make(map[groupKey][]int)
	for inst := 0; inst < numInst; inst++ {
		fr := arpFullRanges[inst]
		if len(fr.content) == 0 {
			continue
		}
		key := groupKey{string(fr.content), fr.loopOffset}
		arpGroups[key] = append(arpGroups[key], inst)
	}

	var arpSorted []sortedGroup
	for key, insts := range arpGroups {
		arpSorted = append(arpSorted, sortedGroup{key, insts})
	}
	// Helper to apply arp remapping for comparison
	remapArpContent := func(content []byte) []byte {
		result := make([]byte, len(content))
		copy(result, content)
		for i := range result {
			if result[i] == 0xFF {
				result[i] = 0xE7
			}
		}
		return result
	}
	// Sort: shared content first, then by size desc
	for i := 0; i < len(arpSorted)-1; i++ {
		for j := i + 1; j < len(arpSorted); j++ {
			iContent := remapArpContent([]byte(arpSorted[i].key.content))
			jContent := remapArpContent([]byte(arpSorted[j].key.content))
			iInPrev := prevTables != nil && len(prevTables.Arp) > 0 && findInTable(prevTables.Arp, iContent) >= 0
			jInPrev := prevTables != nil && len(prevTables.Arp) > 0 && findInTable(prevTables.Arp, jContent) >= 0
			if jInPrev && !iInPrev {
				arpSorted[i], arpSorted[j] = arpSorted[j], arpSorted[i]
			} else if iInPrev == jInPrev {
				li, lj := len(arpSorted[i].key.content), len(arpSorted[j].key.content)
				if lj > li || (lj == li && arpSorted[j].key.content < arpSorted[i].key.content) {
					arpSorted[i], arpSorted[j] = arpSorted[j], arpSorted[i]
				}
			}
		}
	}

	var newArpTable []byte
	arpPlaced := make([]bool, 188)
	arpRemap := make([]int, numInst)
	for _, sg := range arpSorted {
		content := []byte(sg.key.content)
		// Remap absolute note 127 ($FF) to note 103 ($E7) to shrink freq table
		for i := range content {
			if content[i] == 0xFF {
				content[i] = 0xE7 // $80 | 103
			}
		}
		pos := -1
		// First, check if this content exists in previous song's table
		if prevTables != nil && len(prevTables.Arp) > 0 {
			prevPos := findInTable(prevTables.Arp, content)
			if prevPos >= 0 {
				endPos := prevPos + len(content)
				// Only allow cross-song placement if it fits and doesn't create gaps
				if endPos <= 188 && prevPos <= len(newArpTable) {
					canPlace := true
					for i := 0; i < len(content); i++ {
						if arpPlaced[prevPos+i] && newArpTable[prevPos+i] != content[i] {
							canPlace = false
							break
						}
					}
					if canPlace {
						for len(newArpTable) < endPos {
							newArpTable = append(newArpTable, 0)
						}
						copy(newArpTable[prevPos:], content)
						for i := 0; i < len(content); i++ {
							arpPlaced[prevPos+i] = true
						}
						pos = prevPos
					}
				}
			}
		}
		if pos < 0 {
			pos = findInTable(newArpTable, content)
			if pos < 0 {
				pos = len(newArpTable)
				newArpTable = append(newArpTable, content...)
			}
			for i := 0; i < len(content); i++ {
				if pos+i < len(arpPlaced) {
					arpPlaced[pos+i] = true
				}
			}
		}
		for _, inst := range sg.insts {
			arpRemap[inst] = pos - arpFullRanges[inst].min
		}
	}

	// Build filter table with full deduplication (same logic as wave/arp)
	filterFullRanges := make([]fullRange, numInst)
	for inst := 0; inst < numInst; inst++ {
		start := int(raw[srcInstOff+13*numInst+inst])
		end := int(raw[srcInstOff+14*numInst+inst])
		loop := int(raw[srcInstOff+15*numInst+inst])
		if start >= 255 || end >= 255 || end < start {
			continue
		}
		minIdx, maxIdx := start, end
		// Only include loop in range if it's valid (< 255)
		if loop < 255 {
			if loop < minIdx {
				minIdx = loop
			}
			if loop > maxIdx {
				maxIdx = loop
			}
		}
		off := filtertableOff + minIdx
		length := maxIdx - minIdx + 1
		if off >= 0 && off+length <= len(raw) {
			loopOff := loop - minIdx
			if loop >= 255 {
				loopOff = 255 // Keep loop=255 as-is (disabled)
			}
			filterFullRanges[inst] = fullRange{
				min:        minIdx,
				max:        maxIdx,
				loopOffset: loopOff,
				content:    raw[off : off+length],
			}
		}
	}

	filterGroups := make(map[groupKey][]int)
	for inst := 0; inst < numInst; inst++ {
		fr := filterFullRanges[inst]
		if len(fr.content) == 0 {
			continue
		}
		key := groupKey{string(fr.content), fr.loopOffset}
		filterGroups[key] = append(filterGroups[key], inst)
	}

	var filterSorted []sortedGroup
	for key, insts := range filterGroups {
		filterSorted = append(filterSorted, sortedGroup{key, insts})
	}
	// Sort: shared content first, then by size desc
	for i := 0; i < len(filterSorted)-1; i++ {
		for j := i + 1; j < len(filterSorted); j++ {
			iInPrev := prevTables != nil && len(prevTables.Filter) > 0 && findInTable(prevTables.Filter, []byte(filterSorted[i].key.content)) >= 0
			jInPrev := prevTables != nil && len(prevTables.Filter) > 0 && findInTable(prevTables.Filter, []byte(filterSorted[j].key.content)) >= 0
			if jInPrev && !iInPrev {
				filterSorted[i], filterSorted[j] = filterSorted[j], filterSorted[i]
			} else if iInPrev == jInPrev {
				li, lj := len(filterSorted[i].key.content), len(filterSorted[j].key.content)
				if lj > li || (lj == li && filterSorted[j].key.content < filterSorted[i].key.content) {
					filterSorted[i], filterSorted[j] = filterSorted[j], filterSorted[i]
				}
			}
		}
	}

	// Start filter table at position 1 (position 0 reserved for "no filter" sentinel)
	newFilterTable := []byte{0}
	filterPlaced := make([]bool, 234)
	filterPlaced[0] = true // Position 0 is the sentinel
	filterRemap := make([]int, numInst)
	for _, sg := range filterSorted {
		content := []byte(sg.key.content)
		pos := -1
		// First, check if this content exists in previous song's table
		if prevTables != nil && len(prevTables.Filter) > 0 {
			prevPos := findInTable(prevTables.Filter, content)
			if prevPos >= 0 {
				endPos := prevPos + len(content)
				// Only allow cross-song placement if it fits and doesn't create gaps
				if endPos <= 234 && prevPos <= len(newFilterTable) {
					canPlace := true
					for i := 0; i < len(content); i++ {
						if filterPlaced[prevPos+i] && newFilterTable[prevPos+i] != content[i] {
							canPlace = false
							break
						}
					}
					if canPlace {
						for len(newFilterTable) < endPos {
							newFilterTable = append(newFilterTable, 0)
						}
						copy(newFilterTable[prevPos:], content)
						for i := 0; i < len(content); i++ {
							filterPlaced[prevPos+i] = true
						}
						pos = prevPos
					}
				}
			}
		}
		if pos < 0 {
			pos = findInTable(newFilterTable, content)
			if pos < 0 {
				pos = len(newFilterTable)
				newFilterTable = append(newFilterTable, content...)
			}
			for i := 0; i < len(content); i++ {
				if pos+i < len(filterPlaced) {
					filterPlaced[pos+i] = true
				}
			}
		}
		for _, inst := range sg.insts {
			filterRemap[inst] = pos - filterFullRanges[inst].min
		}
	}

	newWaveSize := len(newWaveTable)
	if globalWave != nil {
		newWaveSize = 0 // Wave is global, not per-song
	}
	newArpSize := len(newArpTable)
	newFilterSize := len(newFilterTable)
	stats.NewWaveSize = newWaveSize
	stats.NewArpSize = newArpSize
	stats.NewFilterSize = newFilterSize


	// Build new format (packed, exact sizes, all tables deduplicated)
	// $000: Instruments (512 bytes, 32 instruments × 16 params)
	// $200: Transpose ch0 (256 bytes)
	// $300: Transpose ch1 (256 bytes)
	// $400: Transpose ch2 (256 bytes)
	// $500: Trackptr ch0 (256 bytes)
	// $600: Trackptr ch1 (256 bytes)
	// $700: Trackptr ch2 (256 bytes)
	// $800: Filtertable (234 bytes max deduped)
	// $8EA: Wavetable (51 bytes max deduped)
	// $91D: Arptable (188 bytes max deduped)
	// $9D9: Packed pattern header + dictionary + data

	newInstOff := 0x000    // Instruments at start
	transpose0Off := 0x200 // Page-aligned offsets
	transpose1Off := 0x300
	transpose2Off := 0x400
	trackptr0Off := 0x500
	trackptr1Off := 0x600
	trackptr2Off := 0x700
	filterOff := 0x800         // Filter at $800 (227 bytes max)
	arpOff := 0x8E3            // Arp at $8E3 (188 bytes max)
	rowDictOff := 0x99F        // Row dict0 (notes), dict1 at +410, dict2 at +820
	packedPtrsOff := 0xE6D     // Packed pointers (182 bytes = 91 patterns × 2)
	packedDataOff := 0xF23     // Packed pattern data

	// Extract patterns to slice for packing (do effect/order remapping first)
	patternData := make([][]byte, numPatterns)
	for i, addr := range patterns {
		srcOff := int(addr) - baseAddr
		pat := make([]byte, 192)
		if srcOff >= 0 && srcOff+192 <= len(raw) {
			copy(pat, raw[srcOff:srcOff+192])
			remapPatternPositionJumps(pat, orderMap)
			remapPatternEffects(pat, effectRemap, fSubRemap)
		}
		patternData[i] = pat
	}

	// Compute cross-channel truncation limits for each pattern
	// For each order, find the minimum break row across all 3 channels
	// For each pattern, the truncation limit is the max across all orders where it's used
	patternTruncate := make([]int, numPatterns)
	for i := range patternTruncate {
		patternTruncate[i] = 0
	}
	for _, oldOrder := range reachableOrders {
		// Get patterns and break rows for all 3 channels at this order
		var orderPatIdx [3]byte
		var orderBreakRow [3]int
		for ch := 0; ch < 3; ch++ {
			lo := raw[trackLoOff[ch]+oldOrder]
			hi := raw[trackHiOff[ch]+oldOrder]
			addr := uint16(lo) | uint16(hi)<<8
			orderPatIdx[ch] = patternIndex[addr]
			srcOff := int(addr) - baseAddr
			if srcOff >= 0 && srcOff+192 <= rawLen {
				orderBreakRow[ch], _ = getPatternBreakInfo(raw[srcOff : srcOff+192])
			} else {
				orderBreakRow[ch] = 64
			}
		}
		// Find min break row across all channels at this order
		minBreak := orderBreakRow[0]
		for ch := 1; ch < 3; ch++ {
			if orderBreakRow[ch] < minBreak {
				minBreak = orderBreakRow[ch]
			}
		}
		// Update truncation limit for each pattern used at this order
		// The limit is minBreak + 1 (include the row with the break/jump)
		truncLimit := minBreak + 1
		if truncLimit > 64 {
			truncLimit = 64
		}
		for ch := 0; ch < 3; ch++ {
			idx := int(orderPatIdx[ch])
			if truncLimit > patternTruncate[idx] {
				patternTruncate[idx] = truncLimit
			}
		}
	}

	// Extract previous row dictionary for cross-song deduplication
	var prevDict []byte
	if prevTables != nil && len(prevTables.RowDict) > 0 {
		prevDict = prevTables.RowDict
	}

	// Build dictionary from ALL patterns (including non-canonical transpose equivalents)
	// This ensures rows from transpose-equivalent patterns are available for other patterns that share them
	allPatternData := make([][]byte, len(sortedPatternAddrs))
	for i, addr := range sortedPatternAddrs {
		srcOff := int(addr) - baseAddr
		pat := make([]byte, 192)
		if srcOff >= 0 && srcOff+192 <= len(raw) {
			copy(pat, raw[srcOff:srcOff+192])
			remapPatternPositionJumps(pat, orderMap)
			remapPatternEffects(pat, effectRemap, fSubRemap)
		}
		allPatternData[i] = pat
	}

	// Build dictionary first to find NOP entry for dead entry mapping
	dict, _ := buildPatternDict(allPatternData, prevDict)

	// Load equivalence map for this song (needs dict to find NOP)
	equivMap := loadSongEquivMap(songNum, dict)

	// Pack patterns with per-song dictionary + RLE
	dict, packed, patOffsets, primaryCount, extendedCount := packPatternsWithEquiv(patternData, equivMap, prevDict, patternTruncate)

	stats.PrimaryIndices = primaryCount
	stats.ExtendedIndices = extendedCount

	// Fixed layout:
	// $9D9: row dictionary (1236 bytes = 412 entries × 3)
	// $EAD: packed pointers (182 bytes = 91 patterns × 2)
	// $F63: packed data
	packedSize := len(packed)
	totalSize := packedDataOff + packedSize

	// Validate limits
	if numPatterns > 91 {
		panic(fmt.Sprintf("too many patterns: %d (max 91)", numPatterns))
	}
	if len(dict)/3 > 412 {
		panic(fmt.Sprintf("dictionary too large: %d entries (max 412)", len(dict)/3))
	}

	out := make([]byte, totalSize)

	// Write transpose tables (only reachable orders, remapped to 0..n-1)
	srcTransposeOff := []int{srcTranspose0Off, srcTranspose1Off, srcTranspose2Off}
	dstTransposeOff := []int{transpose0Off, transpose1Off, transpose2Off}
	for ch := 0; ch < 3; ch++ {
		for newIdx, oldIdx := range reachableOrders {
			out[dstTransposeOff[ch]+newIdx] = raw[srcTransposeOff[ch]+oldIdx]
		}
	}
	// Write track pointers (only reachable orders, remapped)
	dstTrackptrOff := []int{trackptr0Off, trackptr1Off, trackptr2Off}
	for ch := 0; ch < 3; ch++ {
		for newIdx, oldIdx := range reachableOrders {
			lo := raw[trackLoOff[ch]+oldIdx]
			hi := raw[trackHiOff[ch]+oldIdx]
			addr := uint16(lo) | uint16(hi)<<8
			out[dstTrackptrOff[ch]+newIdx] = patternIndex[addr]
		}
	}

	// Write instruments (interleaved layout: inst N at offset N*16)
	// Old format: all AD[0..n], all SR[0..n], all waveStart[0..n], etc.
	// New format: inst 0 params at $000-$00F, inst 1 at $010-$01F, etc.
	// Wave params: 2=start, 3=end, 4=loop; Arp params: 5=start, 6=end, 7=loop
	// Filter params: 13=start, 14=end, 15=loop
	for inst := 0; inst < numInst; inst++ {
		for param := 0; param < 16; param++ {
			srcIdx := srcInstOff + param*numInst + inst
			dstIdx := newInstOff + inst*16 + param
			if srcIdx < len(raw) {
				val := raw[srcIdx]
				// Remap wave indices (params 2,3,4) using global wavetable
				if param >= 2 && param <= 4 && val < 255 {
					if globalWave != nil {
						if waveIdx, ok := globalWave.Remap[songNum][inst]; ok {
							// param 2=start, 3=end, 4=loop -> indices 0,1,2 in waveIdx
							val = byte(waveIdx[param-2])
						}
					} else {
						newVal := int(val) + waveRemap[inst]
						if newVal < 0 {
							newVal = 0
						} else if newVal > 254 {
							newVal = 254
						}
						val = byte(newVal)
					}
				}
				// Remap arp indices (params 5,6,7)
				if param >= 5 && param <= 7 && val < 255 {
					newVal := int(val) + arpRemap[inst]
					if newVal < 0 {
						newVal = 0
					} else if newVal > 254 {
						newVal = 254
					}
					val = byte(newVal)
				}
				// Remap vibrato depth (param 9 = INST_VIBDEPSP, high nibble is depth)
				// Frequency-sorted: 4(22) 2(13) 3(11) 1(6) 6(2) 10(1) 5(1) 8(1) 15(1)
				// Remap to 0,1,2,3,4,5,6,7,8,9 by frequency
				if param == 9 {
					vibDepthRemap := [16]byte{0, 4, 2, 3, 1, 7, 5, 0, 8, 0, 6, 0, 0, 0, 0, 9}
					oldDepth := val >> 4
					speed := val & 0x0F
					newDepth := vibDepthRemap[oldDepth]
					val = (newDepth << 4) | speed
				}
				// Swap nibbles for pulse width (param 10 = INST_PULSEWIDTH)
				// Allows player to use AND instead of 4x ASL/LSR
				if param == 10 {
					val = (val << 4) | (val >> 4)
				}
				// Remap filter indices (params 13,14,15)
				if param >= 13 && param <= 15 && val < 255 {
					if len(filterFullRanges[inst].content) > 0 {
						// Valid filter range - apply remap
						newVal := int(val) + filterRemap[inst]
						if newVal < 0 {
							newVal = 0
						} else if newVal > 254 {
							newVal = 254
						}
						val = byte(newVal)
					} else {
						// No valid filter range - disable filter for this instrument
						val = 255
					}
				}
				// Store end+1 for wave/arp/filter end indices (params 3, 6, 14)
				// Allows player to use single BCC instead of BCC+BEQ for <= check
				if (param == 3 || param == 6 || param == 14) && val < 255 {
					val++
				}
				out[dstIdx] = val
			}
		}
	}

	// Copy deduplicated tables (wavetable is global in player)
	copy(out[filterOff:], newFilterTable)
	copy(out[arpOff:], newArpTable)

	// Write pattern packing data
	// Row dictionary in split format: 3 arrays of 409 bytes each (dict[0] implicit)
	// dict0 (notes), dict1 (inst|effect), dict2 (params)
	// dict[0] is always [0,0,0], not stored - dict[1] starts at offset 0
	numEntries := len(dict) / 3
	for i := 1; i < numEntries; i++ {
		out[rowDictOff+i-1] = dict[i*3]         // note (dict[i] at offset i-1)
		out[rowDictOff+410+i-1] = dict[i*3+1]   // inst|effect
		out[rowDictOff+820+i-1] = dict[i*3+2]   // param
	}
	// Packed pointers (offset into packed data per pattern)
	for i, pOff := range patOffsets {
		out[packedPtrsOff+i*2] = byte(pOff & 0xFF)
		out[packedPtrsOff+i*2+1] = byte(pOff >> 8)
	}
	// Packed pattern data
	copy(out[packedDataOff:], packed)

	stats.PatternDictSize = len(dict) / 3
	stats.PatternPackedSize = len(packed)

	return out, stats
}

// remapPatternEffects remaps effect numbers and parameters in pattern data
// New encoding:
// - Effect 0: param 0=none, 1=vib, 2=break, 3=fineslide (no-param effects)
// - Effects 1-E: the 14 variable-param effects (regular + F sub-effects)
func remapPatternEffects(pattern []byte, remap [16]byte, fSubRemap map[int]byte) {
	for row := 0; row < 64; row++ {
		off := row * 3
		byte0 := pattern[off]
		byte1 := pattern[off+1]
		byte2 := pattern[off+2]
		// Extract old effect: (byte1 >> 5) | ((byte0 >> 4) & 8)
		oldEffect := (byte1 >> 5) | ((byte0 >> 4) & 8)

		var newEffect byte
		var newParam byte = byte2

		switch oldEffect {
		case 0:
			// No effect -> effect 0, param 0
			newEffect = 0
			newParam = 0

		case 4:
			// Vib (always param $00) -> effect 0, param 1
			newEffect = 0
			newParam = 1

		case 0xD:
			// Break (always param $00) -> effect 0, param 2
			newEffect = 0
			newParam = 2

		case 0xF:
			// Extended effects - split into separate effects or effect 0
			if byte2 < 0x80 {
				// Speed ($00-$7F) -> separate effect
				newEffect = fSubRemap[0x10] // speed
				newParam = byte2
			} else {
				hiNib := byte2 & 0xF0
				loNib := byte2 & 0x0F
				switch hiNib {
				case 0xB0:
					// Fineslide (always param $B1) -> effect 0, param 3
					newEffect = 0
					newParam = 3
				case 0xF0:
					// Hard restart -> separate effect
					newEffect = fSubRemap[0x11]
					newParam = loNib
				case 0xE0:
					// Filter trigger -> separate effect (pre-shifted *16 for player)
					newEffect = fSubRemap[0x12]
					newParam = loNib << 4
				case 0x80:
					// Global volume -> separate effect
					newEffect = fSubRemap[0x13]
					newParam = loNib
				case 0x90:
					// Filter mode -> separate effect (pre-shifted for player)
					newEffect = fSubRemap[0x14]
					newParam = loNib << 4
				default:
					newEffect = 0
					newParam = 0
				}
			}

		case 1:
			// Slide: $80/$81 -> 0 (up), $00 -> 1 (down)
			newEffect = remap[1]
			if byte2&0x80 != 0 {
				newParam = 0 // up
			} else {
				newParam = 1 // down
			}

		case 2:
			// Pulse width: $00 -> 0, $80 -> 1
			newEffect = remap[2]
			if byte2 == 0x80 {
				newParam = 1
			} else {
				newParam = 0
			}

		case 7:
			// AD: keep literal value
			newEffect = remap[7]
			newParam = byte2

		case 8:
			// SR: keep literal value
			newEffect = remap[8]
			newParam = byte2

		case 9:
			// Wave: keep literal value
			newEffect = remap[9]
			newParam = byte2

		case 0xE:
			// Reso: keep literal value
			newEffect = remap[0xE]
			newParam = byte2

		case 3:
			// Porta: swap nibbles for faster player processing
			newEffect = remap[3]
			newParam = ((byte2 & 0x0F) << 4) | ((byte2 & 0xF0) >> 4)

		default:
			// Other effects (A=arp, B=jump) - just remap effect number
			newEffect = remap[oldEffect]
			newParam = byte2
		}

		// Encode new effect: byte0 bit 7 = effect bit 3, byte1 bits 5-7 = effect bits 0-2
		byte0 = (byte0 & 0x7F) | ((newEffect & 8) << 4)
		byte1 = (byte1 & 0x1F) | ((newEffect & 7) << 5)
		pattern[off] = byte0
		pattern[off+1] = byte1
		pattern[off+2] = newParam
	}
}


// Global caches loaded once
// NOTE: Delete tools/odin_convert/equiv_cache.json when changing the pattern format
// (e.g., effect parameter encoding, row dictionary structure)
var globalEquivCache []EquivResult
var equivCacheLoaded bool

// loadEquivCache loads the equivalence cache from disk
func loadEquivCache() []EquivResult {
	if equivCacheLoaded {
		return globalEquivCache
	}
	equivCacheLoaded = true

	data, err := os.ReadFile(projectPath("tools/odin_convert/equiv_cache.json"))
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

// loadSongEquivMap returns equivalence map for a specific song
// Uses verified equivalences from equiv_cache (extended -> primary with identical output)
func loadSongEquivMap(songNum int, dict []byte) map[int]int {
	if songNum < 1 || songNum > 9 {
		return nil
	}

	equivCache := loadEquivCache()
	if equivCache == nil {
		return nil
	}

	songEquiv := equivCache[songNum-1].Equiv
	if len(songEquiv) == 0 {
		return nil
	}

	equivMap := make(map[int]int)
	for extIdxStr, priIdx := range songEquiv {
		var extIdx int
		fmt.Sscanf(extIdxStr, "%d", &extIdx)
		equivMap[extIdx] = priIdx
	}

	return equivMap
}

// buildPatternDict builds the dictionary from patterns (sorted by frequency)
// If prevDict is provided, shared rows are placed at matching positions for cross-song deduplication
func buildPatternDict(patterns [][]byte, prevDict []byte) (dict []byte, rowToIdx map[string]int) {
	rowFreq := make(map[string]int)
	for _, pat := range patterns {
		var prevRow [3]byte
		for row := 0; row < 64; row++ {
			off := row * 3
			curRow := [3]byte{pat[off], pat[off+1], pat[off+2]}
			if curRow != prevRow {
				rowFreq[string(curRow[:])]++
			}
			prevRow = curRow
		}
	}

	type rowEntry struct {
		row  string
		freq int
	}
	var sortedRows []rowEntry
	for row, freq := range rowFreq {
		sortedRows = append(sortedRows, rowEntry{row, freq})
	}
	for i := 0; i < len(sortedRows)-1; i++ {
		for j := i + 1; j < len(sortedRows); j++ {
			if sortedRows[j].freq > sortedRows[i].freq ||
				(sortedRows[j].freq == sortedRows[i].freq && sortedRows[j].row < sortedRows[i].row) {
				sortedRows[i], sortedRows[j] = sortedRows[j], sortedRows[i]
			}
		}
	}

	maxEntries := len(sortedRows)
	if maxEntries > 412 {
		maxEntries = 412
	}

	rowToIdx = make(map[string]int)
	dict = make([]byte, maxEntries*3)
	placed := make([]bool, maxEntries)

	// Index 0 is always reserved for [0,0,0] (implicit, not stored)
	zeroRow := string([]byte{0, 0, 0})
	rowToIdx[zeroRow] = 0
	placed[0] = true
	// dict[0:3] stays zero (not stored in output anyway)

	// Build map of previous dictionary rows to their indices
	prevRowToIdx := make(map[string]int)
	if prevDict != nil {
		for i := 0; i < len(prevDict)/3; i++ {
			row := string(prevDict[i*3 : i*3+3])
			prevRowToIdx[row] = i
		}
	}

	// First pass: place shared rows at their previous positions (skip index 0)
	remaining := make([]rowEntry, 0, len(sortedRows))
	for _, entry := range sortedRows {
		if entry.row == zeroRow {
			continue // already placed at index 0
		}
		if prevIdx, ok := prevRowToIdx[entry.row]; ok && prevIdx < maxEntries && prevIdx > 0 && !placed[prevIdx] {
			rowToIdx[entry.row] = prevIdx
			copy(dict[prevIdx*3:], entry.row)
			placed[prevIdx] = true
		} else {
			remaining = append(remaining, entry)
		}
	}

	// Second pass: fill remaining slots with non-shared rows by frequency (skip index 0)
	nextSlot := 1
	for _, entry := range remaining {
		for nextSlot < maxEntries && placed[nextSlot] {
			nextSlot++
		}
		if nextSlot >= maxEntries {
			break
		}
		rowToIdx[entry.row] = nextSlot
		copy(dict[nextSlot*3:], entry.row)
		placed[nextSlot] = true
		nextSlot++
	}

	return dict, rowToIdx
}

// packPatternsWithEquiv packs pattern data, using equivalences to reduce extended indices
// truncateLimits provides cross-channel truncation limits (max reachable row+1 for each pattern)
func packPatternsWithEquiv(patterns [][]byte, equivMap map[int]int, prevDict []byte, truncateLimits []int) (dict []byte, packed []byte, offsets []uint16, primaryCount int, extendedCount int) {
	dict, rowToIdx := buildPatternDict(patterns, prevDict)

	// Pack each pattern individually first
	// Format: $00-$0E = dict[0]+RLE 0-14, $0F-$EE = dict[1-224], $EF-$FE = RLE 1-16, $FF = extended
	const primaryMax = 225
	const rleMax = 16
	const rleBase = 0xEF
	const extMarker = 0xFF
	const dictZeroRleMax = 14   // $00-$0E for dict[0] with RLE 0-14
	const dictOffsetBase = 0x0F // dict[1] = $0F, dict[2] = $10, etc.

	patternPacked := make([][]byte, len(patterns))
	for i, pat := range patterns {
		var patPacked []byte
		var prevRow [3]byte
		repeatCount := 0
		numRows := len(pat) / 3

		// Track if last emitted was dict[0] (for combining with RLE)
		lastWasDictZero := false
		lastDictZeroPos := -1

		// Use cross-channel truncation limit if provided, otherwise use pattern length
		truncateAfter := numRows
		if truncateLimits != nil && i < len(truncateLimits) && truncateLimits[i] > 0 && truncateLimits[i] < truncateAfter {
			truncateAfter = truncateLimits[i]
		}

		// Helper to emit pending RLE
		emitRLE := func() {
			if repeatCount == 0 {
				return
			}
			if lastWasDictZero && lastDictZeroPos >= 0 && repeatCount <= dictZeroRleMax {
				// Combine dict[0] with RLE into single byte $00-$0E
				patPacked[lastDictZeroPos] = byte(repeatCount)
				lastWasDictZero = false
			} else {
				// Emit separate RLE byte(s)
				if lastWasDictZero {
					lastWasDictZero = false
				}
				for repeatCount > 0 {
					emit := repeatCount
					if emit > rleMax {
						emit = rleMax
					}
					patPacked = append(patPacked, byte(rleBase+emit-1))
					repeatCount -= emit
				}
			}
			repeatCount = 0
		}

		for row := 0; row < truncateAfter; row++ {
			off := row * 3
			curRow := [3]byte{pat[off], pat[off+1], pat[off+2]}

			if curRow == prevRow {
				repeatCount++
				// Check if we need to flush: hit max RLE, or dict[0]+RLE limit
				maxAllowed := rleMax
				if lastWasDictZero && lastDictZeroPos >= 0 {
					maxAllowed = dictZeroRleMax
				}
				if repeatCount == maxAllowed || row == truncateAfter-1 {
					emitRLE()
				}
			} else {
				emitRLE()
				idx := rowToIdx[string(curRow[:])]

				// Apply equivalence: if this is an extended index with a primary equivalent, use it
				if idx >= primaryMax && equivMap != nil {
					if priIdx, ok := equivMap[idx]; ok {
						idx = priIdx
					}
				}

				if idx == 0 {
					// Dict[0]: emit $00, might be combined with following RLE
					lastDictZeroPos = len(patPacked)
					patPacked = append(patPacked, 0x00)
					lastWasDictZero = true
					primaryCount++
				} else if idx < primaryMax {
					// Dict[1-224]: emit $0F-$EE (offset by $0E)
					patPacked = append(patPacked, byte(dictOffsetBase+idx-1))
					lastWasDictZero = false
					primaryCount++
				} else {
					patPacked = append(patPacked, extMarker, byte(idx-primaryMax))
					lastWasDictZero = false
					extendedCount++
				}
			}
			prevRow = curRow
		}
		emitRLE()
		patternPacked[i] = patPacked
	}

	// Optimize packing using suffix/prefix overlap (greedy superstring)
	packed, offsets = optimizePackedOverlap(patternPacked)

	return dict, packed, offsets, primaryCount, extendedCount
}

// optimizePackedOverlap uses greedy superstring algorithm to find optimal overlapping
func optimizePackedOverlap(patterns [][]byte) (packed []byte, offsets []uint16) {
	n := len(patterns)
	if n == 0 {
		return nil, nil
	}

	// Find identical patterns first - they can share the same offset
	canonical := make([]int, n)
	for i := range canonical {
		canonical[i] = i
	}
	for i := 0; i < n; i++ {
		if canonical[i] != i {
			continue
		}
		for j := i + 1; j < n; j++ {
			if canonical[j] != j {
				continue
			}
			if string(patterns[i]) == string(patterns[j]) {
				canonical[j] = i
			}
		}
	}

	// Get unique patterns
	var uniquePatterns [][]byte
	uniqueToOrig := make([]int, 0)
	origToUnique := make([]int, n)
	for i := 0; i < n; i++ {
		if canonical[i] == i {
			origToUnique[i] = len(uniquePatterns)
			uniqueToOrig = append(uniqueToOrig, i)
			uniquePatterns = append(uniquePatterns, patterns[i])
		} else {
			origToUnique[i] = -1 // will be set from canonical
		}
	}
	for i := 0; i < n; i++ {
		if canonical[i] != i {
			origToUnique[i] = origToUnique[canonical[i]]
		}
	}

	numUnique := len(uniquePatterns)
	if numUnique == 0 {
		return nil, make([]uint16, n)
	}

	// Greedy superstring: work with indices into uniquePatterns
	// We'll build a superstring by greedily merging patterns with maximum overlap

	// strings[i] is the current merged string at position i (nil if merged into another)
	strings := make([][]byte, numUnique)
	for i := range strings {
		strings[i] = make([]byte, len(uniquePatterns[i]))
		copy(strings[i], uniquePatterns[i])
	}

	// patternOffset[i] = offset of original pattern i within strings[root[i]]
	patternOffset := make([]int, numUnique)
	// root[i] = which string index pattern i currently belongs to
	root := make([]int, numUnique)
	for i := range root {
		root[i] = i
	}

	// Repeatedly find and apply best merge
	for {
		bestOverlap := 0
		bestI, bestJ := -1, -1

		// Find best overlap between any two active strings
		for i := 0; i < numUnique; i++ {
			if strings[i] == nil {
				continue
			}
			for j := 0; j < numUnique; j++ {
				if i == j || strings[j] == nil {
					continue
				}
				// Check suffix of strings[i] vs prefix of strings[j]
				si, sj := strings[i], strings[j]
				maxLen := len(si)
				if len(sj) < maxLen {
					maxLen = len(sj)
				}
				for l := maxLen; l >= 1; l-- {
					if string(si[len(si)-l:]) == string(sj[:l]) {
						if l > bestOverlap {
							bestOverlap = l
							bestI, bestJ = i, j
						}
						break
					}
				}
			}
		}

		if bestOverlap == 0 {
			break
		}

		// Merge strings[bestJ] into strings[bestI]
		si := strings[bestI]
		sj := strings[bestJ]
		merged := make([]byte, len(si)+len(sj)-bestOverlap)
		copy(merged, si)
		copy(merged[len(si):], sj[bestOverlap:])
		strings[bestI] = merged

		// Update all patterns that were in bestJ to now be in bestI
		offsetShift := len(si) - bestOverlap
		for p := 0; p < numUnique; p++ {
			if root[p] == bestJ {
				root[p] = bestI
				patternOffset[p] += offsetShift
			}
		}

		strings[bestJ] = nil
	}

	// Concatenate all remaining strings and compute final offsets
	uniqueOffset := make([]int, numUnique)
	for i := 0; i < numUnique; i++ {
		if strings[i] != nil {
			// This is a root string, record its position
			baseOffset := len(packed)
			packed = append(packed, strings[i]...)
			// Update all patterns rooted here
			for p := 0; p < numUnique; p++ {
				if root[p] == i {
					uniqueOffset[p] = baseOffset + patternOffset[p]
				}
			}
		}
	}

	// Build final offsets for all original patterns
	offsets = make([]uint16, n)
	for i := 0; i < n; i++ {
		offsets[i] = uint16(uniqueOffset[origToUnique[i]])
	}

	return packed, offsets
}

// packPatterns packs pattern data using per-song row dictionary + RLE (no equivalence)
func packPatterns(patterns [][]byte, prevDict []byte) (dict []byte, packed []byte, offsets []uint16, primaryCount int, extendedCount int) {
	return packPatternsWithEquiv(patterns, nil, prevDict, nil)
}

// decodePattern simulates the 6502 decode routine for verification
// Format: $00-$0E = dict[0]+RLE 0-14, $0F-$EE = dict[1-224], $EF-$FE = RLE 1-16, $FF = extended 225+
// dict[0] is implicit [0,0,0], dict[1] starts at offset 0 in the dict array
func decodePattern(dict, packed []byte, offset uint16) []byte {
	const primaryMax = 225
	const rleBase = 0xEF
	const dictZeroRleMax = 0x0E
	const dictOffsetBase = 0x0F

	decoded := make([]byte, 192)
	srcOff := int(offset)
	dstOff := 0
	prevRow := [3]byte{0, 0, 0}

	for dstOff < 192 {
		b := packed[srcOff]
		srcOff++

		if b <= dictZeroRleMax {
			// $00-$0E: dict[0] with RLE 0-14 (dict[0] is implicit [0,0,0])
			decoded[dstOff] = 0
			decoded[dstOff+1] = 0
			decoded[dstOff+2] = 0
			prevRow = [3]byte{0, 0, 0}
			dstOff += 3
			for j := 0; j < int(b) && dstOff < 192; j++ {
				copy(decoded[dstOff:], prevRow[:])
				dstOff += 3
			}
		} else if b < rleBase {
			// $0F-$EE: dict[1-224] (dict[1] at offset 0)
			idx := int(b) - dictOffsetBase + 1 // $0F->1, $10->2, etc.
			off := (idx - 1) * 3               // dict[1] at offset 0
			copy(decoded[dstOff:], dict[off:off+3])
			copy(prevRow[:], dict[off:off+3])
			dstOff += 3
		} else if b < 0xFF {
			// $EF-$FE: RLE 1-16
			count := int(b - rleBase + 1)
			for j := 0; j < count; j++ {
				copy(decoded[dstOff:], prevRow[:])
				dstOff += 3
			}
		} else {
			// $FF + byte: Extended dict index 225+
			extIdx := int(packed[srcOff])
			srcOff++
			idx := primaryMax + extIdx
			off := (idx - 1) * 3 // dict[1] at offset 0
			copy(decoded[dstOff:], dict[off:off+3])
			copy(prevRow[:], dict[off:off+3])
			dstOff += 3
		}
	}

	return decoded
}

func findFirstDiff(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return -1
}

// SIDWrite represents a single write to a SID register
type SIDWrite struct {
	Addr  uint16
	Value byte
	Frame int
}

// CPU6502 emulates the 6502 processor
type CPU6502 struct {
	A, X, Y byte   // Accumulator, X index, Y index
	SP      byte   // Stack pointer
	PC      uint16 // Program counter
	P       byte   // Status register: NV-BDIZC

	Memory        [65536]byte
	SIDWrites     []SIDWrite
	Cycles        uint64
	CurrentFrame  int
	Coverage      map[uint16]bool // Code coverage
	DataCoverage  map[uint16]bool // Data read coverage
	DataBase      uint16          // Start of data region to track
	DataSize      int             // Size of data region
	RedundantCLC   map[uint16]int    // Addresses with redundant CLC (C already 0)
	RedundantSEC   map[uint16]int    // Addresses with redundant SEC (C already 1)
	TotalCLC       map[uint16]int    // Total CLC executions per address
	TotalSEC       map[uint16]int    // Total SEC executions per address
	LastCheckpointCycle  uint64 // Cycle count at last checkpoint visit
	LastCheckpointCaller uint16 // Address of JSR that called checkpoint last time
	CheckpointAddr       uint16 // Address of checkpoint routine (0 = disabled)
	CheckpointGap        uint64 // Max cycles between checkpoint calls
	CheckpointGapFrom    uint16 // JSR address at start of longest gap
	CheckpointGapTo      uint16 // JSR address at end of longest gap
}

// Status flags
const (
	FlagC byte = 1 << 0 // Carry
	FlagZ byte = 1 << 1 // Zero
	FlagI byte = 1 << 2 // Interrupt disable
	FlagD byte = 1 << 3 // Decimal mode
	FlagB byte = 1 << 4 // Break
	FlagU byte = 1 << 5 // Unused (always 1)
	FlagV byte = 1 << 6 // Overflow
	FlagN byte = 1 << 7 // Negative
)

func NewCPU() *CPU6502 {
	cpu := &CPU6502{
		SP:             0xFF,
		P:              FlagU | FlagI,
		RedundantCLC:   make(map[uint16]int),
		RedundantSEC:   make(map[uint16]int),
		TotalCLC: make(map[uint16]int),
		TotalSEC: make(map[uint16]int),
	}
	return cpu
}

func (c *CPU6502) Reset() {
	c.A, c.X, c.Y = 0, 0, 0
	c.SP = 0xFF
	c.P = FlagU | FlagI
	c.SIDWrites = nil
	c.Cycles = 0
}

// CPUSnapshot holds CPU state for fast restore
type CPUSnapshot struct {
	A, X, Y      byte
	SP           byte
	PC           uint16
	P            byte
	Memory       [65536]byte
	Cycles       uint64
	CurrentFrame int
}

func (c *CPU6502) Snapshot() CPUSnapshot {
	return CPUSnapshot{
		A: c.A, X: c.X, Y: c.Y,
		SP: c.SP, PC: c.PC, P: c.P,
		Memory:       c.Memory,
		Cycles:       c.Cycles,
		CurrentFrame: c.CurrentFrame,
	}
}

func (c *CPU6502) Restore(snap CPUSnapshot) {
	c.A, c.X, c.Y = snap.A, snap.X, snap.Y
	c.SP, c.PC, c.P = snap.SP, snap.PC, snap.P
	c.Memory = snap.Memory
	c.Cycles = snap.Cycles
	c.CurrentFrame = snap.CurrentFrame
	c.SIDWrites = nil
}

func (c *CPU6502) Read(addr uint16) byte {
	if c.DataCoverage != nil && addr >= c.DataBase && int(addr-c.DataBase) < c.DataSize {
		c.DataCoverage[addr] = true
	}
	return c.Memory[addr]
}

func (c *CPU6502) Write(addr uint16, val byte) {
	// Capture SID writes ($D400-$D418)
	if addr >= 0xD400 && addr <= 0xD418 {
		c.SIDWrites = append(c.SIDWrites, SIDWrite{Addr: addr, Value: val, Frame: c.CurrentFrame})
	}
	c.Memory[addr] = val
}

func (c *CPU6502) Read16(addr uint16) uint16 {
	lo := uint16(c.Read(addr))
	hi := uint16(c.Read(addr + 1))
	return hi<<8 | lo
}

func (c *CPU6502) Push(val byte) {
	c.Write(0x0100|uint16(c.SP), val)
	c.SP--
}

func (c *CPU6502) Pull() byte {
	c.SP++
	return c.Read(0x0100 | uint16(c.SP))
}

func (c *CPU6502) Push16(val uint16) {
	c.Push(byte(val >> 8))
	c.Push(byte(val))
}

func (c *CPU6502) Pull16() uint16 {
	lo := uint16(c.Pull())
	hi := uint16(c.Pull())
	return hi<<8 | lo
}

func (c *CPU6502) setZ(val byte) {
	if val == 0 {
		c.P |= FlagZ
	} else {
		c.P &^= FlagZ
	}
}

func (c *CPU6502) setN(val byte) {
	if val&0x80 != 0 {
		c.P |= FlagN
	} else {
		c.P &^= FlagN
	}
}

func (c *CPU6502) setZN(val byte) {
	c.setZ(val)
	c.setN(val)
}

func (c *CPU6502) getFlag(f byte) bool {
	return c.P&f != 0
}

func (c *CPU6502) setFlag(f byte, v bool) {
	if v {
		c.P |= f
	} else {
		c.P &^= f
	}
}

// Addressing modes return the address and whether a page was crossed
func (c *CPU6502) addrImmediate() uint16 {
	addr := c.PC
	c.PC++
	return addr
}

func (c *CPU6502) addrZeroPage() uint16 {
	addr := uint16(c.Read(c.PC))
	c.PC++
	return addr
}

func (c *CPU6502) addrZeroPageX() uint16 {
	addr := uint16(c.Read(c.PC) + c.X)
	c.PC++
	return addr
}

func (c *CPU6502) addrZeroPageY() uint16 {
	addr := uint16(c.Read(c.PC) + c.Y)
	c.PC++
	return addr
}

func (c *CPU6502) addrAbsolute() uint16 {
	addr := c.Read16(c.PC)
	c.PC += 2
	return addr
}

func (c *CPU6502) addrAbsoluteX() (uint16, bool) {
	base := c.Read16(c.PC)
	c.PC += 2
	addr := base + uint16(c.X)
	crossed := (base & 0xFF00) != (addr & 0xFF00)
	return addr, crossed
}

func (c *CPU6502) addrAbsoluteY() (uint16, bool) {
	base := c.Read16(c.PC)
	c.PC += 2
	addr := base + uint16(c.Y)
	crossed := (base & 0xFF00) != (addr & 0xFF00)
	return addr, crossed
}

func (c *CPU6502) addrIndirectX() uint16 {
	zp := c.Read(c.PC) + c.X
	c.PC++
	lo := uint16(c.Read(uint16(zp)))
	hi := uint16(c.Read(uint16(zp + 1)))
	return hi<<8 | lo
}

func (c *CPU6502) addrIndirectY() (uint16, bool) {
	zp := c.Read(c.PC)
	c.PC++
	lo := uint16(c.Read(uint16(zp)))
	hi := uint16(c.Read(uint16(zp + 1)))
	base := hi<<8 | lo
	addr := base + uint16(c.Y)
	crossed := (base & 0xFF00) != (addr & 0xFF00)
	return addr, crossed
}

// Execute a single instruction, returns true if RTS from call level 0
func (c *CPU6502) Step() bool {
	if c.Coverage != nil {
		c.Coverage[c.PC] = true
	}
	if c.CheckpointAddr != 0 && c.PC == c.CheckpointAddr {
		// Get caller address from stack (return addr - 3 = JSR instruction)
		retLo := c.Memory[0x100+uint16(c.SP)+1]
		retHi := c.Memory[0x100+uint16(c.SP)+2]
		caller := (uint16(retHi)<<8 | uint16(retLo)) - 2 // -2 because return addr is after JSR
		if c.LastCheckpointCycle > 0 {
			gap := c.Cycles - c.LastCheckpointCycle
			if gap > c.CheckpointGap {
				c.CheckpointGap = gap
				c.CheckpointGapFrom = c.LastCheckpointCaller
				c.CheckpointGapTo = caller
			}
		}
		c.LastCheckpointCycle = c.Cycles
		c.LastCheckpointCaller = caller
	}
	opcode := c.Read(c.PC)
	c.PC++

	switch opcode {
	// LDA
	case 0xA9: // LDA immediate
		c.A = c.Read(c.addrImmediate())
		c.setZN(c.A)
	case 0xA5: // LDA zeropage
		c.A = c.Read(c.addrZeroPage())
		c.setZN(c.A)
	case 0xB5: // LDA zeropage,X
		c.A = c.Read(c.addrZeroPageX())
		c.setZN(c.A)
	case 0xAD: // LDA absolute
		c.A = c.Read(c.addrAbsolute())
		c.setZN(c.A)
	case 0xBD: // LDA absolute,X
		addr, _ := c.addrAbsoluteX()
		c.A = c.Read(addr)
		c.setZN(c.A)
	case 0xB9: // LDA absolute,Y
		addr, _ := c.addrAbsoluteY()
		c.A = c.Read(addr)
		c.setZN(c.A)
	case 0xA1: // LDA (indirect,X)
		c.A = c.Read(c.addrIndirectX())
		c.setZN(c.A)
	case 0xB1: // LDA (indirect),Y
		addr, _ := c.addrIndirectY()
		c.A = c.Read(addr)
		c.setZN(c.A)

	// LDX
	case 0xA2: // LDX immediate
		c.X = c.Read(c.addrImmediate())
		c.setZN(c.X)
	case 0xA6: // LDX zeropage
		c.X = c.Read(c.addrZeroPage())
		c.setZN(c.X)
	case 0xB6: // LDX zeropage,Y
		c.X = c.Read(c.addrZeroPageY())
		c.setZN(c.X)
	case 0xAE: // LDX absolute
		c.X = c.Read(c.addrAbsolute())
		c.setZN(c.X)
	case 0xBE: // LDX absolute,Y
		addr, _ := c.addrAbsoluteY()
		c.X = c.Read(addr)
		c.setZN(c.X)

	// LDY
	case 0xA0: // LDY immediate
		c.Y = c.Read(c.addrImmediate())
		c.setZN(c.Y)
	case 0xA4: // LDY zeropage
		c.Y = c.Read(c.addrZeroPage())
		c.setZN(c.Y)
	case 0xB4: // LDY zeropage,X
		c.Y = c.Read(c.addrZeroPageX())
		c.setZN(c.Y)
	case 0xAC: // LDY absolute
		c.Y = c.Read(c.addrAbsolute())
		c.setZN(c.Y)
	case 0xBC: // LDY absolute,X
		addr, _ := c.addrAbsoluteX()
		c.Y = c.Read(addr)
		c.setZN(c.Y)

	// STA
	case 0x85: // STA zeropage
		c.Write(c.addrZeroPage(), c.A)
	case 0x95: // STA zeropage,X
		c.Write(c.addrZeroPageX(), c.A)
	case 0x8D: // STA absolute
		c.Write(c.addrAbsolute(), c.A)
	case 0x9D: // STA absolute,X
		addr, _ := c.addrAbsoluteX()
		c.Write(addr, c.A)
	case 0x99: // STA absolute,Y
		addr, _ := c.addrAbsoluteY()
		c.Write(addr, c.A)
	case 0x81: // STA (indirect,X)
		c.Write(c.addrIndirectX(), c.A)
	case 0x91: // STA (indirect),Y
		addr, _ := c.addrIndirectY()
		c.Write(addr, c.A)

	// STX
	case 0x86: // STX zeropage
		c.Write(c.addrZeroPage(), c.X)
	case 0x96: // STX zeropage,Y
		c.Write(c.addrZeroPageY(), c.X)
	case 0x8E: // STX absolute
		c.Write(c.addrAbsolute(), c.X)

	// STY
	case 0x84: // STY zeropage
		c.Write(c.addrZeroPage(), c.Y)
	case 0x94: // STY zeropage,X
		c.Write(c.addrZeroPageX(), c.Y)
	case 0x8C: // STY absolute
		c.Write(c.addrAbsolute(), c.Y)

	// Transfer
	case 0xAA: // TAX
		c.X = c.A
		c.setZN(c.X)
	case 0xA8: // TAY
		c.Y = c.A
		c.setZN(c.Y)
	case 0x8A: // TXA
		c.A = c.X
		c.setZN(c.A)
	case 0x98: // TYA
		c.A = c.Y
		c.setZN(c.A)
	case 0xBA: // TSX
		c.X = c.SP
		c.setZN(c.X)
	case 0x9A: // TXS
		c.SP = c.X

	// Stack
	case 0x48: // PHA
		c.Push(c.A)
	case 0x68: // PLA
		c.A = c.Pull()
		c.setZN(c.A)
	case 0x08: // PHP
		c.Push(c.P | FlagB | FlagU)
	case 0x28: // PLP
		c.P = c.Pull()&^FlagB | FlagU

	// AND
	case 0x29: // AND immediate
		c.A &= c.Read(c.addrImmediate())
		c.setZN(c.A)
	case 0x25: // AND zeropage
		c.A &= c.Read(c.addrZeroPage())
		c.setZN(c.A)
	case 0x35: // AND zeropage,X
		c.A &= c.Read(c.addrZeroPageX())
		c.setZN(c.A)
	case 0x2D: // AND absolute
		c.A &= c.Read(c.addrAbsolute())
		c.setZN(c.A)
	case 0x3D: // AND absolute,X
		addr, _ := c.addrAbsoluteX()
		c.A &= c.Read(addr)
		c.setZN(c.A)
	case 0x39: // AND absolute,Y
		addr, _ := c.addrAbsoluteY()
		c.A &= c.Read(addr)
		c.setZN(c.A)
	case 0x21: // AND (indirect,X)
		c.A &= c.Read(c.addrIndirectX())
		c.setZN(c.A)
	case 0x31: // AND (indirect),Y
		addr, _ := c.addrIndirectY()
		c.A &= c.Read(addr)
		c.setZN(c.A)

	// ORA
	case 0x09: // ORA immediate
		c.A |= c.Read(c.addrImmediate())
		c.setZN(c.A)
	case 0x05: // ORA zeropage
		c.A |= c.Read(c.addrZeroPage())
		c.setZN(c.A)
	case 0x15: // ORA zeropage,X
		c.A |= c.Read(c.addrZeroPageX())
		c.setZN(c.A)
	case 0x0D: // ORA absolute
		c.A |= c.Read(c.addrAbsolute())
		c.setZN(c.A)
	case 0x1D: // ORA absolute,X
		addr, _ := c.addrAbsoluteX()
		c.A |= c.Read(addr)
		c.setZN(c.A)
	case 0x19: // ORA absolute,Y
		addr, _ := c.addrAbsoluteY()
		c.A |= c.Read(addr)
		c.setZN(c.A)
	case 0x01: // ORA (indirect,X)
		c.A |= c.Read(c.addrIndirectX())
		c.setZN(c.A)
	case 0x11: // ORA (indirect),Y
		addr, _ := c.addrIndirectY()
		c.A |= c.Read(addr)
		c.setZN(c.A)

	// EOR
	case 0x49: // EOR immediate
		c.A ^= c.Read(c.addrImmediate())
		c.setZN(c.A)
	case 0x45: // EOR zeropage
		c.A ^= c.Read(c.addrZeroPage())
		c.setZN(c.A)
	case 0x55: // EOR zeropage,X
		c.A ^= c.Read(c.addrZeroPageX())
		c.setZN(c.A)
	case 0x4D: // EOR absolute
		c.A ^= c.Read(c.addrAbsolute())
		c.setZN(c.A)
	case 0x5D: // EOR absolute,X
		addr, _ := c.addrAbsoluteX()
		c.A ^= c.Read(addr)
		c.setZN(c.A)
	case 0x59: // EOR absolute,Y
		addr, _ := c.addrAbsoluteY()
		c.A ^= c.Read(addr)
		c.setZN(c.A)
	case 0x41: // EOR (indirect,X)
		c.A ^= c.Read(c.addrIndirectX())
		c.setZN(c.A)
	case 0x51: // EOR (indirect),Y
		addr, _ := c.addrIndirectY()
		c.A ^= c.Read(addr)
		c.setZN(c.A)

	// BIT
	case 0x24: // BIT zeropage
		val := c.Read(c.addrZeroPage())
		c.setFlag(FlagZ, c.A&val == 0)
		c.setFlag(FlagV, val&0x40 != 0)
		c.setFlag(FlagN, val&0x80 != 0)
	case 0x2C: // BIT absolute
		val := c.Read(c.addrAbsolute())
		c.setFlag(FlagZ, c.A&val == 0)
		c.setFlag(FlagV, val&0x40 != 0)
		c.setFlag(FlagN, val&0x80 != 0)

	// ADC
	case 0x69: // ADC immediate
		c.adc(c.Read(c.addrImmediate()))
	case 0x65: // ADC zeropage
		c.adc(c.Read(c.addrZeroPage()))
	case 0x75: // ADC zeropage,X
		c.adc(c.Read(c.addrZeroPageX()))
	case 0x6D: // ADC absolute
		c.adc(c.Read(c.addrAbsolute()))
	case 0x7D: // ADC absolute,X
		addr, _ := c.addrAbsoluteX()
		c.adc(c.Read(addr))
	case 0x79: // ADC absolute,Y
		addr, _ := c.addrAbsoluteY()
		c.adc(c.Read(addr))
	case 0x61: // ADC (indirect,X)
		c.adc(c.Read(c.addrIndirectX()))
	case 0x71: // ADC (indirect),Y
		addr, _ := c.addrIndirectY()
		c.adc(c.Read(addr))

	// SBC
	case 0xE9: // SBC immediate
		c.sbc(c.Read(c.addrImmediate()))
	case 0xE5: // SBC zeropage
		c.sbc(c.Read(c.addrZeroPage()))
	case 0xF5: // SBC zeropage,X
		c.sbc(c.Read(c.addrZeroPageX()))
	case 0xED: // SBC absolute
		c.sbc(c.Read(c.addrAbsolute()))
	case 0xFD: // SBC absolute,X
		addr, _ := c.addrAbsoluteX()
		c.sbc(c.Read(addr))
	case 0xF9: // SBC absolute,Y
		addr, _ := c.addrAbsoluteY()
		c.sbc(c.Read(addr))
	case 0xE1: // SBC (indirect,X)
		c.sbc(c.Read(c.addrIndirectX()))
	case 0xF1: // SBC (indirect),Y
		addr, _ := c.addrIndirectY()
		c.sbc(c.Read(addr))

	// CMP
	case 0xC9: // CMP immediate
		c.cmp(c.A, c.Read(c.addrImmediate()))
	case 0xC5: // CMP zeropage
		c.cmp(c.A, c.Read(c.addrZeroPage()))
	case 0xD5: // CMP zeropage,X
		c.cmp(c.A, c.Read(c.addrZeroPageX()))
	case 0xCD: // CMP absolute
		c.cmp(c.A, c.Read(c.addrAbsolute()))
	case 0xDD: // CMP absolute,X
		addr, _ := c.addrAbsoluteX()
		c.cmp(c.A, c.Read(addr))
	case 0xD9: // CMP absolute,Y
		addr, _ := c.addrAbsoluteY()
		c.cmp(c.A, c.Read(addr))
	case 0xC1: // CMP (indirect,X)
		c.cmp(c.A, c.Read(c.addrIndirectX()))
	case 0xD1: // CMP (indirect),Y
		addr, _ := c.addrIndirectY()
		c.cmp(c.A, c.Read(addr))

	// CPX
	case 0xE0: // CPX immediate
		c.cmp(c.X, c.Read(c.addrImmediate()))
	case 0xE4: // CPX zeropage
		c.cmp(c.X, c.Read(c.addrZeroPage()))
	case 0xEC: // CPX absolute
		c.cmp(c.X, c.Read(c.addrAbsolute()))

	// CPY
	case 0xC0: // CPY immediate
		c.cmp(c.Y, c.Read(c.addrImmediate()))
	case 0xC4: // CPY zeropage
		c.cmp(c.Y, c.Read(c.addrZeroPage()))
	case 0xCC: // CPY absolute
		c.cmp(c.Y, c.Read(c.addrAbsolute()))

	// INC
	case 0xE6: // INC zeropage
		addr := c.addrZeroPage()
		val := c.Read(addr) + 1
		c.Write(addr, val)
		c.setZN(val)
	case 0xF6: // INC zeropage,X
		addr := c.addrZeroPageX()
		val := c.Read(addr) + 1
		c.Write(addr, val)
		c.setZN(val)
	case 0xEE: // INC absolute
		addr := c.addrAbsolute()
		val := c.Read(addr) + 1
		c.Write(addr, val)
		c.setZN(val)
	case 0xFE: // INC absolute,X
		addr, _ := c.addrAbsoluteX()
		val := c.Read(addr) + 1
		c.Write(addr, val)
		c.setZN(val)

	// DEC
	case 0xC6: // DEC zeropage
		addr := c.addrZeroPage()
		val := c.Read(addr) - 1
		c.Write(addr, val)
		c.setZN(val)
	case 0xD6: // DEC zeropage,X
		addr := c.addrZeroPageX()
		val := c.Read(addr) - 1
		c.Write(addr, val)
		c.setZN(val)
	case 0xCE: // DEC absolute
		addr := c.addrAbsolute()
		val := c.Read(addr) - 1
		c.Write(addr, val)
		c.setZN(val)
	case 0xDE: // DEC absolute,X
		addr, _ := c.addrAbsoluteX()
		val := c.Read(addr) - 1
		c.Write(addr, val)
		c.setZN(val)

	// INX, INY, DEX, DEY
	case 0xE8: // INX
		c.X++
		c.setZN(c.X)
	case 0xC8: // INY
		c.Y++
		c.setZN(c.Y)
	case 0xCA: // DEX
		c.X--
		c.setZN(c.X)
	case 0x88: // DEY
		c.Y--
		c.setZN(c.Y)

	// ASL
	case 0x0A: // ASL A
		c.setFlag(FlagC, c.A&0x80 != 0)
		c.A <<= 1
		c.setZN(c.A)
	case 0x06: // ASL zeropage
		addr := c.addrZeroPage()
		val := c.Read(addr)
		c.setFlag(FlagC, val&0x80 != 0)
		val <<= 1
		c.Write(addr, val)
		c.setZN(val)
	case 0x16: // ASL zeropage,X
		addr := c.addrZeroPageX()
		val := c.Read(addr)
		c.setFlag(FlagC, val&0x80 != 0)
		val <<= 1
		c.Write(addr, val)
		c.setZN(val)
	case 0x0E: // ASL absolute
		addr := c.addrAbsolute()
		val := c.Read(addr)
		c.setFlag(FlagC, val&0x80 != 0)
		val <<= 1
		c.Write(addr, val)
		c.setZN(val)
	case 0x1E: // ASL absolute,X
		addr, _ := c.addrAbsoluteX()
		val := c.Read(addr)
		c.setFlag(FlagC, val&0x80 != 0)
		val <<= 1
		c.Write(addr, val)
		c.setZN(val)

	// LSR
	case 0x4A: // LSR A
		c.setFlag(FlagC, c.A&0x01 != 0)
		c.A >>= 1
		c.setZN(c.A)
	case 0x46: // LSR zeropage
		addr := c.addrZeroPage()
		val := c.Read(addr)
		c.setFlag(FlagC, val&0x01 != 0)
		val >>= 1
		c.Write(addr, val)
		c.setZN(val)
	case 0x56: // LSR zeropage,X
		addr := c.addrZeroPageX()
		val := c.Read(addr)
		c.setFlag(FlagC, val&0x01 != 0)
		val >>= 1
		c.Write(addr, val)
		c.setZN(val)
	case 0x4E: // LSR absolute
		addr := c.addrAbsolute()
		val := c.Read(addr)
		c.setFlag(FlagC, val&0x01 != 0)
		val >>= 1
		c.Write(addr, val)
		c.setZN(val)
	case 0x5E: // LSR absolute,X
		addr, _ := c.addrAbsoluteX()
		val := c.Read(addr)
		c.setFlag(FlagC, val&0x01 != 0)
		val >>= 1
		c.Write(addr, val)
		c.setZN(val)

	// ROL
	case 0x2A: // ROL A
		carry := c.P & FlagC
		c.setFlag(FlagC, c.A&0x80 != 0)
		c.A = c.A<<1 | carry
		c.setZN(c.A)
	case 0x26: // ROL zeropage
		addr := c.addrZeroPage()
		val := c.Read(addr)
		carry := c.P & FlagC
		c.setFlag(FlagC, val&0x80 != 0)
		val = val<<1 | carry
		c.Write(addr, val)
		c.setZN(val)
	case 0x36: // ROL zeropage,X
		addr := c.addrZeroPageX()
		val := c.Read(addr)
		carry := c.P & FlagC
		c.setFlag(FlagC, val&0x80 != 0)
		val = val<<1 | carry
		c.Write(addr, val)
		c.setZN(val)
	case 0x2E: // ROL absolute
		addr := c.addrAbsolute()
		val := c.Read(addr)
		carry := c.P & FlagC
		c.setFlag(FlagC, val&0x80 != 0)
		val = val<<1 | carry
		c.Write(addr, val)
		c.setZN(val)
	case 0x3E: // ROL absolute,X
		addr, _ := c.addrAbsoluteX()
		val := c.Read(addr)
		carry := c.P & FlagC
		c.setFlag(FlagC, val&0x80 != 0)
		val = val<<1 | carry
		c.Write(addr, val)
		c.setZN(val)

	// ROR
	case 0x6A: // ROR A
		carry := c.P & FlagC
		c.setFlag(FlagC, c.A&0x01 != 0)
		c.A = c.A>>1 | carry<<7
		c.setZN(c.A)
	case 0x66: // ROR zeropage
		addr := c.addrZeroPage()
		val := c.Read(addr)
		carry := c.P & FlagC
		c.setFlag(FlagC, val&0x01 != 0)
		val = val>>1 | carry<<7
		c.Write(addr, val)
		c.setZN(val)
	case 0x76: // ROR zeropage,X
		addr := c.addrZeroPageX()
		val := c.Read(addr)
		carry := c.P & FlagC
		c.setFlag(FlagC, val&0x01 != 0)
		val = val>>1 | carry<<7
		c.Write(addr, val)
		c.setZN(val)
	case 0x6E: // ROR absolute
		addr := c.addrAbsolute()
		val := c.Read(addr)
		carry := c.P & FlagC
		c.setFlag(FlagC, val&0x01 != 0)
		val = val>>1 | carry<<7
		c.Write(addr, val)
		c.setZN(val)
	case 0x7E: // ROR absolute,X
		addr, _ := c.addrAbsoluteX()
		val := c.Read(addr)
		carry := c.P & FlagC
		c.setFlag(FlagC, val&0x01 != 0)
		val = val>>1 | carry<<7
		c.Write(addr, val)
		c.setZN(val)

	// JMP
	case 0x4C: // JMP absolute
		c.PC = c.addrAbsolute()
	case 0x6C: // JMP indirect
		addr := c.Read16(c.PC)
		// 6502 bug: wraps within page
		lo := uint16(c.Read(addr))
		hi := uint16(c.Read((addr & 0xFF00) | ((addr + 1) & 0x00FF)))
		c.PC = hi<<8 | lo

	// JSR
	case 0x20: // JSR absolute
		addr := c.addrAbsolute()
		c.Push16(c.PC - 1)
		c.PC = addr

	// RTS
	case 0x60: // RTS
		c.PC = c.Pull16() + 1
		return true // Signal return

	// RTI
	case 0x40: // RTI
		c.P = c.Pull()&^FlagB | FlagU
		c.PC = c.Pull16()

	// Branches
	case 0x10: // BPL
		c.branch(!c.getFlag(FlagN))
	case 0x30: // BMI
		c.branch(c.getFlag(FlagN))
	case 0x50: // BVC
		c.branch(!c.getFlag(FlagV))
	case 0x70: // BVS
		c.branch(c.getFlag(FlagV))
	case 0x90: // BCC
		c.branch(!c.getFlag(FlagC))
	case 0xB0: // BCS
		c.branch(c.getFlag(FlagC))
	case 0xD0: // BNE
		c.branch(!c.getFlag(FlagZ))
	case 0xF0: // BEQ
		c.branch(c.getFlag(FlagZ))

	// Flag operations
	case 0x18: // CLC
		c.TotalCLC[c.PC-1]++
		if c.P&FlagC == 0 {
			c.RedundantCLC[c.PC-1]++
		}
		c.P &^= FlagC
	case 0x38: // SEC
		c.TotalSEC[c.PC-1]++
		if c.P&FlagC != 0 {
			c.RedundantSEC[c.PC-1]++
		}
		c.P |= FlagC
	case 0x58: // CLI
		c.P &^= FlagI
	case 0x78: // SEI
		c.P |= FlagI
	case 0xB8: // CLV
		c.P &^= FlagV
	case 0xD8: // CLD
		c.P &^= FlagD
	case 0xF8: // SED
		c.P |= FlagD

	// NOP
	case 0xEA: // NOP
		// Do nothing

	// BRK
	case 0x00: // BRK
		c.PC++
		c.Push16(c.PC)
		c.Push(c.P | FlagB | FlagU)
		c.P |= FlagI
		c.PC = c.Read16(0xFFFE)

	default:
		panic(fmt.Sprintf("Unknown opcode: $%02X at $%04X", opcode, c.PC-1))
	}

	c.Cycles++
	return false
}

func (c *CPU6502) branch(cond bool) {
	offset := int8(c.Read(c.PC))
	c.PC++
	if cond {
		c.PC = uint16(int32(c.PC) + int32(offset))
	}
}

func (c *CPU6502) adc(val byte) {
	a := uint16(c.A)
	v := uint16(val)
	carry := uint16(0)
	if c.getFlag(FlagC) {
		carry = 1
	}
	sum := a + v + carry
	c.setFlag(FlagC, sum > 0xFF)
	c.setFlag(FlagV, (^(a^v))&(a^sum)&0x80 != 0)
	c.A = byte(sum)
	c.setZN(c.A)
}

func (c *CPU6502) sbc(val byte) {
	c.adc(^val)
}

func (c *CPU6502) cmp(reg, val byte) {
	result := uint16(reg) - uint16(val)
	c.setFlag(FlagC, reg >= val)
	c.setZN(byte(result))
}

// Call executes a subroutine and returns when it hits RTS
func (c *CPU6502) Call(addr uint16) {
	// Push a fake return address that we can detect
	c.Push16(0xFFFF)
	c.PC = addr

	count := 0
	for {
		count++
		if count > 1000000 {
			panic("Infinite loop detected")
		}
		if c.Step() && c.PC == 0x0000 {
			break
		}
	}
}

// RunFrames runs the play routine for n frames
// If initAddr != 0, measures init+play after normal playback for worst-case frame time
func (c *CPU6502) RunFrames(playAddr uint16, frames int, initAddr uint16, initA, initX byte) ([]SIDWrite, uint64) {
	c.SIDWrites = nil
	c.CurrentFrame = 0
	var maxCyclesPerFrame uint64
	for i := 0; i < frames; i++ {
		c.CurrentFrame = i
		startCycles := c.Cycles
		c.Call(playAddr)
		frameCycles := c.Cycles - startCycles
		if frameCycles > maxCyclesPerFrame {
			maxCyclesPerFrame = frameCycles
		}
	}
	// Measure init+play cycle time separately (worst-case song switch)
	if initAddr != 0 {
		savedWrites := c.SIDWrites
		c.SIDWrites = nil
		startCycles := c.Cycles
		c.A = initA
		c.X = initX
		c.Call(initAddr)
		c.Call(playAddr)
		frameCycles := c.Cycles - startCycles
		if frameCycles > maxCyclesPerFrame {
			maxCyclesPerFrame = frameCycles
		}
		c.SIDWrites = savedWrites // Restore original writes for comparison
	}
	return c.SIDWrites, maxCyclesPerFrame
}

// Part times (little-endian frame counts)
var partTimes = []uint16{
	0xBB44, // Song 1
	0x7234, // Song 2
	0x57C0, // Song 3
	0x88D0, // Song 4
	0xC0A4, // Song 5
	0x79F6, // Song 6
	0x491A, // Song 7
	0x7BF0, // Song 8
	0x6D80, // Song 9
}

type result struct {
	songNum           int
	passed            bool
	writes            int
	builtinCycles     uint64
	newCycles         uint64
	builtinMaxCycles  uint64
	newMaxCycles      uint64
	origSize          int
	newSize           int
	convStats         ConversionStats
	err               string
	coverage          map[uint16]bool
	dataCoverage      map[uint16]bool
	dataBase          uint16
	dataSize          int
	redundantCLC      map[uint16]int
	redundantSEC      map[uint16]int
	totalCLC          map[uint16]int
	totalSEC          map[uint16]int
	checkpointGap     uint64
	checkpointGapFrom uint16
	checkpointGapTo   uint16
}

func testSong(songNum int, rawData, convertedData []byte, convStats ConversionStats, playerData []byte) result {
	testFrames := int(partTimes[songNum-1])

	var bufferBase uint16
	if songNum%2 == 1 {
		bufferBase = 0x1000
	} else {
		bufferBase = 0x7000
	}
	playAddr := bufferBase + 3
	playerBase := uint16(0xF000)

	// Run built-in player (original embedded player)
	cpuBuiltin := NewCPU()
	copy(cpuBuiltin.Memory[bufferBase:], rawData)
	cpuBuiltin.A = 0
	cpuBuiltin.Call(bufferBase)
	cpuBuiltin.SIDWrites = nil
	cpuBuiltin.Cycles = 0
	builtinWrites, builtinMaxCycles := cpuBuiltin.RunFrames(playAddr, testFrames, bufferBase, 0, 0)
	builtinCycles := cpuBuiltin.Cycles

	// Use pre-converted data (with cross-song deduplication applied)

	// Run new player with converted data
	cpuNew := NewCPU()
	cpuNew.Coverage = make(map[uint16]bool)
	cpuNew.DataCoverage = make(map[uint16]bool)
	cpuNew.DataBase = playerBase
	cpuNew.DataSize = len(playerData)
	cpuNew.CheckpointAddr = playerBase + uint16(len(playerData)) - 1 // checkpoint stub at end
	copy(cpuNew.Memory[bufferBase:], convertedData)
	copy(cpuNew.Memory[playerBase:], playerData)
	cpuNew.A = 0
	cpuNew.X = byte(bufferBase >> 8)

	cpuNew.Call(playerBase)

	cpuNew.SIDWrites = nil
	cpuNew.Cycles = 0
	cpuNew.LastCheckpointCycle = 0 // Reset to avoid underflow when cycles reset
	newWrites, newMaxCycles := cpuNew.RunFrames(playerBase+3, testFrames, playerBase, 0, byte(bufferBase>>8))
	newCycles := cpuNew.Cycles

	match := bytes.Equal(serializeWrites(builtinWrites), serializeWrites(newWrites))
	if !match {
		// Find first mismatch
		for i := 0; i < len(builtinWrites) && i < len(newWrites); i++ {
			if builtinWrites[i] != newWrites[i] {
				fmt.Printf("  Song %d mismatch at write %d (frame %d): builtin=$%04X=%02X, new=$%04X=%02X\n",
					songNum, i, builtinWrites[i].Frame,
					builtinWrites[i].Addr, builtinWrites[i].Value,
					newWrites[i].Addr, newWrites[i].Value)
				break
			}
		}
	}
	return result{songNum: songNum, passed: match, writes: len(builtinWrites), builtinCycles: builtinCycles, newCycles: newCycles, builtinMaxCycles: builtinMaxCycles, newMaxCycles: newMaxCycles, origSize: len(rawData), newSize: len(convertedData), convStats: convStats, coverage: cpuNew.Coverage, dataCoverage: cpuNew.DataCoverage, dataBase: playerBase, dataSize: len(playerData), redundantCLC: cpuNew.RedundantCLC, redundantSEC: cpuNew.RedundantSEC, totalCLC: cpuNew.TotalCLC, totalSEC: cpuNew.TotalSEC, checkpointGap: cpuNew.CheckpointGap, checkpointGapFrom: cpuNew.CheckpointGapFrom, checkpointGapTo: cpuNew.CheckpointGapTo}
}

// rebuildPlayer rebuilds player.bin from source after wavetable.inc is generated
func rebuildPlayer() error {
	// Run from tools/odin_convert directory
	toolsDir := projectPath("tools/odin_convert")
	buildDir := projectPath("build")

	// Ensure build directory exists
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return fmt.Errorf("mkdir build: %w", err)
	}

	// Assemble: ca65 -o build/player.o player_standalone.asm
	asmCmd := exec.Command("ca65", "-o", filepath.Join(buildDir, "player.o"),
		filepath.Join(toolsDir, "player_standalone.asm"))
	asmCmd.Dir = toolsDir
	if out, err := asmCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ca65: %w\n%s", err, out)
	}

	// Link: ld65 -C player.cfg -o build/player.bin build/player.o
	linkCmd := exec.Command("ld65", "-C", filepath.Join(toolsDir, "player.cfg"),
		"-o", filepath.Join(buildDir, "player.bin"),
		filepath.Join(buildDir, "player.o"))
	linkCmd.Dir = toolsDir
	if out, err := linkCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ld65: %w\n%s", err, out)
	}

	return nil
}

func printUsage() {
	fmt.Println("Usage: odin_convert [options]")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  (none)       Convert songs and run verification tests")
	fmt.Println("  -equivtest   Rebuild equivalence cache (slow, tests all candidates)")
	fmt.Println("  -h, --help   Show this help message")
}

func main() {
	// Parse command line arguments
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-equivtest":
			// Handled at end of main
		case "-h", "-help", "--help":
			printUsage()
			return
		default:
			fmt.Fprintf(os.Stderr, "Unknown option: %s\n\n", os.Args[1])
			printUsage()
			os.Exit(1)
		}
	}

	// First pass: analyze all songs and print statistics
	fmt.Println("=== Song Analysis ===")
	songData := make([][]byte, 9)
	var allEffects uint16
	var allFSubEffects uint16
	var allEffectCounts [16]int
	for songNum := 1; songNum <= 9; songNum++ {
		rawPath := projectPath(filepath.Join("uncompressed", fmt.Sprintf("d%dp.raw", songNum)))
		rawData, err := os.ReadFile(rawPath)
		if err != nil {
			fmt.Printf("Song %d: ERROR %v\n", songNum, err)
			continue
		}
		songData[songNum-1] = rawData
		stats := analyzeSong(rawData)
		printSongStats(songNum, stats)
		analyzeTableDupes(rawData)
		effects, fSubs := analyzeEffects(rawData)
		allEffects |= effects
		allFSubEffects |= fSubs
		songCounts := countEffectUsage(rawData)
		for i := 0; i < 16; i++ {
			allEffectCounts[i] += songCounts[i]
		}
	}
	// Print used effects
	fmt.Printf("Used effects:")
	for i := 1; i < 16; i++ {
		if allEffects&(1<<i) != 0 {
			fmt.Printf(" %X", i)
		}
	}
	fmt.Printf(" (unused:")
	for i := 1; i < 16; i++ {
		if allEffects&(1<<i) == 0 {
			fmt.Printf(" %X", i)
		}
	}
	fmt.Println(")")
	// Print F sub-effects (high nibble of Fxx parameter)
	fmt.Printf("F sub-effects: ")
	for i := 0; i < 16; i++ {
		if allFSubEffects&(1<<i) != 0 {
			fmt.Printf("F%Xx ", i)
		}
	}
	fmt.Println()

	// Build frequency-sorted effect remapping with new encoding:
	// - Effect 0: param 0=none, 1=vib(was 4), 2=break(was D), 3=fineslide(was FB)
	// - Effects 1-E: the 14 variable-param effects sorted by frequency
	//
	// Count effects with F sub-effects split out:
	// - Regular effects: 1,2,3,7,8,9,A,B,E (skip 4,D,F which are handled specially)
	// - F sub-effects: speed($00-$7F), hrdrest($Fx), filttrig($Ex), globalvol($8x), filtmode($9x)
	// - Fineslide($Bx) becomes effect 0 param 3
	type effectFreq struct {
		name  string
		code  int // 0-15 for regular, 0x10+ for F sub-effects
		count int
	}
	var usedEffects []effectFreq

	// Count F sub-effects from all songs
	fSubCounts := make(map[string]int)
	for songNum := 1; songNum <= 9; songNum++ {
		if songData[songNum-1] == nil {
			continue
		}
		params := countEffectParams(songData[songNum-1])
		for p, c := range params[0xF] {
			switch {
			case p < 0x80:
				fSubCounts["speed"] += c
			case p >= 0x80 && p < 0x90:
				fSubCounts["globalvol"] += c
			case p >= 0x90 && p < 0xA0:
				fSubCounts["filtmode"] += c
			case p >= 0xB0 && p < 0xC0:
				fSubCounts["fineslide"] += c
			case p >= 0xE0 && p < 0xF0:
				fSubCounts["filttrig"] += c
			case p >= 0xF0:
				fSubCounts["hrdrest"] += c
			}
		}
	}

	// Add regular effects (excluding 4=vib, D=break, F=extended which are handled specially)
	for i := 1; i < 16; i++ {
		if i == 4 || i == 0xD || i == 0xF {
			continue // These become effect 0 or are split into sub-effects
		}
		if allEffects&(1<<i) != 0 {
			usedEffects = append(usedEffects, effectFreq{
				name:  fmt.Sprintf("%X", i),
				code:  i,
				count: allEffectCounts[i],
			})
		}
	}

	// Add F sub-effects (excluding fineslide which becomes effect 0)
	fSubNames := []struct {
		name string
		code int
	}{
		{"speed", 0x10},
		{"hrdrest", 0x11},
		{"filttrig", 0x12},
		{"globalvol", 0x13},
		{"filtmode", 0x14},
	}
	for _, fs := range fSubNames {
		if c := fSubCounts[fs.name]; c > 0 {
			usedEffects = append(usedEffects, effectFreq{
				name:  "F/" + fs.name,
				code:  fs.code,
				count: c,
			})
		}
	}

	sort.Slice(usedEffects, func(i, j int) bool {
		return usedEffects[i].count > usedEffects[j].count
	})

	// Generate remapping: code -> new effect (1-based, sorted by frequency)
	// effectRemap[0-15] for regular effects, fSubRemap for F sub-effects
	effectRemap := [16]byte{} // 0 stays 0, 4->will use 0, D->will use 0, F->special
	fSubRemap := make(map[int]byte) // F sub-effect code -> new effect number
	fmt.Printf("Effect frequency order (old->new):")
	for newIdx, ef := range usedEffects {
		newEffect := byte(newIdx + 1)
		if ef.code < 0x10 {
			effectRemap[ef.code] = newEffect
		} else {
			fSubRemap[ef.code] = newEffect
		}
		fmt.Printf(" %s->%X(%d)", ef.name, newEffect, ef.count)
	}
	fmt.Println()

	// Mark special effects that become effect 0
	effectRemap[4] = 0   // vib -> effect 0, param 1
	effectRemap[0xD] = 0 // break -> effect 0, param 2
	effectRemap[0xF] = 0 // F is handled via fSubRemap; fineslide -> effect 0, param 3

	// Analyze cross-song pattern sharing
	allPatterns := make(map[string][]int) // content -> list of songs
	for songNum := 1; songNum <= 9; songNum++ {
		if songData[songNum-1] == nil {
			continue
		}
		patterns := collectPatternContents(songData[songNum-1])
		for content := range patterns {
			allPatterns[content] = append(allPatterns[content], songNum)
		}
	}
	totalUniquePatterns := len(allPatterns)
	sharedPatterns := 0
	songPatternCounts := make([]int, 9)
	for _, songs := range allPatterns {
		if len(songs) > 1 {
			sharedPatterns++
		}
		for _, s := range songs {
			songPatternCounts[s-1]++
		}
	}
	totalPatternRefs := 0
	for _, c := range songPatternCounts {
		totalPatternRefs += c
	}
	fmt.Printf("Global patterns: %d unique across all songs, %d shared between songs\n", totalUniquePatterns, sharedPatterns)
	fmt.Printf("Pattern refs per song: %v (total %d, dedup saves %d)\n", songPatternCounts, totalPatternRefs, totalPatternRefs-totalUniquePatterns)

	// Analyze shared row dictionary encoding
	analyzeTransposePatterns(songData)
	analyzeSharedRowDict(songData, effectRemap, fSubRemap)

	// Analyze vibrato depth usage frequency across all instruments
	vibDepthCount := make(map[int]int)
	for songNum := 1; songNum <= 9; songNum++ {
		if songData[songNum-1] == nil {
			continue
		}
		raw := songData[songNum-1]
		baseAddr := int(raw[2]) << 8
		instADAddr := readWord(raw, codeInstAD)
		instSRAddr := readWord(raw, codeInstSR)
		numInst := int(instSRAddr) - int(instADAddr)
		srcInstOff := int(instADAddr) - baseAddr
		// INST_VIBDEPSP is param 9, high nibble is depth
		for inst := 0; inst < numInst; inst++ {
			vibDepSp := raw[srcInstOff+9*numInst+inst]
			depth := int(vibDepSp >> 4)
			if depth > 0 {
				vibDepthCount[depth]++
			}
		}
	}
	// Sort depths by frequency (descending)
	type depthFreq struct {
		depth int
		count int
	}
	var depthFreqs []depthFreq
	for d, c := range vibDepthCount {
		depthFreqs = append(depthFreqs, depthFreq{d, c})
	}
	sort.Slice(depthFreqs, func(i, j int) bool {
		return depthFreqs[i].count > depthFreqs[j].count
	})
	fmt.Printf("Vibrato depths by frequency:")
	for _, df := range depthFreqs {
		fmt.Printf(" %d(%d)", df.depth, df.count)
	}
	fmt.Printf(" (unused:")
	for d := 1; d < 16; d++ {
		if vibDepthCount[d] == 0 {
			fmt.Printf(" %d", d)
		}
	}
	fmt.Println(")")

	// Analyze effect parameter distributions
	effectNames := []string{"0", "1(slide)", "2(pulse)", "3(porta)", "4(vib)", "5", "6", "7(AD)", "8(SR)", "9(wave)", "A(arp)", "B(jump)", "C", "D(break)", "E(reso)", "F(ext)"}
	allEffectParams := make(map[int]map[int]int)
	for i := 0; i < 16; i++ {
		allEffectParams[i] = make(map[int]int)
	}
	for songNum := 1; songNum <= 9; songNum++ {
		if songData[songNum-1] == nil {
			continue
		}
		songParams := countEffectParams(songData[songNum-1])
		for eff := 0; eff < 16; eff++ {
			for param, count := range songParams[eff] {
				allEffectParams[eff][param] += count
			}
		}
	}
	fmt.Println("Effect parameter analysis:")
	for eff := 1; eff < 16; eff++ {
		if len(allEffectParams[eff]) == 0 {
			continue
		}
		// For effect F, break down by sub-effect (OLD/original Fxx meanings)
		if eff == 0xF {
			// Group Fxx params by sub-effect type (labels show original meanings)
			subEffects := map[string]map[int]int{
				"F(speed)":     make(map[int]int), // 00-7F
				"F8(globalvol)": make(map[int]int), // F8x
				"F9(filtmode)": make(map[int]int), // F9x
				"FB(fineslide)": make(map[int]int), // FBx
				"FE(filttrig)": make(map[int]int),  // FEx
				"FF(hrdrest)":  make(map[int]int),  // FFx
			}
			subOrder := []string{"F(speed)", "FF(hrdrest)", "FE(filttrig)", "F8(globalvol)", "F9(filtmode)", "FB(fineslide)"}
			for p, c := range allEffectParams[eff] {
				switch {
				case p < 0x80:
					subEffects["F(speed)"][p] = c
				case p >= 0x80 && p < 0x90:
					subEffects["F8(globalvol)"][p&0x0F] = c
				case p >= 0x90 && p < 0xA0:
					subEffects["F9(filtmode)"][p&0x0F] = c
				case p >= 0xB0 && p < 0xC0:
					subEffects["FB(fineslide)"][p&0x0F] = c
				case p >= 0xE0 && p < 0xF0:
					subEffects["FE(filttrig)"][p&0x0F] = c
				case p >= 0xF0:
					subEffects["FF(hrdrest)"][p&0x0F] = c
				}
			}
			for _, name := range subOrder {
				params := subEffects[name]
				if len(params) == 0 {
					continue
				}
				type pv struct {
					param int
					count int
				}
				var pvs []pv
				total := 0
				for p, c := range params {
					pvs = append(pvs, pv{p, c})
					total += c
				}
				sort.Slice(pvs, func(i, j int) bool {
					return pvs[i].count > pvs[j].count
				})
				if len(pvs) == 1 {
					fmt.Printf("  %s: %d uses, always $%X\n", name, total, pvs[0].param)
				} else {
					fmt.Printf("  %s: %d unique values, %d total uses\n", name, len(pvs), total)
					fmt.Printf("    ")
					for i, pv := range pvs {
						if i > 0 {
							fmt.Printf(" ")
						}
						if i >= 10 {
							fmt.Printf("...")
							break
						}
						fmt.Printf("$%X(%d)", pv.param, pv.count)
					}
					fmt.Println()
				}
			}
			continue
		}
		type pv struct {
			param int
			count int
		}
		var pvs []pv
		total := 0
		for p, c := range allEffectParams[eff] {
			pvs = append(pvs, pv{p, c})
			total += c
		}
		sort.Slice(pvs, func(i, j int) bool {
			return pvs[i].count > pvs[j].count
		})
		if len(pvs) == 1 {
			fmt.Printf("  %s: %d uses, always $%02X\n", effectNames[eff], total, pvs[0].param)
		} else if len(pvs) <= 20 {
			fmt.Printf("  %s: %d unique values, %d total uses\n", effectNames[eff], len(pvs), total)
			fmt.Printf("    ")
			for i, pv := range pvs {
				if i > 0 {
					fmt.Printf(" ")
				}
				fmt.Printf("$%02X(%d)", pv.param, pv.count)
			}
			fmt.Println()
		} else {
			fmt.Printf("  %s: %d unique values, %d total uses\n", effectNames[eff], len(pvs), total)
			fmt.Printf("    top 10:")
			for i := 0; i < 10 && i < len(pvs); i++ {
				fmt.Printf(" $%02X(%d)", pvs[i].param, pvs[i].count)
			}
			fmt.Println()
		}
	}

	fmt.Println()

	// Build global wavetable from all songs
	globalWave := buildGlobalWaveTable(songData)
	fmt.Printf("Global wavetable: %d bytes (from %d unique snippets)\n", len(globalWave.Data), len(globalWave.Snippets))

	// Write global wavetable to generated file
	waveTablePath := projectPath("generated/wavetable.inc")
	if err := writeGlobalWaveTable(globalWave, waveTablePath); err != nil {
		fmt.Printf("Error writing wavetable: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Wrote global wavetable to %s\n", waveTablePath)

	// Rebuild player.bin with new wavetable
	if err := rebuildPlayer(); err != nil {
		fmt.Printf("Error rebuilding player: %v\n", err)
		os.Exit(1)
	}

	// Load freshly built player
	playerData, err := os.ReadFile(projectPath("build/player.bin"))
	if err != nil {
		fmt.Printf("Error loading build/player.bin: %v\n", err)
		os.Exit(1)
	}

	// Convert all songs with cross-song table deduplication
	// Each song's tables are passed to the next song for dedup
	convertedSongs := make([][]byte, 9)
	convertedStats := make([]ConversionStats, 9)
	var prevTables *PrevSongTables
	for songNum := 1; songNum <= 9; songNum++ {
		if songData[songNum-1] == nil {
			continue
		}
		convertedData, stats := convertToNewFormat(songData[songNum-1], songNum, prevTables, effectRemap, fSubRemap, globalWave)
		convertedSongs[songNum-1] = convertedData
		convertedStats[songNum-1] = stats

		// Extract tables for next song's dedup
		const (
			filterOff  = 0x800
			arpOff     = 0x8E3
			rowDictOff = 0x99F
		)
		// Read split dict back into interleaved format for dedup comparison
		// dict[0] is implicit [0,0,0] (not stored), dict[1] starts at file offset 0
		numEntries := stats.PatternDictSize
		prevDict := make([]byte, numEntries*3)
		// dict[0] = [0,0,0] (implicit, already zero in fresh slice)
		for i := 1; i < numEntries; i++ {
			prevDict[i*3] = convertedData[rowDictOff+i-1]       // note (dict[i] at file offset i-1)
			prevDict[i*3+1] = convertedData[rowDictOff+410+i-1] // inst|effect
			prevDict[i*3+2] = convertedData[rowDictOff+820+i-1] // param
		}
		prevTables = &PrevSongTables{
			Arp:     append([]byte{}, convertedData[arpOff:arpOff+stats.NewArpSize]...),
			Filter:  append([]byte{}, convertedData[filterOff:filterOff+stats.NewFilterSize]...),
			RowDict: prevDict,
		}
	}

	// Write converted parts to generated/parts directory
	partsDir := projectPath("generated/parts")
	if err := os.MkdirAll(partsDir, 0755); err != nil {
		fmt.Printf("Error creating parts directory: %v\n", err)
		os.Exit(1)
	}
	for songNum := 1; songNum <= 9; songNum++ {
		if convertedSongs[songNum-1] == nil {
			continue
		}
		partPath := filepath.Join(partsDir, fmt.Sprintf("part%d.bin", songNum))
		if err := os.WriteFile(partPath, convertedSongs[songNum-1], 0644); err != nil {
			fmt.Printf("Error writing %s: %v\n", partPath, err)
			os.Exit(1)
		}
	}
	fmt.Printf("Wrote %d parts to %s\n", 9, partsDir)

	// Analyze row dict combinations across all songs
	var totalCombos [8]int
	const rowDictOffAnalysis = 0x99F
	for songNum := 1; songNum <= 9; songNum++ {
		if convertedSongs[songNum-1] == nil {
			continue
		}
		// Convert split dict format back to interleaved for analysis
		numEntries := convertedStats[songNum-1].PatternDictSize
		interleavedDict := make([]byte, numEntries*3)
		data := convertedSongs[songNum-1]
		for i := 0; i < numEntries; i++ {
			interleavedDict[i*3] = data[rowDictOffAnalysis+i]
			interleavedDict[i*3+1] = data[rowDictOffAnalysis+410+i]
			interleavedDict[i*3+2] = data[rowDictOffAnalysis+820+i]
		}
		combos := analyzeRowDictCombinations(interleavedDict)
		for i := 0; i < 8; i++ {
			totalCombos[i] += combos[i]
		}
	}
	comboNames := []string{"nop", "note", "inst", "fx", "note+inst", "note+fx", "inst+fx", "note+inst+fx"}
	fmt.Println("Row dict combinations:")
	for i, name := range comboNames {
		if totalCombos[i] > 0 {
			fmt.Printf("  %s: %d\n", name, totalCombos[i])
		}
	}

	// Second pass: run tests
	fmt.Println("=== Test Results ===")
	var wg sync.WaitGroup
	results := make(chan result, 9)

	for songNum := 1; songNum <= 9; songNum++ {
		if songData[songNum-1] == nil {
			continue
		}
		wg.Add(1)
		go func(n int, rawData, convertedData []byte, stats ConversionStats) {
			defer wg.Done()
			results <- testSong(n, rawData, convertedData, stats, playerData)
		}(songNum, songData[songNum-1], convertedSongs[songNum-1], convertedStats[songNum-1])
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect all results
	var allResults []result
	for r := range results {
		allResults = append(allResults, r)
	}
	// Sort by song number
	sort.Slice(allResults, func(i, j int) bool {
		return allResults[i].songNum < allResults[j].songNum
	})

	allPassed := true
	maxWave, maxArp, maxFilter, maxPatterns, maxOrders := 0, 0, 0, 0, 0
	maxDictSize, maxPackedSize := 0, 0
	totalOrigSize, totalNewSize := 0, 0
	totalPrimary, totalExtended := 0, 0
	mergedCoverage := make(map[uint16]bool)
	mergedDataCoverage := make(map[int]bool)
	mergedRedundantCLC := make(map[uint16]int)
	mergedRedundantSEC := make(map[uint16]int)
	mergedTotalCLC := make(map[uint16]int)
	mergedTotalSEC := make(map[uint16]int)
	maxDataSize := 0
	for _, r := range allResults {
		if r.err != "" {
			fmt.Printf("Song %d: ERROR %s\n", r.songNum, r.err)
			allPassed = false
		} else if !r.passed {
			fmt.Printf("Song %d: FAIL\n", r.songNum)
			allPassed = false
		} else {
			s := r.convStats
			totalOrigSize += r.origSize
			totalNewSize += r.newSize
			cycleRatio := float64(r.newCycles) / float64(r.builtinCycles)
			maxCycleRatio := float64(r.newMaxCycles) / float64(r.builtinMaxCycles)
			sizeRatio := float64(r.newSize) / float64(r.origSize)
			fmt.Printf("Song %d: cycles: %.2fx, max: %.2fx, size: %.2fx\n", r.songNum, cycleRatio, maxCycleRatio, sizeRatio)
			if s.NewWaveSize > maxWave {
				maxWave = s.NewWaveSize
			}
			if s.NewArpSize > maxArp {
				maxArp = s.NewArpSize
			}
			if s.NewFilterSize > maxFilter {
				maxFilter = s.NewFilterSize
			}
			if s.UniquePatterns > maxPatterns {
				maxPatterns = s.UniquePatterns
			}
			if s.NewOrders > maxOrders {
				maxOrders = s.NewOrders
			}
			if s.PatternDictSize > maxDictSize {
				maxDictSize = s.PatternDictSize
			}
			if s.PatternPackedSize > maxPackedSize {
				maxPackedSize = s.PatternPackedSize
			}
			totalPrimary += s.PrimaryIndices
			totalExtended += s.ExtendedIndices
			for addr := range r.coverage {
				mergedCoverage[addr] = true
			}
			for addr := range r.dataCoverage {
				mergedDataCoverage[int(addr-r.dataBase)] = true
			}
			for addr, count := range r.redundantCLC {
				mergedRedundantCLC[addr] += count
			}
			for addr, count := range r.redundantSEC {
				mergedRedundantSEC[addr] += count
			}
			for addr, count := range r.totalCLC {
				mergedTotalCLC[addr] += count
			}
			for addr, count := range r.totalSEC {
				mergedTotalSEC[addr] += count
			}
			if r.dataSize > maxDataSize {
				maxDataSize = r.dataSize
			}
		}
	}

	if allPassed {
		savings := totalOrigSize - totalNewSize
		fmt.Printf("\nAll 9 songs passed! Saved %d bytes (%.1f%%)\n", savings, 100*float64(savings)/float64(totalOrigSize))
		fmt.Printf("Max sizes: orders=%d, wave=%d, arp=%d, filter=%d, patterns=%d, dict=%d, packed=%d\n", maxOrders, maxWave, maxArp, maxFilter, maxPatterns, maxDictSize, maxPackedSize)
		fmt.Printf("Pattern indices: %d primary (1 byte), %d extended (2 bytes) = %d extra bytes\n", totalPrimary, totalExtended, totalExtended)

		// Report coverage gaps in player code
		playerBase := uint16(0xF000)
		instrStarts := findInstructionStarts(playerData, playerBase)
		var uncovered []uint16
		for _, addr := range instrStarts {
			if !mergedCoverage[addr] {
				uncovered = append(uncovered, addr)
			}
		}
		fmt.Printf("\nCode coverage: %d/%d instructions executed\n", len(instrStarts)-len(uncovered), len(instrStarts))
		if len(uncovered) > 0 {
			fmt.Printf("Uncovered instructions:")
			for _, addr := range uncovered {
				fmt.Printf(" $%04X", addr)
			}
			fmt.Println()
		}

		// Report player data coverage (all data after code)
		codeEnd := len(instrStarts)
		if codeEnd > 0 {
			lastInstr := instrStarts[codeEnd-1]
			lastSize := instrSize[playerData[lastInstr-playerBase]]
			codeEnd = int(lastInstr-playerBase) + lastSize
		}
		dataStart := codeEnd
		dataEnd := len(playerData)
		dataCovered := 0
		for off := dataStart; off < dataEnd; off++ {
			if mergedDataCoverage[off] {
				dataCovered++
			}
		}
		fmt.Printf("Data coverage: %d/%d bytes (%.0f%%)\n", dataCovered, dataEnd-dataStart, 100*float64(dataCovered)/float64(dataEnd-dataStart))

		// Show uncovered data bytes
		var uncoveredData []string
		for off := dataStart; off < dataEnd; off++ {
			if !mergedDataCoverage[off] {
				uncoveredData = append(uncoveredData, fmt.Sprintf("%d:$%02X", off-dataStart, playerData[off]))
			}
		}
		if len(uncoveredData) > 0 {
			fmt.Printf("Uncovered data: %s\n", strings.Join(uncoveredData, ", "))
		}

		// Report 100% redundant CLC/SEC instructions (always redundant across all executions)
		var redundantAddrs []uint16
		for addr, redundant := range mergedRedundantCLC {
			if redundant == mergedTotalCLC[addr] {
				redundantAddrs = append(redundantAddrs, addr)
			}
		}
		for addr, redundant := range mergedRedundantSEC {
			if redundant == mergedTotalSEC[addr] {
				redundantAddrs = append(redundantAddrs, addr)
			}
		}
		if len(redundantAddrs) > 0 {
			sort.Slice(redundantAddrs, func(i, j int) bool { return redundantAddrs[i] < redundantAddrs[j] })
			fmt.Println("\nAlways-redundant flag operations:")
			for _, addr := range redundantAddrs {
				if count, ok := mergedRedundantCLC[addr]; ok && count == mergedTotalCLC[addr] {
					fmt.Printf("  $%04X: CLC (C always 0) x%d\n", addr, count)
				}
				if count, ok := mergedRedundantSEC[addr]; ok && count == mergedTotalSEC[addr] {
					fmt.Printf("  $%04X: SEC (C always 1) x%d\n", addr, count)
				}
			}
		}

		// Report checkpoint timing - worst case across all songs
		var worstGapVal uint64
		var worstGapFrom, worstGapTo uint16
		for _, r := range allResults {
			if r.checkpointGap > worstGapVal {
				worstGapVal = r.checkpointGap
				worstGapFrom = r.checkpointGapFrom
				worstGapTo = r.checkpointGapTo
			}
		}
		if worstGapVal > 0 {
			fmt.Printf("\nSlowest checkpoint: %s cycles (from $%04X to $%04X)\n", commas(worstGapVal), worstGapFrom, worstGapTo)
		}

	} else {
		os.Exit(1)
	}

	// Dead entry test if requested
	if len(os.Args) > 1 && os.Args[1] == "-equivtest" {
		fmt.Println("\n=== Equivalence Test ===")
		testEquivalence(convertedSongs, convertedStats, playerData)
	}
}

// Equivalence types
const (
	EquivNOP      = "nop"       // [0,0,0] full NOP
	EquivSameSig  = "same_sig"  // [*,y,z] same inst/effect/param
	EquivSameNote = "same_note" // [x,*,*] same note
	EquivSameEff  = "same_eff"  // [x,y,*] same note, same inst/effect
)

// EquivResult stores equivalence test results
type EquivResult struct {
	SongNum    int            `json:"song"`
	Equiv      map[string]int `json:"equiv"`       // extended idx -> best primary idx
	EquivTypes map[string]string `json:"equiv_types"` // extended idx -> type of match
	Tested     int            `json:"tested"`
	Found      int            `json:"found"`
	TypeCounts map[string]int `json:"type_counts"` // count per type
}

// testEquivalence tests all candidate types:
// 1. Full NOP [0,0,0] - the universal silent entry
// 2. Same effect [x,y,*] - same note and inst/effect, any param
// 3. Same signature [*,y,z] - same inst/effect/param, any note
// 4. Same note [x,*,*] - same note, any inst/effect/param
func testEquivalence(convertedSongs [][]byte, convertedStats []ConversionStats, playerData []byte) {
	cacheFile := projectPath("tools/odin_convert/equiv_cache.json")

	// Try to load cached results
	var results []EquivResult
	if data, err := os.ReadFile(cacheFile); err == nil {
		if json.Unmarshal(data, &results) == nil && len(results) == 9 {
			fmt.Println("Loaded cached equivalence results from", cacheFile)
			analyzeEquivResults(results)
			return
		}
	}

	fmt.Println("Running equivalence tests (parallel)...")
	fmt.Println("Testing: [0,0,0] NOP, [x,y,*] same_eff, [*,y,z] same_sig, [x,*,*] same_note")
	results = make([]EquivResult, 9)

	var wg sync.WaitGroup
	var mu sync.Mutex
	resultChan := make(chan EquivResult, 9)

	for songNum := 1; songNum <= 9; songNum++ {
		if convertedSongs[songNum-1] == nil {
			continue
		}
		wg.Add(1)
		go func(sn int, convertedData []byte, stats ConversionStats) {
			defer wg.Done()
			resultChan <- testEquivalenceSong(sn, convertedData, stats.PatternDictSize, playerData)
		}(songNum, convertedSongs[songNum-1], convertedStats[songNum-1])
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	for r := range resultChan {
		mu.Lock()
		results[r.SongNum-1] = r
		mu.Unlock()
		fmt.Printf("Song %d: tested %d, found %d (nop=%d, same_eff=%d, same_sig=%d, same_note=%d)\n",
			r.SongNum, r.Tested, r.Found,
			r.TypeCounts[EquivNOP], r.TypeCounts[EquivSameEff], r.TypeCounts[EquivSameSig], r.TypeCounts[EquivSameNote])
	}

	// Save cache
	if data, err := json.MarshalIndent(results, "", "  "); err == nil {
		os.WriteFile(cacheFile, data, 0644)
		fmt.Println("Saved results to", cacheFile)
	}

	analyzeEquivResults(results)
}

// testEquivalenceSong tests a single song's equivalence candidates
func testEquivalenceSong(songNum int, convertedData []byte, dictSize int, playerData []byte) EquivResult {
	testFrames := int(partTimes[songNum-1])

	var bufferBase uint16
	if songNum%2 == 1 {
		bufferBase = 0x1000
	} else {
		bufferBase = 0x7000
	}
	playerBase := uint16(0xF000)

	// Build candidate groups
	type signature struct {
		instEffect byte
		param      byte
	}
	type noteEff struct {
		note       byte
		instEffect byte
	}
	sigGroups := make(map[signature][]int)
	noteGroups := make(map[byte][]int)
	noteEffGroups := make(map[noteEff][]int)

	// Track special entries
	nopIdx := -1 // [0,0,0]

	for idx := 0; idx < dictSize; idx++ {
		// Split dict format: 3 arrays of 410 bytes each
		note := convertedData[0x99F+idx]
		instEffect := convertedData[0x99F+410+idx]
		param := convertedData[0x99F+820+idx]

		sig := signature{instEffect, param}
		sigGroups[sig] = append(sigGroups[sig], idx)
		noteGroups[note] = append(noteGroups[note], idx)
		ne := noteEff{note, instEffect}
		noteEffGroups[ne] = append(noteEffGroups[ne], idx)

		if idx < 225 {
			// [0,0,0] NOP
			if note == 0 && instEffect == 0 && param == 0 {
				nopIdx = idx
			}
		}
	}

	// Initialize player and take snapshot after init
	cpu := NewCPU()
	copy(cpu.Memory[bufferBase:], convertedData)
	copy(cpu.Memory[playerBase:], playerData)
	cpu.A = 0
	cpu.X = byte(bufferBase >> 8)
	cpu.Call(playerBase)
	cpu.SIDWrites = nil
	cpu.Cycles = 0
	snapshot := cpu.Snapshot()

	// Get baseline
	baselineWrites, _ := cpu.RunFrames(playerBase+3, testFrames, 0, 0, 0)
	baseline := serializeWrites(baselineWrites)

	// Test equivalences for each extended entry
	equiv := make(map[string]int)
	equivTypes := make(map[string]string)
	typeCounts := make(map[string]int)
	tested := 0
	found := 0

	for extIdx := 225; extIdx < dictSize; extIdx++ {
		// Split dict format: note at +idx, instEffect at +410+idx, param at +820+idx
		extNote := convertedData[0x99F+extIdx]
		extInstEffect := convertedData[0x99F+410+extIdx]
		extParam := convertedData[0x99F+820+extIdx]
		extSig := signature{extInstEffect, extParam}
		extNE := noteEff{extNote, extInstEffect}

		// Build ordered candidate list with types (test in priority order)
		type candidate struct {
			idx      int
			equivTyp string
		}
		var candidates []candidate
		seen := make(map[int]bool)

		// 1. NOP [0,0,0] - highest priority
		if nopIdx >= 0 && !seen[nopIdx] {
			candidates = append(candidates, candidate{nopIdx, EquivNOP})
			seen[nopIdx] = true
		}

		// 2. Same effect [x,y,*] - same note and inst/effect, any param
		for _, idx := range noteEffGroups[extNE] {
			if idx < 225 && !seen[idx] {
				candidates = append(candidates, candidate{idx, EquivSameEff})
				seen[idx] = true
			}
		}

		// 3. Same signature [*,y,z]
		for _, idx := range sigGroups[extSig] {
			if idx < 225 && !seen[idx] {
				candidates = append(candidates, candidate{idx, EquivSameSig})
				seen[idx] = true
			}
		}

		// 4. Same note [x,*,*]
		for _, idx := range noteGroups[extNote] {
			if idx < 225 && !seen[idx] {
				candidates = append(candidates, candidate{idx, EquivSameNote})
				seen[idx] = true
			}
		}

		// Test each candidate in priority order
		for _, cand := range candidates {
			tested++

			cpu.Restore(snapshot)

			// Swap extended entry with primary entry (split dict format)
			// Note bytes at 0x99F+idx, instEffect at 0x99F+410+idx, param at 0x99F+820+idx
			cpu.Memory[bufferBase+0x99F+uint16(extIdx)] = cpu.Memory[bufferBase+0x99F+uint16(cand.idx)]
			cpu.Memory[bufferBase+0x99F+410+uint16(extIdx)] = cpu.Memory[bufferBase+0x99F+410+uint16(cand.idx)]
			cpu.Memory[bufferBase+0x99F+820+uint16(extIdx)] = cpu.Memory[bufferBase+0x99F+820+uint16(cand.idx)]

			testWrites, _ := cpu.RunFrames(playerBase+3, testFrames, 0, 0, 0)
			testResult := serializeWrites(testWrites)

			if bytes.Equal(baseline, testResult) {
				key := fmt.Sprintf("%d", extIdx)
				equiv[key] = cand.idx
				equivTypes[key] = cand.equivTyp
				typeCounts[cand.equivTyp]++
				found++
				break
			}
		}
	}

	return EquivResult{
		SongNum:    songNum,
		Equiv:      equiv,
		EquivTypes: equivTypes,
		Tested:     tested,
		Found:      found,
		TypeCounts: typeCounts,
	}
}

func analyzeEquivResults(results []EquivResult) {
	fmt.Println("\n=== Equivalence Analysis ===")
	totalEquiv := 0
	totalCounts := make(map[string]int)
	for _, r := range results {
		if len(r.Equiv) > 0 {
			totalEquiv += len(r.Equiv)
			for typ, cnt := range r.TypeCounts {
				totalCounts[typ] += cnt
			}
		}
	}
	if totalEquiv > 0 {
		fmt.Printf("Total: %d extended entries -> save %d bytes\n", totalEquiv, totalEquiv)
		fmt.Printf("  [0,0,0] nop:      %d\n", totalCounts[EquivNOP])
		fmt.Printf("  [x,y,*] same_eff: %d\n", totalCounts[EquivSameEff])
		fmt.Printf("  [*,y,z] same_sig: %d\n", totalCounts[EquivSameSig])
		fmt.Printf("  [x,*,*] same_note: %d\n", totalCounts[EquivSameNote])
	} else {
		fmt.Println("No equivalences found")
	}
}

// analyzeTransposePatterns finds patterns that are duplicates with transpose applied
func analyzeTransposePatterns(songData [][]byte) {
	fmt.Println("\n=== Transpose Pattern Analysis ===")

	// Collect all unique patterns across all songs
	type patInfo struct {
		songNum int
		addr    uint16
		data    []byte
	}
	var allPatterns []patInfo

	for songNum := 1; songNum <= 9; songNum++ {
		raw := songData[songNum-1]
		if raw == nil {
			continue
		}
		baseAddr := int(raw[2]) << 8
		trackLo0Off := int(readWord(raw, codeTrackLo0)) - baseAddr
		trackHi0Off := int(readWord(raw, codeTrackHi0)) - baseAddr
		trackLo1Off := int(readWord(raw, codeTrackLo1)) - baseAddr
		trackHi1Off := int(readWord(raw, codeTrackHi1)) - baseAddr
		trackLo2Off := int(readWord(raw, codeTrackLo2)) - baseAddr
		trackHi2Off := int(readWord(raw, codeTrackHi2)) - baseAddr
		trackLoOff := []int{trackLo0Off, trackLo1Off, trackLo2Off}
		trackHiOff := []int{trackHi0Off, trackHi1Off, trackHi2Off}
		rawLen := len(raw)

		seen := make(map[uint16]bool)
		for order := 0; order < 256; order++ {
			for ch := 0; ch < 3; ch++ {
				if trackLoOff[ch]+order >= rawLen || trackHiOff[ch]+order >= rawLen {
					continue
				}
				lo := raw[trackLoOff[ch]+order]
				hi := raw[trackHiOff[ch]+order]
				addr := uint16(lo) | uint16(hi)<<8
				srcOff := int(addr) - baseAddr
				if srcOff >= 0 && srcOff+192 <= rawLen && !seen[addr] {
					seen[addr] = true
					pat := make([]byte, 192)
					copy(pat, raw[srcOff:srcOff+192])
					allPatterns = append(allPatterns, patInfo{songNum, addr, pat})
				}
			}
		}
	}

	fmt.Printf("Total unique patterns: %d\n", len(allPatterns))

	// Check each pair of patterns for transpose equivalence
	// Two patterns are transpose-equivalent if all non-zero notes differ by the same constant
	// and all other fields (inst, effect, param) are identical
	transposeGroups := make(map[int][]int) // canonical index -> list of equivalent indices
	canonical := make([]int, len(allPatterns))
	for i := range canonical {
		canonical[i] = i
	}

	for i := 0; i < len(allPatterns); i++ {
		if canonical[i] != i {
			continue // already matched
		}
		for j := i + 1; j < len(allPatterns); j++ {
			if canonical[j] != j {
				continue // already matched
			}
			// Check if patterns are transpose-equivalent
			patA := allPatterns[i].data
			patB := allPatterns[j].data
			transpose := 0
			transposeSet := false
			match := true

			for row := 0; row < 64 && match; row++ {
				off := row * 3
				noteA := patA[off] & 0x7F
				noteB := patB[off] & 0x7F
				// Compare inst+effect bits (byte0 bit7 + byte1)
				if (patA[off]&0x80) != (patB[off]&0x80) || patA[off+1] != patB[off+1] || patA[off+2] != patB[off+2] {
					match = false
					break
				}
				// Check note transpose
				if noteA != 0 || noteB != 0 {
					if noteA == 0 || noteB == 0 {
						// One has note, other doesn't
						match = false
						break
					}
					diff := int(noteB) - int(noteA)
					if !transposeSet {
						transpose = diff
						transposeSet = true
					} else if diff != transpose {
						match = false
						break
					}
				}
			}

			if match && transposeSet && transpose != 0 {
				canonical[j] = i
				transposeGroups[i] = append(transposeGroups[i], j)
			}
		}
	}

	// Count and report
	totalDupes := 0
	for i, group := range transposeGroups {
		if len(group) > 0 {
			totalDupes += len(group)
			if len(group) <= 3 {
				fmt.Printf("  Pattern song%d@$%04X has %d transpose dupes: ",
					allPatterns[i].songNum, allPatterns[i].addr, len(group))
				for _, j := range group {
					// Calculate transpose
					noteA := allPatterns[i].data[0] & 0x7F
					noteB := allPatterns[j].data[0] & 0x7F
					for row := 0; row < 64; row++ {
						na := allPatterns[i].data[row*3] & 0x7F
						nb := allPatterns[j].data[row*3] & 0x7F
						if na != 0 && nb != 0 {
							noteA, noteB = na, nb
							break
						}
					}
					transpose := int(noteB) - int(noteA)
					fmt.Printf("song%d@$%04X(%+d) ", allPatterns[j].songNum, allPatterns[j].addr, transpose)
				}
				fmt.Println()
			}
		}
	}

	uniqueAfterDedup := len(allPatterns) - totalDupes
	fmt.Printf("Transpose duplicates: %d patterns could be deduped\n", totalDupes)
	fmt.Printf("Unique after dedup: %d (was %d)\n", uniqueAfterDedup, len(allPatterns))
}

// analyzeSharedRowDict analyzes encoding with a shared row dictionary across all songs
// Encoding: $00-$0E: dict[0]+RLE, $0F-$EE: dict[1-224], $EF-$FE: RLE 1-16, $FF + byte: extended
func analyzeSharedRowDict(songData [][]byte, effectRemap [16]byte, fSubRemap map[int]byte) {
	fmt.Println("\n=== Shared Row Dictionary Analysis ===")

	// Collect all patterns from all songs after effect remapping
	type patternInfo struct {
		songNum int
		data    []byte
	}
	var allPats []patternInfo

	for songNum := 1; songNum <= 9; songNum++ {
		raw := songData[songNum-1]
		if raw == nil {
			continue
		}
		baseAddr := int(raw[2]) << 8
		trackLo0Off := int(readWord(raw, codeTrackLo0)) - baseAddr
		trackHi0Off := int(readWord(raw, codeTrackHi0)) - baseAddr
		trackLo1Off := int(readWord(raw, codeTrackLo1)) - baseAddr
		trackHi1Off := int(readWord(raw, codeTrackHi1)) - baseAddr
		trackLo2Off := int(readWord(raw, codeTrackLo2)) - baseAddr
		trackHi2Off := int(readWord(raw, codeTrackHi2)) - baseAddr
		trackLoOff := []int{trackLo0Off, trackLo1Off, trackLo2Off}
		trackHiOff := []int{trackHi0Off, trackHi1Off, trackHi2Off}
		rawLen := len(raw)

		patternAddrs := make(map[uint16]bool)
		for order := 0; order < 256; order++ {
			for ch := 0; ch < 3; ch++ {
				if trackLoOff[ch]+order >= rawLen || trackHiOff[ch]+order >= rawLen {
					continue
				}
				lo := raw[trackLoOff[ch]+order]
				hi := raw[trackHiOff[ch]+order]
				addr := uint16(lo) | uint16(hi)<<8
				srcOff := int(addr) - baseAddr
				if srcOff >= 0 && srcOff+192 <= rawLen {
					patternAddrs[addr] = true
				}
			}
		}

		for addr := range patternAddrs {
			srcOff := int(addr) - baseAddr
			pat := make([]byte, 192)
			copy(pat, raw[srcOff:srcOff+192])
			remapPatternEffects(pat, effectRemap, fSubRemap)
			allPats = append(allPats, patternInfo{songNum, pat})
		}
	}

	fmt.Printf("Total patterns across all songs: %d\n", len(allPats))

	// Count row frequencies (after RLE - only count when row changes)
	rowFreq := make(map[string]int)
	for _, p := range allPats {
		var prevRow [3]byte
		for row := 0; row < 64; row++ {
			off := row * 3
			curRow := [3]byte{p.data[off], p.data[off+1], p.data[off+2]}
			if curRow != prevRow {
				rowFreq[string(curRow[:])]++
			}
			prevRow = curRow
		}
	}

	fmt.Printf("Unique row entries: %d\n", len(rowFreq))

	// Sort rows by frequency (most frequent first)
	type rowEntry struct {
		row  string
		freq int
	}
	var sortedRows []rowEntry
	for row, freq := range rowFreq {
		sortedRows = append(sortedRows, rowEntry{row, freq})
	}
	sort.Slice(sortedRows, func(i, j int) bool {
		if sortedRows[i].freq != sortedRows[j].freq {
			return sortedRows[i].freq > sortedRows[j].freq
		}
		return sortedRows[i].row < sortedRows[j].row
	})

	// Build dictionary (row -> index)
	rowToIdx := make(map[string]int)
	for i, entry := range sortedRows {
		rowToIdx[entry.row] = i
	}

	// Analyze effect parameters in per-song dictionaries (after remapping)
	// Track max # of dict entries per effect/param across all songs
	effectParamsMax := make(map[int]map[int]int) // effect -> param -> max count across songs
	for i := 0; i < 16; i++ {
		effectParamsMax[i] = make(map[int]int)
	}

	// Build per-song dictionaries and count effect/param entries
	for songNum := 1; songNum <= 9; songNum++ {
		// Find patterns for this song
		var songPatterns [][]byte
		for _, p := range allPats {
			if p.songNum == songNum {
				songPatterns = append(songPatterns, p.data)
			}
		}
		if len(songPatterns) == 0 {
			continue
		}

		// Build this song's dictionary (unique rows after RLE)
		songRows := make(map[string]bool)
		for _, pat := range songPatterns {
			var prevRow [3]byte
			for row := 0; row < 64; row++ {
				off := row * 3
				curRow := [3]byte{pat[off], pat[off+1], pat[off+2]}
				if curRow != prevRow {
					songRows[string(curRow[:])] = true
				}
				prevRow = curRow
			}
		}

		// Count effect/param in this song's dict
		songEffectParams := make(map[int]map[int]int)
		for i := 0; i < 16; i++ {
			songEffectParams[i] = make(map[int]int)
		}
		for rowStr := range songRows {
			row := []byte(rowStr)
			byte0 := row[0]
			byte1 := row[1]
			byte2 := row[2]
			effect := int((byte1 >> 5) | ((byte0 >> 4) & 8))
			songEffectParams[effect][int(byte2)]++
		}

		// Update max
		for eff := 0; eff < 16; eff++ {
			for param, count := range songEffectParams[eff] {
				if count > effectParamsMax[eff][param] {
					effectParamsMax[eff][param] = count
				}
			}
		}
	}

	// Use effectParamsMax for display
	effectParams := effectParamsMax

	// Print effect parameter analysis (remapped effects)
	fmt.Println("Effect parameters (max per-song dict entries):")
	remappedNames := []string{"0", "1(D)", "2(3)", "3(F)", "4(1)", "5(4)", "6(7)", "7(8)", "8(9)", "9(A)", "10(2)", "11(E)", "12(B)"}
	for eff := 0; eff < 13; eff++ {
		if len(effectParams[eff]) == 0 {
			continue
		}
		type pv struct {
			param int
			count int
		}

		// Effect 3 is remapped F - break down by sub-effect
		// After remapping: $8x=hrdrst, $9x=ftrig, $Ax=gvol, $Bx=fmode, $Cx=fslide, $00-$7F=speed
		if eff == 3 {
			subEffects := map[string]map[int]int{
				"3(F) speed":   make(map[int]int),
				"3(FF) hrdrst": make(map[int]int),
				"3(FE) ftrig":  make(map[int]int),
				"3(F8) gvol":   make(map[int]int),
				"3(F9) fmode":  make(map[int]int),
				"3(FB) fslide": make(map[int]int),
			}
			for p, c := range effectParams[eff] {
				switch {
				case p < 0x80:
					subEffects["3(F) speed"][p] = c
				case p >= 0x80 && p < 0x90:
					subEffects["3(FF) hrdrst"][p&0x0F] = c
				case p >= 0x90 && p < 0xA0:
					subEffects["3(FE) ftrig"][p&0x0F] = c
				case p >= 0xA0 && p < 0xB0:
					subEffects["3(F8) gvol"][p&0x0F] = c
				case p >= 0xB0 && p < 0xC0:
					subEffects["3(F9) fmode"][p&0x0F] = c
				case p >= 0xC0 && p < 0xD0:
					subEffects["3(FB) fslide"][p&0x0F] = c
				}
			}
			subOrder := []string{"3(F) speed", "3(FF) hrdrst", "3(FE) ftrig", "3(F8) gvol", "3(F9) fmode", "3(FB) fslide"}
			for _, name := range subOrder {
				params := subEffects[name]
				if len(params) == 0 {
					continue
				}
				var pvs []pv
				inDict := 0
				for p, c := range params {
					pvs = append(pvs, pv{p, c})
					inDict += c
				}
				sort.Slice(pvs, func(i, j int) bool {
					return pvs[i].count > pvs[j].count
				})
				fmt.Printf("  %s: %d params, %d max: ", name, len(pvs), inDict)
				for i, pv := range pvs {
					if i > 0 {
						fmt.Printf(" ")
					}
					if i < 10 {
						fmt.Printf("$%X(%d)", pv.param, pv.count)
					}
				}
				if len(pvs) > 10 {
					fmt.Printf(" ...")
				}
				fmt.Println()
			}
			continue
		}

		var pvs []pv
		inDict := 0
		for p, c := range effectParams[eff] {
			pvs = append(pvs, pv{p, c})
			inDict += c
		}
		sort.Slice(pvs, func(i, j int) bool {
			return pvs[i].count > pvs[j].count
		})
		name := remappedNames[eff]
		if len(pvs) <= 10 {
			fmt.Printf("  %s: %d params, %d max: ", name, len(pvs), inDict)
			for i, pv := range pvs {
				if i > 0 {
					fmt.Printf(" ")
				}
				fmt.Printf("$%02X(%d)", pv.param, pv.count)
			}
			fmt.Println()
		} else {
			fmt.Printf("  %s: %d params, %d max, top 10: ", name, len(pvs), inDict)
			for i := 0; i < 10; i++ {
				fmt.Printf("$%02X(%d) ", pvs[i].param, pvs[i].count)
			}
			fmt.Println()
		}
	}

	// Pack all patterns with encoding (analysis only)
	const primaryMax = 225
	const rleMax = 16
	const rleBase = 0xEF

	totalBytes := 0
	primaryCount := 0
	extendedCount := 0
	rleCount := 0

	for _, p := range allPats {
		var prevRow [3]byte
		repeatCount := 0

		for row := 0; row < 64; row++ {
			off := row * 3
			curRow := [3]byte{p.data[off], p.data[off+1], p.data[off+2]}

			if curRow == prevRow {
				repeatCount++
				if repeatCount == rleMax || row == 63 {
					totalBytes++
					rleCount++
					repeatCount = 0
				}
			} else {
				if repeatCount > 0 {
					totalBytes++
					rleCount++
					repeatCount = 0
				}
				idx := rowToIdx[string(curRow[:])]
				if idx < primaryMax {
					totalBytes++
					primaryCount++
				} else {
					totalBytes += 2
					extendedCount++
				}
			}
			prevRow = curRow
		}
		if repeatCount > 0 {
			totalBytes++
			rleCount++
		}
	}

	dictBytes := len(sortedRows) * 3

	fmt.Printf("\nDict size: %d entries = %d bytes\n", len(sortedRows), dictBytes)
	fmt.Printf("Packed data: %d bytes\n", totalBytes)
	fmt.Printf("  Primary (1-byte): %d\n", primaryCount)
	fmt.Printf("  Extended (2-byte): %d = %d extra bytes\n", extendedCount, extendedCount)
	fmt.Printf("  RLE: %d\n", rleCount)
	fmt.Printf("Total: %d bytes (dict) + %d bytes (data) = %d bytes\n", dictBytes, totalBytes, dictBytes+totalBytes)
	fmt.Printf("Index distribution: %d < 225 (primary), %d >= 225 (extended)\n",
		min(225, len(sortedRows)), max(0, len(sortedRows)-225))
}

func serializeWrites(writes []SIDWrite) []byte {
	buf := make([]byte, len(writes)*3)
	for i, w := range writes {
		buf[i*3] = byte(w.Addr >> 8)
		buf[i*3+1] = byte(w.Addr)
		buf[i*3+2] = w.Value
	}
	return buf
}

// 6502 instruction sizes by opcode
var instrSize = [256]int{
	1, 2, 0, 0, 0, 2, 2, 0, 1, 2, 1, 0, 0, 3, 3, 0, // 00-0F
	2, 2, 0, 0, 0, 2, 2, 0, 1, 3, 0, 0, 0, 3, 3, 0, // 10-1F
	3, 2, 0, 0, 2, 2, 2, 0, 1, 2, 1, 0, 3, 3, 3, 0, // 20-2F
	2, 2, 0, 0, 0, 2, 2, 0, 1, 3, 0, 0, 0, 3, 3, 0, // 30-3F
	1, 2, 0, 0, 0, 2, 2, 0, 1, 2, 1, 0, 3, 3, 3, 0, // 40-4F
	2, 2, 0, 0, 0, 2, 2, 0, 1, 3, 0, 0, 0, 3, 3, 0, // 50-5F
	1, 2, 0, 0, 0, 2, 2, 0, 1, 2, 1, 0, 3, 3, 3, 0, // 60-6F
	2, 2, 0, 0, 0, 2, 2, 0, 1, 3, 0, 0, 0, 3, 3, 0, // 70-7F
	0, 2, 0, 0, 2, 2, 2, 0, 1, 0, 1, 0, 3, 3, 3, 0, // 80-8F
	2, 2, 0, 0, 2, 2, 2, 0, 1, 3, 1, 0, 0, 3, 0, 0, // 90-9F
	2, 2, 2, 0, 2, 2, 2, 0, 1, 2, 1, 0, 3, 3, 3, 0, // A0-AF
	2, 2, 0, 0, 2, 2, 2, 0, 1, 3, 1, 0, 3, 3, 3, 0, // B0-BF
	2, 2, 0, 0, 2, 2, 2, 0, 1, 2, 1, 0, 3, 3, 3, 0, // C0-CF
	2, 2, 0, 0, 0, 2, 2, 0, 1, 3, 0, 0, 0, 3, 3, 0, // D0-DF
	2, 2, 0, 0, 2, 2, 2, 0, 1, 2, 1, 0, 3, 3, 3, 0, // E0-EF
	2, 2, 0, 0, 0, 2, 2, 0, 1, 3, 0, 0, 0, 3, 3, 0, // F0-FF
}

// Find instruction starts in binary, stopping when we hit data
func findInstructionStarts(data []byte, base uint16) []uint16 {
	var starts []uint16
	offset := 0
	rtsCount := 0
	lastRtsOffset := -100
	for offset < len(data) {
		// Check for three consecutive zero bytes (code/data boundary marker)
		if offset+2 < len(data) && data[offset] == 0 && data[offset+1] == 0 && data[offset+2] == 0 {
			break // Hit data section
		}
		opcode := data[offset]
		size := instrSize[opcode]
		if size == 0 {
			break // Invalid opcode - probably data
		}
		// Track RTS density - multiple RTS within 12 bytes = end of SMC accessors
		if opcode == 0x60 {
			if offset-lastRtsOffset <= 4 {
				rtsCount++
				if rtsCount >= 3 {
					starts = append(starts, base+uint16(offset))
					break // Hit end of SMC accessor block
				}
			} else {
				rtsCount = 1
			}
			lastRtsOffset = offset
		}
		starts = append(starts, base+uint16(offset))
		offset += size
	}
	return starts
}


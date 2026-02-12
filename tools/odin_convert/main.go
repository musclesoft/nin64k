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
	"sync/atomic"
	"time"
)

var projectRoot string
var disableEquivSong int

// Delta table solver
type DeltaTableResult struct {
	Table      []int8
	Bases      [9]int
	SongSets   [9][]int
	StartConst int
}

const deltaEmpty int8 = -128

type deltaSolverState struct {
	sorted [9][]int
	needs  [9][256]bool
	window int
}

func newDeltaSolverWithWindow(songSets [9][]int, window int) *deltaSolverState {
	s := &deltaSolverState{window: window}
	for i, set := range songSets {
		if len(set) == 0 {
			continue
		}
		s.sorted[i] = make([]int, len(set))
		copy(s.sorted[i], set)
		sort.Ints(s.sorted[i])
		for _, e := range set {
			s.needs[i][e+128] = true
		}
	}
	return s
}

func newDeltaSolver(songSets [9][]int) *deltaSolverState {
	return newDeltaSolverWithWindow(songSets, 32)
}

// buildWithLimit returns length only, -1 if exceeds maxLen (for fast rejection)
func (s *deltaSolverState) buildLen(order []int, maxLen int) int {
	w := s.window
	var arr [512]int8
	for i := range arr {
		arr[i] = deltaEmpty
	}
	elems := s.sorted[order[0]]
	for i, e := range elems {
		arr[i] = int8(e)
	}
	arrLen := w
	for orderIdx := 1; orderIdx < 9; orderIdx++ {
		song := order[orderIdx]
		elems := s.sorted[song]
		if len(elems) == 0 {
			continue
		}
		needs := &s.needs[song]
		songSize := len(elems)
		bestBase, bestCost := arrLen, w
		var covCount [256]int8
		emptySlots := 0
		for i := 0; i < w && i < arrLen; i++ {
			if arr[i] == deltaEmpty {
				emptySlots++
			} else {
				covCount[int(arr[i])+128]++
			}
		}
		if w > arrLen {
			emptySlots += w - arrLen
		}
		missing := songSize
		for i := range covCount {
			if needs[i] && covCount[i] > 0 {
				missing--
			}
		}
		if missing <= emptySlots {
			cost := w - arrLen
			if cost < 0 {
				cost = 0
			}
			if cost < bestCost {
				bestCost, bestBase = cost, 0
			}
		}
		for base := 1; base <= arrLen; base++ {
			oldPos, newPos := base-1, base+w-1
			if arr[oldPos] == deltaEmpty {
				emptySlots--
			} else {
				idx := int(arr[oldPos]) + 128
				covCount[idx]--
				if needs[idx] && covCount[idx] == 0 {
					missing++
				}
			}
			if newPos < arrLen {
				if arr[newPos] == deltaEmpty {
					emptySlots++
				} else {
					idx := int(arr[newPos]) + 128
					if needs[idx] && covCount[idx] == 0 {
						missing--
					}
					covCount[idx]++
				}
			} else {
				emptySlots++
			}
			if missing <= emptySlots {
				cost := base + w - arrLen
				if cost < 0 {
					cost = 0
				}
				if cost < bestCost {
					bestCost, bestBase = cost, base
				}
			}
		}
		newLen := bestBase + w
		if newLen > arrLen {
			arrLen = newLen
		}
		// Early exit if already too long
		if arrLen >= maxLen {
			return -1
		}
		var covered [256]bool
		for i := bestBase; i < bestBase+w; i++ {
			if arr[i] != deltaEmpty {
				covered[int(arr[i])+128] = true
			}
		}
		slot := bestBase
		for _, e := range elems {
			if !covered[e+128] {
				for arr[slot] != deltaEmpty {
					slot++
				}
				arr[slot] = int8(e)
				slot++
			}
		}
	}
	return arrLen
}

func (s *deltaSolverState) build(order []int) DeltaTableResult {
	w := s.window
	var arr [512]int8
	for i := range arr {
		arr[i] = deltaEmpty
	}
	var bases [9]int
	elems := s.sorted[order[0]]
	for i, e := range elems {
		arr[i] = int8(e)
	}
	arrLen := w
	bases[order[0]] = 0
	for orderIdx := 1; orderIdx < 9; orderIdx++ {
		song := order[orderIdx]
		elems := s.sorted[song]
		if len(elems) == 0 {
			continue
		}
		needs := &s.needs[song]
		songSize := len(elems)
		bestBase, bestCost := arrLen, w
		var covCount [256]int8
		emptySlots := 0
		for i := 0; i < w && i < arrLen; i++ {
			if arr[i] == deltaEmpty {
				emptySlots++
			} else {
				covCount[int(arr[i])+128]++
			}
		}
		if w > arrLen {
			emptySlots += w - arrLen
		}
		missing := songSize
		for i := range covCount {
			if needs[i] && covCount[i] > 0 {
				missing--
			}
		}
		if missing <= emptySlots {
			cost := w - arrLen
			if cost < 0 {
				cost = 0
			}
			if cost < bestCost {
				bestCost, bestBase = cost, 0
			}
		}
		for base := 1; base <= arrLen; base++ {
			oldPos, newPos := base-1, base+w-1
			if arr[oldPos] == deltaEmpty {
				emptySlots--
			} else {
				idx := int(arr[oldPos]) + 128
				covCount[idx]--
				if needs[idx] && covCount[idx] == 0 {
					missing++
				}
			}
			if newPos < arrLen {
				if arr[newPos] == deltaEmpty {
					emptySlots++
				} else {
					idx := int(arr[newPos]) + 128
					if needs[idx] && covCount[idx] == 0 {
						missing--
					}
					covCount[idx]++
				}
			} else {
				emptySlots++
			}
			if missing <= emptySlots {
				cost := base + w - arrLen
				if cost < 0 {
					cost = 0
				}
				if cost < bestCost {
					bestCost, bestBase = cost, base
				}
			}
		}
		if bestBase+w > arrLen {
			arrLen = bestBase + w
		}
		bases[song] = bestBase
		var covered [256]bool
		for i := bestBase; i < bestBase+w; i++ {
			if arr[i] != deltaEmpty {
				covered[int(arr[i])+128] = true
			}
		}
		slot := bestBase
		for _, e := range elems {
			if !covered[e+128] {
				for arr[slot] != deltaEmpty {
					slot++
				}
				arr[slot] = int8(e)
				slot++
			}
		}
	}
	return DeltaTableResult{Table: append([]int8{}, arr[:arrLen]...), Bases: bases}
}

// Brute force: try all 8! permutations with given first song
func (s *deltaSolverState) searchWithFirst(first int, globalBest *int32) DeltaTableResult {
	var bestResult DeltaTableResult
	perm := [9]int{}
	j := 0
	for i := 0; i < 9; i++ {
		if i == first {
			continue
		}
		perm[j+1] = i
		j++
	}
	perm[0] = first
	var c [8]int
	// First permutation
	maxLen := int(atomic.LoadInt32(globalBest))
	l := s.buildLen(perm[:], maxLen)
	if l > 0 && l < maxLen {
		bestResult = s.build(perm[:])
		for {
			old := atomic.LoadInt32(globalBest)
			if int32(l) >= old || atomic.CompareAndSwapInt32(globalBest, old, int32(l)) {
				break
			}
		}
	}
	i := 0
	for i < 8 {
		if c[i] < i {
			if i&1 == 0 {
				perm[1], perm[i+1] = perm[i+1], perm[1]
			} else {
				perm[c[i]+1], perm[i+1] = perm[i+1], perm[c[i]+1]
			}
			maxLen := int(atomic.LoadInt32(globalBest))
			l := s.buildLen(perm[:], maxLen)
			if l > 0 && l < maxLen {
				bestResult = s.build(perm[:])
				for {
					old := atomic.LoadInt32(globalBest)
					if int32(l) >= old || atomic.CompareAndSwapInt32(globalBest, old, int32(l)) {
						break
					}
				}
			}
			c[i]++
			i = 0
		} else {
			c[i] = 0
			i++
		}
	}
	return bestResult
}

func solveDeltaTableWithWindow(songSets [9][]int, window int) DeltaTableResult {
	s := newDeltaSolverWithWindow(songSets, window)
	results := make(chan DeltaTableResult, 9)
	var globalBest int32 = 9999
	var wg sync.WaitGroup
	for first := 0; first < 9; first++ {
		if len(songSets[first]) == 0 {
			continue
		}
		wg.Add(1)
		go func(f int) {
			defer wg.Done()
			results <- s.searchWithFirst(f, &globalBest)
		}(first)
	}
	go func() {
		wg.Wait()
		close(results)
	}()
	bestLen := 9999
	var bestResult DeltaTableResult
	for r := range results {
		if len(r.Table) > 0 && len(r.Table) < bestLen {
			bestLen = len(r.Table)
			bestResult = r
		}
	}
	bestResult.SongSets = songSets
	return bestResult
}

func solveDeltaTable(songSets [9][]int) DeltaTableResult {
	return solveDeltaTableWithWindow(songSets, 32)
}

func writeTablesInc(deltaResult DeltaTableResult, transposeResult TransposeTableResult, path string) error {
	var buf bytes.Buffer
	buf.WriteString("; Auto-generated lookup tables - DO NOT EDIT\n\n")

	// Write delta table
	buf.WriteString(fmt.Sprintf("; Delta table: %d bytes\n", len(deltaResult.Table)))
	buf.WriteString("delta_table:\n")
	for i := 0; i < len(deltaResult.Table); i += 16 {
		buf.WriteString("\t.byte\t")
		end := i + 16
		if end > len(deltaResult.Table) {
			end = len(deltaResult.Table)
		}
		for j := i; j < end; j++ {
			v := deltaResult.Table[j]
			if v == deltaEmpty {
				v = 0
			}
			buf.WriteString(fmt.Sprintf("$%02X", byte(v)))
			if j < end-1 {
				buf.WriteString(", ")
			}
		}
		buf.WriteString(fmt.Sprintf("\t; %d\n", i))
	}
	buf.WriteString(fmt.Sprintf("\nTRACKPTR_START = %d\n\n", deltaResult.StartConst))

	// Write transpose table
	buf.WriteString(fmt.Sprintf("; Transpose table: %d bytes\n", len(transposeResult.Table)))
	buf.WriteString("transpose_table:\n")
	for i := 0; i < len(transposeResult.Table); i += 16 {
		buf.WriteString("\t.byte\t")
		end := i + 16
		if end > len(transposeResult.Table) {
			end = len(transposeResult.Table)
		}
		for j := i; j < end; j++ {
			buf.WriteString(fmt.Sprintf("$%02X", byte(transposeResult.Table[j])))
			if j < end-1 {
				buf.WriteString(", ")
			}
		}
		buf.WriteString(fmt.Sprintf("\t; %d\n", i))
	}

	return os.WriteFile(path, buf.Bytes(), 0644)
}

func verifyDeltaTable(result DeltaTableResult) bool {
	for songIdx := 0; songIdx < 9; songIdx++ {
		if len(result.SongSets[songIdx]) == 0 {
			continue
		}
		base := result.Bases[songIdx]
		found := make(map[int]bool)
		for i := base; i < base+32 && i < len(result.Table); i++ {
			if result.Table[i] != deltaEmpty {
				found[int(result.Table[i])] = true
			}
		}
		for _, e := range result.SongSets[songIdx] {
			if !found[e] {
				return false
			}
		}
	}
	return true
}

// Transpose table solver - simpler than delta table since values are sparse
type TransposeTableResult struct {
	Table []int8   // All unique transpose values
	Bases [9]int   // Per-song offset into table
}

func solveTransposeTable(songSets [9][]int8) TransposeTableResult {
	// Convert int8 sets to int sets for delta solver
	var intSets [9][]int
	for i, set := range songSets {
		intSets[i] = make([]int, len(set))
		for j, v := range set {
			intSets[i][j] = int(v)
		}
	}

	// Reuse delta solver with window=16
	deltaResult := solveDeltaTableWithWindow(intSets, 16)

	// Convert back to int8
	table := make([]int8, len(deltaResult.Table))
	for i, v := range deltaResult.Table {
		if v == deltaEmpty {
			table[i] = 0
		} else {
			table[i] = v
		}
	}

	return TransposeTableResult{Table: table, Bases: deltaResult.Bases}
}

// packOrderBitstream packs transpose (4-bit) and trackptr (5-bit) indices into a bitstream.
// Layout per order (4 bytes, 27 bits used, 5 bits unused):
//   Byte 0: [ch1_tr:4][ch0_tr:4]     - nibble packed transposes
//   Byte 1: [ch0_tp_lo:4][ch2_tr:4]  - ch2 transpose + low 4 bits of ch0 trackptr
//   Byte 2: [ch1_tp:5][ch0_tp_hi:1][ch2_tp_lo:2]
//   Byte 3: [unused:5][ch2_tp_hi:3]
func packOrderBitstream(numOrders int, transpose [3][]byte, trackptr [3][]byte) []byte {
	out := make([]byte, numOrders*4)
	for i := 0; i < numOrders; i++ {
		ch0_tr := transpose[0][i] & 0x0F
		ch1_tr := transpose[1][i] & 0x0F
		ch2_tr := transpose[2][i] & 0x0F
		ch0_tp := trackptr[0][i] & 0x1F
		ch1_tp := trackptr[1][i] & 0x1F
		ch2_tp := trackptr[2][i] & 0x1F

		out[i*4+0] = ch0_tr | (ch1_tr << 4)
		out[i*4+1] = ch2_tr | ((ch0_tp & 0x0F) << 4)
		out[i*4+2] = (ch0_tp >> 4) | (ch1_tp << 1) | ((ch2_tp & 0x03) << 6)
		out[i*4+3] = ch2_tp >> 2
	}
	return out
}

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

// loadAllSongData loads all 9 song files into a slice.
func loadAllSongData() [][]byte {
	songData := make([][]byte, 9)
	for sn := 1; sn <= 9; sn++ {
		path := projectPath(filepath.Join("uncompressed", fmt.Sprintf("d%dp.raw", sn)))
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		songData[sn-1] = raw
	}
	return songData
}

// buildEffectRemap analyzes all songs and builds the frequency-sorted effect remapping.
// Returns effectRemap for regular effects and fSubRemap for F sub-effects.
func buildEffectRemap(songData [][]byte) ([16]byte, map[int]byte) {
	var allEffects uint16
	var allEffectCounts [16]int
	fSubCounts := make(map[string]int)

	for _, raw := range songData {
		if raw == nil {
			continue
		}
		effects, _ := analyzeEffects(raw)
		allEffects |= effects
		songCounts := countEffectUsage(raw)
		for i := 0; i < 16; i++ {
			allEffectCounts[i] += songCounts[i]
		}
		params := countEffectParams(raw)
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

	type effectFreq struct {
		name  string
		code  int
		count int
	}
	var usedEffects []effectFreq

	for i := 1; i < 16; i++ {
		if i == 4 || i == 0xD || i == 0xF {
			continue
		}
		if allEffects&(1<<i) != 0 {
			usedEffects = append(usedEffects, effectFreq{
				name:  fmt.Sprintf("%X", i),
				code:  i,
				count: allEffectCounts[i],
			})
		}
	}

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

	effectRemap := [16]byte{}
	fSubRemap := make(map[int]byte)
	for newIdx, ef := range usedEffects {
		newEffect := byte(newIdx + 1)
		if ef.code < 0x10 {
			effectRemap[ef.code] = newEffect
		} else {
			fSubRemap[ef.code] = newEffect
		}
	}
	effectRemap[4] = 0
	effectRemap[0xD] = 0
	effectRemap[0xF] = 0

	return effectRemap, fSubRemap
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

// remapPatternPositionJumps converts position jumps (0x0B) to pattern breaks (0x0D).
// Since trackptrs are now in playback order, jumps are redundant - the next sequential
// order IS where the jump would go. Pattern breaks naturally advance to the next order.
// The remapPatternEffects pass will then convert 0x0D to effect 0 with proper byte encoding.
func remapPatternPositionJumps(pat []byte, orderMap map[int]int) {
	for row := 0; row < 64; row++ {
		rowOff := row * 3
		byte0 := pat[rowOff]
		byte1 := pat[rowOff+1]
		effect := (byte1 >> 5) | ((byte0 >> 4) & 0x08)
		if effect == 0x0B {
			// Convert to pattern break (0x0D): keep byte0[7] for effect bit 3, change byte1[7:5] to 5
			// Effect 0x0D = bit3=1, bits2:0=5 -> byte0[7]=1, byte1[7:5]=5
			pat[rowOff+1] = (byte1 & 0x1F) | 0xA0 // 0xA0 = 5 << 5
			pat[rowOff+2] = 0                     // break param = 0 (start at row 0)
		}
	}
}

// findReachableOrders finds all orders reachable from startOrder by following playback sequence.
// Uses cross-channel analysis: only follows jumps that execute before any channel breaks.
// Returns: ordered list in PLAYBACK sequence (following jumps), and a map from old order to new order.
func findReachableOrders(raw []byte, baseAddr, startOrder, numOrders int,
	trackLoOff, trackHiOff []int) ([]int, map[int]int) {

	rawLen := len(raw)

	// Follow playback sequence until we revisit an order (loop) or reach end
	visited := make(map[int]bool)
	var orders []int
	order := startOrder

	for len(orders) < 512 { // safety limit
		if visited[order] {
			break // loop detected, stop
		}
		visited[order] = true
		orders = append(orders, order)

		// Get break info for all 3 channels at this order
		var breakRow [3]int
		var jumpTarget [3]int
		for ch := 0; ch < 3; ch++ {
			if trackLoOff[ch]+order >= rawLen || trackHiOff[ch]+order >= rawLen {
				breakRow[ch], jumpTarget[ch] = 64, -1
				continue
			}
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

		// Determine next order: follow position jump if present, else sequential
		nextOrder := -1
		for ch := 0; ch < 3; ch++ {
			if breakRow[ch] == minBreak && jumpTarget[ch] >= 0 {
				nextOrder = jumpTarget[ch]
				break
			}
		}
		if nextOrder < 0 {
			nextOrder = order + 1
		}
		if nextOrder >= numOrders {
			break // song ends
		}
		order = nextOrder
	}

	orderMap := make(map[int]int)
	for newIdx, oldIdx := range orders {
		orderMap[oldIdx] = newIdx
	}

	return orders, orderMap
}

// ConversionStats holds before/after statistics
type ConversionStats struct {
	OrigOrders          int
	NewOrders           int
	OrderGapUsed        int // bytes of order table gaps used for patterns
	OrigPatterns        int
	UniquePatterns      int
	OrigWaveSize        int
	NewWaveSize         int
	OrigArpSize         int
	NewArpSize          int
	NewFilterSize       int
	PatternDictSize     int
	PatternPackedSize   int
	PrimaryIndices      int
	ExtendedIndices     int
	ExtendedBeforeEquiv int
	DeltaSet            []int
	TrackStarts         [3]byte
	TempTranspose       [3][]byte // Temp arrays for relative index conversion
	TempTrackptr        [3][]byte
}

func convertToNewFormat(raw []byte, songNum int, effectRemap [16]byte, fSubRemap map[int]byte, globalWave *GlobalWaveTable, excludeEquiv map[int]bool, instRemap []int, maxUsedSlot int) ([]byte, ConversionStats, error) {
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
	// Sort by size desc
	for i := 0; i < len(arpSorted)-1; i++ {
		for j := i + 1; j < len(arpSorted); j++ {
			li, lj := len(arpSorted[i].key.content), len(arpSorted[j].key.content)
			if lj > li || (lj == li && arpSorted[j].key.content < arpSorted[i].key.content) {
				arpSorted[i], arpSorted[j] = arpSorted[j], arpSorted[i]
			}
		}
	}

	var newArpTable []byte
	arpRemap := make([]int, numInst)
	for _, sg := range arpSorted {
		content := []byte(sg.key.content)
		// Remap absolute note 127 ($FF) to note 103 ($E7) to shrink freq table
		for i := range content {
			if content[i] == 0xFF {
				content[i] = 0xE7 // $80 | 103
			}
		}
		pos := findInTable(newArpTable, content)
		if pos < 0 {
			pos = len(newArpTable)
			newArpTable = append(newArpTable, content...)
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
	// Sort by size desc
	for i := 0; i < len(filterSorted)-1; i++ {
		for j := i + 1; j < len(filterSorted); j++ {
			li, lj := len(filterSorted[i].key.content), len(filterSorted[j].key.content)
			if lj > li || (lj == li && filterSorted[j].key.content < filterSorted[i].key.content) {
				filterSorted[i], filterSorted[j] = filterSorted[j], filterSorted[i]
			}
		}
	}

	// Start filter table at position 1 (position 0 reserved for "no filter" sentinel)
	newFilterTable := []byte{0}
	filterRemap := make([]int, numInst)

	for _, sg := range filterSorted {
		content := []byte(sg.key.content)
		pos := findInTable(newFilterTable, content)
		if pos < 0 {
			pos = len(newFilterTable)
			newFilterTable = append(newFilterTable, content...)
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
	// $000: Order bitstream (4 bytes per order: transpose+trackptr packed)
	//       Layout per order: [ch1_tr:4][ch0_tr:4] [ch0_tp_lo:4][ch2_tr:4] [ch1_tp:5][ch0_tp_hi:1][ch2_tp_lo:2] [unused:5][ch2_tp_hi:3]
	//       Max 255 orders = 1020 bytes ($3FC)
	// $3FC to $600: gap (available for pattern data)
	// $600: Instruments 1-31 (496 bytes, inst 0 not stored)
	// $7F0: Filtertable (227 bytes max deduped)
	// $8D3: Arptable (188 bytes max deduped)
	// $98F: Row dictionary + packed pattern data
	// Player uses inst_base = $5F0 (actual - 16) so inst N is at $5F0 + N*16

	bitstreamOff := 0x000
	maxOrders := 255                    // Max possible orders (order index is 1 byte)
	bitstreamMaxSize := maxOrders * 4   // 1020 bytes = $3FC
	bitstreamSize := newNumOrders * 4   // Actual size for this song
	_ = bitstreamMaxSize                // Used for format documentation
	newInstOff := 0x600                 // Instruments 1-31 start here (inst 0 not stored)
	filterOff := 0x7F0     // Filter at $7F0 (227 bytes max)
	arpOff := 0x8D3        // Arp at $8D3 (188 bytes max)
	rowDictOff := 0x991    // Row dict0 (notes), dict1 at +365, dict2 at +730 (2 byte gap after arp for bases)
	packedPtrsOff := 0xDD8 // Packed pointers (fixed location)

	// Extract patterns to slice for packing (do effect/order remapping first)
	patternData := make([][]byte, numPatterns)
	for i, addr := range patterns {
		srcOff := int(addr) - baseAddr
		pat := make([]byte, 192)
		if srcOff >= 0 && srcOff+192 <= len(raw) {
			copy(pat, raw[srcOff:srcOff+192])
			remapPatternPositionJumps(pat, orderMap)
			remapPatternEffects(pat, effectRemap, fSubRemap, instRemap)
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

	// Build dictionary from ALL patterns (including non-canonical transpose equivalents)
	// This ensures rows from transpose-equivalent patterns are available for other patterns that share them
	allPatternData := make([][]byte, len(sortedPatternAddrs))
	for i, addr := range sortedPatternAddrs {
		srcOff := int(addr) - baseAddr
		pat := make([]byte, 192)
		if srcOff >= 0 && srcOff+192 <= len(raw) {
			copy(pat, raw[srcOff:srcOff+192])
			remapPatternPositionJumps(pat, orderMap)
			remapPatternEffects(pat, effectRemap, fSubRemap, instRemap)
		}
		allPatternData[i] = pat
	}

	// Build dictionary first to find NOP entry for dead entry mapping
	// Build without prevDict - cross-song matching happens after compaction
	dict, _ := buildPatternDict(allPatternData, nil)



	// Optimize equiv map for smallest dict (fast, no packing needed)
	// disableEquivSong: 0=apply all, -1=disable all, N=disable song N only
	var equivMap map[int]int
	if disableEquivSong == 0 || (disableEquivSong > 0 && songNum != disableEquivSong) {
		equivMap = optimizeEquivMapMinDict(songNum, dict, patternData, effectRemap, fSubRemap, instRemap)
		// Remove excluded mappings
		if excludeEquiv != nil {
			for idx := range excludeEquiv {
				delete(equivMap, idx)
			}
		}
	}

	// Find patterns that are equiv-equivalent (same index sequence after equiv mapping)
	// Build row hex → dict index map
	numDictEntries := len(dict) / 3
	rowHexToIdx := make(map[string]int)
	rowHexToIdx["000000"] = 0
	for idx := 1; idx < numDictEntries; idx++ {
		rowHex := fmt.Sprintf("%02x%02x%02x", dict[idx*3], dict[idx*3+1], dict[idx*3+2])
		rowHexToIdx[rowHex] = idx
	}

	// Compute equiv signature for each pattern (sequence of indices after equiv mapping)
	getEquivSignature := func(pat []byte) string {
		var sig strings.Builder
		for row := 0; row < len(pat)/3; row++ {
			off := row * 3
			rowHex := fmt.Sprintf("%02x%02x%02x", pat[off], pat[off+1], pat[off+2])
			idx := rowHexToIdx[rowHex]
			if equivMap != nil {
				if mappedIdx, ok := equivMap[idx]; ok {
					idx = mappedIdx
				}
			}
			sig.WriteString(fmt.Sprintf("%d,", idx))
		}
		return sig.String()
	}

	// Find duplicate patterns by equiv signature
	sigToCanon := make(map[string]int)
	patternToCanon := make(map[int]int) // pattern index → canonical index
	equivDedupCount := 0
	for i, pat := range patternData {
		sig := getEquivSignature(pat)
		if canon, exists := sigToCanon[sig]; exists {
			patternToCanon[i] = canon
			equivDedupCount++
		} else {
			sigToCanon[sig] = i
			patternToCanon[i] = i
		}
	}
	if equivDedupCount > 0 {
		fmt.Printf("  Equiv pattern dedup: %d patterns\n", equivDedupCount)
	}

	// Update patternIndex to use canonical patterns
	for addr, idx := range patternIndex {
		patternIndex[addr] = byte(patternToCanon[int(idx)])
	}

	// Build list of canonical patterns only
	canonPatternData := make([][]byte, 0, len(patternData)-equivDedupCount)
	canonTruncate := make([]int, 0, len(patternData)-equivDedupCount)
	oldToNew := make(map[int]int)
	for i, pat := range patternData {
		if patternToCanon[i] == i {
			oldToNew[i] = len(canonPatternData)
			canonPatternData = append(canonPatternData, pat)
			canonTruncate = append(canonTruncate, patternTruncate[i])
		}
	}

	// Update patternIndex with new indices
	for addr, idx := range patternIndex {
		canonIdx := patternToCanon[int(idx)]
		patternIndex[addr] = byte(oldToNew[canonIdx])
	}

	// Use canonical patterns for packing
	patternData = canonPatternData
	patternTruncate = canonTruncate
	numPatterns = len(patternData)

	// Optimize pattern indices to minimize trackptr deltas (Cuthill-McKee)
	// Build adjacency graph: patterns that appear consecutively in track sequences
	adjSet := make(map[[2]int]bool)
	trackSeqs := [3][]int{}
	for ch := 0; ch < 3; ch++ {
		trackSeqs[ch] = make([]int, len(reachableOrders))
		for newIdx, oldIdx := range reachableOrders {
			lo := raw[trackLoOff[ch]+oldIdx]
			hi := raw[trackHiOff[ch]+oldIdx]
			addr := uint16(lo) | uint16(hi)<<8
			trackSeqs[ch][newIdx] = int(patternIndex[addr])
		}
		for i := 1; i < len(trackSeqs[ch]); i++ {
			a, b := trackSeqs[ch][i-1], trackSeqs[ch][i]
			if a > b {
				a, b = b, a
			}
			if a != b {
				adjSet[[2]int{a, b}] = true
			}
		}
	}

	// Build adjacency lists and compute degrees (deterministic order)
	degree := make([]int, numPatterns)
	adj := make([][]int, numPatterns)
	for i := range adj {
		adj[i] = []int{}
	}
	pairs := make([][2]int, 0, len(adjSet))
	for pair := range adjSet {
		pairs = append(pairs, pair)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
	for _, pair := range pairs {
		a, b := pair[0], pair[1]
		adj[a] = append(adj[a], b)
		adj[b] = append(adj[b], a)
		degree[a]++
		degree[b]++
	}
	for i := range adj {
		sort.Ints(adj[i])
	}

	// Fast delta counter for swap optimization (without initial deltas)
	countDeltasFast := func(mapping []int) int {
		var seen [256]bool
		count := 0
		for ch := 0; ch < 3; ch++ {
			seq := trackSeqs[ch]
			for i := 1; i < len(seq); i++ {
				d := mapping[seq[i]] - mapping[seq[i-1]]
				if d > 127 {
					d -= 256
				} else if d < -128 {
					d += 256
				}
				if !seen[d+128] {
					seen[d+128] = true
					count++
				}
			}
		}
		return count
	}

	// Full delta counter including initial deltas (for final evaluation)
	countDeltasFull := func(mapping []int) int {
		var baseDeltas [256]bool
		baseCount := 0
		for ch := 0; ch < 3; ch++ {
			seq := trackSeqs[ch]
			for i := 1; i < len(seq); i++ {
				d := mapping[seq[i]] - mapping[seq[i-1]]
				if d > 127 {
					d -= 256
				} else if d < -128 {
					d += 256
				}
				if !baseDeltas[d+128] {
					baseDeltas[d+128] = true
					baseCount++
				}
			}
		}
		starts := [3]int{}
		for ch := 0; ch < 3; ch++ {
			if len(trackSeqs[ch]) > 0 {
				starts[ch] = mapping[trackSeqs[ch][0]]
			}
		}
		bestNewDeltas := 3
		for tryConst := 0; tryConst < 256; tryConst++ {
			newDeltas := 0
			var initSeen [256]bool
			for ch := 0; ch < 3; ch++ {
				d := starts[ch] - tryConst
				if d > 127 {
					d -= 256
				} else if d < -128 {
					d += 256
				}
				if !baseDeltas[d+128] && !initSeen[d+128] {
					initSeen[d+128] = true
					newDeltas++
				}
			}
			if newDeltas < bestNewDeltas {
				bestNewDeltas = newDeltas
			}
		}
		return baseCount + bestNewDeltas
	}
	countDeltas := countDeltasFast // Use fast version for swaps
	_ = countDeltasFull            // Used for candidate evaluation

	// Optimize pattern indices using Cuthill-McKee + swaps
	optimizeFromStart := func(startNode int) ([]int, int) {
		visited := make([]bool, numPatterns)
		cmOrder := make([]int, 0, numPatterns)
		queue := []int{startNode}
		visited[startNode] = true
		for len(queue) > 0 {
			curr := queue[0]
			queue = queue[1:]
			cmOrder = append(cmOrder, curr)
			neighbors := make([]int, len(adj[curr]))
			copy(neighbors, adj[curr])
			sort.Slice(neighbors, func(i, j int) bool {
				if degree[neighbors[i]] != degree[neighbors[j]] {
					return degree[neighbors[i]] < degree[neighbors[j]]
				}
				return neighbors[i] < neighbors[j]
			})
			for _, n := range neighbors {
				if !visited[n] {
					visited[n] = true
					queue = append(queue, n)
				}
			}
		}
		for i := 0; i < numPatterns; i++ {
			if !visited[i] {
				cmOrder = append(cmOrder, i)
			}
		}
		mapping := make([]int, numPatterns)
		for newIdx, oldIdx := range cmOrder {
			mapping[oldIdx] = newIdx
		}
		posToPattern := make([]int, numPatterns)
		for pat, pos := range mapping {
			posToPattern[pos] = pat
		}
		bestScore := countDeltas(mapping)
		// Swap all positions
		for bestScore > 32 {
			improved := false
			for i := 0; i < numPatterns; i++ {
				for j := i + 1; j < numPatterns; j++ {
					patI, patJ := posToPattern[i], posToPattern[j]
					mapping[patI], mapping[patJ] = j, i
					posToPattern[i], posToPattern[j] = patJ, patI
					if score := countDeltas(mapping); score < bestScore {
						bestScore = score
						improved = true
					} else {
						mapping[patI], mapping[patJ] = i, j
						posToPattern[i], posToPattern[j] = patI, patJ
					}
				}
			}
			if !improved {
				break
			}
		}
		if bestScore > 32 {
			for i := 0; i < numPatterns && bestScore > 32; i++ {
				for j := i + 1; j < numPatterns && bestScore > 32; j++ {
					for k := j + 1; k < numPatterns && bestScore > 32; k++ {
						patI, patJ, patK := posToPattern[i], posToPattern[j], posToPattern[k]
						mapping[patI], mapping[patJ], mapping[patK] = j, k, i
						posToPattern[i], posToPattern[j], posToPattern[k] = patK, patI, patJ
						if score := countDeltas(mapping); score < bestScore {
							bestScore = score
						} else {
							mapping[patI], mapping[patJ], mapping[patK] = k, i, j
							posToPattern[i], posToPattern[j], posToPattern[k] = patJ, patK, patI
							if score := countDeltas(mapping); score < bestScore {
								bestScore = score
							} else {
								mapping[patI], mapping[patJ], mapping[patK] = i, j, k
								posToPattern[i], posToPattern[j], posToPattern[k] = patI, patJ, patK
							}
						}
					}
				}
			}
		}
		return mapping, bestScore
	}

	// Find nodes with low degree as candidates
	minDeg := numPatterns + 1
	for i := 0; i < numPatterns; i++ {
		if degree[i] > 0 && degree[i] < minDeg {
			minDeg = degree[i]
		}
	}
	startCandidates := []int{}
	for deg := minDeg; deg <= minDeg+2 && len(startCandidates) < 24; deg++ {
		for i := 0; i < numPatterns && len(startCandidates) < 24; i++ {
			if degree[i] == deg {
				startCandidates = append(startCandidates, i)
			}
		}
	}

	// Try each candidate in parallel and pick the best
	type result struct {
		mapping []int
		score   int
		start   int
	}
	results := make(chan result, len(startCandidates))
	for _, startNode := range startCandidates {
		go func(s int) {
			m, sc := optimizeFromStart(s)
			results <- result{m, sc, s}
		}(startNode)
	}
	allResults := make([]result, 0, len(startCandidates))
	for range startCandidates {
		allResults = append(allResults, <-results)
	}
	// Re-evaluate top candidates with full delta count (including initial deltas)
	sort.Slice(allResults, func(i, j int) bool {
		return allResults[i].score < allResults[j].score
	})
	bestFullScore := 9999
	bestIdx := 0
	for i := 0; i < len(allResults) && i < 10; i++ {
		fullScore := countDeltasFull(allResults[i].mapping)
		if fullScore < bestFullScore || (fullScore == bestFullScore && allResults[i].start < allResults[bestIdx].start) {
			bestFullScore = fullScore
			bestIdx = i
		}
	}
	oldToNewCM := allResults[bestIdx].mapping

	// Apply renumbering to patterns
	reorderedPatterns := make([][]byte, numPatterns)
	reorderedTruncate := make([]int, numPatterns)
	for oldIdx, newIdx := range oldToNewCM {
		reorderedPatterns[newIdx] = patternData[oldIdx]
		reorderedTruncate[newIdx] = patternTruncate[oldIdx]
	}
	patternData = reorderedPatterns
	patternTruncate = reorderedTruncate

	// Update patternIndex with new indices
	for addr := range patternIndex {
		patternIndex[addr] = byte(oldToNewCM[int(patternIndex[addr])])
	}

	// Pack patterns with optimized equiv map and gap encoding
	dict, packed, patOffsets, patGapCodes, primaryCount, extendedCount, extendedBeforeEquiv, individualPacked := packPatternsWithEquiv(patternData, dict, equivMap, patternTruncate)

	stats.PrimaryIndices = primaryCount
	stats.ExtendedIndices = extendedCount
	stats.ExtendedBeforeEquiv = extendedBeforeEquiv

	// Calculate all available gaps for pattern placement
	// Gap 0: after used instruments, before filter (unused instrument slots)
	// With MFU packing, used instruments end at slot maxUsedSlot
	instGapStart := newInstOff + maxUsedSlot*16
	instGapSize := filterOff - instGapStart
	if instGapSize < 0 {
		instGapSize = 0
	}
	// Gap 1: after filter, before arp
	filterGapStart := filterOff + newFilterSize
	filterGapSize := arpOff - filterGapStart
	if filterGapSize < 0 {
		filterGapSize = 0
	}
	// Gap 2: after arp, before reserved bytes at 0x98F-0x990 (dict starts at 0x991)
	arpGapStart := arpOff + newArpSize
	arpGapSize := 0x98F - arpGapStart // Exclude 2 reserved bytes for transpose/delta bases
	if arpGapSize < 0 {
		arpGapSize = 0
	}
	// Gaps 3-5: free slots in each of the 3 row dict arrays
	// Each dict array is 365 bytes max; dict[0] is implicit, entries 1-N stored at indices 0-(N-1)
	// Free slots start at index (numDictEntries - 1) in each array
	numDictEntries = len(dict) / 3
	dictFreeStart := numDictEntries - 1 // first free index in each array
	dictFreeSize := 365 - dictFreeStart
	if dictFreeSize < 0 {
		dictFreeSize = 0
	}
	// Gap 3: free slots in dict0 (notes)
	dict0GapStart := rowDictOff + dictFreeStart
	// Gap 4: free slots in dict1 (inst|effect)
	dict1GapStart := rowDictOff + 365 + dictFreeStart
	// Gap 5: free slots in dict2 (params)
	dict2GapStart := rowDictOff + 730 + dictFreeStart

	// Gap 6: unused space after order bitstream (bitstreamSize to $600)
	bitstreamGapStart := bitstreamOff + bitstreamSize
	bitstreamGapSize := newInstOff - bitstreamGapStart
	if bitstreamGapSize < 0 {
		bitstreamGapSize = 0
	}

	// Collect all gaps for pattern placement
	type gap struct {
		start int
		size  int
		used  int
	}
	gaps := []*gap{
		{instGapStart, instGapSize, 0},
		{filterGapStart, filterGapSize, 0},
		{arpGapStart, arpGapSize, 0},
		{dict0GapStart, dictFreeSize, 0},
		{dict1GapStart, dictFreeSize, 0},
		{dict2GapStart, dictFreeSize, 0},
		{bitstreamGapStart, bitstreamGapSize, 0}, // Gap 6: after order bitstream
	}

	// Compute overlap potential for each pattern
	// For each pattern, find max overlap as suffix (its suffix matches another's prefix)
	// and max overlap as prefix (its prefix matches another's suffix)
	// Patterns with low overlap potential should go in gaps
	overlapPotential := make([]int, len(individualPacked))
	for i, pi := range individualPacked {
		maxOverlap := 0
		for j, pj := range individualPacked {
			if i == j {
				continue
			}
			// Check pi's suffix vs pj's prefix (pi could precede pj)
			maxLen := len(pi)
			if len(pj) < maxLen {
				maxLen = len(pj)
			}
			for l := maxLen; l >= 1; l-- {
				if string(pi[len(pi)-l:]) == string(pj[:l]) {
					if l > maxOverlap {
						maxOverlap = l
					}
					break
				}
			}
			// Check pj's suffix vs pi's prefix (pj could precede pi)
			for l := maxLen; l >= 1; l-- {
				if string(pj[len(pj)-l:]) == string(pi[:l]) {
					if l > maxOverlap {
						maxOverlap = l
					}
					break
				}
			}
		}
		overlapPotential[i] = maxOverlap
	}

	// Find patterns that fit in any gap (prioritize low overlap potential)
	type gapPattern struct {
		idx     int
		size    int
		overlap int
	}
	var candidates []gapPattern
	for i, p := range individualPacked {
		candidates = append(candidates, gapPattern{i, len(p), overlapPotential[i]})
	}
	sort.Slice(candidates, func(i, j int) bool {
		// Primary: low overlap first (don't benefit from blob)
		// Secondary: large size first (fill gaps efficiently)
		if candidates[i].overlap != candidates[j].overlap {
			return candidates[i].overlap < candidates[j].overlap
		}
		return candidates[i].size > candidates[j].size
	})

	// Place patterns in gaps with overlap optimization
	// First pass: assign patterns to gaps (greedy, low-overlap patterns first)
	gapAssign := make([]int, len(individualPacked)) // pattern index -> gap index (-1 = not in gap)
	for i := range gapAssign {
		gapAssign[i] = -1
	}
	gapPatterns := make([][]int, len(gaps)) // gap index -> list of pattern indices

	for _, c := range candidates {
		// Find smallest gap that could fit this pattern
		bestGap := -1
		bestRemaining := int(^uint(0) >> 1)
		for gi, g := range gaps {
			remaining := g.size - g.used
			if c.size <= remaining && remaining < bestRemaining {
				bestGap = gi
				bestRemaining = remaining
			}
		}
		if bestGap >= 0 {
			gapAssign[c.idx] = bestGap
			gapPatterns[bestGap] = append(gapPatterns[bestGap], c.idx)
			gaps[bestGap].used += c.size // Reserve space (will be refined with overlap)
		}
	}

	// Second pass: optimize overlap within each gap
	// Note: In practice, gap patterns have minimal overlap since we assign
	// low-overlap-potential patterns to gaps. But we still run the optimization
	// to handle any cases where overlap exists.
	type gapResult struct {
		blob    []byte
		offsets map[int]uint16 // pattern index -> offset within blob
	}
	gapResults := make([]gapResult, len(gaps))

	for gi, patIdxs := range gapPatterns {
		if len(patIdxs) == 0 {
			continue
		}
		// Collect patterns for this gap
		gapPats := make([][]byte, len(patIdxs))
		for i, idx := range patIdxs {
			gapPats[i] = individualPacked[idx]
		}
		// Run overlap optimization
		blob, offsets := optimizePackedOverlap(gapPats)
		// Check if overlapped blob fits
		if len(blob) <= gaps[gi].size {
			gapResults[gi].blob = blob
			gapResults[gi].offsets = make(map[int]uint16)
			for i, idx := range patIdxs {
				gapResults[gi].offsets[idx] = offsets[i]
			}
			gaps[gi].used = len(blob)
		} else {
			// Overlap didn't help enough, fall back to sequential
			// (shouldn't happen since we reserved space for full sizes)
			gapResults[gi].blob = nil
			for _, idx := range patIdxs {
				gapAssign[idx] = -1 // Remove from gap
			}
		}
	}

	// Build inGap map with final offsets
	inGap := make(map[int]int) // pattern index -> absolute offset
	for gi, res := range gapResults {
		if res.blob == nil {
			continue
		}
		gapStart := gaps[gi].start
		for idx, off := range res.offsets {
			inGap[idx] = gapStart + int(off)
		}
	}

	// Calculate order gap usage (gaps 6-11 are order table gaps)
	orderGapUsed := 0
	for gi := 6; gi < len(gaps); gi++ {
		orderGapUsed += gaps[gi].used
	}
	stats.OrderGapUsed = orderGapUsed

	// Build remaining patterns for overlap optimization (exclude gap patterns)
	var remainingPacked [][]byte
	remainingIdx := make(map[int]int) // original index -> remaining index
	for i, p := range individualPacked {
		if _, ok := inGap[i]; !ok {
			remainingIdx[i] = len(remainingPacked)
			remainingPacked = append(remainingPacked, p)
		}
	}

	// Re-run overlap on remaining patterns
	packedPtrsEnd := packedPtrsOff + numPatterns*2
	var packedRemaining []byte
	var offsetsRemaining []uint16
	if len(remainingPacked) > 0 {
		packedRemaining, offsetsRemaining = optimizePackedOverlap(remainingPacked)
	}

	// Build final offsets: gap patterns use gap offset, others use remaining blob offset
	finalOffsets := make([]uint16, numPatterns)
	for i := range individualPacked {
		if gapOff, ok := inGap[i]; ok {
			finalOffsets[i] = uint16(gapOff)
		} else {
			remIdx := remainingIdx[i]
			finalOffsets[i] = uint16(packedPtrsEnd + int(offsetsRemaining[remIdx]))
		}
	}
	patOffsets = finalOffsets
	packed = packedRemaining

	packedSize := len(packed)
	packedDataOff := packedPtrsEnd
	totalSize := packedDataOff + packedSize

	// Validate limits (skip when equiv disabled for all songs - equivtest mode)
	if disableEquivSong != -1 {
		if numPatterns > 91 {
			return nil, stats, fmt.Errorf("too many patterns: %d (max 91)", numPatterns)
		}
		if len(dict)/3 > 366 {
			return nil, stats, fmt.Errorf("dictionary too large: %d entries (max 366)", len(dict)/3)
		}
	}

	out := make([]byte, totalSize)

	// Collect transpose and trackptr values into temp arrays for later packing
	// These will be converted to relative indices and then packed into bitstream
	srcTransposeOff := []int{srcTranspose0Off, srcTranspose1Off, srcTranspose2Off}
	for ch := 0; ch < 3; ch++ {
		stats.TempTranspose[ch] = make([]byte, newNumOrders)
		stats.TempTrackptr[ch] = make([]byte, newNumOrders)
		for newIdx, oldIdx := range reachableOrders {
			stats.TempTranspose[ch][newIdx] = raw[srcTransposeOff[ch]+oldIdx]
			lo := raw[trackLoOff[ch]+oldIdx]
			hi := raw[trackHiOff[ch]+oldIdx]
			addr := uint16(lo) | uint16(hi)<<8
			stats.TempTrackptr[ch][newIdx] = patternIndex[addr]
		}
		if len(reachableOrders) > 0 {
			stats.TrackStarts[ch] = stats.TempTrackptr[ch][0]
		}
	}

	// Write instruments 1-31 (inst 0 not stored, saves 16 bytes)
	// Layout: inst 1 at newInstOff, inst 2 at newInstOff+16, etc.
	// Player uses virtual base = newInstOff - 16 so inst N is at base + N*16
	// Old format: all AD[0..n], all SR[0..n], all waveStart[0..n], etc.
	// New format: inst 1 params at $600-$60F, inst 2 at $610-$61F, etc.
	// Wave params: 2=start, 3=end, 4=loop; Arp params: 5=start, 6=end, 7=loop
	// Filter params: 13=start, 14=end, 15=loop
	// If instRemap provided, remap instruments: source inst V -> slot instRemap[V]
	instVirtualBase := newInstOff - 16 // inst 0 would be here if stored
	for inst := 1; inst < numInst; inst++ {
		dstInst := inst // default: no remap
		if instRemap != nil && inst < len(instRemap) && instRemap[inst] > 0 {
			dstInst = instRemap[inst]
		}
		for param := 0; param < 16; param++ {
			srcIdx := srcInstOff + param*numInst + inst
			dstIdx := instVirtualBase + dstInst*16 + param
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
	// Note: reserved bytes at 0x98F-0x990 are filled with transpose/delta bases later



	// Write pattern packing data
	// Row dictionary in split format: 3 arrays of 365 bytes each (dict[0] implicit)
	// dict0 (notes), dict1 (inst|effect), dict2 (params)
	// dict[0] is always [0,0,0], not stored - dict[1] starts at offset 0
	numEntries := len(dict) / 3
	for i := 1; i < numEntries; i++ {
		out[rowDictOff+i-1] = dict[i*3]         // note (dict[i] at offset i-1)
		out[rowDictOff+365+i-1] = dict[i*3+1]   // inst|effect
		out[rowDictOff+730+i-1] = dict[i*3+2]   // param
	}
	// Packed pointers (absolute song-base offsets with gap code in high bits)
	// Bits 0-12: offset (13 bits = 8KB max), bits 13-15: gap code (0-6)
	// Gap code N means implicit zeros: 0,1,3,7,15,31,63 (2^N - 1 for N>0)
	for i, pOff := range patOffsets {
		if pOff > 0x1FFF {
			panic(fmt.Sprintf("pattern offset %d exceeds 13-bit max", pOff))
		}
		out[packedPtrsOff+i*2] = byte(pOff & 0xFF)
		out[packedPtrsOff+i*2+1] = byte(pOff>>8) | (patGapCodes[i] << 5)
	}
	// Write optimized gap blobs
	for gi, res := range gapResults {
		if res.blob != nil {
			copy(out[gaps[gi].start:], res.blob)
		}
	}
	// Packed pattern data (remaining patterns after overlap)
	if len(packed) > 0 {
		copy(out[packedDataOff:], packed)
	}

	stats.PatternDictSize = len(dict) / 3
	stats.PatternPackedSize = len(packed)

	// Compute delta set from trackptr values (regular deltas only, initial deltas added later)
	deltaSet := make(map[int]bool)
	for ch := 0; ch < 3; ch++ {
		if len(reachableOrders) == 0 {
			continue
		}
		// Deltas between consecutive trackptr values
		for i := 1; i < len(reachableOrders); i++ {
			prev := int(stats.TempTrackptr[ch][i-1])
			curr := int(stats.TempTrackptr[ch][i])
			d := curr - prev
			if d > 127 {
				d -= 256
			} else if d < -128 {
				d += 256
			}
			deltaSet[d] = true
		}
	}
	stats.DeltaSet = make([]int, 0, len(deltaSet))
	for d := range deltaSet {
		stats.DeltaSet = append(stats.DeltaSet, d)
	}
	sort.Ints(stats.DeltaSet)

	return out, stats, nil
}

// remapRowBytes transforms a single row's bytes using effect and instrument remapping.
// This is the single source of truth for all row format transformations.
// instRemap can be nil to skip instrument remapping.
func remapRowBytes(b0, b1, b2 byte, remap [16]byte, fSubRemap map[int]byte, instRemap []int) (byte, byte, byte) {
	oldEffect := (b1 >> 5) | ((b0 >> 4) & 8)
	var newEffect byte
	var newParam byte = b2

	switch oldEffect {
	case 0:
		newEffect = 0
		newParam = 0
	case 1:
		newEffect = remap[1]
		if b2&0x80 != 0 {
			newParam = 0
		} else {
			newParam = 1
		}
	case 2:
		newEffect = remap[2]
		if b2 == 0x80 {
			newParam = 1
		} else {
			newParam = 0
		}
	case 3:
		newEffect = remap[3]
		newParam = ((b2 & 0x0F) << 4) | ((b2 & 0xF0) >> 4)
	case 4:
		newEffect = 0
		newParam = 1
	case 7:
		newEffect = remap[7]
		newParam = b2
	case 8:
		newEffect = remap[8]
		newParam = b2
	case 9:
		newEffect = remap[9]
		newParam = b2
	case 0xA:
		newEffect = remap[0xA]
		newParam = b2
	case 0xB:
		newEffect = remap[0xB]
		newParam = b2
	case 0xD:
		newEffect = 0
		newParam = 2
	case 0xE:
		newEffect = remap[0xE]
		newParam = b2
	case 0xF:
		if b2 < 0x80 {
			newEffect = fSubRemap[0x10]
			newParam = b2
		} else {
			hiNib := b2 & 0xF0
			loNib := b2 & 0x0F
			switch hiNib {
			case 0xB0:
				newEffect = 0
				newParam = 3
			case 0xF0:
				newEffect = fSubRemap[0x11]
				newParam = loNib
			case 0xE0:
				newEffect = fSubRemap[0x12]
				// loNib is inst index for filter trigger - remap it
				// Note: filter trigger can only address inst 0-15 (4-bit field * 16)
				instIdx := int(loNib)
				if instRemap != nil && instIdx > 0 && instIdx < len(instRemap) && instRemap[instIdx] > 0 {
					remapped := instRemap[instIdx]
					if remapped <= 15 {
						instIdx = remapped
					}
					// If remapped > 15, keep original (will read wrong inst but won't overflow)
				}
				newParam = byte(instIdx << 4)
			case 0x80:
				newEffect = fSubRemap[0x13]
				newParam = loNib
			case 0x90:
				newEffect = fSubRemap[0x14]
				newParam = loNib << 4
			default:
				newEffect = 0
				newParam = 0
			}
		}
	default:
		newEffect = remap[oldEffect]
		newParam = b2
	}

	newB0 := (b0 & 0x7F) | ((newEffect & 8) << 4)

	inst := int(b1 & 0x1F)
	if instRemap != nil && inst > 0 && inst < len(instRemap) && instRemap[inst] > 0 {
		inst = instRemap[inst]
	}
	newB1 := byte(inst&0x1F) | ((newEffect & 7) << 5)

	return newB0, newB1, newParam
}

// remapPatternEffects remaps effect numbers, parameters, and instruments in pattern data
func remapPatternEffects(pattern []byte, remap [16]byte, fSubRemap map[int]byte, instRemap []int) {
	for row := 0; row < 64; row++ {
		off := row * 3
		pattern[off], pattern[off+1], pattern[off+2] = remapRowBytes(
			pattern[off], pattern[off+1], pattern[off+2], remap, fSubRemap, instRemap)
	}
}

// translateRowHex translates a hex-encoded row from original to converted format
func translateRowHex(origHex string, remap [16]byte, fSubRemap map[int]byte, instRemap []int) string {
	if len(origHex) != 6 {
		return origHex
	}
	var b0, b1, b2 byte
	fmt.Sscanf(origHex, "%02x%02x%02x", &b0, &b1, &b2)
	newB0, newB1, newParam := remapRowBytes(b0, b1, b2, remap, fSubRemap, instRemap)
	return fmt.Sprintf("%02x%02x%02x", newB0, newB1, newParam)
}

// Global caches loaded once
// NOTE: Delete tools/odin_convert/equiv_cache.json when changing the pattern format
// (e.g., effect parameter encoding, row dictionary structure)
var globalEquivCache []EquivResult
var equivCacheLoaded bool
var globalExcludedOrig map[string]bool // temporary exclusions for validation pass

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

// optimizeEquivMapMinDict minimizes dictionary size by mapping rows to already-used targets or idx 0
func optimizeEquivMapMinDict(songNum int, dict []byte, patterns [][]byte, effectRemap [16]byte, fSubRemap map[int]byte, instRemap []int) map[int]int {
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

	// Build set of excluded original rows
	excludedOrig := make(map[string]bool)
	for _, origHex := range equivCache[songNum-1].ExcludedOrig {
		excludedOrig[origHex] = true
	}
	// Also check global exclusion set for validation pass
	for origHex := range globalExcludedOrig {
		excludedOrig[origHex] = true
	}

	// Translate equiv cache from original format to converted format
	translatedEquiv := make(map[string][]string)
	for origSrc, origDsts := range songEquiv {
		if excludedOrig[origSrc] {
			continue // Skip excluded original mappings
		}
		convSrc := translateRowHex(origSrc, effectRemap, fSubRemap, instRemap)
		var convDsts []string
		for _, origDst := range origDsts {
			convDsts = append(convDsts, translateRowHex(origDst, effectRemap, fSubRemap, instRemap))
		}
		translatedEquiv[convSrc] = convDsts
	}

	// Build row hex -> index map
	numEntries := len(dict) / 3
	rowToIdx := make(map[string]int)
	idxToHex := make(map[int]string)
	rowToIdx["000000"] = 0
	idxToHex[0] = "000000"
	for idx := 1; idx < numEntries; idx++ {
		rowHex := fmt.Sprintf("%02x%02x%02x", dict[idx*3], dict[idx*3+1], dict[idx*3+2])
		rowToIdx[rowHex] = idx
		idxToHex[idx] = rowHex
	}
	// Collect rows used in patterns (these form the initial "needed" set)
	usedInPatterns := make(map[int]bool)
	usedInPatterns[0] = true
	for _, pat := range patterns {
		var prevRow [3]byte
		numRows := len(pat) / 3
		for row := 0; row < numRows; row++ {
			off := row * 3
			curRow := [3]byte{pat[off], pat[off+1], pat[off+2]}
			if curRow != prevRow {
				rowHex := fmt.Sprintf("%02x%02x%02x", curRow[0], curRow[1], curRow[2])
				if idx, ok := rowToIdx[rowHex]; ok {
					usedInPatterns[idx] = true
				}
			}
			prevRow = curRow
		}
	}

	const primaryMax = 225
	type equivRow struct {
		idx     int
		options []int
		hasZero bool
	}
	var rows []equivRow
	for rowHex, optionHexList := range translatedEquiv {
		idx, ok := rowToIdx[rowHex]
		if !ok {
			continue
		}
		if !usedInPatterns[idx] || idx == 0 {
			continue
		}
		var options []int
		hasZero := false
		for _, optHex := range optionHexList {
			if optIdx, ok := rowToIdx[optHex]; ok && optIdx != idx {
				options = append(options, optIdx)
				if optIdx == 0 {
					hasZero = true
				}
			}
		}
		if len(options) > 0 {
			rows = append(rows, equivRow{idx: idx, options: options, hasZero: hasZero})
		}
	}

	// Sort: rows with idx 0 option first, then fewest options, then by idx for determinism
	for i := 0; i < len(rows)-1; i++ {
		for j := i + 1; j < len(rows); j++ {
			swap := false
			if rows[j].hasZero && !rows[i].hasZero {
				swap = true
			} else if rows[j].hasZero == rows[i].hasZero {
				if len(rows[j].options) < len(rows[i].options) {
					swap = true
				} else if len(rows[j].options) == len(rows[i].options) && rows[j].idx < rows[i].idx {
					swap = true
				}
			}
			if swap {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}

	// Track which indices will be used after equiv (start with patterns)
	finalUsed := make(map[int]bool)
	for idx := range usedInPatterns {
		finalUsed[idx] = true
	}

	// Build equiv map: only map if target is already used (reduces dict)
	equivMap := make(map[int]int)

	// First pass: map to idx 0 (always reduces dict by 1)
	for _, r := range rows {
		if r.hasZero {
			equivMap[r.idx] = 0
			delete(finalUsed, r.idx)
		}
	}

	// Second pass: map to already-used targets (reduces dict by 1)
	for _, r := range rows {
		if _, mapped := equivMap[r.idx]; mapped {
			continue
		}

		bestTarget := -1
		for _, opt := range r.options {
			if finalUsed[opt] && (bestTarget < 0 || opt < bestTarget) {
				bestTarget = opt
			}
		}

		if bestTarget >= 0 {
			equivMap[r.idx] = bestTarget
			delete(finalUsed, r.idx)
		}
	}

	// Third pass: cluster unmapped rows - only if target already in finalUsed
	// Keep iterating as each mapping may enable more
	changed := true
	for changed {
		changed = false
		for _, r := range rows {
			if _, mapped := equivMap[r.idx]; mapped {
				continue
			}

			// Only consider targets already in finalUsed
			bestTarget := -1
			for _, opt := range r.options {
				if finalUsed[opt] && (bestTarget < 0 || opt < bestTarget) {
					bestTarget = opt
				}
			}

			if bestTarget >= 0 {
				equivMap[r.idx] = bestTarget
				delete(finalUsed, r.idx)
				changed = true
			}
		}
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

// gapCodeToValue maps gap codes (0-6) to actual gap values (0,1,3,7,15,31,63)
var gapCodeToValue = []int{0, 1, 3, 7, 15, 31, 63}

// calculatePatternGap finds the best (largest) gap code for a pattern
// Returns gap code 0-6 where code N means gap = 2^N - 1 (except 0 means no gap)
func calculatePatternGap(pat []byte, truncateAfter int) int {
	numRows := len(pat) / 3
	if truncateAfter <= 0 || truncateAfter > numRows {
		truncateAfter = numRows
	}

	// Try gaps from largest to smallest (codes 6 down to 1)
	// Gap code 0 = no implicit zeros
	for code := 6; code >= 1; code-- {
		gap := gapCodeToValue[code]
		spacing := gap + 1
		if 64%spacing != 0 {
			continue
		}
		numSlots := 64 / spacing
		matches := true
		for slot := 0; slot < numSlots && matches; slot++ {
			startRow := slot * spacing
			// Check that rows startRow+1 through startRow+gap are all zero
			for zeroIdx := 1; zeroIdx <= gap && matches; zeroIdx++ {
				rowNum := startRow + zeroIdx
				if rowNum >= truncateAfter {
					break // Beyond truncation point
				}
				off := rowNum * 3
				if pat[off] != 0 || pat[off+1] != 0 || pat[off+2] != 0 {
					matches = false
				}
			}
		}
		if matches {
			return code
		}
	}
	return 0 // No gap encoding
}

// packPatternsWithEquiv packs pattern data, using equivalences to reduce extended indices
// truncateLimits provides cross-channel truncation limits (max reachable row+1 for each pattern)
// Returns gapCodes slice with gap code (0-6) for each pattern to store in pointer high nibble
func packPatternsWithEquiv(patterns [][]byte, inputDict []byte, equivMap map[int]int, truncateLimits []int) (dict []byte, packed []byte, offsets []uint16, gapCodes []byte, primaryCount int, extendedCount int, extendedBeforeEquiv int, individualPacked [][]byte) {
	// Use provided dict (already built from all patterns including transpose equivalents)
	fullDict := inputDict
	numFullEntries := len(fullDict) / 3

	// Build row -> index map
	rowToIdx := make(map[string]int)
	rowToIdx[string([]byte{0, 0, 0})] = 0 // implicit zero row
	for idx := 1; idx < numFullEntries; idx++ {
		row := string(fullDict[idx*3 : idx*3+3])
		rowToIdx[row] = idx
	}

	const primaryMax = 224
	const rleMax = 16
	const rleBase = 0xEF
	const extMarker = 0xFF
	const dictZeroRleMax = 15
	const dictOffsetBase = 0x10

	// First pass: collect which dict indices are actually used after equiv AND count usage
	usedIdx := make(map[int]bool)
	idxUsageCount := make(map[int]int)
	usedIdx[0] = true
	equivSubstitutions := 0
	for i, pat := range patterns {
		var prevRow [3]byte
		numRows := len(pat) / 3
		truncateAfter := numRows
		if truncateLimits != nil && i < len(truncateLimits) && truncateLimits[i] > 0 && truncateLimits[i] < truncateAfter {
			truncateAfter = truncateLimits[i]
		}
		for row := 0; row < truncateAfter; row++ {
			off := row * 3
			curRow := [3]byte{pat[off], pat[off+1], pat[off+2]}
			if curRow != prevRow {
				idx := rowToIdx[string(curRow[:])]
				if equivMap != nil {
					if priIdx, ok := equivMap[idx]; ok {
						idx = priIdx
						equivSubstitutions++
					}
				}
				usedIdx[idx] = true
				idxUsageCount[idx]++
			}
			prevRow = curRow
		}
	}
	extendedBeforeEquiv = equivSubstitutions

	// Build compacted dict with only used entries
	numCompacted := 0
	for idx := 1; idx < numFullEntries; idx++ {
		if usedIdx[idx] {
			numCompacted++
		}
	}

	// Create compacted dict entries (not yet in final order)
	type dictEntry struct {
		row      [3]byte
		oldIdx   int
		finalIdx int
	}
	compactedEntries := make([]dictEntry, 0, numCompacted)
	for idx := 1; idx < numFullEntries; idx++ {
		if usedIdx[idx] {
			var row [3]byte
			copy(row[:], fullDict[idx*3:idx*3+3])
			compactedEntries = append(compactedEntries, dictEntry{row: row, oldIdx: idx})
		}
	}

	// Sort by frequency (descending) and assign final positions
	sort.Slice(compactedEntries, func(i, j int) bool {
		return idxUsageCount[compactedEntries[i].oldIdx] > idxUsageCount[compactedEntries[j].oldIdx]
	})

	finalDict := make([]byte, (numCompacted+1)*3)
	oldToNew := make(map[int]int)
	oldToNew[0] = 0

	for i, entry := range compactedEntries {
		slot := i + 1
		compactedEntries[i].finalIdx = slot
		copy(finalDict[slot*3:], entry.row[:])
		oldToNew[entry.oldIdx] = slot
	}

	dict = finalDict

	// Second pass: calculate gap codes and pack patterns
	patternPacked := make([][]byte, len(patterns))
	gapCodes = make([]byte, len(patterns))

	for i, pat := range patterns {
		numRows := len(pat) / 3
		truncateAfter := numRows
		if truncateLimits != nil && i < len(truncateLimits) && truncateLimits[i] > 0 && truncateLimits[i] < truncateAfter {
			truncateAfter = truncateLimits[i]
		}

		// Calculate best gap code for this pattern
		gapCode := calculatePatternGap(pat, truncateAfter)
		gapCodes[i] = byte(gapCode)
		gap := gapCodeToValue[gapCode]
		spacing := gap + 1

		var patPacked []byte
		var prevRow [3]byte
		repeatCount := 0
		lastWasDictZero := false
		lastDictZeroPos := -1

		emitRLE := func() {
			if repeatCount == 0 {
				return
			}
			if lastWasDictZero && lastDictZeroPos >= 0 && repeatCount <= dictZeroRleMax {
				patPacked[lastDictZeroPos] = byte(repeatCount)
				lastWasDictZero = false
			} else {
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

		// Only encode rows at positions 0, spacing, 2*spacing, etc.
		// The implicit zeros at positions 1..gap, spacing+1..spacing+gap, etc. are skipped
		for slot := 0; slot*spacing < truncateAfter; slot++ {
			row := slot * spacing
			off := row * 3
			curRow := [3]byte{pat[off], pat[off+1], pat[off+2]}

			if curRow == prevRow {
				repeatCount++
				maxAllowed := rleMax
				if lastWasDictZero && lastDictZeroPos >= 0 {
					maxAllowed = dictZeroRleMax
				}
				if repeatCount == maxAllowed || (slot+1)*spacing >= truncateAfter {
					emitRLE()
				}
			} else {
				emitRLE()
				idx := rowToIdx[string(curRow[:])]

				// Apply equivalence
				if equivMap != nil {
					if priIdx, ok := equivMap[idx]; ok {
						idx = priIdx
					}
				}

				// Map to compacted index
				idx = oldToNew[idx]

				if idx == 0 {
					lastDictZeroPos = len(patPacked)
					patPacked = append(patPacked, 0x00)
					lastWasDictZero = true
					primaryCount++
				} else if idx < primaryMax {
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

	packed, offsets = optimizePackedOverlap(patternPacked)

	return dict, packed, offsets, gapCodes, primaryCount, extendedCount, extendedBeforeEquiv, patternPacked
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
	addr := uint16(c.Memory[c.PC])
	c.PC++
	return addr
}

func (c *CPU6502) addrZeroPageX() uint16 {
	addr := uint16(c.Memory[c.PC] + c.X)
	c.PC++
	return addr
}

func (c *CPU6502) addrZeroPageY() uint16 {
	addr := uint16(c.Memory[c.PC] + c.Y)
	c.PC++
	return addr
}

func (c *CPU6502) addrAbsolute() uint16 {
	lo := uint16(c.Memory[c.PC])
	hi := uint16(c.Memory[c.PC+1])
	c.PC += 2
	return hi<<8 | lo
}

func (c *CPU6502) addrAbsoluteX() (uint16, bool) {
	lo := uint16(c.Memory[c.PC])
	hi := uint16(c.Memory[c.PC+1])
	c.PC += 2
	base := hi<<8 | lo
	addr := base + uint16(c.X)
	crossed := (base & 0xFF00) != (addr & 0xFF00)
	return addr, crossed
}

func (c *CPU6502) addrAbsoluteY() (uint16, bool) {
	lo := uint16(c.Memory[c.PC])
	hi := uint16(c.Memory[c.PC+1])
	c.PC += 2
	base := hi<<8 | lo
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
	opcode := c.Memory[c.PC]
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

// RunFramesMatch runs frames and compares against baseline, returning true if they match.
// Aborts early on first difference for efficiency.
func (c *CPU6502) RunFramesMatch(playAddr uint16, frames int, baseline []SIDWrite) bool {
	c.SIDWrites = nil
	c.CurrentFrame = 0
	baseIdx := 0
	for i := 0; i < frames; i++ {
		c.CurrentFrame = i
		startLen := len(c.SIDWrites)
		c.Call(playAddr)
		for j := startLen; j < len(c.SIDWrites); j++ {
			if baseIdx >= len(baseline) ||
				c.SIDWrites[j].Addr != baseline[baseIdx].Addr ||
				c.SIDWrites[j].Value != baseline[baseIdx].Value {
				return false
			}
			baseIdx++
		}
	}
	return baseIdx == len(baseline)
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
	fmt.Println("  (none)            Convert songs and run verification tests")
	fmt.Println("  -equivtest [N]    Rebuild equivalence cache (slow, tests all pairs)")
	fmt.Println("  -equivvalidate N  Validate cached equiv pairs for song N, find bad combos")
	fmt.Println("  -h, --help        Show this help message")
}

func main() {
	// Parse command line arguments
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-equivtest":
			// Standalone equiv test - completely independent from conversion
			var onlySong int
			if len(os.Args) > 2 {
				fmt.Sscanf(os.Args[2], "%d", &onlySong)
			}
			runStandaloneEquivTest(onlySong)
			return
		case "-equivvalidate":
			// Validate cached equiv pairs for a specific song
			if len(os.Args) < 3 {
				fmt.Fprintf(os.Stderr, "Error: -equivvalidate requires a song number\n\n")
				printUsage()
				os.Exit(1)
			}
			var songNum int
			fmt.Sscanf(os.Args[2], "%d", &songNum)
			if songNum < 1 || songNum > 9 {
				fmt.Fprintf(os.Stderr, "Error: song number must be 1-9\n")
				os.Exit(1)
			}
			runEquivValidate(songNum)
			return
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

	// Analyze instrument usage and build remap for packing
	fmt.Println("\n=== Instrument Analysis ===")
	type songInstInfo struct {
		numInst          int
		usageCounts      []int      // usageCounts[i] = count for pattern inst i+1
		instData         [][16]byte // instData[i] = 16-byte data for source loop index i
		filterTriggerUse map[int]bool // instruments used by filter trigger (FEx effect)
		usedCount        int        // number of instruments actually used
		maxUsedSlot      int        // highest slot number used after remapping
	}
	songInstInfos := make([]songInstInfo, 9)

	for sn := 1; sn <= 9; sn++ {
		if songData[sn-1] == nil {
			continue
		}
		raw := songData[sn-1]
		baseAddr := int(raw[2]) << 8
		instADAddr := readWord(raw, codeInstAD)
		instSRAddr := readWord(raw, codeInstSR)
		numInst := int(instSRAddr) - int(instADAddr)
		srcInstOff := int(instADAddr) - baseAddr

		// Count which instruments are used in patterns
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

		usageCounts := make([]int, numInst)
		filterTriggerUse := make(map[int]bool)
		for addr := range patternAddrs {
			srcOff := int(addr) - baseAddr
			for row := 0; row < 64; row++ {
				off := srcOff + row*3
				byte0 := raw[off]
				byte1 := raw[off+1]
				byte2 := raw[off+2]
				inst := int(byte1 & 0x1F)
				if inst > 0 && inst < numInst {
					usageCounts[inst]++
				}
				// Track filter trigger usage (effect F, param 0xE0-0xEF)
				effect := int((byte1 >> 5) | ((byte0 >> 4) & 8))
				if effect == 0xF && byte2 >= 0xE0 && byte2 < 0xF0 {
					triggerInst := int(byte2 & 0x0F)
					if triggerInst > 0 {
						filterTriggerUse[triggerInst] = true
					}
				}
			}
		}

		// Extract instrument data (16 bytes each)
		instData := make([][16]byte, numInst)
		for i := 0; i < numInst; i++ {
			for p := 0; p < 16; p++ {
				idx := srcInstOff + p*numInst + i
				if idx < len(raw) {
					instData[i][p] = raw[idx]
				}
			}
		}

		usedCount := 0
		for i := 1; i < numInst; i++ {
			if usageCounts[i] > 0 {
				usedCount++
			}
		}
		songInstInfos[sn-1] = songInstInfo{numInst, usageCounts, instData, filterTriggerUse, usedCount, 0}

		unused := numInst - 1 - usedCount // slot 0 never used
		fmt.Printf("Song %d: %d/%d instruments used (%d unused = %d bytes)\n",
			sn, usedCount, numInst-1, unused, unused*16)
	}

	// Build instRemap for each song: instRemap[oldInst] = newInst (both 1-based pattern values)
	// Strategy: MFU (most frequently used) at lower indices for contiguous packing
	instRemaps := make([][]int, 9)

	for sn := 1; sn <= 9; sn++ {
		info := songInstInfos[sn-1]
		if info.instData == nil {
			continue
		}

		// Get used instruments sorted by frequency (descending)
		type instFreq struct {
			idx   int
			count int
		}
		var used []instFreq
		for i := 1; i < info.numInst; i++ {
			if info.usageCounts[i] > 0 {
				used = append(used, instFreq{i, info.usageCounts[i]})
			}
		}
		sort.Slice(used, func(a, b int) bool {
			if used[a].count != used[b].count {
				return used[a].count > used[b].count
			}
			return used[a].idx < used[b].idx
		})

		remap := make([]int, info.numInst)
		slotUsed := make([]bool, info.numInst)
		slotUsed[0] = true // slot 0 reserved

		// Separate filter trigger instruments (must be slots 1-15) from others
		var filterTriggerInsts []instFreq
		var otherInsts []instFreq
		for _, u := range used {
			if info.filterTriggerUse[u.idx] {
				filterTriggerInsts = append(filterTriggerInsts, u)
			} else {
				otherInsts = append(otherInsts, u)
			}
		}
		if len(filterTriggerInsts) > 15 {
			fmt.Printf("WARNING Song %d: %d filter trigger instruments but only slots 1-15 available\n",
				sn, len(filterTriggerInsts))
		}

		// Assign filter trigger instruments to slots 1-15
		nextSlot := 1
		for _, u := range filterTriggerInsts {
			if remap[u.idx] != 0 {
				continue
			}
			for slotUsed[nextSlot] {
				nextSlot++
			}
			remap[u.idx] = nextSlot
			slotUsed[nextSlot] = true
			nextSlot++
		}

		// Assign remaining other instruments
		for _, u := range otherInsts {
			if remap[u.idx] != 0 {
				continue
			}
			for slotUsed[nextSlot] {
				nextSlot++
			}
			remap[u.idx] = nextSlot
			slotUsed[nextSlot] = true
			nextSlot++
		}

		// Assign unused instruments to remaining slots
		for i := 1; i < info.numInst; i++ {
			if remap[i] != 0 {
				continue
			}
			for nextSlot < len(slotUsed) && slotUsed[nextSlot] {
				nextSlot++
			}
			if nextSlot < len(slotUsed) {
				remap[i] = nextSlot
				slotUsed[nextSlot] = true
				nextSlot++
			} else {
				remap[i] = nextSlot
				nextSlot++
			}
		}

		// Debug: check for duplicate targets
		usedSlots := make(map[int]int)
		for i := 1; i < info.numInst; i++ {
			if prev, ok := usedSlots[remap[i]]; ok {
				fmt.Printf("ERROR Song %d: slot %d used by both inst %d and %d\n", sn, remap[i], prev, i)
			}
			usedSlots[remap[i]] = i
		}

		_ = used

		// Calculate maxUsedSlot - highest slot with a used instrument
		maxUsedSlot := 0
		for i := 1; i < info.numInst; i++ {
			if info.usageCounts[i] > 0 && remap[i] > maxUsedSlot {
				maxUsedSlot = remap[i]
			}
		}
		songInstInfos[sn-1].maxUsedSlot = maxUsedSlot

		instRemaps[sn-1] = remap
	}

	// Print remap summary
	fmt.Println("Instrument remap (MFU packing)")

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
	effectNames := []string{"0", "1(slide)", "2(pulse)", "3(porta)", "4(vib)", "5", "6", "7(AD)", "8(SR)", "9(wave)", "A(arp)", "B(slide)", "C", "D(break)", "E(reso)", "F(ext)"}
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

	// Convert all songs
	convertedSongs := make([][]byte, 9)
	convertedStats := make([]ConversionStats, 9)
	conversionErrors := make(map[int]string)
	for songNum := 1; songNum <= 9; songNum++ {
		if songData[songNum-1] == nil {
			continue
		}
		convertedData, stats, err := convertToNewFormat(songData[songNum-1], songNum, effectRemap, fSubRemap, globalWave, nil, instRemaps[songNum-1], songInstInfos[songNum-1].maxUsedSlot)
		if err != nil {
			conversionErrors[songNum] = err.Error()
			continue
		}
		convertedSongs[songNum-1] = convertedData
		convertedStats[songNum-1] = stats
	}

	// Write converted parts to generated/parts directory
	partsDir := projectPath("generated/parts")
	if err := os.MkdirAll(partsDir, 0755); err != nil {
		fmt.Printf("Error creating parts directory: %v\n", err)
		os.Exit(1)
	}
	// Generate global delta table
	// First collect regular deltas and track starts
	var baseDeltaSets [9][]int
	var trackStarts [9][3]byte
	for songNum := 1; songNum <= 9; songNum++ {
		if convertedStats[songNum-1].DeltaSet != nil {
			baseDeltaSets[songNum-1] = convertedStats[songNum-1].DeltaSet
		}
		trackStarts[songNum-1] = convertedStats[songNum-1].TrackStarts
	}

	// Single constant approach - find best starting constant
	// Pre-compute base deltas union (without initial deltas)
	var baseUnion [256]bool
	for s := 0; s < 9; s++ {
		for _, d := range baseDeltaSets[s] {
			baseUnion[byte(d)] = true
		}
	}

	// Score each constant by union size (quick filter)
	type constScore struct {
		c, union int
	}
	scores := make([]constScore, 256)
	for c := 0; c < 256; c++ {
		var seen [256]bool
		copy(seen[:], baseUnion[:])
		for s := 0; s < 9; s++ {
			for ch := 0; ch < 3; ch++ {
				d := int(trackStarts[s][ch]) - c
				if d > 127 {
					d -= 256
				} else if d < -128 {
					d += 256
				}
				seen[byte(d)] = true
			}
		}
		count := 0
		for _, v := range seen {
			if v {
				count++
			}
		}
		scores[c] = constScore{c, count}
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i].union < scores[j].union })

	// Try top 10 constants in parallel
	type constResult struct {
		c, size int
	}
	constResults := make(chan constResult, 10)
	for i := 0; i < 10; i++ {
		go func(c int) {
			var testSets [9][]int
			for s := 0; s < 9; s++ {
				var seen [256]bool
				for _, d := range baseDeltaSets[s] {
					seen[byte(d)] = true
				}
				for ch := 0; ch < 3; ch++ {
					d := int(trackStarts[s][ch]) - c
					if d > 127 {
						d -= 256
					} else if d < -128 {
						d += 256
					}
					seen[byte(d)] = true
				}
				set := make([]int, 0, 35)
				for d := 0; d < 256; d++ {
					if seen[d] {
						if d > 127 {
							set = append(set, d-256)
						} else {
							set = append(set, d)
						}
					}
				}
				testSets[s] = set
			}
			result := solveDeltaTable(testSets)
			constResults <- constResult{c, len(result.Table)}
		}(scores[i].c)
	}
	bestConst, bestSize := 0, 9999
	for i := 0; i < 10; i++ {
		r := <-constResults
		if r.size < bestSize || (r.size == bestSize && r.c < bestConst) {
			bestSize, bestConst = r.size, r.c
		}
	}
	fmt.Printf("  Single const %d: %d bytes\n", bestConst, bestSize)

	// Build final delta sets with best constant
	var allDeltaSets [9][]int
	for s := 0; s < 9; s++ {
		var seen [256]bool
		for _, d := range baseDeltaSets[s] {
			seen[byte(d)] = true
		}
		for ch := 0; ch < 3; ch++ {
			d := int(trackStarts[s][ch]) - bestConst
			if d > 127 {
				d -= 256
			} else if d < -128 {
				d += 256
			}
			seen[byte(d)] = true
		}
		set := make([]int, 0, 35)
		for d := 0; d < 256; d++ {
			if seen[d] {
				if d > 127 {
					set = append(set, d-256)
				} else {
					set = append(set, d)
				}
			}
		}
		allDeltaSets[s] = set
		sort.Ints(allDeltaSets[s])
	}

	// Show delta counts per song and compute union
	unionDeltas := make(map[int]bool)
	for _, ds := range allDeltaSets {
		for _, d := range ds {
			unionDeltas[d] = true
		}
	}
	fmt.Printf("  Total unique deltas (union): %d\n", len(unionDeltas))
	deltaResult := solveDeltaTable(allDeltaSets)
	deltaResult.StartConst = bestConst
	if !verifyDeltaTable(deltaResult) {
		fmt.Println("WARNING: Delta table verification failed!")
	}

	// Collect transpose values from each song and build transpose table
	// Use TempTranspose arrays (raw values) from stats, not convertedSongs offsets
	var transposeSets [9][]int8
	for songNum := 1; songNum <= 9; songNum++ {
		if convertedSongs[songNum-1] == nil {
			continue
		}
		stats := &convertedStats[songNum-1]
		numOrders := stats.NewOrders
		unique := make(map[int8]bool)
		for order := 0; order < numOrders; order++ {
			for ch := 0; ch < 3; ch++ {
				t := int8(stats.TempTranspose[ch][order])
				unique[t] = true
			}
		}
		set := make([]int8, 0, len(unique))
		for v := range unique {
			set = append(set, v)
		}
		transposeSets[songNum-1] = set
	}
	transposeResult := solveTransposeTable(transposeSets)

	// Write combined tables file
	tablesPath := projectPath("generated/tables.inc")
	if err := writeTablesInc(deltaResult, transposeResult, tablesPath); err != nil {
		fmt.Printf("Error writing tables: %v\n", err)
	} else {
		fmt.Printf("Tables: delta=%d + transpose=%d bytes -> %s\n",
			len(deltaResult.Table), len(transposeResult.Table), tablesPath)
	}

	// Rebuild player with correct tables
	if err := rebuildPlayer(); err != nil {
		fmt.Printf("Error rebuilding player with tables: %v\n", err)
		os.Exit(1)
	}

	// Reload player with correct tables
	playerData, err = os.ReadFile(projectPath("build/player.bin"))
	if err != nil {
		fmt.Printf("Error reloading build/player.bin: %v\n", err)
		os.Exit(1)
	}

	// Build per-song transpose value -> relative index maps
	var transposeToRelIdx [9]map[int8]byte
	for songIdx := 0; songIdx < 9; songIdx++ {
		base := transposeResult.Bases[songIdx]
		transposeToRelIdx[songIdx] = make(map[int8]byte)
		for i := 0; i < 16 && base+i < len(transposeResult.Table); i++ {
			v := transposeResult.Table[base+i]
			if _, exists := transposeToRelIdx[songIdx][v]; !exists {
				transposeToRelIdx[songIdx][v] = byte(i)
			}
		}
	}

	// Analyze row dict combinations across all songs
	var totalCombos [8]int
	const rowDictOffAnalysis = 0x991
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
			interleavedDict[i*3+1] = data[rowDictOffAnalysis+365+i]
			interleavedDict[i*3+2] = data[rowDictOffAnalysis+730+i]
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

	// Build per-song delta value -> relative index lookup
	// Each song has a 32-entry window in the delta table
	var songDeltaToRelIdx [9]map[int8]byte
	for songIdx := 0; songIdx < 9; songIdx++ {
		base := deltaResult.Bases[songIdx]
		songDeltaToRelIdx[songIdx] = make(map[int8]byte)
		for i := 0; i < 32 && base+i < len(deltaResult.Table); i++ {
			d := deltaResult.Table[base+i]
			songDeltaToRelIdx[songIdx][d] = byte(i)
		}
	}

	// Convert trackptr/transpose to relative indices and pack into bitstream
	for songNum := 1; songNum <= 9; songNum++ {
		if convertedSongs[songNum-1] == nil {
			continue
		}
		stats := &convertedStats[songNum-1]
		// Set transpose base at 0x98F, delta base at 0x990
		convertedSongs[songNum-1][0x98F] = byte(transposeResult.Bases[songNum-1])
		convertedSongs[songNum-1][0x990] = byte(deltaResult.Bases[songNum-1])
		// Convert absolute trackptr values to relative delta indices (0-31)
		numOrders := stats.NewOrders
		deltaMap := songDeltaToRelIdx[songNum-1]
		for ch := 0; ch < 3; ch++ {
			prev := bestConst // TRACKPTR_START
			for i := 0; i < numOrders; i++ {
				curr := int(stats.TempTrackptr[ch][i])
				delta := int8(curr - prev)
				relIdx, ok := deltaMap[delta]
				if !ok {
					fmt.Printf("WARNING: Song %d ch%d order %d: delta %d not in song's window\n", songNum, ch, i, delta)
					relIdx = 0
				}
				stats.TempTrackptr[ch][i] = relIdx
				prev = curr
			}
		}
		// Convert transpose values to relative table indices (0-15, in temp array)
		for ch := 0; ch < 3; ch++ {
			for i := 0; i < numOrders; i++ {
				val := int8(stats.TempTranspose[ch][i])
				idx, ok := transposeToRelIdx[songNum-1][val]
				if !ok {
					fmt.Printf("WARNING: Song %d ch%d order %d: transpose %d not in window\n", songNum, ch, i, val)
					idx = 0
				}
				stats.TempTranspose[ch][i] = idx
			}
		}
		// Pack into bitstream at offset 0x000
		bitstream := packOrderBitstream(numOrders, stats.TempTranspose, stats.TempTrackptr)
		copy(convertedSongs[songNum-1][0x000:], bitstream)
	}

	// Second pass: run tests
	fmt.Println("=== Test Results ===")
	var wg sync.WaitGroup
	results := make(chan result, 9)

	for songNum := 1; songNum <= 9; songNum++ {
		if songData[songNum-1] == nil {
			continue
		}
		if errMsg, hasErr := conversionErrors[songNum]; hasErr {
			results <- result{songNum: songNum, err: errMsg}
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
	totalOrderWaste := 0
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
			orderGapAvail := 6 * (256 - s.NewOrders)
			totalOrderWaste += orderGapAvail - s.OrderGapUsed
			fmt.Printf("Song %d: cycles: %.2fx, max: %.2fx, size: %.2fx, dict: %d, len: $%X, ord: %d, gap: %d/%d\n", r.songNum, cycleRatio, maxCycleRatio, sizeRatio, s.PatternDictSize, r.newSize, s.NewOrders, s.OrderGapUsed, orderGapAvail)
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
		fmt.Printf("Order table gaps: %d bytes unused\n", totalOrderWaste)
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

		// Write parts (delta encoding already applied before tests)
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

	} else {
		os.Exit(1)
	}
}

// EquivResult stores equivalence cache results
// Uses row values (hex) instead of indices for position-independent caching
type EquivResult struct {
	SongNum      int                 `json:"song"`
	Equiv        map[string][]string `json:"equiv"`                    // original row hex -> list of valid replacements
	ExcludedOrig []string            `json:"excluded_orig,omitempty"`  // original row hex values to exclude from equiv
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
			remapPatternEffects(pat, effectRemap, fSubRemap, nil)
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

// runStandaloneEquivTest runs equiv testing independently from conversion
func runStandaloneEquivTest(onlySong int) {
	fmt.Println("=== Standalone Equivalence Test ===")
	fmt.Println("Testing equiv pairs against ORIGINAL audio...")

	// Disable equiv for all songs during this run
	disableEquivSong = -1 // Special value meaning "disable for all"

	songData := loadAllSongData()
	runEquivTest(songData, onlySong)
}

// runEquivValidate validates cached equiv pairs for a specific song
// Tests if applying all cached pairs causes audio failure, then binary searches to find bad combos
func runEquivValidate(songNum int) {
	fmt.Printf("=== Equiv Validation for Song %d ===\n", songNum)

	// Initialize global exclusion set
	globalExcludedOrig = make(map[string]bool)

	// Load all song data
	songData := loadAllSongData()
	rawData := songData[songNum-1]
	if rawData == nil {
		fmt.Printf("Error loading song %d\n", songNum)
		os.Exit(1)
	}

	// Build effect remapping
	effectRemap, fSubRemap := buildEffectRemap(songData)

	// Build global wavetable and write it (needed for player build)
	globalWave := buildGlobalWaveTable(songData)
	waveTablePath := projectPath("tools/odin_convert/wavetable.inc")
	if err := writeGlobalWaveTable(globalWave, waveTablePath); err != nil {
		fmt.Printf("Error writing wavetable: %v\n", err)
		os.Exit(1)
	}

	// Build and load player
	if err := rebuildPlayer(); err != nil {
		fmt.Printf("Error building player: %v\n", err)
		os.Exit(1)
	}
	playerPath := projectPath("build/player.bin")
	playerData, err := os.ReadFile(playerPath)
	if err != nil {
		fmt.Printf("Error loading player: %v\n", err)
		os.Exit(1)
	}

	// Load equiv cache and get original source hexes
	cache := loadEquivCache()
	if cache == nil || len(cache) < songNum {
		fmt.Println("No equiv cache found")
		os.Exit(1)
	}
	songEquiv := cache[songNum-1].Equiv
	if len(songEquiv) == 0 {
		fmt.Println("No equiv entries for this song")
		return
	}

	var origSources []string
	for origSrc := range songEquiv {
		origSources = append(origSources, origSrc)
	}
	sort.Strings(origSources)
	fmt.Printf("Found %d equiv sources to test\n", len(origSources))

	// Helper to convert and test
	testWithExclusions := func() bool {
		// Clear cache to force reload
		equivCacheLoaded = false
		globalEquivCache = nil

		// Convert song
		convData, convStats, err := convertToNewFormat(rawData, songNum, effectRemap, fSubRemap, globalWave, nil, nil, 31)
		if err != nil {
			return false
		}

		// Test
		result := testSong(songNum, rawData, convData, convStats, playerData)
		return result.passed
	}

	// First test with all equiv applied
	fmt.Printf("Testing with all %d equiv sources...\n", len(origSources))
	if testWithExclusions() {
		fmt.Println("PASS - All equiv pairs work together")
		return
	}
	fmt.Println("FAIL - Some equiv pairs cause issues when combined")

	// Verify that excluding ALL sources passes
	for _, src := range origSources {
		globalExcludedOrig[src] = true
	}
	if !testWithExclusions() {
		fmt.Println("ERROR - Song still fails without any equiv (other issue)")
		os.Exit(1)
	}
	fmt.Println("Confirmed: excluding all equiv passes")

	// Binary search for bad sources
	badSources := make(map[string]bool)

	var findBadSources func(sources []string)
	findBadSources = func(sources []string) {
		if len(sources) == 0 {
			return
		}
		if len(sources) == 1 {
			// Single source - test if excluding it helps
			globalExcludedOrig = make(map[string]bool)
			for s := range badSources {
				globalExcludedOrig[s] = true
			}
			globalExcludedOrig[sources[0]] = true
			if testWithExclusions() {
				badSources[sources[0]] = true
				fmt.Printf("  Found bad source: %s\n", sources[0])
			}
			return
		}

		// Test first half
		mid := len(sources) / 2
		firstHalf := sources[:mid]
		secondHalf := sources[mid:]

		// Exclude first half + known bad, keep second half
		globalExcludedOrig = make(map[string]bool)
		for s := range badSources {
			globalExcludedOrig[s] = true
		}
		for _, s := range firstHalf {
			globalExcludedOrig[s] = true
		}

		if testWithExclusions() {
			// Bad source is in first half
			findBadSources(firstHalf)
		} else {
			// Check second half
			globalExcludedOrig = make(map[string]bool)
			for s := range badSources {
				globalExcludedOrig[s] = true
			}
			for _, s := range secondHalf {
				globalExcludedOrig[s] = true
			}
			if testWithExclusions() {
				// Bad source is in second half
				findBadSources(secondHalf)
			} else {
				// Bad sources in both halves
				findBadSources(firstHalf)
				findBadSources(secondHalf)
			}
		}
	}

	// Start search with all sources included except known bad
	globalExcludedOrig = make(map[string]bool)
	findBadSources(origSources)

	if len(badSources) == 0 {
		fmt.Println("No individual bad sources found (might be interaction between multiple)")
		return
	}

	// Verify final result
	globalExcludedOrig = make(map[string]bool)
	for s := range badSources {
		globalExcludedOrig[s] = true
	}
	if testWithExclusions() {
		fmt.Printf("PASS with %d excluded sources\n", len(badSources))
	} else {
		fmt.Printf("WARNING: Still fails with %d exclusions\n", len(badSources))
	}

	// Update cache with bad sources
	cacheFile := projectPath("tools/odin_convert/equiv_cache.json")
	cacheData, err := os.ReadFile(cacheFile)
	if err != nil {
		fmt.Printf("Error reading cache: %v\n", err)
		return
	}
	var fullCache []EquivResult
	if json.Unmarshal(cacheData, &fullCache) != nil || len(fullCache) < songNum {
		fmt.Println("Error parsing cache")
		return
	}

	// Add new exclusions (avoid duplicates)
	existingExcluded := make(map[string]bool)
	for _, s := range fullCache[songNum-1].ExcludedOrig {
		existingExcluded[s] = true
	}
	for s := range badSources {
		if !existingExcluded[s] {
			fullCache[songNum-1].ExcludedOrig = append(fullCache[songNum-1].ExcludedOrig, s)
		}
	}
	sort.Strings(fullCache[songNum-1].ExcludedOrig)

	newData, _ := json.MarshalIndent(fullCache, "", "  ")
	os.WriteFile(cacheFile, newData, 0644)
	fmt.Printf("Updated cache with %d total excluded sources for song %d\n",
		len(fullCache[songNum-1].ExcludedOrig), songNum)

	// Clean up
	globalExcludedOrig = nil
}

// runEquivTest tests all pairs against ORIGINAL song audio to build the equivalence cache
// Uses original row values (not converted format)
// If onlySong > 0, only test that song
func runEquivTest(songData [][]byte, onlySong int) {
	fmt.Println("Testing ALL pairs against ORIGINAL audio...")
	if onlySong > 0 {
		fmt.Printf("Filtering to song %d only\n", onlySong)
	}

	results := make([]EquivResult, 9)
	songMu := make([]sync.Mutex, 9)
	for i := range results {
		results[i] = EquivResult{SongNum: i + 1, Equiv: make(map[string][]string)}
	}

	// Load existing cache to preserve data for songs not being tested
	if onlySong > 0 {
		cacheFile := projectPath("tools/odin_convert/equiv_cache.json")
		if data, err := os.ReadFile(cacheFile); err == nil {
			var existing []EquivResult
			if json.Unmarshal(data, &existing) == nil {
				for _, e := range existing {
					if e.SongNum != onlySong && e.SongNum >= 1 && e.SongNum <= 9 {
						results[e.SongNum-1].Equiv = e.Equiv
					}
				}
			}
		}
	}

	// Extract unique rows from each song's patterns
	type rowInfo struct {
		hex       string
		locations []int // offsets in raw data where this row appears
	}
	songRows := make([]map[string]*rowInfo, 9)

	for songNum := 1; songNum <= 9; songNum++ {
		if onlySong > 0 && songNum != onlySong {
			continue
		}
		raw := songData[songNum-1]
		if raw == nil {
			continue
		}

		// Extract pattern addresses from reachable orders only (same as conversion)
		baseAddr := 0x1000
		if songNum%2 == 0 {
			baseAddr = 0x7000
		}
		// Read track table offsets from embedded player code (same as conversion)
		trackLoOff := []int{
			int(readWord(raw, codeTrackLo0)) - baseAddr,
			int(readWord(raw, codeTrackLo1)) - baseAddr,
			int(readWord(raw, codeTrackLo2)) - baseAddr,
		}
		trackHiOff := []int{
			int(readWord(raw, codeTrackHi0)) - baseAddr,
			int(readWord(raw, codeTrackHi1)) - baseAddr,
			int(readWord(raw, codeTrackHi2)) - baseAddr,
		}
		// Calculate numOrders from table layout gap (same as conversion)
		srcTranspose0Off := int(readWord(raw, codeTranspose0)) - baseAddr
		trackLo0Off := int(readWord(raw, codeTrackLo0)) - baseAddr
		numOrders := trackLo0Off - srcTranspose0Off
		if numOrders <= 0 || numOrders > 255 {
			numOrders = 255
		}
		songStartOff := int(readWord(raw, codeSongStart)) - baseAddr
		startOrder := int(raw[songStartOff])
		reachableOrders, _ := findReachableOrders(raw, baseAddr, startOrder, numOrders, trackLoOff, trackHiOff)

		patternAddrs := make(map[uint16]bool)
		for _, order := range reachableOrders {
			for ch := 0; ch < 3; ch++ {
				lo := raw[trackLoOff[ch]+order]
				hi := raw[trackHiOff[ch]+order]
				addr := uint16(lo) | uint16(hi)<<8
				srcOff := int(addr) - baseAddr
				if srcOff >= 0 && srcOff+192 <= len(raw) {
					patternAddrs[addr] = true
				}
			}
		}

		// Collect unique rows and their locations from reachable patterns
		rows := make(map[string]*rowInfo)
		for addr := range patternAddrs {
			srcOff := int(addr) - baseAddr
			for row := 0; row < 64; row++ {
				off := srcOff + row*3
				rowBytes := [3]byte{raw[off], raw[off+1], raw[off+2]}
				hex := fmt.Sprintf("%02x%02x%02x", rowBytes[0], rowBytes[1], rowBytes[2])
				if rows[hex] == nil {
					rows[hex] = &rowInfo{hex: hex}
				}
				rows[hex].locations = append(rows[hex].locations, off)
			}
		}
		songRows[songNum-1] = rows

		numRows := len(rows)
		fmt.Printf("Song %d: %d unique rows\n", songNum, numRows)
	}

	// Count actual tests (done during work generation, updated via atomic)
	var totalTests int64

	// Shared progress counters
	var testsCompleted int64
	var matchesFound int64
	var songsComplete int32
	startTime := time.Now()
	done := make(chan struct{})

	// Progress and save ticker
	go func() {
		cacheFile := projectPath("tools/odin_convert/equiv_cache.json")
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				completed := atomic.LoadInt64(&testsCompleted)
				matches := atomic.LoadInt64(&matchesFound)
				songs := atomic.LoadInt32(&songsComplete)
				total := atomic.LoadInt64(&totalTests)
				elapsed := time.Since(startTime)
				rate := float64(completed) / elapsed.Seconds()

				fmt.Printf("\n=== Progress at %s ===\n", time.Now().Format("15:04:05"))
				if total > 0 && songs == 9 {
					// All songs queued, show accurate progress
					pct := float64(completed) / float64(total) * 100
					eta := time.Duration(float64(total-completed)/rate) * time.Second
					fmt.Printf("Tested: %d / %d (%.1f%%) - Songs complete: %d/9\n", completed, total, pct, songs)
					fmt.Printf("Speed: %.0f tests/sec - ETA: %v\n", rate, eta.Round(time.Second))
				} else {
					// Still queueing, show progress without percentage
					fmt.Printf("Tested: %d (queueing songs %d/9)\n", completed, songs)
					fmt.Printf("Speed: %.0f tests/sec\n", rate)
				}
				fmt.Printf("Matches found: %d (%.4f%%)\n", matches, float64(matches)/float64(completed)*100)

				// Save current results
				var resultsCopy []EquivResult
				for i := range results {
					songMu[i].Lock()
					equivCopy := make(map[string][]string)
					for k, v := range results[i].Equiv {
						vCopy := make([]string, len(v))
						copy(vCopy, v)
						equivCopy[k] = vCopy
					}
					resultsCopy = append(resultsCopy, EquivResult{SongNum: i + 1, Equiv: equivCopy})
					songMu[i].Unlock()
				}
				data, _ := json.MarshalIndent(resultsCopy, "", "  ")
				os.WriteFile(cacheFile, data, 0644)
				fmt.Printf("Saved checkpoint to %s\n", cacheFile)
			}
		}
	}()

	// Create work items for all songs
	type workItem struct {
		songNum int
		row1Hex string
		row2Hex string
	}
	work := make(chan workItem, 10000)

	// Start workers
	numWorkers := 32
	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each worker has its own CPU and song data copy per song for isolation
			cpus := make([]*CPU6502, 9)
			songDataCopies := make([][]byte, 9)
			baselines := make([][]SIDWrite, 9)
			snapshotInit := make([]bool, 9)

			for item := range work {
				sn := item.songNum
				row1Hex, row2Hex := item.row1Hex, item.row2Hex

				// Lazy init CPU for this song (baseline from ORIGINAL audio)
				if !snapshotInit[sn-1] {
					raw := songData[sn-1]
					testFrames := int(partTimes[sn-1])
					var bufferBase uint16
					if sn%2 == 1 {
						bufferBase = 0x1000
					} else {
						bufferBase = 0x7000
					}

					// Create baseline from original song
					cpu := NewCPU()
					copy(cpu.Memory[bufferBase:], raw)
					cpu.A = 0
					cpu.Call(bufferBase)
					cpu.SIDWrites = nil
					cpu.Cycles = 0
					baselines[sn-1], _ = cpu.RunFrames(bufferBase+3, testFrames, 0, 0, 0)
					cpus[sn-1] = cpu
					songDataCopies[sn-1] = make([]byte, len(raw))
					snapshotInit[sn-1] = true
				}

				// Test substitution: replace all occurrences of row2 with row1
				raw := songData[sn-1]
				dataCopy := songDataCopies[sn-1]
				copy(dataCopy, raw)

				// Decode row hex values
				row1Bytes := make([]byte, 3)
				row2Bytes := make([]byte, 3)
				fmt.Sscanf(row1Hex, "%02x%02x%02x", &row1Bytes[0], &row1Bytes[1], &row1Bytes[2])
				fmt.Sscanf(row2Hex, "%02x%02x%02x", &row2Bytes[0], &row2Bytes[1], &row2Bytes[2])

				// Replace row2 with row1 at all locations
				rows := songRows[sn-1]
				if rows[row2Hex] != nil {
					for _, off := range rows[row2Hex].locations {
						dataCopy[off] = row1Bytes[0]
						dataCopy[off+1] = row1Bytes[1]
						dataCopy[off+2] = row1Bytes[2]
					}
				}

				// Test modified song against baseline using fresh CPU
				testFrames := int(partTimes[sn-1])
				var bufferBase uint16
				if sn%2 == 1 {
					bufferBase = 0x1000
				} else {
					bufferBase = 0x7000
				}

				// Use func to allow recover from panic (invalid substitutions may cause infinite loops)
				func() {
					defer func() {
						if r := recover(); r != nil {
							// Invalid substitution - treat as no match
						}
					}()

					testCpu := NewCPU()
					copy(testCpu.Memory[bufferBase:], dataCopy)
					testCpu.A = 0
					testCpu.Call(bufferBase)
					testCpu.SIDWrites = nil
					testCpu.Cycles = 0

					if testCpu.RunFramesMatch(bufferBase+3, testFrames, baselines[sn-1]) {
						// row2 can be replaced by row1
						songMu[sn-1].Lock()
						results[sn-1].Equiv[row2Hex] = append(results[sn-1].Equiv[row2Hex], row1Hex)
						songMu[sn-1].Unlock()
						atomic.AddInt64(&matchesFound, 1)
					}
				}()
				atomic.AddInt64(&testsCompleted, 1)
			}
		}()
	}

	// Generate work items
	// Generate work items from row pairs
	go func() {
		for songNum := 1; songNum <= 9; songNum++ {
			if onlySong > 0 && songNum != onlySong {
				continue
			}
			rows := songRows[songNum-1]
			if rows == nil {
				continue
			}
			// Get sorted list of row hex values
			var rowList []string
			for hex := range rows {
				rowList = append(rowList, hex)
			}
			sort.Strings(rowList)

			// Helper to get effect from row hex
			getEffect := func(hex string) byte {
				if len(hex) != 6 {
					return 0
				}
				var b0, b1 byte
				fmt.Sscanf(hex, "%02x%02x", &b0, &b1)
				return (b1 >> 5) | ((b0 >> 4) & 8)
			}

			// Helper to check if effect will convert to effect 0
			isEffectZeroAfterConvert := func(effect byte) bool {
				return effect == 0 || effect == 4 || effect == 0xD
			}

			// Generate all pairs (row1, row2) where row1 != row2
			// Skip pairs where BOTH rows have effect 0 after conversion (trivial equiv)
			for i, row1 := range rowList {
				eff1 := getEffect(row1)
				isZero1 := isEffectZeroAfterConvert(eff1)
				for j := i + 1; j < len(rowList); j++ {
					row2 := rowList[j]
					eff2 := getEffect(row2)
					isZero2 := isEffectZeroAfterConvert(eff2)

					// Skip if both convert to effect 0 - these are trivial
					if isZero1 && isZero2 {
						continue
					}

					// Test both directions: row2 -> row1 and row1 -> row2
					work <- workItem{songNum, row1, row2}
					work <- workItem{songNum, row2, row1}
					atomic.AddInt64(&totalTests, 2)
				}
			}
			atomic.AddInt32(&songsComplete, 1)
			fmt.Printf("Song %d: %d tests queued\n", songNum, atomic.LoadInt64(&totalTests))
		}
		close(work)
	}()

	wg.Wait()
	close(done)

	// Sort all values and write final cache
	for i := range results {
		for k := range results[i].Equiv {
			sort.Strings(results[i].Equiv[k])
		}
	}

	cacheFile := projectPath("tools/odin_convert/equiv_cache.json")
	data, _ := json.MarshalIndent(results, "", "  ")
	os.WriteFile(cacheFile, data, 0644)

	totalFound := atomic.LoadInt64(&matchesFound)
	fmt.Printf("\n=== COMPLETE ===\n")
	fmt.Printf("Wrote %d pairs to %s\n", totalFound, cacheFile)
	fmt.Printf("Duration: %v\n", time.Since(startTime).Round(time.Second))
}

func rowHexFromData(convertedData []byte, idx int) string {
	if idx == 0 {
		return "000000"
	}
	return fmt.Sprintf("%02x%02x%02x", convertedData[0x99F+idx-1], convertedData[0x99F+365+idx-1], convertedData[0x99F+730+idx-1])
}

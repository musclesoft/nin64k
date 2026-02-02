package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ROW_DICT_OFF    = 0x9D9
	PACKED_PTRS_OFF = 0xEAD
	PACKED_DATA_OFF = 0xF63
)

type Row struct{ Note, InstEff, Param byte }

func decodePattern(data []byte, patternIdx int) []Row {
	ptrOff := PACKED_PTRS_OFF + patternIdx*2
	if ptrOff+1 >= len(data) {
		return nil
	}
	packedOff := int(data[ptrOff]) | int(data[ptrOff+1])<<8
	srcOff := PACKED_DATA_OFF + packedOff
	rows := make([]Row, 0, 64)
	var prevRow Row
	for len(rows) < 64 && srcOff < len(data) {
		b := data[srcOff]
		srcOff++
		if b <= 0xDF {
			dictOff := ROW_DICT_OFF + int(b)*3
			if dictOff+2 < len(data) {
				prevRow = Row{data[dictOff], data[dictOff+1], data[dictOff+2]}
				rows = append(rows, prevRow)
			}
		} else if b <= 0xFE {
			for i := 0; i < int(b)-0xDF && len(rows) < 64; i++ {
				rows = append(rows, prevRow)
			}
		} else {
			if srcOff >= len(data) {
				break
			}
			extByte := data[srcOff]
			srcOff++
			dictOff := ROW_DICT_OFF + (224+int(extByte))*3
			if dictOff+2 < len(data) {
				prevRow = Row{data[dictOff], data[dictOff+1], data[dictOff+2]}
				rows = append(rows, prevRow)
			}
		}
	}
	return rows
}

func getNumOrders(data []byte, maxFrames int) int {
	patternCache := make(map[int][]Row)
	frame := 0
	speed := 6
	maxOrderSeen := 0
	loopTarget := -1

	// Simulate order progression with loops
	for order := 0; order < 256 && frame < maxFrames; {
		if order > maxOrderSeen {
			maxOrderSeen = order
		}
		patternBroken := false
		for rowNum := 0; rowNum < 64 && !patternBroken; rowNum++ {
			for ch := 0; ch < 3; ch++ {
				patIdx := int(data[0x500+ch*256+order])
				pattern, ok := patternCache[patIdx]
				if !ok {
					pattern = decodePattern(data, patIdx)
					patternCache[patIdx] = pattern
				}
				if rowNum >= len(pattern) {
					continue
				}
				r := pattern[rowNum]
				effect := (r.InstEff >> 5) | ((r.Note >> 4) & 0x08)
				if effect == 0x03 && r.Param < 0x80 && r.Param > 0 {
					speed = int(r.Param) // Effect 3 = Fxx, param < 0x80 = speed
				}
				// Effect C = position jump
				if effect == 0x0C {
					loopTarget = int(r.Param)
				}
				// Effect B = pattern break
				if effect == 0x0B {
					patternBroken = true
				}
			}
			frame += speed
			if frame >= maxFrames {
				break
			}
		}
		if loopTarget >= 0 {
			order = loopTarget
			loopTarget = -1
		} else {
			order++
		}
	}
	// Add margin for timing variations
	result := maxOrderSeen + 5
	if result > 256 {
		result = 256
	}
	return result
}

func extractSongStreams(data []byte, maxFrames int) [3][]byte {
	numOrders := getNumOrders(data, maxFrames)
	var streams [3][]byte
	for ch := 0; ch < 3; ch++ {
		for order := 0; order < numOrders; order++ {
			patIdx := int(data[0x500+ch*256+order])
			pattern := decodePattern(data, patIdx)
			for _, r := range pattern {
				streams[ch] = append(streams[ch], r.Note, r.InstEff, r.Param)
			}
		}
	}
	return streams
}

func extractTranspose(data []byte, maxFrames int) [3][]int8 {
	numOrders := getNumOrders(data, maxFrames)
	var transpose [3][]int8
	for ch := 0; ch < 3; ch++ {
		off := 0x200 + ch*0x100
		for order := 0; order < numOrders; order++ {
			transpose[ch] = append(transpose[ch], int8(data[off+order]))
		}
	}
	return transpose
}

func extractOrderTable(data []byte, maxFrames int) [3][]byte {
	numOrders := getNumOrders(data, maxFrames)
	var orders [3][]byte
	for ch := 0; ch < 3; ch++ {
		off := 0x500 + ch*0x100
		for order := 0; order < numOrders; order++ {
			orders[ch] = append(orders[ch], data[off+order])
		}
	}
	return orders
}

func extractInstruments(data []byte) []byte {
	// 32 instruments × 16 bytes = 512 bytes
	if len(data) < 512 {
		return nil
	}
	inst := make([]byte, 512)
	copy(inst, data[:512])
	return inst
}

func extractFilterTable(data []byte) []byte {
	const filterOff = 0x800
	const filterSize = 234
	if len(data) < filterOff+filterSize {
		return nil
	}
	filter := make([]byte, filterSize)
	copy(filter, data[filterOff:filterOff+filterSize])
	return filter
}

func extractWaveTable(data []byte) []byte {
	const waveOff = 0x8EA
	const waveSize = 256 // Match validation's size
	if len(data) < waveOff+waveSize {
		return nil
	}
	wave := make([]byte, waveSize)
	copy(wave, data[waveOff:waveOff+waveSize])
	return wave
}

func extractArpTable(data []byte) []byte {
	const arpOff = 0x91D
	const arpSize = 256 // Match validation's size
	if len(data) < arpOff+arpSize {
		return nil
	}
	arp := make([]byte, arpSize)
	copy(arp, data[arpOff:arpOff+arpSize])
	return arp
}

func countUsedBytes(data []byte) int {
	if data == nil {
		return 0
	}
	// Find last non-zero byte
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] != 0 {
			return i + 1
		}
	}
	return 0
}

func countUniqueTables[T any](tables []T, extract func(T) []byte) int {
	seen := make(map[string]bool)
	for _, t := range tables {
		data := extract(t)
		if data == nil {
			continue
		}
		key := string(data)
		seen[key] = true
	}
	return len(seen)
}

func countUniqueBytes(data []byte) int {
	seen := make(map[byte]bool)
	for _, b := range data {
		seen[b] = true
	}
	return len(seen)
}

func calculateEntropy(data []byte) float64 {
	if len(data) == 0 {
		return 0
	}
	counts := make(map[byte]int)
	for _, b := range data {
		counts[b]++
	}
	entropy := 0.0
	n := float64(len(data))
	for _, count := range counts {
		p := float64(count) / n
		entropy -= p * math.Log2(p)
	}
	return entropy
}

func countRuns(data []byte) int {
	runs := 0
	for i := 1; i < len(data); i++ {
		if data[i] == data[i-1] {
			runs++
		}
	}
	return runs
}

func count4GramsInt(data []int) (distinct int, repeats int) {
	grams := make(map[[4]int]int)
	for i := 0; i <= len(data)-4; i++ {
		var g [4]int
		copy(g[:], data[i:i+4])
		grams[g]++
	}
	for _, count := range grams {
		distinct++
		if count > 1 {
			repeats += count - 1
		}
	}
	return
}

func calculateEntropyInt(data []int) float64 {
	if len(data) == 0 {
		return 0
	}
	counts := make(map[int]int)
	for _, v := range data {
		counts[v]++
	}
	entropy := 0.0
	n := float64(len(data))
	for _, count := range counts {
		p := float64(count) / n
		entropy -= p * math.Log2(p)
	}
	return entropy
}

func calculateEntropyInt8(data []int8) float64 {
	if len(data) == 0 {
		return 0
	}
	counts := make(map[int8]int)
	for _, v := range data {
		counts[v]++
	}
	entropy := 0.0
	n := float64(len(data))
	for _, count := range counts {
		p := float64(count) / n
		entropy -= p * math.Log2(p)
	}
	return entropy
}

type compressResult struct {
	song         int
	numOrders    int
	chCompressed [3][]byte
	chStats      [3]CompressStats
}

type intStream struct {
	data     []int
	bitWidth int
	name     string
	isExp    bool
	window   int
}

type SongTables struct {
	instruments []byte
	filter      []byte
	wave        []byte
	arp         []byte
}

// Vibrato lookup table (10 depths × 16 positions, frequency-sorted)
// Matches player's vibrato_table in odin_player.inc
// Frequency order: 4(22) 2(13) 3(11) 1(6) 6(2) 10(1) 5(1) 8(1) 15(1)
var vibratoTable = []byte{
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // depth 0
	0x00, 0x06, 0x0c, 0x13, 0x18, 0x1e, 0x24, 0x29, 0x2d, 0x31, 0x35, 0x38, 0x3b, 0x3d, 0x3f, 0x40, // new 1 = old depth 4
	0x00, 0x03, 0x06, 0x09, 0x0c, 0x0f, 0x12, 0x14, 0x17, 0x19, 0x1b, 0x1c, 0x1e, 0x1f, 0x1f, 0x20, // new 2 = old depth 2
	0x00, 0x05, 0x09, 0x0e, 0x12, 0x17, 0x1b, 0x1e, 0x22, 0x25, 0x28, 0x2a, 0x2c, 0x2e, 0x2f, 0x30, // new 3 = old depth 3
	0x00, 0x02, 0x03, 0x05, 0x06, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x0f, 0x10, 0x10, // new 4 = old depth 1
	0x00, 0x09, 0x13, 0x1c, 0x25, 0x2d, 0x35, 0x3d, 0x44, 0x4a, 0x50, 0x55, 0x59, 0x5c, 0x5e, 0x60, // new 5 = old depth 6
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x66, 0x71, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x9f, // new 6 = old depth 10
	0x00, 0x08, 0x10, 0x17, 0x1f, 0x26, 0x2c, 0x33, 0x39, 0x3e, 0x43, 0x47, 0x4a, 0x4d, 0x4e, 0x50, // new 7 = old depth 5
	0x00, 0x0d, 0x19, 0x25, 0x31, 0x3c, 0x47, 0x51, 0x5b, 0x63, 0x6a, 0x71, 0x76, 0x7a, 0x7e, 0x7f, // new 8 = old depth 8
	0x00, 0x18, 0x2f, 0x46, 0x5c, 0x71, 0x85, 0x98, 0xaa, 0xba, 0xc8, 0xd4, 0xde, 0xe6, 0xeb, 0xef, // new 9 = old depth 15
}

// SID frequency table from player (indices 0-103, includes 8 extended entries)
var freqTableLo = []byte{
	0x12, 0x00, 0x00, 0x46, 0x5a, 0x6e, 0x84, 0x9b, 0xb3, 0xcd, 0xe9, 0x06,
	0x25, 0x45, 0x68, 0x8c, 0xb3, 0xdc, 0x08, 0x36, 0x67, 0x9b, 0xd2, 0x0c,
	0x49, 0x8b, 0xd0, 0x19, 0x67, 0xb9, 0x10, 0x6c, 0xce, 0x35, 0xa3, 0x17,
	0x93, 0x15, 0x9f, 0x32, 0xcd, 0x72, 0x20, 0xd8, 0x9c, 0x6b, 0x46, 0x2f,
	0x25, 0x2a, 0x3f, 0x64, 0x9a, 0xe3, 0x3f, 0xb1, 0x38, 0xd6, 0x8d, 0x5e,
	0x4b, 0x55, 0x7e, 0xc8, 0x34, 0xc6, 0x7f, 0x61, 0x6f, 0xac, 0x1a, 0xbc,
	0x95, 0xa9, 0xfc, 0x8f, 0x69, 0x8c, 0xfe, 0xc2, 0xdf, 0x58, 0x34, 0x78,
	0x2b, 0x53, 0xf7, 0x1f, 0xd2, 0x19, 0xfc, 0x85, 0xbd, 0xb0, 0x67, 0xff,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x2f, // Notes 96-103 (extended)
}
var freqTableHi = []byte{
	0x01, 0x00, 0x00, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x02,
	0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x03, 0x03, 0x03, 0x03, 0x03, 0x04,
	0x04, 0x04, 0x04, 0x05, 0x05, 0x05, 0x06, 0x06, 0x06, 0x07, 0x07, 0x08,
	0x08, 0x09, 0x09, 0x0a, 0x0a, 0x0b, 0x0c, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
	0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x18, 0x19, 0x1b, 0x1c, 0x1e, 0x20,
	0x22, 0x24, 0x26, 0x28, 0x2b, 0x2d, 0x30, 0x33, 0x36, 0x39, 0x3d, 0x40,
	0x44, 0x48, 0x4c, 0x51, 0x56, 0x5b, 0x60, 0x66, 0x6c, 0x73, 0x7a, 0x81,
	0x89, 0x91, 0x99, 0xa3, 0xac, 0xb7, 0xc1, 0xcd, 0xd9, 0xe6, 0xf4, 0xff,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x30, // Notes 96-103 (extended)
}

// sidFreqToMIDI converts a SID frequency value to MIDI note number
// The odin_player frequency table is tuned to A4 ≈ 459 Hz (not 440 Hz)
// We convert to standard A=440 MIDI notes for compatibility
func sidFreqToMIDI(freq uint16) int {
	if freq == 0 {
		return -1
	}
	// Convert SID freq to Hz: freq * 985248 / 16777216 (PAL clock)
	hz := float64(freq) * 985248.0 / 16777216.0
	if hz < 8.0 {
		return -1
	}
	// odin_player uses A4 ≈ 459 Hz (tracker note 58 = 0x1E8D)
	// To get tracker-relative notes: 12 * log2(hz / 459) + 69
	// But for standard A440 MIDI: 12 * log2(hz / 440) + 69
	// The difference is about 0.73 semitones - we use tracker-relative
	// so MIDI playback matches the original relative pitches
	const playerA4 = 459.0 // Hz - A4 frequency in odin_player table
	midiFloat := 12*math.Log2(hz/playerA4) + 69
	return int(midiFloat + 0.5)
}

// getExpectedSIDFreq computes expected SID frequency for a tracker note + transpose
// Note: The player subtracts 1 from the note before lookup (see odin_player.inc line 689)
func getExpectedSIDFreq(trackerNote int, trans int8) uint16 {
	if trackerNote <= 0 || trackerNote >= 0x61 {
		return 0
	}
	// Player does: chn_note = (note - 1), then freqtable[chn_note + transpose]
	idx := (trackerNote - 1) + int(trans)
	if idx < 0 || idx >= len(freqTableLo) {
		return 0
	}
	return uint16(freqTableLo[idx]) | uint16(freqTableHi[idx])<<8
}

// Part times (frame counts for each song)
var partTimes = []uint16{
	0xBB44, // Song 1: 47940 frames
	0x7234, // Song 2: 29236 frames
	0x57C0, // Song 3: 22464 frames
	0x88D0, // Song 4: 35024 frames
	0xC0A4, // Song 5: 49316 frames
	0x79F6, // Song 6: 31222 frames
	0x491A, // Song 7: 18714 frames
	0x7BF0, // Song 8: 31728 frames
	0x6D80, // Song 9: 28032 frames
}

// VoiceFreq holds frequency data for one voice across all frames
type VoiceFreq struct {
	Freqs []uint16 // frequency per frame
}

// captureSIDFrequencies runs VM emulation using the new player (build/player.bin)
// with partX.bin song data and captures SID frequencies for MIDI generation
func captureSIDFrequencies() [3][]uint16 {
	var sidFreqs [3][]uint16
	playerData, err := os.ReadFile("build/player.bin")
	if err != nil {
		fmt.Printf("Warning: could not load player: %v\n", err)
		return sidFreqs
	}

	const playerBase = uint16(0xF000)

	for song := 1; song <= 9; song++ {
		songPath := filepath.Join("generated", "parts", fmt.Sprintf("part%d.bin", song))
		songData, err := os.ReadFile(songPath)
		if err != nil {
			fmt.Printf("Warning: could not load song %d: %v\n", song, err)
			continue
		}

		numFrames := int(partTimes[song-1])
		var bufferBase uint16
		if song%2 == 1 {
			bufferBase = 0x1000
		} else {
			bufferBase = 0x7000
		}

		cpu := NewCPU()
		copy(cpu.Memory[bufferBase:], songData)
		copy(cpu.Memory[playerBase:], playerData)
		cpu.A = 0
		cpu.X = byte(bufferBase >> 8)
		cpu.Call(playerBase) // init with X = buffer page

		// Run frames and capture SID writes
		voiceFreqLo := [3]byte{}
		voiceFreqHi := [3]byte{}
		songFreqs := [3][]uint16{}
		for ch := 0; ch < 3; ch++ {
			songFreqs[ch] = make([]uint16, numFrames)
		}

		for frame := 0; frame < numFrames; frame++ {
			cpu.SIDWrites = nil
			cpu.CurrentFrame = frame
			cpu.Call(playerBase + 3) // play

			// Process SID writes for frequency capture
			for _, w := range cpu.SIDWrites {
				switch w.Addr {
				case 0xD400:
					voiceFreqLo[0] = w.Value
				case 0xD401:
					voiceFreqHi[0] = w.Value
				case 0xD407:
					voiceFreqLo[1] = w.Value
				case 0xD408:
					voiceFreqHi[1] = w.Value
				case 0xD40E:
					voiceFreqLo[2] = w.Value
				case 0xD40F:
					voiceFreqHi[2] = w.Value
				}
			}

			// Record current frequencies for this frame
			for ch := 0; ch < 3; ch++ {
				songFreqs[ch][frame] = uint16(voiceFreqLo[ch]) | uint16(voiceFreqHi[ch])<<8
			}
		}

		// Append SID frequencies
		for ch := 0; ch < 3; ch++ {
			sidFreqs[ch] = append(sidFreqs[ch], songFreqs[ch]...)
		}
	}

	return sidFreqs
}

// Instrument offsets in partX.bin
const (
	INST_AD          = 0
	INST_SR          = 1
	INST_WAVESTART   = 2
	INST_WAVEEND     = 3
	INST_WAVELOOP    = 4
	INST_ARPSTART    = 5
	INST_ARPEND      = 6
	INST_ARPLOOP     = 7
	INST_VIBDELAY    = 8
	INST_VIBDEPSP    = 9
	INST_PULSEWIDTH  = 10
	INST_PULSESPEED  = 11
	INST_PULSELIMITS = 12
	INST_FILTSTART   = 13
	INST_FILTEND     = 14
	INST_FILTLOOP    = 15
	INST_SIZE        = 16
	WAVETABLE_OFF   = 0x8EA // Player patches: wave_load_addr = song_base + $08EA
	ARPTABLE_OFF    = 0x91D // Player patches: arp_load_addr = song_base + $091D
	FILTERTABLE_OFF = 0x800 // Filter table offset
	FILTERTABLE_SZ  = 234   // Filter table size
)

// Effect parameter unmap tables (convert remapped indices back to original values)
// These match the remap tables in odin_player.inc
var adUnmap = []byte{0x08, 0x09, 0x48, 0x0A}             // effect 7 (AD)
var srUnmap = []byte{0xF9, 0x0D, 0xFF, 0xF8, 0x0F, 0x0E} // effect 4 (SR)
var waveUnmap = []byte{0xFF, 0x80, 0x43, 0x81}           // effect 5 (wave)
var resoUnmap = []byte{0xF1, 0x00, 0xF4, 0xF0, 0xF2, 0x52, 0xF5} // effect 8 (reso)

// Vibrato depth unmap: new depth index (1-9) -> original depth (0,4,2,3,1,6,10,5,8,15)
// Index 0 = depth 0, indices 1-9 are frequency-sorted
var vibDepthUnmap = []byte{0, 4, 2, 3, 1, 6, 10, 5, 8, 15}

// unmapParam converts a remapped effect param back to its original value
func unmapParam(param byte, table []byte) byte {
	if int(param) < len(table) {
		return table[param]
	}
	return param // return as-is if out of bounds
}

// simulateStreamPlayer runs a frame-exact player simulation using extracted stream data
// This simulates the actual player's behavior including instrument arpeggio tables
func simulateStreamPlayer(streams [3][]byte, transpose [3][]int8, songBoundaries []int) [3][]uint16 {
	numRows := len(streams[0]) / 3

	// Load all song data to get instrument and arp tables
	type songData struct {
		instData []byte // instruments at offset 0
		arpTable []byte // arpeggio table at offset 0x91D
	}
	var songs []songData
	for song := 1; song <= 9; song++ {
		data, err := os.ReadFile(filepath.Join("generated", "parts", fmt.Sprintf("part%d.bin", song)))
		if err != nil {
			songs = append(songs, songData{})
			continue
		}
		inst := make([]byte, 32*INST_SIZE)
		if len(data) >= 32*INST_SIZE {
			copy(inst, data[:32*INST_SIZE])
		}
		arp := make([]byte, 256)
		if len(data) > ARPTABLE_OFF+256 {
			copy(arp, data[ARPTABLE_OFF:ARPTABLE_OFF+256])
		}
		songs = append(songs, songData{inst, arp})
	}

	// Estimate total frames from speed changes
	totalFrames := 0
	speed := 6
	songIdx := 0
	for row := 0; row < numRows; row++ {
		if songIdx < len(songBoundaries)-1 && row == songBoundaries[songIdx+1] {
			speed = 6
			songIdx++
		}
		for ch := 0; ch < 3; ch++ {
			off := row * 3
			noteByte, instEff, param := streams[ch][off], streams[ch][off+1], streams[ch][off+2]
			effect := int((instEff >> 5) | ((noteByte >> 4) & 0x08))
			if effect == 0x03 && param < 0x80 && param > 0 { // Effect 3 = Fxx speed
				speed = int(param)
			}
		}
		totalFrames += speed
	}

	// Channel state
	type chanState struct {
		freq        uint16 // current SID frequency (from arp lookup)
		noteFreq    uint16 // base note frequency (for slides/vibrato)
		note        int    // note - 1 (as stored by player)
		inst        int    // current instrument
		effect      int    // current effect
		param       byte   // current param
		arpIdx      int    // current arpeggio index
		portaTarget uint16 // portamento target frequency
		vibratoPos  int    // vibrato oscillator position (player: chn_vibpos)
		vibSpeed    int    // instrument vibrato speed
		vibDepth    int    // instrument vibrato depth
		vibDelay    int    // instrument vibrato delay countdown
	}

	var state [3]chanState
	var result [3][]uint16
	for ch := 0; ch < 3; ch++ {
		result[ch] = make([]uint16, totalFrames)
	}

	// Run simulation
	frame := 0
	speed = 6
	speedCounter := 5 // Player init sets speedcounter=speed-1, so first frame triggers row
	mod3Counter := 0  // Player init clears to 0, first frame decrements then wraps to 2
	row := 0
	songIdx = 0

	for frame < totalFrames && row < numRows {
		// Check for song boundary - reset state
		if songIdx < len(songBoundaries)-1 && row == songBoundaries[songIdx+1] {
			speed = 6
			speedCounter = 5 // Reset like init
			mod3Counter = 0  // Reset like init
			songIdx++
			for ch := 0; ch < 3; ch++ {
				state[ch] = chanState{}
			}
		}

		// Update mod3 counter (player decrements at start of frame)
		mod3Counter--
		if mod3Counter < 0 {
			mod3Counter = 2
		}

		// Check if new row (player increments speedCounter, then checks >= speed)
		speedCounter++
		if speedCounter >= speed {
			speedCounter = 0

			// Read row data for all channels
			for ch := 0; ch < 3; ch++ {
				off := row * 3
				noteByte, instEff, param := streams[ch][off], streams[ch][off+1], streams[ch][off+2]
				note := int(noteByte & 0x7F)
				inst := int(instEff & 0x1F)
				effect := int((instEff >> 5) | ((noteByte >> 4) & 0x08))

				// Get transpose for this order
				order := row / 64
				trans := int8(0)
				if order < len(transpose[ch]) {
					trans = transpose[ch][order]
				}

				// Handle note
				if note > 0 && note < 0x61 {
					// New note - store note-1 as player does
					state[ch].note = note - 1

					// Set instrument and reset arp/vib indices
					if inst > 0 {
						state[ch].inst = inst
						if songIdx < len(songs) && len(songs[songIdx].instData) > inst*INST_SIZE+INST_VIBDEPSP {
							sd := songs[songIdx]
							state[ch].arpIdx = int(sd.instData[inst*INST_SIZE+INST_ARPSTART])
							state[ch].vibDelay = int(sd.instData[inst*INST_SIZE+INST_VIBDELAY])
							vibDepSp := sd.instData[inst*INST_SIZE+INST_VIBDEPSP]
							state[ch].vibDepth = int(vibDepSp >> 4)
							state[ch].vibSpeed = int(vibDepSp & 0x0F)
						}
					}

					// For portamento (new effect 2), set target but don't change current freq
					if effect == 2 {
						idx := state[ch].note + int(trans)
						if idx >= 0 && idx < len(freqTableLo) {
							state[ch].portaTarget = uint16(freqTableLo[idx]) | uint16(freqTableHi[idx])<<8
						}
					}

					state[ch].vibratoPos = 0
				} else if note == 0x61 {
					// Note off
					state[ch].freq = 0
					state[ch].noteFreq = 0
					state[ch].note = -1
				}

				// Update effect (tracker effect A=10 sets vibrato params)
				if effect > 0 {
					state[ch].effect = effect
					state[ch].param = param
					if effect == 10 { // Vibrato (new effect A = old effect 4)
						state[ch].vibDepth = int(param >> 4)
						state[ch].vibSpeed = int(param & 0x0F)
					}
				} else if param == 0 {
					state[ch].effect = 0
					state[ch].param = 0
				}

				// Handle speed change (effect C)
				if effect == 0x03 && param < 0x80 && param > 0 { // Effect 3 = Fxx speed
					speed = int(param)
				}
			}
			row++
		}

		// Process instruments and effects for each channel
		for ch := 0; ch < 3; ch++ {
			s := &state[ch]
			if s.note < 0 {
				continue
			}

			// Get transpose for current position
			order := (row - 1) / 64
			if row == 0 {
				order = 0
			}
			trans := int8(0)
			if order < len(transpose[ch]) {
				trans = transpose[ch][order]
			}

			// Process arpeggio table (unless portamento effect 2)
			if s.effect != 2 && songIdx < len(songs) {
				sd := songs[songIdx]
				if s.inst > 0 && len(sd.instData) > s.inst*INST_SIZE+INST_ARPLOOP && len(sd.arpTable) > s.arpIdx {
					// Read arp value
					arpVal := int(sd.arpTable[s.arpIdx])
					if arpVal >= 0x80 {
						// Absolute note (high bit set)
						arpVal = arpVal & 0x7F
					} else {
						// Relative offset
						arpVal = s.note + int(trans) + arpVal
					}

					// Look up frequency
					if arpVal >= 0 && arpVal < len(freqTableLo) {
						s.freq = uint16(freqTableLo[arpVal]) | uint16(freqTableHi[arpVal])<<8
						s.noteFreq = s.freq
					}

					// Advance arp index
					s.arpIdx++
					arpEnd := int(sd.instData[s.inst*INST_SIZE+INST_ARPEND])
					if s.arpIdx > arpEnd {
						s.arpIdx = int(sd.instData[s.inst*INST_SIZE+INST_ARPLOOP])
					}
				}
			}

			// Apply tracker effects (new effect numbers: 1=arp, 2=porta, 9=slideup)
			switch s.effect {
			case 9: // Slide (old effect 1 -> new 9), param: 0=up, 1=down
				if s.param == 0 {
					s.freq += 0x20
				} else if s.freq > 0x20 {
					s.freq -= 0x20
				}
			case 2: // Portamento (old effect 3 -> new effect 2)
				if s.portaTarget > s.freq {
					newFreq := s.freq + uint16(s.param)
					if newFreq > s.portaTarget {
						newFreq = s.portaTarget
					}
					s.freq = newFreq
				} else if s.portaTarget < s.freq {
					if uint16(s.param) > s.freq {
						s.freq = s.portaTarget
					} else {
						newFreq := s.freq - uint16(s.param)
						if newFreq < s.portaTarget {
							newFreq = s.portaTarget
						}
						s.freq = newFreq
					}
				}
			case 1: // Arpeggio (old effect A -> new effect 1)
				// Player order: mod3=0→arpY, mod3=1→arpX, mod3=2→base
				arpX := int(s.param >> 4)
				arpY := int(s.param & 0x0F)
				var arpOffset int
				switch mod3Counter {
				case 0:
					arpOffset = arpY
				case 1:
					arpOffset = arpX
				case 2:
					arpOffset = 0
				}
				idx := s.note + int(trans) + arpOffset
				if idx >= 0 && idx < len(freqTableLo) {
					s.freq = uint16(freqTableLo[idx]) | uint16(freqTableHi[idx])<<8
					s.noteFreq = s.freq
				}
			}

			// Apply instrument vibrato (after effects)
			if s.vibDelay > 0 {
				s.vibDelay--
			} else if s.vibDepth > 0 && s.vibSpeed > 0 {
				// Calculate vibrato offset
				pos := s.vibratoPos & 0x1F
				var offset int
				if pos < 0x10 {
					offset = pos * s.vibDepth
				} else {
					offset = (0x1F - pos) * s.vibDepth
				}
				if s.vibratoPos >= 0x20 {
					offset = -offset
				}
				// Apply to frequency
				s.freq = uint16(int(s.noteFreq) + offset)
				// Advance vibrato position
				s.vibratoPos = (s.vibratoPos + s.vibSpeed) & 0x3F
			}
		}

		// Record frequencies for this frame
		for ch := 0; ch < 3; ch++ {
			result[ch][frame] = state[ch].freq
		}
		frame++
	}

	return result
}

// runSideBySideValidation runs VM and simulation frame-by-frame, comparing as we go
// This helps identify exactly where and why divergence occurs
// SongTableData holds per-song table data for validation
type SongTableData struct {
	InstData    []byte
	WaveTable   []byte
	ArpTable    []byte
	FilterTable []byte
}

// Returns: vmRegs, simRegs, noteFreqs, isDrum, patternStartFrames
// If tableDatas is nil, loads tables from files; otherwise uses provided data
func runSideBySideValidation(streams [3][]byte, transpose [3][]int8, songBoundaries []int, orderBoundaries []int, tableDatas []SongTableData) (SIDRegisters, SIDRegisters, [3][]uint16, [3][]bool, []int) {
	// Run side-by-side VM vs Simulation validation (quiet mode)

	playerData, err := os.ReadFile("build/player.bin")
	if err != nil {
		fmt.Printf("Error: could not load player: %v\n", err)
		return SIDRegisters{}, SIDRegisters{}, [3][]uint16{}, [3][]bool{}, nil
	}

	var patternStartFrames []int // Frame numbers where each pattern starts

	// Load song data for instrument/wave/arp tables
	type songData struct {
		data        []byte
		instData    []byte
		waveTable   []byte
		arpTable    []byte
		filterTable []byte
		drumInst    [32]bool // Pre-classified drum instruments
	}

	classifyDrums := func(inst, wave, arp []byte) [32]bool {
		var drumInst [32]bool
		for i := 1; i < 32; i++ {
			off := i * INST_SIZE
			if off+INST_WAVEEND >= len(inst) {
				continue
			}
			arpStart := int(inst[off+INST_ARPSTART])
			arpEnd := int(inst[off+INST_ARPEND])
			waveStart := int(inst[off+INST_WAVESTART])
			waveEnd := int(inst[off+INST_WAVEEND])

			absCount := 0
			relCount := 0
			minAbsNote := 255
			maxAbsNote := 0
			for j := arpStart; j <= arpEnd && j < len(arp); j++ {
				if arp[j] >= 0x80 {
					absCount++
					noteIdx := int(arp[j] & 0x7F)
					if noteIdx < minAbsNote {
						minAbsNote = noteIdx
					}
					if noteIdx > maxAbsNote {
						maxAbsNote = noteIdx
					}
				} else {
					relCount++
				}
			}

			noiseCount := 0
			totalWave := 0
			for j := waveStart; j <= waveEnd && j < len(wave); j++ {
				totalWave++
				if wave[j]&0xF0 == 0x80 {
					noiseCount++
				}
			}

			if absCount > 0 && relCount == 0 {
				drumInst[i] = true
			}
			if totalWave > 0 && noiseCount == totalWave {
				drumInst[i] = true
			}
			if absCount > 0 && maxAbsNote-minAbsNote > 24 {
				drumInst[i] = true
			}
		}
		return drumInst
	}

	var songs []songData
	if tableDatas != nil {
		// Use provided table data
		for _, td := range tableDatas {
			inst := make([]byte, 32*INST_SIZE)
			copy(inst, td.InstData)
			wave := make([]byte, 256)
			copy(wave, td.WaveTable)
			arp := make([]byte, 256)
			copy(arp, td.ArpTable)
			filter := make([]byte, FILTERTABLE_SZ)
			copy(filter, td.FilterTable)
			drumInst := classifyDrums(inst, wave, arp)
			songs = append(songs, songData{nil, inst, wave, arp, filter, drumInst})
		}
	} else {
		// Load from files
		for song := 1; song <= 9; song++ {
			data, err := os.ReadFile(filepath.Join("generated", "parts", fmt.Sprintf("part%d.bin", song)))
			if err != nil {
				songs = append(songs, songData{})
				continue
			}
			inst := make([]byte, 32*INST_SIZE)
			if len(data) >= 32*INST_SIZE {
				copy(inst, data[:32*INST_SIZE])
			}
			wave := make([]byte, 256)
			if len(data) > WAVETABLE_OFF+256 {
				copy(wave, data[WAVETABLE_OFF:WAVETABLE_OFF+256])
			}
			arp := make([]byte, 256)
			if len(data) > ARPTABLE_OFF+256 {
				copy(arp, data[ARPTABLE_OFF:ARPTABLE_OFF+256])
			}
			filter := make([]byte, FILTERTABLE_SZ)
			if len(data) > FILTERTABLE_OFF+FILTERTABLE_SZ {
				copy(filter, data[FILTERTABLE_OFF:FILTERTABLE_OFF+FILTERTABLE_SZ])
			}
			drumInst := classifyDrums(inst, wave, arp)
			songs = append(songs, songData{data, inst, wave, arp, filter, drumInst})
		}
	}

	const playerBase = uint16(0xF000)

	var vmFreqs [3][]uint16
	var vmPWs [3][]uint16
	var vmControls [3][]byte
	var vmADs [3][]byte
	var vmSRs [3][]byte
	var vmFilterLos []byte
	var vmFilterHis []byte
	var vmFilterRess []byte
	var vmFilterVols []byte
	var simFreqs [3][]uint16
	var simControls [3][]byte
	var simADs [3][]byte
	var simSRs [3][]byte
	var noteFreqs [3][]uint16 // Base note frequencies without vibrato (for clean MIDI)
	var isDrum [3][]bool      // True when frame uses absolute arp note, sync, ring mod, or noise (drum-like)

	// SID envelope rates (frames per step) - approximation based on SID timing
	// Index is the 4-bit rate value from ADSR register
	envRates := []int{2, 4, 8, 12, 19, 28, 34, 40, 50, 125, 250, 500, 800, 1000, 3000, 5000}

	// Simulation state
	type chanState struct {
		freq        uint16
		finFreq     uint16 // final frequency with vibrato applied (like player's chn_finfreq)
		noteFreq    uint16
		note        int
		inst        int
		effect      int
		param       byte
		waveIdx     int
		arpIdx      int
		portaTarget uint16
		vibratoPos  int
		vibSpeed    int    // current frame's vibrato speed
		vibDepth    int    // current frame's vibrato depth (high nibble: 0x10=depth1, etc)
		vibDelay    int    // countdown before vibrato starts
		slideEnable bool   // whether slide is active
		slideDelta  int16  // slide delta per frame (signed 16-bit)
		gate        bool   // gate on/off
		envPhase    int    // 0=attack, 1=decay, 2=sustain, 3=release
		envLevel    int    // 0-255 envelope level
		envCounter  int    // frames until next envelope step
		ad          byte   // attack/decay from instrument
		sr          byte   // sustain/release from instrument
		portaFrames int    // consecutive frames in portamento
		portaStart  uint16 // frequency when portamento started
		waveform    byte   // current waveform register (set by effect 7 or wave table)
		gateOn      byte   // gate mask: $FF=gate on, $FE=gate off (like player's chn_gateon)
		hardRestart int    // hard restart offset (frames to look ahead, typically 2)
		// Pulse width
		plsWidthLo  byte
		plsWidthHi  byte
		plsSpeed    byte
		plsDir      byte // 0=up, $80=down
		plsLimitUp  byte
		plsLimitDn  byte
	}
	// Filter state (global, not per-channel)
	filterIdx := 0
	filterEnd := 0
	filterLoop := 0
	filterCutoff := byte(0)
	filterResonance := byte(0)
	filterMode := byte(0)
	globalVolume := byte(0x0F) // Default volume

	totalMismatches := 0
	totalFreqMismatches := 0
	totalControlMismatches := 0
	totalADMismatches := 0
	totalSRMismatches := 0
	totalPWMismatches := 0
	totalFilterMismatches := 0
	globalFrameOffset := 0

	for songIdx := 0; songIdx < 9; songIdx++ {
		if songIdx >= len(songs) || len(songs[songIdx].data) == 0 {
			continue
		}

		song := songIdx + 1
		numFrames := int(partTimes[songIdx])
		var bufferBase uint16
		if song%2 == 1 {
			bufferBase = 0x1000
		} else {
			bufferBase = 0x7000
		}

		// Initialize VM
		cpu := NewCPU()
		copy(cpu.Memory[bufferBase:], songs[songIdx].data)
		copy(cpu.Memory[playerBase:], playerData)
		cpu.A = 0
		cpu.X = byte(bufferBase >> 8)
		cpu.Call(playerBase)

		vmFreqLo := [3]byte{}
		vmFreqHi := [3]byte{}
		vmPwLo := [3]byte{}
		vmPwHi := [3]byte{}
		vmControl := [3]byte{}
		vmAD := [3]byte{}
		vmSR := [3]byte{}
		vmFilterLo := byte(0)
		vmFilterHi := byte(0)
		vmFilterRes := byte(0)
		vmFilterMode := byte(0)

		// Initialize simulation state (pure simulation - no VM reads)
		// Player init clears ALL player variables to 0 (@clear loop), then:
		// - sets hardrestart=2 for all channels
		// - sets chn_decoded_pat = $FF,$FF,$FF (not needed for simulation)
		// All other values (gateon, waveform, AD, SR, etc.) start at 0
		var simState [3]chanState
		for ch := 0; ch < 3; ch++ {
			simState[ch].hardRestart = 2 // Player inits this to 2
			// All other fields start at 0 (gateOn=0, waveform=0, ad=0, sr=0, etc.)
			// This matches the player's @clear loop which sets everything to 0
		}
		// Reset global filter state for new song (player's @clear loop sets these to 0)
		filterIdx = 0
		filterEnd = 0
		filterLoop = 0
		filterCutoff = 0
		filterResonance = 0
		filterMode = 0
		globalVolume = 0x0F // Player doesn't reset volume in @clear, but we use default
		speed := 6
		speedCounter := 5
		mod3Counter := 0
		row := songBoundaries[songIdx]
		endRow := songBoundaries[songIdx+1]
		ordernumber := 0        // song-local order, like VM's ordernumber
		nextordernumber := 0    // next order (advanced when pattern changes)
		trackRow := -1          // -1 so first inc brings to 0 (VM skips inc on first frame due to forcenewpattern)
		forcenewpattern := true // true on first frame, like VM's forcenewpattern=$80
		firsttrackrow := 0      // starting row for next pattern (set by pattern break)

		songMismatches := 0
		songFirstMismatch := false

		// Record song start as pattern start
		patternStartFrames = append(patternStartFrames, globalFrameOffset)

		for frame := 0; frame < numFrames; frame++ {

			// === Run VM frame ===
			cpu.SIDWrites = nil
			cpu.CurrentFrame = frame
			cpu.Call(playerBase + 3)



			for _, w := range cpu.SIDWrites {
				switch w.Addr {
				// Voice 1
				case 0xD400:
					vmFreqLo[0] = w.Value
				case 0xD401:
					vmFreqHi[0] = w.Value
				case 0xD402:
					vmPwLo[0] = w.Value
				case 0xD403:
					vmPwHi[0] = w.Value
				case 0xD404:
					vmControl[0] = w.Value
				case 0xD405:
					vmAD[0] = w.Value
				case 0xD406:
					vmSR[0] = w.Value
				// Voice 2
				case 0xD407:
					vmFreqLo[1] = w.Value
				case 0xD408:
					vmFreqHi[1] = w.Value
				case 0xD409:
					vmPwLo[1] = w.Value
				case 0xD40A:
					vmPwHi[1] = w.Value
				case 0xD40B:
					vmControl[1] = w.Value
				case 0xD40C:
					vmAD[1] = w.Value
				case 0xD40D:
					vmSR[1] = w.Value
				// Voice 3
				case 0xD40E:
					vmFreqLo[2] = w.Value
				case 0xD40F:
					vmFreqHi[2] = w.Value
				case 0xD410:
					vmPwLo[2] = w.Value
				case 0xD411:
					vmPwHi[2] = w.Value
				case 0xD412:
					vmControl[2] = w.Value
				case 0xD413:
					vmAD[2] = w.Value
				case 0xD414:
					vmSR[2] = w.Value
				// Filter
				case 0xD415:
					vmFilterLo = w.Value
				case 0xD416:
					vmFilterHi = w.Value
				case 0xD417:
					vmFilterRes = w.Value
				case 0xD418:
					vmFilterMode = w.Value
				}
			}

			vmFreq := [3]uint16{}
			for ch := 0; ch < 3; ch++ {
				vmFreq[ch] = uint16(vmFreqLo[ch]) | uint16(vmFreqHi[ch])<<8
				vmFreqs[ch] = append(vmFreqs[ch], vmFreq[ch])
				vmPWs[ch] = append(vmPWs[ch], uint16(vmPwLo[ch])|uint16(vmPwHi[ch])<<8)
				vmControls[ch] = append(vmControls[ch], vmControl[ch])
				vmADs[ch] = append(vmADs[ch], vmAD[ch])
				vmSRs[ch] = append(vmSRs[ch], vmSR[ch])
			}
			vmFilterLos = append(vmFilterLos, vmFilterLo)
			vmFilterHis = append(vmFilterHis, vmFilterHi)
			vmFilterRess = append(vmFilterRess, vmFilterRes)
			vmFilterVols = append(vmFilterVols, vmFilterMode)

			// === Run simulation frame ===
			// Player order: 1) inc speedcounter/fetch row 2) process channels 3) HR check 4) SID dump

			// 1) Increment speedCounter and process row if needed
			mod3Counter--
			if mod3Counter < 0 {
				mod3Counter = 2
			}

			speedCounter++
			if speedCounter >= speed && row < endRow {
				speedCounter = 0
				// Handle pattern boundary
				prevTrackRow := trackRow
				if forcenewpattern {
					ordernumber = nextordernumber
					nextordernumber++
					trackRow = firsttrackrow
					firsttrackrow = 0
					forcenewpattern = false
				} else {
					trackRow++
					if trackRow >= 64 {
						ordernumber = nextordernumber
						nextordernumber++
						trackRow = 0
					}
				}

				// Record pattern start frames (when trackRow becomes 0, excluding first row of song)
				if trackRow == 0 && prevTrackRow != 0 && frame > 0 {
					patternStartFrames = append(patternStartFrames, globalFrameOffset+frame)
				}

				// Compute global row
				row = songBoundaries[songIdx] + ordernumber*64 + trackRow

				for ch := 0; ch < 3; ch++ {
					off := row * 3
					noteByte, instEff, param := streams[ch][off], streams[ch][off+1], streams[ch][off+2]
					note := int(noteByte & 0x7F)
					inst := int(instEff & 0x1F)
					effect := int((instEff >> 5) | ((noteByte >> 4) & 0x08))

				// Use song-local ordernumber for transpose lookup
					transIdx := orderBoundaries[songIdx] + ordernumber
					trans := int8(0)
					if transIdx < len(transpose[ch]) {
						trans = transpose[ch][transIdx]
					}

					// Player ALWAYS stores effect (even if 0) - see lines 600-616
					simState[ch].effect = effect
					simState[ch].param = param

					// Player processes inst BEFORE note (see fetch_channel_row)
					// vibpos is only reset when inst changes, not for every note
					if inst > 0 {
						simState[ch].inst = inst
						// Reset waveIdx, arpIdx, vibDelay, vibPos, pulse, filter when inst changes
						sd := songs[songIdx]
						if len(sd.instData) > inst*INST_SIZE+INST_FILTLOOP {
							simState[ch].waveIdx = int(sd.instData[inst*INST_SIZE+INST_WAVESTART])
							simState[ch].arpIdx = int(sd.instData[inst*INST_SIZE+INST_ARPSTART])
							simState[ch].vibDelay = int(sd.instData[inst*INST_SIZE+INST_VIBDELAY])
							simState[ch].ad = sd.instData[inst*INST_SIZE+INST_AD]
							simState[ch].sr = sd.instData[inst*INST_SIZE+INST_SR]
							// Pulse width params - unpack like player does
							pw := sd.instData[inst*INST_SIZE+INST_PULSEWIDTH]
							simState[ch].plsWidthLo = pw << 4       // low nibble to high nibble of lo byte
							simState[ch].plsWidthHi = pw >> 4       // high nibble to low nibble of hi byte
							simState[ch].plsSpeed = sd.instData[inst*INST_SIZE+INST_PULSESPEED]
							limits := sd.instData[inst*INST_SIZE+INST_PULSELIMITS]
							simState[ch].plsLimitDn = limits >> 4   // high nibble is down limit
							simState[ch].plsLimitUp = limits & 0x0F // low nibble is up limit
							simState[ch].plsDir = 0                 // Start going up
							// Note: Filter params are NOT loaded from inst - only via FEx effect
						}
						simState[ch].vibratoPos = 0
					}

					if note > 0 && note < 0x61 {
						simState[ch].note = note - 1
						// For portamento (new effect 2): only set notefreq as target (not freq)
						// This matches player's set_notefreq_only
						if simState[ch].effect == 2 {
							idx := simState[ch].note + int(trans)
							if idx >= 0 && idx < len(freqTableLo) {
								simState[ch].noteFreq = uint16(freqTableLo[idx]) | uint16(freqTableHi[idx])<<8
							}
						}
						// Reset waveIdx, arpIdx, slide when note is triggered (player does this in @notporta)
						// Player resets these on EVERY note, not just when inst changes
						sd := songs[songIdx]
						currentInst := simState[ch].inst
						if currentInst > 0 && len(sd.instData) > currentInst*INST_SIZE+INST_ARPSTART {
							simState[ch].waveIdx = int(sd.instData[currentInst*INST_SIZE+INST_WAVESTART])
							simState[ch].arpIdx = int(sd.instData[currentInst*INST_SIZE+INST_ARPSTART])
						}
						simState[ch].slideDelta = 0
						simState[ch].slideEnable = false
						// Trigger envelope attack
						simState[ch].gate = true
						simState[ch].gateOn = 0xFF // Gate on (player sets chn_gateon=$FF on note-on)
						simState[ch].envPhase = 0  // attack
						simState[ch].envCounter = 0
					}
					// Note-off ($61) triggers release phase
					if note == 0x61 {
						simState[ch].gate = false
						simState[ch].gateOn = 0xFE // Gate off (player sets chn_gateon=$FE on note-off)
						simState[ch].envPhase = 3  // release
					}

					if effect == 0x03 && param < 0x80 && param > 0 { // Effect 3 = Fxx speed
						speed = int(param)
					}
					// Effect B = pattern break (from old effect D)
					// Sets firsttrackrow and forcenewpattern=$80
					if effect == 0x0B {
						firsttrackrow = int(param)
						forcenewpattern = true
					}
					// Effect C = position jump
					// Sets nextordernumber and forcenewpattern=$80
					if effect == 0x0C {
						nextordernumber = int(param)
						forcenewpattern = true
					}
				}
			}

			// 4) Process channel effects (wave table, arp, vibrato, etc.)
			drumFrame := [3]bool{} // Track if this frame is a drum hit (includes sync/ring mod)
			for ch := 0; ch < 3; ch++ {
				s := &simState[ch]
				sd := songs[songIdx]

				// Check if instrument is pre-classified as drum
				if s.inst > 0 && s.inst < 32 && sd.drumInst[s.inst] {
					drumFrame[ch] = true
				}

				// Track portamento - check target vs start to detect extreme slides from the beginning
				if s.effect == 2 { // Portamento is new effect 2
					if s.portaFrames == 0 {
						s.portaStart = s.freq // Record starting frequency
					}
					s.portaFrames++
					// Mark as drum if portamento TARGET spans > 2 octaves from start
					// This catches extreme slides from the first frame
					if s.portaStart > 0 && s.noteFreq > 0 {
						ratio := float64(s.noteFreq) / float64(s.portaStart)
						if ratio > 4.0 || ratio < 0.25 { // > 2 octaves between start and target
							drumFrame[ch] = true
						}
					}
				} else {
					s.portaFrames = 0
					s.portaStart = 0
				}

				// Clear vibDepth at start of each channel (like player's @chnloop)
				s.vibDepth = 0

				// Use song-local ordernumber for transpose lookup
				transIdx := orderBoundaries[songIdx] + ordernumber
				trans := int8(0)
				if transIdx < len(transpose[ch]) {
					trans = transpose[ch][transIdx]
				}

				// Process wave table - updates s.waveform (unless effect 5 set waveform overrides it)
				if s.effect != 5 && s.inst > 0 && len(sd.instData) > s.inst*INST_SIZE+INST_WAVELOOP && len(sd.waveTable) > s.waveIdx {
					s.waveform = sd.waveTable[s.waveIdx]
					s.waveIdx++
					waveEnd := int(sd.instData[s.inst*INST_SIZE+INST_WAVEEND])
					if s.waveIdx > waveEnd {
						s.waveIdx = int(sd.instData[s.inst*INST_SIZE+INST_WAVELOOP])
					}
				}

				// Check waveform for sync/ring/noise (can be set by wave table or effect 7)
				// All these are drum-like sounds in MIDI
				if s.waveform&0xF0 == 0x80 {
					drumFrame[ch] = true // Noise waveform = drum
				}
				if s.waveform&0x02 != 0 {
					drumFrame[ch] = true // Hard sync = drum
				}
				if s.waveform&0x04 != 0 {
					drumFrame[ch] = true // Ring modulation = drum
				}

				// Process instrument (arpeggio and vibrato setup) - skip arp if portamento (effect 2)
				if s.effect != 2 {
					if s.inst > 0 && len(sd.instData) > s.inst*INST_SIZE+INST_ARPLOOP && len(sd.arpTable) > s.arpIdx {
						arpVal := int(sd.arpTable[s.arpIdx])
						if arpVal >= 0x80 {
							arpVal = arpVal & 0x7F
							drumFrame[ch] = true // Absolute note = drum
						} else {
							arpVal = s.note + int(trans) + arpVal
						}
						// Note table edges (0-2, >=95) are typically drums/effects
						if arpVal <= 2 || arpVal >= 95 {
							drumFrame[ch] = true
						}
						if arpVal >= 0 && arpVal < len(freqTableLo) {
							s.freq = uint16(freqTableLo[arpVal]) | uint16(freqTableHi[arpVal])<<8
							s.noteFreq = s.freq
						}
						s.arpIdx++
						arpEnd := int(sd.instData[s.inst*INST_SIZE+INST_ARPEND])
						if s.arpIdx > arpEnd {
							s.arpIdx = int(sd.instData[s.inst*INST_SIZE+INST_ARPLOOP])
						}
					}
				}

				// Process instrument vibrato (like player's process_instrument)
				if s.inst > 0 && len(sd.instData) > s.inst*INST_SIZE+INST_VIBDEPSP {
					if s.vibDelay > 0 {
						s.vibDelay--
					} else {
						vibDepSp := sd.instData[s.inst*INST_SIZE+INST_VIBDEPSP]
						s.vibDepth = int(vibDepSp & 0xF0) // high nibble as-is (depth * 16)
						if s.vibDepth != 0 {
							s.vibSpeed = int(vibDepSp & 0x0F)
						}
					}
				}

				// Process pulse width (like player's pi_pulse)
				if s.plsSpeed != 0 {
					if s.plsDir != 0 {
						// Going down
						newLo := int(s.plsWidthLo) - int(s.plsSpeed)
						if newLo < 0 {
							s.plsWidthLo = byte(newLo + 256)
							s.plsWidthHi--
						} else {
							s.plsWidthLo = byte(newLo)
						}
						if int8(s.plsWidthHi) < int8(s.plsLimitDn) {
							s.plsDir = 0
							s.plsWidthLo = 0
							s.plsWidthHi = s.plsLimitDn
						}
					} else {
						// Going up
						newLo := int(s.plsWidthLo) + int(s.plsSpeed)
						if newLo > 255 {
							s.plsWidthLo = byte(newLo - 256)
							s.plsWidthHi++
						} else {
							s.plsWidthLo = byte(newLo)
						}
						if int8(s.plsWidthHi) > int8(s.plsLimitUp) {
							s.plsDir = 0x80
							s.plsWidthLo = 0xFF
							s.plsWidthHi = s.plsLimitUp
						}
					}
				}

				// Apply effects (frequency-sorted: 1=arp, 2=porta, 3=Fxx, 4=SR, 5=wave, 6=down, 7=AD, 8=reso, 9=up, A=vib)
				switch s.effect {
				case 9: // Slide (old effect 1 -> new 9), param: 0=up, 1=down
					s.slideEnable = true
					if s.param == 0 {
						s.slideDelta += 0x20
					} else {
						s.slideDelta -= 0x20
					}
				case 6: // Pulse width (old effect 2 -> new 6)
					var pwVal byte
					if s.param != 0 {
						pwVal = 0x80
					}
					s.plsWidthLo = (pwVal << 4) & 0xFF
					s.plsWidthHi = pwVal >> 4
				case 2: // Portamento (old effect 3 -> new 2)
					// Player uses chn_notefreq as target, which is set by:
					// - set_notefreq_only when new note with effect=2
					// - arp table lookup when effect != 2
					deltaLo := (s.param << 4) & 0xFF
					deltaHi := s.param >> 4
					delta := uint16(deltaHi)<<8 | uint16(deltaLo)
					if s.noteFreq > s.freq {
						newFreq := s.freq + delta
						if newFreq > s.noteFreq {
							newFreq = s.noteFreq
						}
						s.freq = newFreq
					} else if s.noteFreq < s.freq {
						if delta > s.freq {
							s.freq = s.noteFreq
						} else {
							newFreq := s.freq - delta
							if newFreq < s.noteFreq {
								newFreq = s.noteFreq
							}
							s.freq = newFreq
						}
					}
				case 10: // Vibrato (old effect 4 -> new A)
					s.vibDepth = int(s.param & 0xF0) // high nibble as-is
					s.vibSpeed = int(s.param & 0x0F)
				case 7: // Set AD (old effect 7 -> new 7)
					s.ad = unmapParam(s.param, adUnmap)
				case 4: // Set SR (old effect 8 -> new 4)
					s.sr = unmapParam(s.param, srUnmap)
				case 5: // Set waveform (old effect 9 -> new 5)
					s.waveform = unmapParam(s.param, waveUnmap)
				case 1: // Arpeggio (old effect A -> new 1)
					arpX := int(s.param >> 4)
					arpY := int(s.param & 0x0F)
					var arpOffset int
					switch mod3Counter {
					case 0:
						arpOffset = arpY
					case 1:
						arpOffset = arpX
					case 2:
						arpOffset = 0
					}
					idx := s.note + int(trans) + arpOffset
					if idx >= 0 && idx < len(freqTableLo) {
						s.freq = uint16(freqTableLo[idx]) | uint16(freqTableHi[idx])<<8
						s.noteFreq = s.freq
					}
				case 8: // Filter resonance (old effect E -> new 8)
					filterResonance = unmapParam(s.param, resoUnmap)
				case 3: // Extended Fxx (old effect F -> new 3)
					// High nibbles frequency-sorted: $8=hardrestart, $9=filttrig, $A=globalvol, $B=filtmode, $C=fineslide
					if speedCounter == 0 && s.param >= 0x80 {
						highNibble := s.param & 0xF0
						lowNibble := s.param & 0x0F
						if highNibble == 0x80 {
							// $8x: Set hard restart offset (old $Fx)
							s.hardRestart = int(lowNibble)
						}
						if highNibble == 0x90 && lowNibble != 0 {
							// $9x: Load filter settings from instrument x (old $Ex)
							inst := int(lowNibble)
							if inst > 0 && inst*INST_SIZE+INST_FILTLOOP < len(sd.instData) {
								filterIdx = int(sd.instData[inst*INST_SIZE+INST_FILTSTART])
								filterEnd = int(sd.instData[inst*INST_SIZE+INST_FILTEND])
								filterLoop = int(sd.instData[inst*INST_SIZE+INST_FILTLOOP])
							}
						}
						if highNibble == 0xA0 {
							// $Ax: Set global volume (old $8x)
							globalVolume = byte(lowNibble)
						}
						if highNibble == 0xB0 {
							// $Bx: Set filter mode (old $9x)
							filterMode = byte(lowNibble << 4)
						}
						if highNibble == 0xC0 {
							// $Cx: Fine slide (old $Bx) - add lowNibble*4 to slideDelta, enable slide
							s.slideDelta += int16(lowNibble) * 4
							s.slideEnable = true
						}
					}
				}

				// Apply slide (if enabled)
				if s.slideEnable {
					newFreq := int(s.freq) + int(s.slideDelta)
					if newFreq < 0 {
						newFreq = 0
					} else if newFreq > 0xFFFF {
						newFreq = 0xFFFF
					}
					s.freq = uint16(newFreq)
				}

				// Compute final frequency (vibrato applied to base freq, like player's calcvibrato)
				// Player stores result in chn_finfreq, not chn_freq - vibrato is applied fresh each frame
				s.finFreq = s.freq
				if s.vibDepth > 0 {
					// Calculate table position (0-15, mirrored for 16-31)
					pos := s.vibratoPos & 0x1F
					if pos >= 0x10 {
						pos = 0x1F ^ pos // mirror: 31-pos
					}
					// Table index = pos | depth (depth is already in high nibble)
					tableIdx := pos | s.vibDepth
					if tableIdx < len(vibratoTable) {
						// Offset is table value * 2 (16-bit)
						offset := int(vibratoTable[tableIdx]) * 2
						// Phase 0-31: subtract, 32-63: add
						if s.vibratoPos&0x20 != 0 {
							s.finFreq = uint16(int(s.freq) + offset)
						} else {
							s.finFreq = uint16(int(s.freq) - offset)
						}
					}
					// Advance vibrato position (byte wraps at 256, like player)
					s.vibratoPos = (s.vibratoPos + s.vibSpeed) & 0xFF
				}

				// Process ADSR envelope (only when gate has been triggered)
				if s.gate || s.envLevel > 0 {
					s.envCounter--
					if s.envCounter <= 0 {
						attack := int(s.ad >> 4)
						decay := int(s.ad & 0x0F)
						sustain := int(s.sr>>4) * 17 // sustain is 0-15, scale to 0-255
						release := int(s.sr & 0x0F)

						switch s.envPhase {
						case 0: // Attack
							s.envLevel += 4 // Fast attack approximation
							if s.envLevel >= 255 {
								s.envLevel = 255
								s.envPhase = 1 // decay
							}
							s.envCounter = envRates[attack] / 16
						case 1: // Decay
							s.envLevel--
							if s.envLevel <= sustain {
								s.envLevel = sustain
								s.envPhase = 2 // sustain
							}
							s.envCounter = envRates[decay] / 8
						case 2: // Sustain
							s.envLevel = sustain
							s.envCounter = 10 // just maintain
						case 3: // Release
							s.envLevel--
							if s.envLevel <= 0 {
								s.envLevel = 0
							}
							s.envCounter = envRates[release] / 8
						}
						if s.envCounter < 1 {
							s.envCounter = 1
						}
					}
				}

				// For MIDI output: portamento as two notes (start/end), switch to target after 25%
				outputFreq := s.noteFreq
				if s.effect == 2 && s.portaStart > 0 && s.noteFreq > 0 { // Portamento is new effect 2
					// Switch point at 25% of the way from start to target
					switchFreq := int(s.portaStart) + (int(s.noteFreq)-int(s.portaStart))/4
					if (s.portaStart < s.noteFreq && int(s.freq) < switchFreq) ||
						(s.portaStart > s.noteFreq && int(s.freq) > switchFreq) {
						outputFreq = s.portaStart
					} else {
						outputFreq = s.noteFreq
					}
				}
				if s.envLevel == 0 {
					outputFreq = 0
				}

				noteFreqs[ch] = append(noteFreqs[ch], outputFreq) // Base note for MIDI (0 when silent)
				isDrum[ch] = append(isDrum[ch], drumFrame[ch])
			}

			// Process filter table (like player's filter processing after @chnloop)
			if filterIdx != 0 {
				sd := songs[songIdx]
				if filterIdx < len(sd.filterTable) {
					filterCutoff = sd.filterTable[filterIdx]
				}
				filterIdx++
				if filterIdx > filterEnd {
					filterIdx = filterLoop
				}
			}

			// 3) Hard restart check (runs AFTER processing, BEFORE dump)
			// Player looks ahead using hrtrackrow which is trackrow + hardrestart relative to speedcounter
			for ch := 0; ch < 3; ch++ {
				s := &simState[ch]
				// Check if we should look ahead for hard restart
				// Uses current speedCounter (after increment, reset if row processed)
				if speedCounter+s.hardRestart >= speed {
					// Calculate look-ahead row (must match player's logic exactly)
					// Player: if forcenewpattern OR (trackrow+1)&0x3F==0, use firsttrackrow/nextordernumber
					var hrTrackRow, hrOrder int
					if forcenewpattern || (trackRow+1)&0x3F == 0 {
						hrTrackRow = firsttrackrow
						hrOrder = nextordernumber
					} else {
						hrTrackRow = trackRow + 1
						hrOrder = ordernumber
					}
					hrRow := songBoundaries[songIdx] + hrOrder*64 + hrTrackRow
					if hrRow >= songBoundaries[songIdx] && hrRow < endRow {
						off := hrRow * 3
						if off+2 < len(streams[ch]) {
							noteByte := streams[ch][off]
							instEff := streams[ch][off+1]
							note := noteByte & 0x7F
							effect := int((instEff >> 5) | ((noteByte >> 4) & 0x08))
							// Hard restart triggers if: note exists (not 0, not $61) AND not portamento
							if note > 0 && note != 0x61 {
								doHR := false
								if noteByte&0x80 != 0 {
									// Note has bit 7 set - always hard restart
									doHR = true
								} else if effect != 2 {
									// Not portamento (new effect 2) - hard restart
									instEffByte := instEff & 0xE0
									if instEffByte != 0x40 { // $40 = effect 2 << 5
										doHR = true
									}
								}
								if doHR {
									s.waveform = 0
									s.ad = 0
									s.sr = 0
								}
							}
						}
					}
				}
			}

			// 4) Compare simulation vs VM (SID dump point)
			for ch := 0; ch < 3; ch++ {
				s := &simState[ch]
				simControl := s.waveform & s.gateOn
				simF := s.finFreq

				// Frequency comparison
				if simF != vmFreq[ch] {
					songMismatches++
					totalMismatches++
					totalFreqMismatches++
					if !songFirstMismatch {
						songFirstMismatch = true
						diff := int(simF) - int(vmFreq[ch])
						if diff < 0 {
							diff = -diff
						}
						_ = diff
					}
				}

				// Register comparisons (count only, no verbose output)
				if simControl != vmControl[ch] {
					totalControlMismatches++
				}
				if s.ad != vmAD[ch] {
					totalADMismatches++
				}
				if s.sr != vmSR[ch] {
					totalSRMismatches++
				}
				simPW := uint16(s.plsWidthLo) | uint16(s.plsWidthHi)<<8
				vmPW := uint16(vmPwLo[ch]) | uint16(vmPwHi[ch])<<8
				if simPW != vmPW {
					totalPWMismatches++
				}

				// Store for output arrays
				simFreqs[ch] = append(simFreqs[ch], simF)
				simControls[ch] = append(simControls[ch], simControl)
				simADs[ch] = append(simADs[ch], s.ad)
				simSRs[ch] = append(simSRs[ch], s.sr)
			}

			// Filter register comparison (global, not per-channel)
			// D415 = filter cutoff low (bits 0-2 only)
			// D416 = filter cutoff high
			// D417 = filter resonance | routing
			// D418 = volume | filter mode
			simFilterCutoffLo := byte(0) // We don't track low 3 bits
			simFilterCutoffHi := filterCutoff
			simFilterRes := filterResonance
			simFilterVol := globalVolume | filterMode

			// Filter register comparisons (count only)
			if simFilterCutoffLo != vmFilterLo {
				totalFilterMismatches++
			}
			if simFilterCutoffHi != vmFilterHi {
				totalFilterMismatches++
			}
			if simFilterRes != vmFilterRes {
				totalFilterMismatches++
			}
			if simFilterVol != vmFilterMode {
				totalFilterMismatches++
			}
		}

		globalFrameOffset += numFrames
	}

	// Summarize validation (removed verbose per-song output)

	vmRegs := SIDRegisters{
		Freq:      vmFreqs,
		PW:        vmPWs,
		Control:   vmControls,
		AD:        vmADs,
		SR:        vmSRs,
		FilterLo:  vmFilterLos,
		FilterHi:  vmFilterHis,
		FilterRes: vmFilterRess,
		FilterVol: vmFilterVols,
	}
	simRegs := SIDRegisters{
		Freq:    simFreqs,
		Control: simControls,
		AD:      simADs,
		SR:      simSRs,
	}
	return vmRegs, simRegs, noteFreqs, isDrum, patternStartFrames
}

// SIDRegisters holds all SID register values per frame
type SIDRegisters struct {
	Freq        [3][]uint16
	PW          [3][]uint16 // Pulse Width per channel
	Control     [3][]byte
	AD          [3][]byte
	SR          [3][]byte
	FilterLo    []byte // Filter cutoff low (D415)
	FilterHi    []byte // Filter cutoff high (D416)
	FilterRes   []byte // Filter resonance/routing (D417)
	FilterVol   []byte // Volume/filter mode (D418)
}

// runStreamOnlySimulation runs simulation using ONLY the int streams
// Returns SID registers for all frames across all songs
// idxToNote is the freq-sorted lookup table for converting note indices back to note values
func runStreamOnlySimulation(streams []intStream, idxToNote []int) SIDRegisters {
	var result SIDRegisters

	// Extract data from streams - SPARSE LAYOUT WITH TRANSPOSE:
	// Stream layout:
	// streams[0-5]: Ch0-2 NoteVal, NoteDur (2 per channel = 6)
	//   NoteVal: combined (note<<5)|inst - inst stored with notes
	// streams[6-11]: Ch0-2 TransV, TransD (2 per channel = 6)
	// streams[12-17]: Ch0-2 FxV, FxD (2 per channel = 6)
	//   FxV: non-zero effects only, FxD: interleaved [gap, dur, ...]
	// streams[18]: TblData

	// Get numRows by summing FxD (interleaved gap+dur sums to total rows)
	numRows := 0
	for _, d := range streams[13].data { // Ch0 FxD (gap+dur interleaved)
		numRows += d
	}

	// Decode sparse notes+inst to row-based format
	// NoteVal now contains combined (note<<5)|inst
	var decodedNotes [3][]int
	var decodedInstFromNote [3][]int // Inst extracted from combined value
	for ch := 0; ch < 3; ch++ {
		noteVals := streams[ch*2].data   // NoteVal at 0,2,4 (combined note+inst)
		noteDurs := streams[ch*2+1].data // NoteDur at 1,3,5
		decodedN := make([]int, numRows)
		decodedI := make([]int, numRows) // Default to 0

		row := 0
		for i := 0; i < len(noteVals) && row < numRows; i++ {
			dur := 0
			if i < len(noteDurs) {
				dur = noteDurs[i]
			}
			row += dur
			if row < numRows && noteVals[i] != 0 {
				combined := noteVals[i]
				note := combined >> 5
				inst := combined & 0x1F
				decodedN[row] = note
				decodedI[row] = inst
				row++
			}
		}
		decodedNotes[ch] = decodedN
		decodedInstFromNote[ch] = decodedI
	}

	// Decode sparse Fx streams back to per-row format
	// FxV/FxD are at indices 12+ch*2 and 12+ch*2+1
	// FxV = non-zero (effect<<8)|param values only
	// FxD = interleaved [gap, duration, gap, duration, ...]
	var decodedFx [3][]int
	for ch := 0; ch < 3; ch++ {
		fxVal := streams[12+ch*2].data   // FxV at 12,14,16
		fxDur := streams[12+ch*2+1].data // FxD at 13,15,17
		decoded := make([]int, numRows)  // Default all to 0
		row := 0
		for i := 0; i < len(fxVal); i++ {
			gap := fxDur[i*2]   // Gap before this effect
			dur := fxDur[i*2+1] // Duration of this effect
			row += gap          // Skip gap rows (effect=0)
			for j := 0; j < dur && row < numRows; j++ {
				decoded[row] = fxVal[i]
				row++
			}
		}
		decodedFx[ch] = decoded
	}

	// Inst is now decoded from combined NoteVal (no separate InstV/InstD streams)
	decodedInst := decodedInstFromNote

	// Decode sparse transpose streams back to per-order format
	// Streams: 6-7=Ch0 TransV,TransD, 8-9=Ch1, 10-11=Ch2
	// TransVal[i] = zigzag-encoded value, TransDur[i] = how many orders it lasts
	var transposeStream [3][]int
	for ch := 0; ch < 3; ch++ {
		transVal := streams[6+ch*2].data   // TransV at 6,8,10
		transDur := streams[6+ch*2+1].data // TransD at 7,9,11
		var decoded []int
		for i := 0; i < len(transVal); i++ {
			// Decode zigzag: 0->0, 1->-1, 2->1, 3->-2, 4->2, ...
			zigzag := transVal[i]
			val := zigzag / 2
			if zigzag%2 == 1 {
				val = -(zigzag + 1) / 2
			}
			dur := transDur[i]
			for j := 0; j < dur; j++ {
				decoded = append(decoded, val)
			}
		}
		transposeStream[ch] = decoded
	}

	// Convert to byte arrays for pattern data
	var patternData [3][]byte
	for ch := 0; ch < 3; ch++ {
		patternData[ch] = make([]byte, numRows*3)
		for row := 0; row < numRows; row++ {
			// Convert note index back to note value using lookup table
			noteIdx := decodedNotes[ch][row]
			note := 0
			if noteIdx > 0 && noteIdx < len(idxToNote) {
				note = idxToNote[noteIdx]
			}
			inst := decodedInst[ch][row]
			fxParam := decodedFx[ch][row]
			effect := (fxParam >> 8) & 0xF
			param := fxParam & 0xFF
			patternData[ch][row*3] = byte(note&0x7F) | byte((effect&0x8)<<4)
			patternData[ch][row*3+1] = byte(inst&0x1F) | byte((effect&0x7)<<5)
			patternData[ch][row*3+2] = byte(param)
		}
	}

	// Decode merged incremental table stream for slot-based loading
	// streams[18]: TblData (merged format: delta, slot, instDef[16], waveLen, waveData..., arpLen, arpData..., filtLen, filtData...)
	// Special: delta=65535 is a song reset marker (resets all state, no slot/data follows)
	const songResetMarker = 65535
	tblData := streams[18].data

	// Build load events from merged stream
	type loadEvent struct {
		row        int
		slot       int
		isReset    bool // true = song reset marker, ignore slot/data
		resetFrame int  // frame at which to trigger reset (only if isReset)
		instDef    []byte
		waveData   []byte
		arpData    []byte
		filtData   []byte
	}
	var loadEvents []loadEvent
	currentRow := 0
	off := 0
	for off < len(tblData) {
		// Delta (or reset marker)
		delta := tblData[off]
		off++
		if delta == songResetMarker {
			// Song reset marker followed by: frameCount, rowBase
			resetFrame := tblData[off]
			off++
			resetRow := tblData[off]
			off++
			loadEvents = append(loadEvents, loadEvent{row: resetRow, isReset: true, resetFrame: resetFrame})
			currentRow = resetRow // Set row base to reset point
			continue
		}
		currentRow += delta
		// Slot
		slot := tblData[off]
		off++
		// InstDef (16 bytes)
		instDef := make([]byte, 16)
		for j := 0; j < 16; j++ {
			instDef[j] = byte(tblData[off+j])
		}
		off += 16
		// Wave: length then data
		wvLen := tblData[off]
		off++
		waveData := make([]byte, wvLen)
		for j := 0; j < wvLen; j++ {
			waveData[j] = byte(tblData[off+j])
		}
		off += wvLen
		// Arp: length then data
		arLen := tblData[off]
		off++
		arpData := make([]byte, arLen)
		for j := 0; j < arLen; j++ {
			arpData[j] = byte(tblData[off+j])
		}
		off += arLen
		// Filter: length then data
		fiLen := tblData[off]
		off++
		filtData := make([]byte, fiLen)
		for j := 0; j < fiLen; j++ {
			filtData[j] = byte(tblData[off+j])
		}
		off += fiLen
		loadEvents = append(loadEvents, loadEvent{row: currentRow, slot: slot, instDef: instDef, waveData: waveData, arpData: arpData, filtData: filtData})
	}

	// Slot tables - each slot has its own instDef, wave, arp, filter data
	type slotData struct {
		instDef  []byte
		waveData []byte
		arpData  []byte
		filtData []byte
	}
	slots := make([]slotData, 32) // max 32 slots
	nextLoadEvent := 0

	// Simulation state (same as in runSideBySideValidation)
	type chanState struct {
		freq        uint16
		noteFreq    uint16
		note        int
		inst        int
		effect      int
		param       byte
		waveIdx     int
		arpIdx      int
		vibratoPos  int
		vibSpeed    int
		vibDepth    int
		vibDelay    int
		slideEnable bool
		slideDelta  int16
		gate        bool
		gateOn      byte
		ad          byte
		sr          byte
		waveform    byte
		hardRestart int
		// Pulse width
		plsWidthLo  byte
		plsWidthHi  byte
		plsSpeed    byte
		plsDir      byte
		plsLimitUp  byte
		plsLimitDn  byte
	}

	// Filter state (global, not per-channel)
	filterActive := false // whether filter is currently running
	filterIdx := 0
	filterEnd := 0
	filterLoop := 0
	filterSlot := -1 // which slot's filter data we're using
	filterCutoff := byte(0)
	filterResonance := byte(0)
	filterMode := byte(0)
	globalVolume := byte(0x0F)

	// Run continuous simulation - no per-song loop
	// Reset events in stream trigger state resets
	var simState [3]chanState
	speed := 6
	speedCounter := 5
	mod3Counter := 0

	// Get total frames and rows from final reset marker in stream
	// (last reset has frame=totalFrames, row=totalRows)
	totalFrames := 0
	totalRows := numRows
	for _, ev := range loadEvents {
		if ev.isReset && ev.resetFrame > totalFrames {
			totalFrames = ev.resetFrame
		}
	}

	// Row tracking - continuous across all songs
	row := 0
	ordernumber := 0
	nextordernumber := 0
	trackRow := -1
	forcenewpattern := true
	firsttrackrow := 0

	// Initialize state for first song (no reset marker at row 0)
	for ch := 0; ch < 3; ch++ {
		simState[ch] = chanState{hardRestart: 2}
	}

	// Extract reset events (frame boundaries from stream)
	type resetInfo struct {
		frame    int
		rowBase  int
	}
	var resets []resetInfo
	for _, ev := range loadEvents {
		if ev.isReset {
			resets = append(resets, resetInfo{ev.resetFrame, ev.row})
		}
	}
	nextResetIdx := 0
	songRowBase := 0      // Row offset for current song
	songOrderBase := 0    // Order offset for current song (= songRowBase / 64)
	songEndRow := totalRows // End row for current song (for HR check)
	if len(resets) > 0 {
		songEndRow = resets[0].rowBase
	}

	for frame := 0; frame < totalFrames; frame++ {
		// Check for song boundary (from stream reset markers) and reset
		if nextResetIdx < len(resets) && frame == resets[nextResetIdx].frame {
			// Reset pattern tracking for new song
			ordernumber = 0
			nextordernumber = 0
			trackRow = -1
			forcenewpattern = true
			firsttrackrow = 0
			songRowBase = resets[nextResetIdx].rowBase
			songOrderBase = songRowBase / 64
			row = songRowBase
			// Update song end row
			if nextResetIdx+1 < len(resets) {
				songEndRow = resets[nextResetIdx+1].rowBase
			} else {
				songEndRow = totalRows
			}
			// Reset all state including timing
			for ch := 0; ch < 3; ch++ {
				simState[ch] = chanState{hardRestart: 2}
			}
			filterActive = false
			filterIdx = 0
			filterEnd = 0
			filterLoop = 0
			filterSlot = -1
			filterCutoff = 0
			filterResonance = 0
			filterMode = 0
			globalVolume = 0x0F
			speed = 6
			speedCounter = 5
			mod3Counter = 0
			nextResetIdx++
		}

		// Increment speedCounter and process row if needed
		mod3Counter--
		if mod3Counter < 0 {
			mod3Counter = 2
		}

		speedCounter++
		if speedCounter >= speed && row < totalRows {
			speedCounter = 0

			if forcenewpattern {
				ordernumber = nextordernumber
				nextordernumber++
				trackRow = firsttrackrow
				firsttrackrow = 0
				forcenewpattern = false
			} else {
				trackRow++
				if trackRow >= 64 {
					ordernumber = nextordernumber
					nextordernumber++
					trackRow = 0
				}
			}

			row = songRowBase + ordernumber*64 + trackRow

			// Process any load events up to this row (reset events handled at frame start)
			for nextLoadEvent < len(loadEvents) && loadEvents[nextLoadEvent].row <= row {
				ev := loadEvents[nextLoadEvent]
				if !ev.isReset {
					slots[ev.slot] = slotData{ev.instDef, ev.waveData, ev.arpData, ev.filtData}
				}
				nextLoadEvent++
			}

			for ch := 0; ch < 3; ch++ {
				off := row * 3
				if off+2 >= len(patternData[ch]) {
					continue
				}
				noteByte, instEff, param := patternData[ch][off], patternData[ch][off+1], patternData[ch][off+2]
				note := int(noteByte & 0x7F)
				inst := int(instEff & 0x1F) // This is now a slot ID, not original instrument ID
				effect := int((instEff >> 5) | ((noteByte >> 4) & 0x08))

				// Look up transpose
				transIdx := songOrderBase + ordernumber
				trans := int8(0)
				if transIdx < len(transposeStream[ch]) {
					trans = int8(transposeStream[ch][transIdx])
				}

					// Player ALWAYS stores effect (even if 0) - see lines 600-616
					simState[ch].effect = effect
					simState[ch].param = param

					if inst > 0 {
						simState[ch].inst = inst
						slotIdx := inst - 1 // inst is 1-based slot ID
						if slotIdx >= 0 && slotIdx < len(slots) && len(slots[slotIdx].instDef) > INST_PULSELIMITS {
							simState[ch].waveIdx = int(slots[slotIdx].instDef[INST_WAVESTART])
							simState[ch].arpIdx = int(slots[slotIdx].instDef[INST_ARPSTART])
							simState[ch].vibDelay = int(slots[slotIdx].instDef[INST_VIBDELAY])
							simState[ch].ad = slots[slotIdx].instDef[INST_AD]
							simState[ch].sr = slots[slotIdx].instDef[INST_SR]
							// Pulse width params
							pw := slots[slotIdx].instDef[INST_PULSEWIDTH]
							simState[ch].plsWidthLo = pw << 4
							simState[ch].plsWidthHi = pw >> 4
							simState[ch].plsSpeed = slots[slotIdx].instDef[INST_PULSESPEED]
							limits := slots[slotIdx].instDef[INST_PULSELIMITS]
							simState[ch].plsLimitDn = limits >> 4
							simState[ch].plsLimitUp = limits & 0x0F
							simState[ch].plsDir = 0
						}
						simState[ch].vibratoPos = 0
					}

					if note > 0 && note < 0x61 {
						simState[ch].note = note - 1
						if simState[ch].effect == 2 { // Portamento (new effect 2)
							idx := simState[ch].note + int(trans)
							if idx >= 0 && idx < len(freqTableLo) {
								simState[ch].noteFreq = uint16(freqTableLo[idx]) | uint16(freqTableHi[idx])<<8
							}
						}
						// Reset waveIdx, arpIdx on EVERY note (player does this in @notporta)
						currentInst := simState[ch].inst
						slotIdx := currentInst - 1
						if slotIdx >= 0 && slotIdx < len(slots) && len(slots[slotIdx].instDef) > INST_ARPSTART {
							simState[ch].waveIdx = int(slots[slotIdx].instDef[INST_WAVESTART])
							simState[ch].arpIdx = int(slots[slotIdx].instDef[INST_ARPSTART])
						}
						simState[ch].slideDelta = 0
						simState[ch].slideEnable = false
						simState[ch].gate = true
						simState[ch].gateOn = 0xFF
					}
					if note == 0x61 {
						simState[ch].gate = false
						simState[ch].gateOn = 0xFE
					}

					if effect == 0x03 && param < 0x80 && param > 0 { // Effect 3 = Fxx speed
						speed = int(param)
					}
					if effect == 0x0B { // Effect B = pattern break
						firsttrackrow = int(param)
						forcenewpattern = true
					}
					if effect == 0x0C { // Effect C = position jump (old B)
						nextordernumber = int(param)
						forcenewpattern = true
					}
				}
			}

			// Process channel effects
			for ch := 0; ch < 3; ch++ {
				s := &simState[ch]
				s.vibDepth = 0
				slotIdx := s.inst - 1

				// Look up transpose
				transIdx := songOrderBase + ordernumber
				trans := int8(0)
				if transIdx < len(transposeStream[ch]) {
					trans = int8(transposeStream[ch][transIdx])
				}

				// Wave table (skip if set waveform effect is active)
				if s.effect != 5 && slotIdx >= 0 && slotIdx < len(slots) && len(slots[slotIdx].instDef) > INST_WAVELOOP && s.waveIdx < len(slots[slotIdx].waveData) {
					s.waveform = slots[slotIdx].waveData[s.waveIdx]
					s.waveIdx++
					waveEnd := int(slots[slotIdx].instDef[INST_WAVEEND])
					if s.waveIdx > waveEnd {
						s.waveIdx = int(slots[slotIdx].instDef[INST_WAVELOOP])
					}
				}

				// Pulse width modulation
				if s.plsSpeed != 0 {
					if s.plsDir != 0 {
						// Going down
						newLo := int(s.plsWidthLo) - int(s.plsSpeed)
						if newLo < 0 {
							s.plsWidthLo = byte(newLo + 256)
							s.plsWidthHi--
						} else {
							s.plsWidthLo = byte(newLo)
						}
						if int8(s.plsWidthHi) < int8(s.plsLimitDn) {
							s.plsDir = 0
							s.plsWidthLo = 0
							s.plsWidthHi = s.plsLimitDn
						}
					} else {
						// Going up
						newLo := int(s.plsWidthLo) + int(s.plsSpeed)
						if newLo > 255 {
							s.plsWidthLo = byte(newLo - 256)
							s.plsWidthHi++
						} else {
							s.plsWidthLo = byte(newLo)
						}
						if int8(s.plsWidthHi) > int8(s.plsLimitUp) {
							s.plsDir = 0x80
							s.plsWidthLo = 0xFF
							s.plsWidthHi = s.plsLimitUp
						}
					}
				}

				// Arp table - apply transpose from stream at runtime (skip if portamento)
				if s.effect != 2 {
					if slotIdx >= 0 && slotIdx < len(slots) && len(slots[slotIdx].instDef) > INST_ARPLOOP && s.arpIdx < len(slots[slotIdx].arpData) {
						arpVal := int(slots[slotIdx].arpData[s.arpIdx])
						if arpVal >= 0x80 {
							arpVal = arpVal & 0x7F
						} else {
							arpVal = s.note + int(trans) + arpVal
						}
						if arpVal >= 0 && arpVal < len(freqTableLo) {
							s.freq = uint16(freqTableLo[arpVal]) | uint16(freqTableHi[arpVal])<<8
							s.noteFreq = s.freq
						}
						s.arpIdx++
						arpEnd := int(slots[slotIdx].instDef[INST_ARPEND])
						if s.arpIdx > arpEnd {
							s.arpIdx = int(slots[slotIdx].instDef[INST_ARPLOOP])
						}
					}
				}

				// Vibrato from instrument
				if slotIdx >= 0 && slotIdx < len(slots) && len(slots[slotIdx].instDef) > INST_VIBDEPSP {
					if s.vibDelay > 0 {
						s.vibDelay--
					} else {
						vibDepSp := slots[slotIdx].instDef[INST_VIBDEPSP]
						s.vibDepth = int(vibDepSp & 0xF0)
						if s.vibDepth != 0 {
							s.vibSpeed = int(vibDepSp & 0x0F)
						}
					}
				}

				// Effects (frequency-sorted: 1=arp, 2=porta, 3=Fxx, 4=SR, 5=wave, 6=down, 7=AD, 8=reso, 9=up, A=vib)
				switch s.effect {
				case 9: // Slide (old effect 1 -> new 9), param: 0=up, 1=down
					s.slideEnable = true
					if s.param == 0 {
						s.slideDelta += 0x20
					} else {
						s.slideDelta -= 0x20
					}
				case 6: // Pulse width (old effect 2 -> new 6)
					var pwVal byte
					if s.param != 0 {
						pwVal = 0x80
					}
					s.plsWidthLo = (pwVal << 4) & 0xFF
					s.plsWidthHi = pwVal >> 4
				case 2: // Portamento (old effect 3 -> new 2)
					deltaLo := (s.param << 4) & 0xFF
					deltaHi := s.param >> 4
					delta := uint16(deltaHi)<<8 | uint16(deltaLo)
					if s.noteFreq > s.freq {
						newFreq := s.freq + delta
						if newFreq > s.noteFreq {
							newFreq = s.noteFreq
						}
						s.freq = newFreq
					} else if s.noteFreq < s.freq {
						if delta > s.freq {
							s.freq = s.noteFreq
						} else {
							newFreq := s.freq - delta
							if newFreq < s.noteFreq {
								newFreq = s.noteFreq
							}
							s.freq = newFreq
						}
					}
				case 10: // Vibrato (old effect 4 -> new A)
					s.vibDepth = int(s.param & 0xF0)
					s.vibSpeed = int(s.param & 0x0F)
				case 7: // Set AD (old effect 7 -> new 7)
					s.ad = unmapParam(s.param, adUnmap)
				case 4: // Set SR (old effect 8 -> new 4)
					s.sr = unmapParam(s.param, srUnmap)
				case 5: // Set waveform (old effect 9 -> new 5)
					s.waveform = unmapParam(s.param, waveUnmap)
				case 1: // Arpeggio (old effect A -> new 1)
					arpX := int(s.param >> 4)
					arpY := int(s.param & 0x0F)
					var arpOffset int
					switch mod3Counter {
					case 0:
						arpOffset = arpY
					case 1:
						arpOffset = arpX
					case 2:
						arpOffset = 0
					}
					idx := s.note + int(trans) + arpOffset
					if idx >= 0 && idx < len(freqTableLo) {
						s.freq = uint16(freqTableLo[idx]) | uint16(freqTableHi[idx])<<8
						s.noteFreq = s.freq
					}
				case 8: // Filter resonance (old effect E -> new 8)
					filterResonance = unmapParam(s.param, resoUnmap)
				case 3: // Extended Fxx (old effect F -> new 3)
					// High nibbles frequency-sorted: $8=hardrestart, $9=filttrig, $A=globalvol, $B=filtmode, $C=fineslide
					if speedCounter == 0 && s.param >= 0x80 {
						highNibble := int(s.param & 0xF0)
						lowNibble := int(s.param & 0x0F)
						if highNibble == 0x80 {
							// $8x: Set hard restart offset (old $Fx)
							s.hardRestart = lowNibble
						}
						if highNibble == 0x90 && lowNibble != 0 {
							// $9x: Load filter settings from slot x (old $Ex)
							instSlot := int(lowNibble) - 1
							if instSlot >= 0 && instSlot < len(slots) && len(slots[instSlot].instDef) > INST_FILTLOOP {
								filterActive = true
								filterSlot = instSlot
								filterIdx = int(slots[instSlot].instDef[INST_FILTSTART])
								filterEnd = int(slots[instSlot].instDef[INST_FILTEND])
								filterLoop = int(slots[instSlot].instDef[INST_FILTLOOP])
							}
						}
						if highNibble == 0xA0 {
							// $Ax: Set global volume (old $8x)
							globalVolume = byte(lowNibble)
						}
						if highNibble == 0xB0 {
							// $Bx: Set filter mode (old $9x)
							filterMode = byte(lowNibble << 4)
						}
						if highNibble == 0xC0 {
							// $Cx: Fine slide (old $Bx) - add lowNibble*4 to slideDelta, enable slide
							s.slideDelta += int16(lowNibble) * 4
							s.slideEnable = true
						}
					}
				}

				// Apply slide
				if s.slideEnable {
					newFreq := int(s.freq) + int(s.slideDelta)
					if newFreq < 0 {
						newFreq = 0
					}
					if newFreq > 0xFFFF {
						newFreq = 0xFFFF
					}
					s.freq = uint16(newFreq)
				}

				// Apply vibrato (same as original validation)
				simF := s.freq
				if s.vibDepth > 0 {
					// Calculate table position (0-15, mirrored for 16-31)
					pos := s.vibratoPos & 0x1F
					if pos >= 0x10 {
						pos = 0x1F ^ pos // mirror: 31-pos
					}
					// Table index = pos | depth (depth is already in high nibble)
					tableIdx := pos | s.vibDepth
					if tableIdx < len(vibratoTable) {
						// Offset is table value * 2 (16-bit)
						offset := int(vibratoTable[tableIdx]) * 2
						// Phase 0-31: subtract, 32-63: add
						if s.vibratoPos&0x20 != 0 {
							simF = uint16(int(s.freq) + offset)
						} else {
							simF = uint16(int(s.freq) - offset)
						}
					}
					// Advance vibrato position (byte wraps at 256, like player)
					s.vibratoPos = (s.vibratoPos + s.vibSpeed) & 0xFF
				}

				// Hard restart check - look ahead to see if we need to pre-release
				if speedCounter+s.hardRestart >= speed {
					var hrTrackRow, hrOrder int
					if forcenewpattern || (trackRow+1)&0x3F == 0 {
						hrTrackRow = firsttrackrow
						hrOrder = nextordernumber
					} else {
						hrTrackRow = trackRow + 1
						hrOrder = ordernumber
					}
					hrRow := songRowBase + hrOrder*64 + hrTrackRow
					if hrRow >= songRowBase && hrRow < songEndRow {
						off := hrRow * 3
						if off+2 < len(patternData[ch]) {
							noteByte := patternData[ch][off]
							instEff := patternData[ch][off+1]
							note := noteByte & 0x7F
							effect := int((instEff >> 5) | ((noteByte >> 4) & 0x08))
							if note > 0 && note != 0x61 {
								doHR := false
								if noteByte&0x80 != 0 {
									doHR = true
								} else if effect != 2 { // Portamento is new effect 2
									instEffByte := instEff & 0xE0
									if instEffByte != 0x40 { // $40 = effect 2 << 5
										doHR = true
									}
								}
								if doHR {
									s.waveform = 0
									s.ad = 0
									s.sr = 0
								}
							}
						}
					}
				}

				// Compute control register
				simControl := s.waveform & s.gateOn

				// Store results
				result.Freq[ch] = append(result.Freq[ch], simF)
				result.PW[ch] = append(result.PW[ch], uint16(s.plsWidthLo)|uint16(s.plsWidthHi)<<8)
				result.Control[ch] = append(result.Control[ch], simControl)
				result.AD[ch] = append(result.AD[ch], s.ad)
				result.SR[ch] = append(result.SR[ch], s.sr)
			}

			// Process filter table (after channel processing)
			if filterActive && filterSlot >= 0 && filterSlot < len(slots) {
				if filterIdx < len(slots[filterSlot].filtData) {
					filterCutoff = slots[filterSlot].filtData[filterIdx]
				}
				filterIdx++
				if filterIdx > filterEnd {
					filterIdx = filterLoop
				}
			}

			// Store filter/volume results (global, not per-channel)
		result.FilterLo = append(result.FilterLo, 0) // Low 3 bits not tracked
		result.FilterHi = append(result.FilterHi, filterCutoff)
		result.FilterRes = append(result.FilterRes, filterResonance)
		result.FilterVol = append(result.FilterVol, globalVolume|filterMode)
	}

	return result
}

// Krumhansl-Schmuckler key profiles (normalized correlation weights)
// These represent how strongly each pitch class correlates with a given key
var majorProfile = [12]float64{6.35, 2.23, 3.48, 2.33, 4.38, 4.09, 2.52, 5.19, 2.39, 3.66, 2.29, 2.88}
var minorProfile = [12]float64{6.33, 2.68, 3.52, 5.38, 2.60, 3.53, 2.54, 4.75, 3.98, 2.69, 3.34, 3.17}

// KeyResult holds the detected key for a window
type KeyResult struct {
	Root      int     // 0-11 (C=0, C#=1, ... B=11)
	IsMinor   bool    // true if minor key
	Score     float64 // correlation score
	StartTick int     // MIDI tick where this key starts
}

// detectKeys analyzes note frequencies and detects musical keys
// patternFrames: frame numbers where each pattern starts (from simulation)
// Returns key detections with their start ticks aligned to patterns
func detectKeys(noteFreqs [3][]uint16, isDrum [3][]bool, patternFrames []int) []KeyResult {
	totalFrames := len(noteFreqs[0])
	if totalFrames == 0 || len(patternFrames) == 0 {
		return nil
	}

	var results []KeyResult
	var prevKey KeyResult
	prevKey.Root = -1

	for i := 0; i < len(patternFrames); i++ {
		windowStart := patternFrames[i]
		windowEnd := totalFrames
		if i+1 < len(patternFrames) {
			windowEnd = patternFrames[i+1]
		}

		// Build weighted pitch class profile for this window
		var pitchProfile [12]float64
		windowSize := windowEnd - windowStart

		for frame := windowStart; frame < windowEnd; frame++ {
			frameInWindow := frame - windowStart

			// Position weight: emphasize pattern boundaries
			// Now aligned to actual pattern boundaries from simulation
			posWeight := 1.0
			if windowSize > 0 {
				windowPos := float64(frameInWindow) / float64(windowSize)
				if windowPos < 0.02 || windowPos > 0.98 { // near start/end
					posWeight = 1.5
				} else if math.Abs(windowPos-0.5) < 0.02 { // near middle
					posWeight = 1.25
				} else if math.Abs(windowPos-0.25) < 0.02 || math.Abs(windowPos-0.75) < 0.02 { // quarter points
					posWeight = 1.1
				}
			}

			// Collect notes from all channels at this frame
			var activeNotes []int
			var activeChannels []int
			for ch := 0; ch < 3; ch++ {
				if frame >= len(isDrum[ch]) || isDrum[ch][frame] {
					continue // skip drum sounds
				}
				freq := noteFreqs[ch][frame]
				midiNote := sidFreqToMIDI(freq)
				if midiNote >= 0 && midiNote < 128 {
					activeNotes = append(activeNotes, midiNote)
					activeChannels = append(activeChannels, ch)
				}
			}

			// Multi-channel weight: same pitch class on multiple channels gets bonus
			pitchClassCount := make(map[int]int)
			for _, note := range activeNotes {
				pc := note % 12
				pitchClassCount[pc]++
			}

			for i, note := range activeNotes {
				pc := note % 12
				octave := note / 12

				// Bass weight: lower octaves get higher weight
				// MIDI octave 2 (notes 24-35) = bass, octave 3-4 = mid, 5+ = treble
				bassWeight := 1.0
				if octave <= 2 {
					bassWeight = 2.0
				} else if octave <= 3 {
					bassWeight = 1.5
				} else if octave <= 4 {
					bassWeight = 1.2
				}

				// Channel weight: channel 0 (bass) gets slight priority
				chanWeight := 1.0
				if activeChannels[i] == 0 {
					chanWeight = 1.1
				}

				// Multi-channel bonus
				multiChanWeight := 1.0
				if pitchClassCount[pc] > 1 {
					multiChanWeight = 1.0 + 0.3*float64(pitchClassCount[pc]-1)
				}

				// Duration weight is implicit: each frame adds to the profile
				// Arpeggiated notes contribute less per pitch class since they spread across frames

				totalWeight := posWeight * bassWeight * chanWeight * multiChanWeight
				pitchProfile[pc] += totalWeight
			}
		}

		// Find best matching key using correlation with key profiles
		bestKey := KeyResult{Root: 0, IsMinor: false, Score: -math.MaxFloat64, StartTick: windowStart}

		for root := 0; root < 12; root++ {
			// Rotate profile to test this root
			var rotated [12]float64
			for pc := 0; pc < 12; pc++ {
				rotated[pc] = pitchProfile[(pc+root)%12]
			}

			// Correlate with major profile
			majorScore := correlate(rotated, majorProfile)
			if majorScore > bestKey.Score {
				bestKey.Score = majorScore
				bestKey.Root = root
				bestKey.IsMinor = false
			}

			// Correlate with minor profile
			minorScore := correlate(rotated, minorProfile)
			if minorScore > bestKey.Score {
				bestKey.Score = minorScore
				bestKey.Root = root
				bestKey.IsMinor = true
			}
		}

		// Consistency check: prefer previous key if scores are close
		if prevKey.Root >= 0 && len(results) > 0 {
			prevScore := results[len(results)-1].Score
			scoreDiff := bestKey.Score - prevScore
			if scoreDiff < 0.1*math.Abs(prevScore) { // within 10%
				if bestKey.Root != prevKey.Root || bestKey.IsMinor != prevKey.IsMinor {
					// Check if staying with previous key is reasonable
					var rotated [12]float64
					for pc := 0; pc < 12; pc++ {
						rotated[pc] = pitchProfile[(pc+prevKey.Root)%12]
					}
					var keepScore float64
					if prevKey.IsMinor {
						keepScore = correlate(rotated, minorProfile)
					} else {
						keepScore = correlate(rotated, majorProfile)
					}
					// Keep previous key if it's still a good fit (within 15% of best)
					if keepScore > bestKey.Score*0.85 {
						bestKey = prevKey
						bestKey.StartTick = windowStart
						bestKey.Score = keepScore
					}
				}
			}
		}

		// Only add if key changed or first result
		if len(results) == 0 || results[len(results)-1].Root != bestKey.Root || results[len(results)-1].IsMinor != bestKey.IsMinor {
			results = append(results, bestKey)
		}
		prevKey = bestKey
	}

	return results
}

// Major scale intervals from root (semitones): 0, 2, 4, 5, 7, 9, 11
var majorIntervals = []int{0, 2, 4, 5, 7, 9, 11}

// Minor scale intervals from root (semitones): 0, 2, 3, 5, 7, 8, 10 (natural minor)
var minorIntervals = []int{0, 2, 3, 5, 7, 8, 10}

// chromaticToDiatonic converts a chromatic pitch class (0-11) to diatonic encoding
// Returns (scaleDegree, isHarmonic) where:
// - If isHarmonic: scaleDegree is 0-6 (diatonic scale degree)
// - If !isHarmonic: scaleDegree is 0-4 (non-harmonic accidental index)
func chromaticToDiatonic(pitchClass int, keyRoot int, isMinor bool) (int, bool) {
	// Get pitch class relative to key root
	relPC := (pitchClass - keyRoot + 12) % 12

	intervals := majorIntervals
	if isMinor {
		intervals = minorIntervals
	}

	// Check if it's a scale tone
	for degree, interval := range intervals {
		if relPC == interval {
			return degree, true
		}
	}

	// Non-harmonic: find which accidental it is
	// For major: non-scale tones are 1, 3, 6, 8, 10 (5 values)
	// For minor: non-scale tones are 1, 4, 6, 9, 11 (5 values)
	accidentalIdx := 0
	for pc := 0; pc < relPC; pc++ {
		isScale := false
		for _, interval := range intervals {
			if pc == interval {
				isScale = true
				break
			}
		}
		if !isScale {
			accidentalIdx++
		}
	}
	return accidentalIdx, false
}

// diatonicToChromatic converts back from diatonic encoding to chromatic pitch class
func diatonicToChromatic(scaleDegree int, isHarmonic bool, keyRoot int, isMinor bool) int {
	intervals := majorIntervals
	if isMinor {
		intervals = minorIntervals
	}

	if isHarmonic {
		if scaleDegree >= 0 && scaleDegree < 7 {
			return (keyRoot + intervals[scaleDegree]) % 12
		}
		return keyRoot // fallback
	}

	// Non-harmonic: find the nth non-scale pitch class
	accidentalIdx := 0
	for pc := 0; pc < 12; pc++ {
		isScale := false
		for _, interval := range intervals {
			if pc == interval {
				isScale = true
				break
			}
		}
		if !isScale {
			if accidentalIdx == scaleDegree {
				return (keyRoot + pc) % 12
			}
			accidentalIdx++
		}
	}
	return keyRoot // fallback
}

// encodeNoteDiatonic encodes a MIDI note using diatonic encoding relative to key
// Returns: encoded value where:
// - Bits 0-2: scale degree (0-6) or accidental index (0-4)
// - Bit 3: 0=harmonic, 1=non-harmonic
// - Bits 4-7: octave (0-15)
// Total: 8 bits per note
func encodeNoteDiatonic(midiNote int, keyRoot int, isMinor bool) int {
	if midiNote < 0 || midiNote >= 128 {
		return 0 // rest/invalid
	}
	octave := midiNote / 12
	pitchClass := midiNote % 12

	degree, isHarmonic := chromaticToDiatonic(pitchClass, keyRoot, isMinor)

	encoded := degree // bits 0-2 (or 0-2 for accidentals)
	if !isHarmonic {
		encoded |= 0x08 // bit 3 = non-harmonic flag
	}
	encoded |= (octave << 4) // bits 4-7 = octave

	return encoded
}

// decodeNoteDiatonic decodes a diatonic-encoded note back to MIDI note
func decodeNoteDiatonic(encoded int, keyRoot int, isMinor bool) int {
	if encoded == 0 {
		return -1 // rest
	}
	degree := encoded & 0x07
	isHarmonic := (encoded & 0x08) == 0
	octave := (encoded >> 4) & 0x0F

	pitchClass := diatonicToChromatic(degree, isHarmonic, keyRoot, isMinor)
	return octave*12 + pitchClass
}

// detectKeysPerRow returns key assignment for each row based on pattern-level detection
// Propagates pattern-level keys to all rows within each pattern
func detectKeysPerRow(noteFreqs [3][]uint16, isDrum [3][]bool, patternFrames []int, numRows int, rowsPerPattern int) []KeyResult {
	patternKeys := detectKeys(noteFreqs, isDrum, patternFrames)
	if len(patternKeys) == 0 {
		return nil
	}

	rowKeys := make([]KeyResult, numRows)
	keyIdx := 0

	for row := 0; row < numRows; row++ {
		// Check if we've moved to a new key's region
		// Key regions are based on StartTick which corresponds to frame numbers
		// Convert row to approximate frame (assuming speed=6)
		approxFrame := row * 6

		// Advance key index if we've passed the next key's start
		for keyIdx < len(patternKeys)-1 && approxFrame >= patternKeys[keyIdx+1].StartTick {
			keyIdx++
		}

		rowKeys[row] = patternKeys[keyIdx]
	}

	return rowKeys
}

// detectKeysMaxInScale finds keys that maximize the number of in-scale notes
// This optimizes for compression rather than perceptual correctness
func detectKeysMaxInScale(transposedNotes [3][]int, numRows int, windowSize int) []KeyResult {
	var results []KeyResult

	for windowStart := 0; windowStart < numRows; windowStart += windowSize {
		windowEnd := windowStart + windowSize
		if windowEnd > numRows {
			windowEnd = numRows
		}

		// Count pitch classes in this window
		var pitchCounts [12]int
		for ch := 0; ch < 3; ch++ {
			for row := windowStart; row < windowEnd; row++ {
				note := transposedNotes[ch][row]
				if note > 0 && note != 0x61 {
					midiNote := note + 12
					pc := midiNote % 12
					pitchCounts[pc]++
				}
			}
		}

		// Try all 24 keys and find the one with most in-scale notes
		bestRoot := 0
		bestMinor := false
		bestInScale := 0

		for root := 0; root < 12; root++ {
			for _, isMinor := range []bool{false, true} {
				intervals := majorIntervals
				if isMinor {
					intervals = minorIntervals
				}

				inScale := 0
				for _, interval := range intervals {
					pc := (root + interval) % 12
					inScale += pitchCounts[pc]
				}

				if inScale > bestInScale {
					bestInScale = inScale
					bestRoot = root
					bestMinor = isMinor
				}
			}
		}

		// Calculate score as percentage in-scale
		totalNotes := 0
		for _, c := range pitchCounts {
			totalNotes += c
		}
		score := 0.0
		if totalNotes > 0 {
			score = float64(bestInScale) / float64(totalNotes)
		}

		results = append(results, KeyResult{
			Root:      bestRoot,
			IsMinor:   bestMinor,
			Score:     score,
			StartTick: windowStart,
		})
	}

	return results
}

// detectKeyMaxInScaleGlobal finds a single key that maximizes in-scale notes across all data
func detectKeyMaxInScaleGlobal(transposedNotes [3][]int, numRows int) (int, bool, float64) {
	// Count all pitch classes
	var pitchCounts [12]int
	for ch := 0; ch < 3; ch++ {
		for row := 0; row < numRows; row++ {
			note := transposedNotes[ch][row]
			if note > 0 && note != 0x61 {
				midiNote := note + 12
				pc := midiNote % 12
				pitchCounts[pc]++
			}
		}
	}

	totalNotes := 0
	for _, c := range pitchCounts {
		totalNotes += c
	}

	bestRoot := 0
	bestMinor := false
	bestInScale := 0

	for root := 0; root < 12; root++ {
		for _, isMinor := range []bool{false, true} {
			intervals := majorIntervals
			if isMinor {
				intervals = minorIntervals
			}

			inScale := 0
			for _, interval := range intervals {
				pc := (root + interval) % 12
				inScale += pitchCounts[pc]
			}

			if inScale > bestInScale {
				bestInScale = inScale
				bestRoot = root
				bestMinor = isMinor
			}
		}
	}

	return bestRoot, bestMinor, float64(bestInScale) / float64(totalNotes)
}

// correlate computes Pearson correlation between two 12-element arrays
func correlate(a, b [12]float64) float64 {
	var sumA, sumB, sumAB, sumA2, sumB2 float64
	n := 12.0
	for i := 0; i < 12; i++ {
		sumA += a[i]
		sumB += b[i]
		sumAB += a[i] * b[i]
		sumA2 += a[i] * a[i]
		sumB2 += b[i] * b[i]
	}
	num := n*sumAB - sumA*sumB
	den := math.Sqrt((n*sumA2 - sumA*sumA) * (n*sumB2 - sumB*sumB))
	if den == 0 {
		return 0
	}
	return num / den
}

// keyName returns the name of a key (e.g., "C", "F#m")
func keyName(root int, isMinor bool) string {
	names := []string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}
	suffix := ""
	if isMinor {
		suffix = "m"
	}
	return names[root] + suffix
}

// writeMIDI outputs a MIDI file with detected notes
// noteFreqs contains base note frequencies (without vibrato - for clean MIDI)
// sidFreqs contains actual SID frequencies per frame (from VM emulation with vibrato)
// isDrum indicates which frames use absolute arp notes, sync, ring mod, or noise (drum-like sounds)
func writeMIDI(noteFreqs [3][]uint16, sidFreqs [3][]uint16, isDrum [3][]bool, patternFrames []int, outDir string) {
	// MIDI helper functions
	writeVarLen := func(out *[]byte, val int) {
		if val == 0 {
			*out = append(*out, 0)
			return
		}
		var buf []byte
		for val > 0 {
			buf = append([]byte{byte(val & 0x7F)}, buf...)
			val >>= 7
		}
		for i := 0; i < len(buf)-1; i++ {
			buf[i] |= 0x80
		}
		*out = append(*out, buf...)
	}

	writeInt16 := func(out *[]byte, val int) {
		*out = append(*out, byte(val>>8), byte(val))
	}

	writeInt32 := func(out *[]byte, val int) {
		*out = append(*out, byte(val>>24), byte(val>>16), byte(val>>8), byte(val))
	}

	// Build MIDI tracks
	// Track 0: tempo track
	// Track 1: melodic notes (non-drum frames, channels 0-2)
	// Track 2: drum notes (absolute arp, sync, ring mod, noise - channel 9)
	// Track 3: detected keys (channel 3)
	// Track 4: VM frequencies (channels 10-12)

	totalFrames := len(sidFreqs[0])

	// Detect keys using actual pattern boundaries from simulation
	_ = detectKeys(noteFreqs, isDrum, patternFrames)

	// Use frame-based timing: 1 tick = 1 frame
	// MIDI ppq (ticks per quarter note) = 6 (so quarter note = 6 frames at default speed)
	// Tempo = microseconds per quarter note = 6 * microseconds per frame
	// PAL: ~50 Hz = 20000 us/frame, so tempo = 6 * 20000 = 120000 us/qn = 120 ms/qn = 500 BPM
	ppq := 6
	tempo := 6 * 20000 // 120000 us per quarter note (PAL timing)

	tempoTrack := []byte{}
	writeVarLen(&tempoTrack, 0)
	tempoTrack = append(tempoTrack, 0xFF, 0x51, 0x03)
	tempoTrack = append(tempoTrack, byte(tempo>>16), byte(tempo>>8), byte(tempo))
	writeVarLen(&tempoTrack, 0)
	tempoTrack = append(tempoTrack, 0xFF, 0x2F, 0x00) // end of track

	// MIDI event for sorting
	type midiEvent struct {
		tick    int
		channel byte
		noteOn  bool
		note    byte
	}

	// Build melodic and drum tracks from simulation
	var melodicEvents []midiEvent
	var drumEvents []midiEvent
	noteTotalFrames := len(noteFreqs[0])

	for ch := 0; ch < 3; ch++ {
		lastMelodicNote := -1
		lastDrumNote := -1
		for frame := 0; frame < noteTotalFrames && frame < totalFrames; frame++ {
			freq := noteFreqs[ch][frame]
			midiNote := sidFreqToMIDI(freq)
			isDrumFrame := frame < len(isDrum[ch]) && isDrum[ch][frame]

			if isDrumFrame {
				// End melodic note if switching to drum
				if lastMelodicNote >= 0 {
					melodicEvents = append(melodicEvents, midiEvent{frame, byte(ch), false, byte(lastMelodicNote)})
					lastMelodicNote = -1
				}
				if midiNote != lastDrumNote {
					if lastDrumNote >= 0 {
						drumEvents = append(drumEvents, midiEvent{frame, 9, false, byte(lastDrumNote)})
					}
					if midiNote >= 0 {
						drumEvents = append(drumEvents, midiEvent{frame, 9, true, byte(midiNote)})
					}
					lastDrumNote = midiNote
				}
			} else {
				// End drum note if switching to melodic
				if lastDrumNote >= 0 {
					drumEvents = append(drumEvents, midiEvent{frame, 9, false, byte(lastDrumNote)})
					lastDrumNote = -1
				}
				if midiNote != lastMelodicNote {
					if lastMelodicNote >= 0 {
						melodicEvents = append(melodicEvents, midiEvent{frame, byte(ch), false, byte(lastMelodicNote)})
					}
					if midiNote >= 0 {
						melodicEvents = append(melodicEvents, midiEvent{frame, byte(ch), true, byte(midiNote)})
					}
					lastMelodicNote = midiNote
				}
			}
		}
		// Final note offs
		if lastMelodicNote >= 0 {
			melodicEvents = append(melodicEvents, midiEvent{totalFrames, byte(ch), false, byte(lastMelodicNote)})
		}
		if lastDrumNote >= 0 {
			drumEvents = append(drumEvents, midiEvent{totalFrames, 9, false, byte(lastDrumNote)})
		}
	}

	// Build combined track for VM (channels 10-12)
	var vmEvents []midiEvent
	for ch := 0; ch < 3; ch++ {
		lastNote := -1
		for frame := 0; frame < totalFrames; frame++ {
			freq := sidFreqs[ch][frame]
			midiNote := sidFreqToMIDI(freq)
			if midiNote != lastNote {
				if lastNote >= 0 {
					vmEvents = append(vmEvents, midiEvent{frame, byte(10 + ch), false, byte(lastNote)})
				}
				if midiNote >= 0 {
					vmEvents = append(vmEvents, midiEvent{frame, byte(10 + ch), true, byte(midiNote)})
				}
				lastNote = midiNote
			}
		}
		if lastNote >= 0 {
			vmEvents = append(vmEvents, midiEvent{totalFrames, byte(10 + ch), false, byte(lastNote)})
		}
	}

	// Sort events by tick using stable sort (preserves note-off before note-on at same tick)
	sort.SliceStable(melodicEvents, func(i, j int) bool { return melodicEvents[i].tick < melodicEvents[j].tick })
	sort.SliceStable(drumEvents, func(i, j int) bool { return drumEvents[i].tick < drumEvents[j].tick })
	sort.SliceStable(vmEvents, func(i, j int) bool { return vmEvents[i].tick < vmEvents[j].tick })

	// Convert events to track bytes
	buildTrack := func(events []midiEvent, channels []byte, program byte) []byte {
		track := []byte{}
		// Program change for each channel (skip for channel 9/drums)
		for _, ch := range channels {
			if ch != 9 {
				writeVarLen(&track, 0)
				track = append(track, 0xC0|ch, program)
			}
		}
		lastTick := 0
		for _, ev := range events {
			delta := ev.tick - lastTick
			writeVarLen(&track, delta)
			if ev.noteOn {
				track = append(track, 0x90|ev.channel, ev.note, 0x64)
			} else {
				track = append(track, 0x80|ev.channel, ev.note, 0x40)
			}
			lastTick = ev.tick
		}
		writeVarLen(&track, 0)
		track = append(track, 0xFF, 0x2F, 0x00)
		return track
	}

	melodicTrack := buildTrack(melodicEvents, []byte{0, 1, 2}, 4) // Electric Piano 1
	drumTrack := buildTrack(drumEvents, []byte{9}, 0)             // Channel 9 = drums
	vmTrack := buildTrack(vmEvents, []byte{10, 11, 12}, 4)        // VM frequencies

	// Assemble MIDI file
	midi := []byte{}

	// Header: MThd
	midi = append(midi, 'M', 'T', 'h', 'd')
	writeInt32(&midi, 6) // header length
	writeInt16(&midi, 1) // format 1
	writeInt16(&midi, 4) // 4 tracks: tempo, melodic, drums, VM
	writeInt16(&midi, ppq)

	// Write tracks
	for _, track := range [][]byte{tempoTrack, melodicTrack, drumTrack, vmTrack} {
		midi = append(midi, 'M', 'T', 'r', 'k')
		writeInt32(&midi, len(track))
		midi = append(midi, track...)
	}

	// Write file
	midiPath := filepath.Join(outDir, "song.mid")
	os.WriteFile(midiPath, midi, 0644)
}

func main() {
	mainStart := time.Now()
	outDir := "generated/stream_compress"
	os.MkdirAll(outDir, 0755)

	// Extract streams and transpose from partX.bin files
	var allStreams [3][]byte
	var allTranspose [3][]int8
	var allOrders [3][]byte
	var songBoundaries []int
	var orderBoundaries []int // cumulative order count per song

	// Collect all tables for analysis
	var allSongTables []SongTables

	for song := 1; song <= 9; song++ {
		data, err := os.ReadFile(filepath.Join("generated", "parts", fmt.Sprintf("part%d.bin", song)))
		if err != nil {
			continue
		}
		songBoundaries = append(songBoundaries, len(allStreams[0])/3)
		orderBoundaries = append(orderBoundaries, len(allTranspose[0]))
		maxFrames := int(partTimes[song-1])
		streams := extractSongStreams(data, maxFrames)
		transpose := extractTranspose(data, maxFrames)
		orders := extractOrderTable(data, maxFrames)
		for ch := 0; ch < 3; ch++ {
			allStreams[ch] = append(allStreams[ch], streams[ch]...)
			allTranspose[ch] = append(allTranspose[ch], transpose[ch]...)
			allOrders[ch] = append(allOrders[ch], orders[ch]...)
		}

		// Extract tables
		allSongTables = append(allSongTables, SongTables{
			instruments: extractInstruments(data),
			filter:      extractFilterTable(data),
			wave:        extractWaveTable(data),
			arp:         extractArpTable(data),
		})
	}
	songBoundaries = append(songBoundaries, len(allStreams[0])/3)
	orderBoundaries = append(orderBoundaries, len(allTranspose[0]))

	totalRows := len(allStreams[0]) / 3

	// Calculate table sizes
	totalInstBytes := 0
	totalFilterBytes := 0
	totalWaveBytes := 0
	totalArpBytes := 0
	for _, st := range allSongTables {
		totalInstBytes += countUsedBytes(st.instruments)
		totalFilterBytes += countUsedBytes(st.filter)
		totalWaveBytes += countUsedBytes(st.wave)
		totalArpBytes += countUsedBytes(st.arp)
	}

	// Calculate raw data sizes for summary
	rawPatternData := totalRows * 9
	rawTableData := totalInstBytes + totalFilterBytes + totalWaveBytes + totalArpBytes
	rawTotal := rawPatternData + rawTableData
	_ = rawTotal // used later in summary


	// Run VM and simulation side-by-side
	vmRegs, _, noteFreqs, isDrum, patternFrames := runSideBySideValidation(allStreams, allTranspose, songBoundaries, orderBoundaries, nil)

	// Output MIDI file
	writeMIDI(noteFreqs, vmRegs.Freq, isDrum, patternFrames, outDir)

	// Write each channel to a tab-separated file with decoded fields
	noteNames := []string{"C-", "C#", "D-", "D#", "E-", "F-", "F#", "G-", "G#", "A-", "A#", "B-"}
	effectNames := []string{"", "sld", "pls", "por", "vib", "AD", "SR", "wav", "arp", "res", "Fxx", "brk", "jmp"}

	noteName := func(n byte) string {
		if n == 0 {
			return "---"
		}
		if n == 0x61 {
			return "OFF"
		}
		n-- // note 1 = C-0
		return fmt.Sprintf("%s%d", noteNames[n%12], n/12)
	}

	hexOrSpace := func(b byte) string {
		if b == 0 {
			return "  "
		}
		return fmt.Sprintf("%02X", b)
	}

	for ch := 0; ch < 3; ch++ {
		f, err := os.Create(filepath.Join(outDir, fmt.Sprintf("channel_%d.csv", ch)))
		if err != nil {
			continue
		}
		stream := allStreams[ch]
		numRows := len(stream) / 3
		for row := 0; row < numRows; row++ {
			off := row * 3
			noteByte, instEff, param := stream[off], stream[off+1], stream[off+2]
			inst := instEff & 0x1F
			note := noteByte & 0x7F
			effect := (instEff >> 5) | ((noteByte >> 4) & 0x08)

			// Build human-readable string
			human := noteName(note)
			if inst > 0 {
				human += fmt.Sprintf(" i%02d", inst)
			}
			if effect > 0 && int(effect) < len(effectNames) {
				human += fmt.Sprintf(" %s%02X", effectNames[effect], param)
			}

			fmt.Fprintf(f, "%s\t%s\t%s\t%s\t%s\n", hexOrSpace(inst), hexOrSpace(note), hexOrSpace(effect), hexOrSpace(param), human)
		}
		f.Close()
	}

	// 9 streams with native bit widths: note(7), inst(5), fx+param(12)
	// Encoding: 0+literal(N bits), 1+dist+len (exp-golomb)

	numRows := len(allStreams[0]) / 3
	numPatterns := (numRows + 63) / 64

	// Extract 10 streams: 9 pattern streams + 1 transpose stream
	// Single transpose per pattern applied to ALL 3 channels
	// Find optimal transpose that gives best compression

	// Helper to get transpose for a row
	getTranspose := func(ch, row int) int8 {
		songIdx := 0
		for i := 0; i < len(songBoundaries)-1; i++ {
			if row >= songBoundaries[i] && row < songBoundaries[i+1] {
				songIdx = i
				break
			}
		}
		songStartRow := songBoundaries[songIdx]
		orderInSong := (row - songStartRow) / 64
		transIdx := orderBoundaries[songIdx] + orderInSong
		if transIdx < len(allTranspose[ch]) {
			return allTranspose[ch][transIdx]
		}
		return 0
	}

	// First pass: find optimal transpose per pattern
	// Use the most common transpose among the 3 channels, constrained to avoid clamping
	optimalTranspose := make([]int, numPatterns)

	for patIdx := 0; patIdx < numPatterns; patIdx++ {
		startRow := patIdx * 64
		endRow := startRow + 64
		if endRow > numRows {
			endRow = numRows
		}

		// Find valid transpose range that won't cause clamping
		// adjusted = note + origTrans - optTrans must be in [1, 96]
		// So: optTrans >= note + origTrans - 96 AND optTrans <= note + origTrans - 1
		minAllowed := -128
		maxAllowed := 127
		for ch := 0; ch < 3; ch++ {
			src := allStreams[ch]
			for row := startRow; row < endRow; row++ {
				note := int(src[row*3] & 0x7F)
				if note == 0 || note == 0x61 {
					continue
				}
				t := int(getTranspose(ch, row))
				// optTrans <= note + t - 1 (to keep adjusted >= 1)
				upper := note + t - 1
				if upper < maxAllowed {
					maxAllowed = upper
				}
				// optTrans >= note + t - 96 (to keep adjusted <= 96)
				lower := note + t - 96
				if lower > minAllowed {
					minAllowed = lower
				}
			}
		}

		// Count transpose values within valid range
		transposeCounts := make(map[int]int)
		for ch := 0; ch < 3; ch++ {
			src := allStreams[ch]
			for row := startRow; row < endRow; row++ {
				note := int(src[row*3] & 0x7F)
				if note == 0 || note == 0x61 {
					continue
				}
				t := int(getTranspose(ch, row))
				if t >= minAllowed && t <= maxAllowed {
					transposeCounts[t]++
				}
			}
		}

		// Pick most common transpose within valid range
		bestTrans := 0
		bestCount := -1
		for t, count := range transposeCounts {
			if count > bestCount {
				bestCount = count
				bestTrans = t
			}
		}
		// If no valid transpose found, use midpoint of allowed range
		if bestCount < 0 {
			bestTrans = (minAllowed + maxAllowed) / 2
		}
		optimalTranspose[patIdx] = bestTrans
	}

	// Count how many notes need adjustment
	adjustedCount := 0
	unadjustedCount := 0
	for patIdx := 0; patIdx < numPatterns; patIdx++ {
		startRow := patIdx * 64
		endRow := startRow + 64
		if endRow > numRows {
			endRow = numRows
		}
		optTrans := optimalTranspose[patIdx]
		for ch := 0; ch < 3; ch++ {
			src := allStreams[ch]
			for row := startRow; row < endRow; row++ {
				note := int(src[row*3] & 0x7F)
				if note == 0 || note == 0x61 {
					continue
				}
				origTrans := int(getTranspose(ch, row))
				if origTrans != optTrans {
					adjustedCount++
				} else {
					unadjustedCount++
				}
			}
		}
	}

	// Build streams with SPARSE note encoding (note value + duration)
	// Notes are pre-transposed, no separate transpose streams needed
	var streams []intStream
	var sparseNoteData [3][]int
	var sparseNoteDur [3][]int
	var instStreamData [3][]int
	var fxStreamData [3][]int

	for ch := 0; ch < 3; ch++ {
		src := allStreams[ch]
		var noteVals []int  // Combined: (note << 5) | inst
		var noteDurs []int
		instData := make([]int, 0, numRows) // Still needed for row-based decoding
		fxData := make([]int, 0, numRows)
		gap := 0

		for row := 0; row < numRows; row++ {
			off := row * 3
			noteByte, instEff, param := src[off], src[off+1], src[off+2]
			rawNote := int(noteByte & 0x7F)
			inst := int(instEff & 0x1F)
			effect := int((instEff >> 5) | ((noteByte >> 4) & 0x08))
			fxParam := (effect << 8) | int(param)

			// DON'T pre-apply transpose - it must be applied at runtime per-frame
			// because the player uses current order's transpose for arp calculations
			note := rawNote

			// Sparse note encoding: combine note+inst since inst only set with notes
			// Format: (note << 5) | inst (note=0-127, inst=0-31)
			if note == 0 {
				gap++
			} else {
				noteDurs = append(noteDurs, gap)
				noteVals = append(noteVals, (note<<5)|inst) // Combined note+inst
				gap = 0
			}

			// Keep row-based inst for decoder validation
			instData = append(instData, inst)
			fxData = append(fxData, fxParam)
		}

		// Handle trailing gap
		if gap > 0 {
			noteDurs = append(noteDurs, gap)
			noteVals = append(noteVals, 0) // Sentinel for end (note=0, inst=0)
		}

		sparseNoteData[ch] = noteVals
		sparseNoteDur[ch] = noteDurs
		instStreamData[ch] = instData
		fxStreamData[ch] = fxData
	}


	// Analyze note value distribution (extract note from combined value)
	noteFreq := make(map[int]int)
	for ch := 0; ch < 3; ch++ {
		for _, combined := range sparseNoteData[ch] {
			if combined > 0 {
				note := combined >> 5 // Extract note from (note<<5)|inst
				noteFreq[note]++
			}
		}
	}
	type noteCount struct {
		note, count int
	}
	var sorted []noteCount
	for n, c := range noteFreq {
		sorted = append(sorted, noteCount{n, c})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].count > sorted[j].count
	})

	// Build note-to-index lookup table (frequency sorted)
	noteToIdx := make(map[int]int)
	idxToNote := make([]int, len(sorted)+1) // +1 for index 0 (unused/sentinel)
	idxToNote[0] = 0                         // Index 0 -> note 0 (no note)
	for i, nc := range sorted {
		noteToIdx[nc.note] = i + 1 // +1 because 0 is reserved for "no note"
		idxToNote[i+1] = nc.note
	}

	// Create frequency-remapped note streams
	// NoteVal now contains (note<<5)|inst, remap the note part only
	var remappedNoteData [3][]int
	for ch := 0; ch < 3; ch++ {
		remapped := make([]int, len(sparseNoteData[ch]))
		for i, combined := range sparseNoteData[ch] {
			if combined == 0 {
				remapped[i] = 0 // Keep 0 as 0 (sentinel in sparse encoding)
			} else {
				note := combined >> 5
				inst := combined & 0x1F
				remappedNote := noteToIdx[note]
				remapped[i] = (remappedNote << 5) | inst // Recombine with inst
			}
		}
		remappedNoteData[ch] = remapped
	}

	// Build stream array - Notes first (Fx added later after FEx conversion)
	// Layout: Ch0-2(NoteVal,NoteDur), Ch0-2(TransV,TransD), Ch0-2(FxV,FxD), TblData
	// Note: NoteVal contains (noteIdx<<5)|inst - inst combined with notes
	for ch := 0; ch < 3; ch++ {
		streams = append(streams, intStream{remappedNoteData[ch], 3, fmt.Sprintf("Ch%d NoteVal", ch), true, 1024})
		streams = append(streams, intStream{sparseNoteDur[ch], 1, fmt.Sprintf("Ch%d NoteDur", ch), true, 512})
	}

	// Build transpose data for sparse encoding
	numOrders := len(allTranspose[0])
	var transposeData [3][]int
	for ch := 0; ch < 3; ch++ {
		transposeData[ch] = make([]int, numOrders)
		for i := 0; i < numOrders && i < len(allTranspose[ch]); i++ {
			transposeData[ch][i] = int(allTranspose[ch][i])
		}
	}

	// Build sparse transpose streams (TransVal + TransDur)
	// Use zigzag encoding for signed values (no LUT needed)
	// Note: TransV=0 (transpose by 0 semitones) is meaningful data, unlike InstV where 0 means "no trigger"
	var sparseTransVal [3][]int
	var sparseTransDur [3][]int
	for ch := 0; ch < 3; ch++ {
		// Build sparse encoding: TransVal[i] = value, TransDur[i] = how long it lasts
		i := 0
		for i < len(transposeData[ch]) {
			val := transposeData[ch][i]
			dur := 1
			for i+dur < len(transposeData[ch]) && transposeData[ch][i+dur] == val {
				dur++
			}
			// Zigzag encode: 0->0, -1->1, 1->2, -2->3, 2->4, ...
			zigzag := val * 2
			if val < 0 {
				zigzag = -val*2 - 1
			}
			sparseTransVal[ch] = append(sparseTransVal[ch], zigzag)
			sparseTransDur[ch] = append(sparseTransDur[ch], dur)
			i += dur
		}
		streams = append(streams, intStream{sparseTransVal[ch], 0, fmt.Sprintf("Ch%d TransV", ch), true, 128})
		streams = append(streams, intStream{sparseTransDur[ch], 0, fmt.Sprintf("Ch%d TransD", ch), true, 128})
	}

	// Add table streams (concatenated per song)
	var instData, filterData, waveData, arpData []int
	for _, st := range allSongTables {
		for _, b := range st.instruments {
			instData = append(instData, int(b))
		}
		for _, b := range st.filter {
			filterData = append(filterData, int(b))
		}
		for _, b := range st.wave {
			waveData = append(waveData, int(b))
		}
		for _, b := range st.arp {
			arpData = append(arpData, int(b))
		}
	}
	// Build incremental table streams with slot reuse
	type incrLoadEvent struct {
		songIdx    int
		row        int
		slotID     int
		origInstID int
		instDef    []byte
		waveData   []byte
		arpData    []byte
		filtData   []byte
	}
	var incrLoadEvents []incrLoadEvent
	// Map from (songIdx<<8 | origInstID) -> slotID for converting inst streams
	instToSlot := make(map[int]int)

	for songIdx := 0; songIdx < len(songBoundaries)-1; songIdx++ {
		startRow := songBoundaries[songIdx]
		endRow := songBoundaries[songIdx+1]
		st := allSongTables[songIdx]

		// Track instrument lifetimes in this song
		// An instrument is active from when it's set until it's REPLACED by another instrument
		// Track per-channel usage intervals, then merge to find overall lifetime
		type instLifetime struct {
			origID   int
			firstRow int
			lastRow  int
			hasFEx   bool
		}
		instLifetimes := make(map[int]*instLifetime)
		songLen := endRow - startRow

		// Track per-channel usage intervals: inst -> list of (startRow, endRow exclusive)
		type interval struct{ start, end int }
		instIntervals := make(map[int][]interval)

		var currentInst [3]int
		var instStartRow [3]int

		for row := startRow; row < endRow; row++ {
			localRow := row - startRow
			for ch := 0; ch < 3; ch++ {
				noteByte := allStreams[ch][row*3]
				instEff := allStreams[ch][row*3+1]
				param := allStreams[ch][row*3+2]
				inst := int(instEff & 0x1F)
				effect := int((instEff >> 5) | ((noteByte >> 4) & 0x08))

				// When a new instrument is set, it replaces the previous one on this channel
				if inst > 0 && inst != currentInst[ch] {
					// End previous instrument's interval on this channel
					if currentInst[ch] > 0 {
						instIntervals[currentInst[ch]] = append(instIntervals[currentInst[ch]],
							interval{instStartRow[ch], localRow})
					}
					currentInst[ch] = inst
					instStartRow[ch] = localRow
				}

				// Track filter trigger effect (effect 3 = Fxx, $9x sub-effect = old FEx)
				if effect == 3 && (param&0xF0) == 0x90 { // Effect 3 = Fxx, $9x = filter trigger
					fxInst := int(param & 0x0F)
					if fxInst > 0 {
						if instLifetimes[fxInst] == nil {
							instLifetimes[fxInst] = &instLifetime{origID: fxInst, firstRow: localRow, lastRow: localRow, hasFEx: true}
						} else {
							instLifetimes[fxInst].hasFEx = true
							if localRow < instLifetimes[fxInst].firstRow {
								instLifetimes[fxInst].firstRow = localRow
							}
							if localRow > instLifetimes[fxInst].lastRow {
								instLifetimes[fxInst].lastRow = localRow
							}
						}
					}
				}
			}
		}

		// End all instruments still active at song boundary
		for ch := 0; ch < 3; ch++ {
			if currentInst[ch] > 0 {
				instIntervals[currentInst[ch]] = append(instIntervals[currentInst[ch]],
					interval{instStartRow[ch], songLen})
			}
		}

		// Merge intervals to find overall lifetime for each instrument
		for inst, intervals := range instIntervals {
			minStart, maxEnd := songLen, 0
			for _, iv := range intervals {
				if iv.start < minStart {
					minStart = iv.start
				}
				if iv.end > maxEnd {
					maxEnd = iv.end
				}
			}
			if instLifetimes[inst] == nil {
				instLifetimes[inst] = &instLifetime{origID: inst, firstRow: minStart, lastRow: maxEnd - 1}
			} else {
				if minStart < instLifetimes[inst].firstRow {
					instLifetimes[inst].firstRow = minStart
				}
				if maxEnd-1 > instLifetimes[inst].lastRow {
					instLifetimes[inst].lastRow = maxEnd - 1
				}
			}
		}

		// Sort for slot allocation: FEx-referenced instruments first (need slots 0-14),
		// then by first use for others
		var byFirstUse []*instLifetime
		for _, lt := range instLifetimes {
			byFirstUse = append(byFirstUse, lt)
		}
		sort.Slice(byFirstUse, func(i, j int) bool {
			// FEx instruments first, then by firstRow
			if byFirstUse[i].hasFEx != byFirstUse[j].hasFEx {
				return byFirstUse[i].hasFEx
			}
			return byFirstUse[i].firstRow < byFirstUse[j].firstRow
		})

		// Allocate slots with reuse
		type slot struct {
			freeAfter int
		}
		slots := make([]slot, 0)

		for _, lt := range byFirstUse {
			// Allocate a new slot for each instrument (no reuse for now)
			// Slot reuse would require tracking when channels release instruments, not just when they're set
			slotID := len(slots)
			slots = append(slots, slot{freeAfter: lt.lastRow})

			// Record mapping for this song's instrument -> slot
			instToSlot[(songIdx<<8)|lt.origID] = slotID

			// Create load event
			off := lt.origID * 16
			if off+16 <= len(st.instruments) {
				origInstDef := st.instruments[off : off+16]
				waveS := int(origInstDef[INST_WAVESTART])
				waveE := int(origInstDef[INST_WAVEEND])
				waveL := int(origInstDef[INST_WAVELOOP])
				arpS := int(origInstDef[INST_ARPSTART])
				arpE := int(origInstDef[INST_ARPEND])
				arpL := int(origInstDef[INST_ARPLOOP])
				filtS := int(origInstDef[INST_FILTSTART])
				filtE := int(origInstDef[INST_FILTEND])
				filtL := int(origInstDef[INST_FILTLOOP])

				var waveDat, arpDat, filtDat []byte
				// Expand data range to include loop point (both before start and after end)
				// The player can read from start to end, then jump to loop - need all that data
				waveActualS := waveS
				waveActualE := waveE
				if waveL < waveS {
					waveActualS = waveL
				}
				if waveL > waveE {
					waveActualE = waveL
				}
				arpActualS := arpS
				arpActualE := arpE
				if arpL < arpS {
					arpActualS = arpL
				}
				if arpL > arpE {
					arpActualE = arpL
				}
				filtActualS := filtS
				filtActualE := filtE
				if filtL < filtS {
					filtActualS = filtL
				}
				if filtL > filtE {
					filtActualE = filtL
				}

				if waveActualE >= waveActualS && waveActualE < len(st.wave) {
					waveDat = st.wave[waveActualS : waveActualE+1]
				}
				if arpActualE >= arpActualS && arpActualE < len(st.arp) {
					arpDat = st.arp[arpActualS : arpActualE+1]
				}
				if filtActualE >= filtActualS && filtActualE < len(st.filter) {
					filtDat = st.filter[filtActualS : filtActualE+1]
				}

				// Create modified instDef with relative indices (0-based for each slot)
				instDef := make([]byte, 16)
				copy(instDef, origInstDef)
				if len(waveDat) > 0 {
					// Start is relative to expanded range; playback starts at waveS offset
					instDef[INST_WAVESTART] = byte(waveS - waveActualS)
					// End is waveE relative to actual start (not length-1 because we may have loop data after)
					instDef[INST_WAVEEND] = byte(waveE - waveActualS)
					// Loop is relative to actual start
					instDef[INST_WAVELOOP] = byte(waveL - waveActualS)
				}
				if len(arpDat) > 0 {
					instDef[INST_ARPSTART] = byte(arpS - arpActualS)
					instDef[INST_ARPEND] = byte(arpE - arpActualS)
					instDef[INST_ARPLOOP] = byte(arpL - arpActualS)
				}
				if len(filtDat) > 0 {
					instDef[INST_FILTSTART] = byte(filtS - filtActualS)
					instDef[INST_FILTEND] = byte(filtE - filtActualS)
					instDef[INST_FILTLOOP] = byte(filtL - filtActualS)
				}

				incrLoadEvents = append(incrLoadEvents, incrLoadEvent{
					songIdx:    songIdx,
					row:        startRow + lt.firstRow,
					slotID:     slotID,
					origInstID: lt.origID,
					instDef:    instDef,
					waveData:   waveDat,
					arpData:    arpDat,
					filtData:   filtDat,
				})

			}
		}
	}

	// Convert inst streams from original instrument IDs to slot IDs
	for ch := 0; ch < 3; ch++ {
		for row := 0; row < len(instStreamData[ch]); row++ {
			origInst := instStreamData[ch][row]
			if origInst > 0 {
				// Find which song this row belongs to
				songIdx := 0
				for s := 0; s < len(songBoundaries)-1; s++ {
					if row >= songBoundaries[s] && row < songBoundaries[s+1] {
						songIdx = s
						break
					}
				}
				// Look up slot for this song's instrument
				key := (songIdx << 8) | origInst
				if slot, ok := instToSlot[key]; ok {
					instStreamData[ch][row] = slot + 1 // +1 so 0 means "no trigger this row"
				}
			}
		}
	}

	// Rebuild NoteVal with slot-converted inst (after inst-to-slot conversion)
	sparseNoteData = [3][]int{}
	sparseNoteDur = [3][]int{}
	for ch := 0; ch < 3; ch++ {
		src := allStreams[ch]
		var noteVals []int
		var noteDurs []int
		gap := 0
		for row := 0; row < numRows; row++ {
			rawNote := int(src[row*3] & 0x7F)
			slotInst := instStreamData[ch][row] // Now slot-converted
			if rawNote == 0 {
				gap++
			} else {
				noteDurs = append(noteDurs, gap)
				noteVals = append(noteVals, (rawNote<<5)|slotInst)
				gap = 0
			}
		}
		if gap > 0 {
			noteDurs = append(noteDurs, gap)
			noteVals = append(noteVals, 0)
		}
		sparseNoteData[ch] = noteVals
		sparseNoteDur[ch] = noteDurs
		// Rebuild remapped note data with slot-converted inst
		remapped := make([]int, len(noteVals))
		for i, combined := range noteVals {
			if combined == 0 {
				remapped[i] = 0
			} else {
				note := combined >> 5
				inst := combined & 0x1F
				remappedNote := noteToIdx[note]
				remapped[i] = (remappedNote << 5) | inst
			}
		}
		// Update streams with new data
		streams[ch*2].data = remapped     // NoteVal
		streams[ch*2+1].data = noteDurs   // NoteDur
	}

	// Convert FEx effect parameters from original instrument IDs to slot IDs
	fexCount := 0
	for ch := 0; ch < 3; ch++ {
		for row := 0; row < len(fxStreamData[ch]); row++ {
			fxParam := fxStreamData[ch][row]
			effect := (fxParam >> 8) & 0xF
			paramHi := (fxParam >> 4) & 0xF
			paramLo := fxParam & 0xF
			// Filter trigger effect: effect=3 (Fxx), param=$9x where x is instrument ID (old FEx)
			if effect == 3 && paramHi == 0x9 && paramLo > 0 { // Effect 3 = Fxx, $9x = filter trigger
				fexCount++
				// Find which song this row belongs to
				songIdx := 0
				for s := 0; s < len(songBoundaries)-1; s++ {
					if row >= songBoundaries[s] && row < songBoundaries[s+1] {
						songIdx = s
						break
					}
				}
				// Look up slot for this song's instrument
				key := (songIdx << 8) | paramLo
				if slot, ok := instToSlot[key]; ok && slot+1 <= 15 {
					// Replace paramLo with slot+1 (only if it fits in 4 bits)
					fxStreamData[ch][row] = (fxParam & 0xFF0) | (slot + 1)
				} else if slot, ok := instToSlot[key]; ok {
					fmt.Printf("WARNING: FEx slot overflow - song%d inst%d -> slot%d (>14)\n", songIdx, paramLo, slot)
				} else {
					fmt.Printf("WARNING: FEx inst not found - song%d inst%d\n", songIdx, paramLo)
				}
			}
		}
	}

	// Now build sparse Fx encoding (after FEx slot conversion)
	// Only store non-zero effects: FxV = values, FxD = interleaved [gap, duration]
	// (Unlike InstV which is point triggers, effects have durations so need gap+dur)
	var sparseFxVal [3][]int // (effect<<8)|param, only non-zero
	var sparseFxEff [3][]int // Effect number (1-12)
	var sparseFxPar [3][]int // Effect parameter
	var sparseFxDur [3][]int // Interleaved [gap, duration, gap, duration, ...]
	fmt.Println("\nFx sparse encoding (after FEx conversion):")
	fxRLEBits := 0
	fxSparseBits := 0
	for ch := 0; ch < 3; ch++ {
		zeros := 0
		rleEntries := 0
		for _, v := range fxStreamData[ch] {
			if v == 0 {
				zeros++
			}
		}
		// Build RLE for comparison
		i := 0
		for i < len(fxStreamData[ch]) {
			val := fxStreamData[ch][i]
			dur := 1
			for i+dur < len(fxStreamData[ch]) && fxStreamData[ch][i+dur] == val {
				dur++
			}
			fxRLEBits += expGolombBits(val, 0) + expGolombBits(dur, 0)
			rleEntries++
			i += dur
		}
		// Build sparse encoding: only non-zero effects with gap+duration
		i = 0
		gap := 0
		for i < len(fxStreamData[ch]) {
			val := fxStreamData[ch][i]
			dur := 1
			for i+dur < len(fxStreamData[ch]) && fxStreamData[ch][i+dur] == val {
				dur++
			}
			if val == 0 {
				gap += dur
			} else {
				sparseFxVal[ch] = append(sparseFxVal[ch], val)
				sparseFxEff[ch] = append(sparseFxEff[ch], (val>>8)&0xF)
				sparseFxPar[ch] = append(sparseFxPar[ch], val&0xFF)
				sparseFxDur[ch] = append(sparseFxDur[ch], gap)
				sparseFxDur[ch] = append(sparseFxDur[ch], dur)
				fxSparseBits += expGolombBits(val, 0) + expGolombBits(gap, 0) + expGolombBits(dur, 0)
				gap = 0
			}
			i += dur
		}
		fmt.Printf("  Ch%d: %d rows, %d zeros (%.1f%%), %d RLE, %d sparse (gap+dur)\n",
			ch, len(fxStreamData[ch]), zeros, 100.0*float64(zeros)/float64(len(fxStreamData[ch])),
			rleEntries, len(sparseFxVal[ch]))
		// Add Fx streams
		streams = append(streams, intStream{sparseFxVal[ch], 0, fmt.Sprintf("Ch%d FxV", ch), true, 512})
		streams = append(streams, intStream{sparseFxDur[ch], 0, fmt.Sprintf("Ch%d FxD", ch), true, 512})
	}
	fmt.Printf("  Total: RLE %d bits, sparse %d bits (%+d bits = %+d bytes)\n",
		fxRLEBits, fxSparseBits, fxSparseBits-fxRLEBits, (fxSparseBits-fxRLEBits)/8)
	// Verify no effect=0 with param>0
	effect0NonZeroParam := 0
	for ch := 0; ch < 3; ch++ {
		for _, val := range fxStreamData[ch] {
			if (val>>8) == 0 && (val&0xFF) != 0 {
				effect0NonZeroParam++
			}
		}
	}
	if effect0NonZeroParam > 0 {
		fmt.Printf("  WARNING: %d rows have effect=0 with param>0\n", effect0NonZeroParam)
	}

	// Now build sparse inst encoding (after inst-to-slot conversion)
	// NEW FORMAT: Only emit non-zero instrument changes with delta timing
	// InstD = delta from last emit (or row 0), InstV = non-zero slot
	var sparseInstVal [3][]int
	var sparseInstDur [3][]int
	fmt.Println("\nInst stream sparsity analysis (after slot conversion):")
	totalCurrentBits := 0
	totalSparseBits := 0
	totalOldEntries := 0
	for ch := 0; ch < 3; ch++ {
		total := len(instStreamData[ch])
		// Build sparse encoding - only emit non-zero values
		lastEmitRow := 0
		for i := 0; i < len(instStreamData[ch]); i++ {
			val := instStreamData[ch][i]
			if val != 0 {
				delta := i - lastEmitRow
				sparseInstVal[ch] = append(sparseInstVal[ch], val-1) // Store 0-30 instead of 1-31
				sparseInstDur[ch] = append(sparseInstDur[ch], delta)
				lastEmitRow = i
			}
		}
		// Count old-style entries for comparison
		oldEntries := 0
		i := 0
		for i < len(instStreamData[ch]) {
			val := instStreamData[ch][i]
			dur := 1
			for i+dur < len(instStreamData[ch]) && instStreamData[ch][i+dur] == val {
				dur++
			}
			oldEntries++
			i += dur
		}
		totalOldEntries += oldEntries
		// Calculate bit costs
		currentBits := 0
		for _, v := range instStreamData[ch] {
			currentBits += expGolombBits(v, 0)
		}
		sparseBits := 0
		for j := 0; j < len(sparseInstVal[ch]); j++ {
			sparseBits += expGolombBits(sparseInstVal[ch][j], 0)
			sparseBits += expGolombBits(sparseInstDur[ch][j], 0)
		}
		fmt.Printf("  Ch%d: %d rows, %d entries (was %d), sparse %d bits\n",
			ch, total, len(sparseInstVal[ch]), oldEntries, sparseBits)
		totalCurrentBits += currentBits
		totalSparseBits += sparseBits
		// InstV/InstD no longer separate - inst is combined with NoteVal
	}
	fmt.Printf("  Total: %d entries (was %d) - now combined with NoteVal\n", len(sparseInstVal[0])+len(sparseInstVal[1])+len(sparseInstVal[2]), totalOldEntries)

	// Sort incrLoadEvents by row for correct delta encoding
	sort.Slice(incrLoadEvents, func(i, j int) bool {
		return incrLoadEvents[i].row < incrLoadEvents[j].row
	})

	// Build single merged incremental table stream (expgol encoded)
	// Format per event: delta, slot, instDef[16], waveLen, waveData..., arpLen, arpData..., filtLen, filtData...
	// Special: delta=65535 is a song reset marker followed by: frameCount, rowBase (resets all state)
	const songResetMarker = 65535
	var incrTableStream []int

	// Compute cumulative frame counts for reset markers
	var cumFrames []int
	cumF := 0
	for _, pt := range partTimes {
		cumF += int(pt)
		cumFrames = append(cumFrames, cumF)
	}

	incrPrevRow := 0
	prevSongIdx := -1
	for _, ev := range incrLoadEvents {
		// Insert song reset marker at song boundaries
		if ev.songIdx != prevSongIdx {
			if prevSongIdx != -1 {
				// Not the first song - insert reset marker with frame count and row base
				incrTableStream = append(incrTableStream, songResetMarker)
				incrTableStream = append(incrTableStream, cumFrames[prevSongIdx]) // Frame at which to reset
				incrTableStream = append(incrTableStream, ev.row)                 // Row base for new song
				incrPrevRow = ev.row
			}
			prevSongIdx = ev.songIdx
		}
		// Delta from previous event
		incrTableStream = append(incrTableStream, ev.row-incrPrevRow)
		incrPrevRow = ev.row
		// Slot ID
		incrTableStream = append(incrTableStream, ev.slotID)
		// InstDef (16 bytes)
		for _, b := range ev.instDef {
			incrTableStream = append(incrTableStream, int(b))
		}
		// Wave: length then data
		incrTableStream = append(incrTableStream, len(ev.waveData))
		for _, b := range ev.waveData {
			incrTableStream = append(incrTableStream, int(b))
		}
		// Arp: length then data
		incrTableStream = append(incrTableStream, len(ev.arpData))
		for _, b := range ev.arpData {
			incrTableStream = append(incrTableStream, int(b))
		}
		// Filter: length then data
		incrTableStream = append(incrTableStream, len(ev.filtData))
		for _, b := range ev.filtData {
			incrTableStream = append(incrTableStream, int(b))
		}
	}

	// Add final reset marker with total frame count (signals end of stream)
	incrTableStream = append(incrTableStream, songResetMarker)
	incrTableStream = append(incrTableStream, cumFrames[len(cumFrames)-1]) // Total frames
	incrTableStream = append(incrTableStream, numRows)                     // End row

	// Single merged table stream with expgol encoding
	streams = append(streams, intStream{incrTableStream, 0, "TblData", true, 512})


	// Validate sparse stream encoding: decode back to row-based and verify
	// Stream layout: Notes at 0-5, Trans at 6-11, Fx at 12-17, Inst at 18-23, TblData at 24
	// Decode sparse notes to row-based
	var decodedNotes [3][]int
	for ch := 0; ch < 3; ch++ {
		noteVals := streams[ch*2].data   // NoteVal at 0,2,4
		noteDurs := streams[ch*2+1].data // NoteDur at 1,3,5
		decoded := make([]int, numRows)

		row := 0
		for i := 0; i < len(noteVals) && row < numRows; i++ {
			// Skip 'dur' rows (zeros)
			dur := 0
			if i < len(noteDurs) {
				dur = noteDurs[i]
			}
			row += dur
			// Place the note index (extract from combined value)
			if row < numRows && noteVals[i] != 0 {
				combined := noteVals[i]
				decoded[row] = combined >> 5 // Extract note index from (noteIdx<<5)|inst
				row++
			}
		}
		decodedNotes[ch] = decoded
	}

	// Verify decoded notes match original raw notes (convert from freq-sorted index)
	verifyErrors := 0
	for ch := 0; ch < 3; ch++ {
		for row := 0; row < numRows; row++ {
			origRawNote := int(allStreams[ch][row*3] & 0x7F)
			decodedIdx := decodedNotes[ch][row]
			// Convert index back to note value
			decodedNote := 0
			if decodedIdx > 0 && decodedIdx < len(idxToNote) {
				decodedNote = idxToNote[decodedIdx]
			}
			if origRawNote != decodedNote {
				verifyErrors++
				if verifyErrors <= 5 {
					fmt.Printf("  Verify error ch%d row%d: expected %d, got idx=%d (note=%d)\n",
						ch, row, origRawNote, decodedIdx, decodedNote)
				}
			}
		}
	}
	if verifyErrors > 0 {
		fmt.Printf("  Sparse decode validation: FAIL (%d errors)\n", verifyErrors)
	} else {
		fmt.Println("  Sparse decode validation: PASS")
	}

	// Run VM validation using reconstructed stream data
	// Reconstruct row-based data from sparse streams
	var streamByteData [3][]byte
	for ch := 0; ch < 3; ch++ {
		// instStreamData and fxStreamData are already row-based (before sparse encoding)
		fxStream := fxStreamData[ch] // Fx (row-based)
		streamByteData[ch] = make([]byte, numRows*3)
		for row := 0; row < numRows; row++ {
			// Note is from decoded sparse stream (freq-sorted index)
			noteIdx := decodedNotes[ch][row]
			note := 0
			if noteIdx > 0 && noteIdx < len(idxToNote) {
				note = idxToNote[noteIdx]
			}
			inst := instStreamData[ch][row]
			fxParam := fxStream[row]
			effect := (fxParam >> 8) & 0xF
			param := fxParam & 0xFF
			// For streamByteData, we need raw note (without transpose) since the
			// simulation will apply transpose. But our notes are pre-transposed.
			// We need to reverse the transpose for the simulation.
			rawNote := note
			if note != 0 && note != 0x61 {
				trans := int(getTranspose(ch, row))
				rawNote = note - trans
			}
			// Reconstruct bytes: note | (effect_high_bit << 4), inst | (effect_low_3 << 5), param
			streamByteData[ch][row*3] = byte(rawNote&0x7F) | byte((effect&0x8)<<4)
			streamByteData[ch][row*3+1] = byte(inst&0x1F) | byte((effect&0x7)<<5)
			streamByteData[ch][row*3+2] = byte(param)
		}
	}

	// Reconstruct transpose: one per order, same for all 3 channels
	var streamTranspose [3][]int8
	for ch := 0; ch < 3; ch++ {
		streamTranspose[ch] = make([]int8, len(allTranspose[0]))
	}
	// Map pattern indices back to order indices (per-channel transpose)
	orderIdx := 0
	for patIdx := 0; patIdx < numPatterns && orderIdx < len(streamTranspose[0]); patIdx++ {
		// Each pattern is 64 rows = 1 order
		if orderIdx < len(streamTranspose[0]) {
			for ch := 0; ch < 3; ch++ {
				streamTranspose[ch][orderIdx] = int8(transposeData[ch][patIdx])
			}
			orderIdx++
		}
	}
	// Fill remaining orders if any
	for orderIdx < len(streamTranspose[0]) {
		for ch := 0; ch < 3; ch++ {
			lastTrans := int8(0)
			if numPatterns > 0 {
				lastTrans = int8(transposeData[ch][numPatterns-1])
			}
			streamTranspose[ch][orderIdx] = lastTrans
		}
		orderIdx++
	}

	// Use original table data for validation (allSongTables)
	var streamTableDatas []SongTableData
	numSongs := len(songBoundaries) - 1
	for song := 0; song < numSongs; song++ {
		var td SongTableData
		st := allSongTables[song]

		td.InstData = st.instruments
		td.WaveTable = st.wave
		td.ArpTable = st.arp

		streamTableDatas = append(streamTableDatas, td)
	}

	// Skip table verification since we're using original data directly
	// (incremental format validated separately)
	// Run stream-only simulation and compare against VM output
	streamRegs := runStreamOnlySimulation(streams, idxToNote)

	// Compare all SID registers: Freq, PW, Control, AD, SR, Filter, Volume
	freqMismatches := 0
	pwMismatches := 0
	controlMismatches := 0
	adMismatches := 0
	srMismatches := 0
	filterLoMismatches := 0
	filterHiMismatches := 0
	filterResMismatches := 0
	filterVolMismatches := 0
	totalFrames := len(vmRegs.Freq[0])
	streamFrames := len(streamRegs.Freq[0])

	if totalFrames != streamFrames {
		fmt.Printf("  WARNING: Frame count mismatch: VM=%d, Stream=%d\n", totalFrames, streamFrames)
	}

	compareFrames := totalFrames
	if streamFrames < compareFrames {
		compareFrames = streamFrames
	}

	for ch := 0; ch < 3; ch++ {
		for i := 0; i < compareFrames; i++ {
			if vmRegs.Freq[ch][i] != streamRegs.Freq[ch][i] {
				freqMismatches++
			}
			if vmRegs.PW[ch][i] != streamRegs.PW[ch][i] {
				pwMismatches++
			}
			if vmRegs.Control[ch][i] != streamRegs.Control[ch][i] {
				controlMismatches++
			}
			if vmRegs.AD[ch][i] != streamRegs.AD[ch][i] {
				adMismatches++
			}
			if vmRegs.SR[ch][i] != streamRegs.SR[ch][i] {
				srMismatches++
			}
		}
	}

	// Compare filter/volume registers (global, not per-channel)
	for i := 0; i < compareFrames; i++ {
		if vmRegs.FilterLo[i] != streamRegs.FilterLo[i] {
			filterLoMismatches++
		}
		if vmRegs.FilterHi[i] != streamRegs.FilterHi[i] {
			filterHiMismatches++
		}
		if vmRegs.FilterRes[i] != streamRegs.FilterRes[i] {
			filterResMismatches++
		}
		if vmRegs.FilterVol[i] != streamRegs.FilterVol[i] {
			filterVolMismatches++
		}
	}

	totalMismatches := freqMismatches + pwMismatches + controlMismatches + adMismatches + srMismatches +
		filterLoMismatches + filterHiMismatches + filterResMismatches + filterVolMismatches
	if totalMismatches == 0 {
		fmt.Println("  Stream vs VM comparison: PASS (all SID registers D400-D418 match)")
	} else {
		fmt.Printf("  Stream vs VM comparison: %d total mismatches\n", totalMismatches)
		fmt.Printf("    Freq: %d, PW: %d, Control: %d, AD: %d, SR: %d\n",
			freqMismatches, pwMismatches, controlMismatches, adMismatches, srMismatches)
		fmt.Printf("    FilterLo: %d, FilterHi: %d, FilterRes: %d, FilterVol: %d\n",
			filterLoMismatches, filterHiMismatches, filterResMismatches, filterVolMismatches)
	}

	// Verify stream encoding matches VM (all SID registers $D400-$D418)
	simMismatch := 0
	firstMismatchPrinted := 0
	for ch := 0; ch < 3; ch++ {
		for i := 0; i < compareFrames; i++ {
			if vmRegs.Freq[ch][i] != streamRegs.Freq[ch][i] {
				simMismatch++
			}
			if vmRegs.PW[ch][i] != streamRegs.PW[ch][i] {
				simMismatch++
			}
			if vmRegs.Control[ch][i] != streamRegs.Control[ch][i] {
				simMismatch++
				if firstMismatchPrinted < 5 {
					fmt.Printf("    Control mismatch ch%d frame%d: vm=%02X stream=%02X\n",
						ch, i, vmRegs.Control[ch][i], streamRegs.Control[ch][i])
					firstMismatchPrinted++
				}
			}
			if vmRegs.AD[ch][i] != streamRegs.AD[ch][i] {
				simMismatch++
			}
			if vmRegs.SR[ch][i] != streamRegs.SR[ch][i] {
				simMismatch++
			}
		}
	}
	for i := 0; i < compareFrames; i++ {
		if vmRegs.FilterLo[i] != streamRegs.FilterLo[i] {
			simMismatch++
		}
		if vmRegs.FilterHi[i] != streamRegs.FilterHi[i] {
			simMismatch++
			if firstMismatchPrinted < 10 && firstMismatchPrinted >= 5 {
				fmt.Printf("    FilterHi mismatch frame%d: vm=%02X stream=%02X\n",
					i, vmRegs.FilterHi[i], streamRegs.FilterHi[i])
				firstMismatchPrinted++
			}
		}
		if vmRegs.FilterRes[i] != streamRegs.FilterRes[i] {
			simMismatch++
		}
		if vmRegs.FilterVol[i] != streamRegs.FilterVol[i] {
			simMismatch++
		}
	}
	validationFailed := false
	if simMismatch == 0 {
		fmt.Println("  Stream encoding: PASS (all SID registers $D400-$D418 match)")
	} else {
		fmt.Printf("  Stream encoding: FAIL (%d mismatches in SID registers)\n", simMismatch)
		validationFailed = true
	}

	// Bit-level DP for integer stream chunks
	type bitChoice struct {
		isBackref bool
		dist      int
		length    int
	}

	// DP function for a chunk with prefix
	dpChunk := func(data []int, prefix []int, bitWidth, window int) []bitChoice {
		combined := append(prefix, data...)
		prefixLen := len(prefix)
		n := len(combined)

		// Build hash table
		hashTable := make(map[int][]int)
		for i := 0; i < n; i++ {
			hashTable[combined[i]] = append(hashTable[combined[i]], i)
		}

		cost := make([]float64, n+1)
		choices := make([]bitChoice, n)

		literalCost := float64(1 + bitWidth)
		backrefCost := func(dist, length int) float64 {
			return float64(1 + expGolombBits(dist-1, optKDist) + expGolombBits(length-2, optKLen))
		}

		for pos := n - 1; pos >= prefixLen; pos-- {
			bestCost := literalCost + cost[pos+1]
			bestChoice := bitChoice{}

			minPos := pos - window
			if minPos < 0 {
				minPos = 0
			}

			if positions, ok := hashTable[combined[pos]]; ok {
				lo, hi := 0, len(positions)
				for lo < hi {
					mid := (lo + hi) / 2
					if positions[mid] < pos {
						lo = mid + 1
					} else {
						hi = mid
					}
				}

				checked := 0
				for i := lo - 1; i >= 0 && checked < 256; i-- {
					srcPos := positions[i]
					if srcPos < minPos {
						break
					}
					checked++

					dist := pos - srcPos
					maxLen := 0
					for pos+maxLen < n && combined[srcPos+(maxLen%dist)] == combined[pos+maxLen] {
						maxLen++
					}
					if maxLen >= 2 {
						lengths := []int{2, 3, 4}
						for l := 8; l <= maxLen; l *= 2 {
							lengths = append(lengths, l)
						}
						if maxLen > 4 {
							lengths = append(lengths, maxLen)
						}
						for _, length := range lengths {
							if length > maxLen {
								continue
							}
							c := backrefCost(dist, length) + cost[pos+length]
							if c < bestCost {
								bestCost = c
								bestChoice = bitChoice{true, dist, length}
							}
						}
					}
				}
			}

			// RLE check
			for dist := 1; dist <= 2 && dist <= pos; dist++ {
				if combined[pos-dist] != combined[pos] {
					continue
				}
				maxLen := 0
				for pos+maxLen < n && combined[pos-dist+(maxLen%dist)] == combined[pos+maxLen] {
					maxLen++
				}
				if maxLen >= 2 {
					lengths := []int{2, 3, 4}
					for l := 8; l <= maxLen; l *= 2 {
						lengths = append(lengths, l)
					}
					if maxLen > 4 {
						lengths = append(lengths, maxLen)
					}
					for _, length := range lengths {
						if length > maxLen {
							continue
						}
						c := backrefCost(dist, length) + cost[pos+length]
						if c < bestCost {
							bestCost = c
							bestChoice = bitChoice{true, dist, length}
						}
					}
				}
			}

			cost[pos] = bestCost
			choices[pos] = bestChoice
		}

		return choices[prefixLen:]
	}

	// Chunked parallel compression
	const numChunks = 16
	numStreams := len(streams)
	type chunkResult struct {
		streamIdx int
		chunkIdx  int
		choices   []bitChoice
		estBits   int
	}

	streamBitsAtomic := make([]atomic.Int64, numStreams)
	numWorkers := runtime.NumCPU()

	// Run all chunks in parallel
	allChunkResults := make([][]chunkResult, numStreams)
	for i := range allChunkResults {
		allChunkResults[i] = make([]chunkResult, numChunks)
	}

	// Build task list: chunks 15-1 first (full prefix), then chunk 0 last (no prefix)
	type task struct {
		sIdx, cIdx int
	}
	tasks := make([]task, 0, numStreams*numChunks)
	for cIdx := numChunks - 1; cIdx >= 1; cIdx-- {
		for sIdx := 0; sIdx < numStreams; sIdx++ {
			tasks = append(tasks, task{sIdx, cIdx})
		}
	}
	for sIdx := 0; sIdx < numStreams; sIdx++ {
		tasks = append(tasks, task{sIdx, 0})
	}

	// Worker pool with bounded concurrency
	taskChan := make(chan task, len(tasks))
	for _, t := range tasks {
		taskChan <- t
	}
	close(taskChan)

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range taskChan {
				sIdx, cIdx := t.sIdx, t.cIdx
				s := streams[sIdx]
				chunkSize := (len(s.data) + numChunks - 1) / numChunks

				start := cIdx * chunkSize
				end := start + chunkSize
				if end > len(s.data) {
					end = len(s.data)
				}
				if start >= len(s.data) {
					continue
				}

				chunkData := s.data[start:end]
				prefixStart := start - s.window
				if prefixStart < 0 {
					prefixStart = 0
				}
				prefix := s.data[prefixStart:start]

				choices := dpChunk(chunkData, prefix, s.bitWidth, s.window)

				estBits := 0
				pos := 0
				for pos < len(choices) {
					ch := choices[pos]
					if !ch.isBackref {
						estBits += 1 + s.bitWidth
						pos++
					} else {
						estBits += 1 + expGolombBits(ch.dist-1, optKDist) + expGolombBits(ch.length-2, optKLen)
						pos += ch.length
					}
				}
				allChunkResults[sIdx][cIdx] = chunkResult{sIdx, cIdx, choices, estBits}
				streamBitsAtomic[sIdx].Add(int64(estBits))
			}
		}()
	}
	wg.Wait()

	// Compute total bits per stream
	streamTotalBits := make([]int, numStreams)
	for sIdx := 0; sIdx < numStreams; sIdx++ {
		for cIdx := 0; cIdx < numChunks; cIdx++ {
			streamTotalBits[sIdx] += allChunkResults[sIdx][cIdx].estBits
		}
	}

	// Calculate totals
	var totalCompBits int
	for sIdx := 0; sIdx < numStreams; sIdx++ {
		totalCompBits += streamTotalBits[sIdx]
	}

	lookupTableBytes := len(idxToNote) - 1
	totalBytes := (totalCompBits+7)/8 + lookupTableBytes

	// Calculate buffer sizes
	bufferBytes := 0
	// Stream order: group by channel, then TblData
	// Layout: Notes at 0-5, Trans at 6-11, Fx at 12-17, Inst at 18-23, TblData at 24
	// Output grouped by channel:
	// Ch0: 0,1 (NoteVal,NoteDur) + 12,13 (FxV,FxD) + 18,19 (InstV,InstD) + 6,7 (TransV,TransD)
	// Ch1: 2,3 + 14,15 + 20,21 + 8,9
	// Ch2: 4,5 + 16,17 + 22,23 + 10,11
	// TblData: 24
	streamOrder := []int{0, 1, 12, 13, 18, 19, 6, 7, 2, 3, 14, 15, 20, 21, 8, 9, 4, 5, 16, 17, 22, 23, 10, 11, 24}
	for _, sIdx := range streamOrder {
		if sIdx >= len(streams) {
			continue
		}
		s := streams[sIdx]
		maxVal := 0
		for _, v := range s.data {
			if v > maxVal {
				maxVal = v
			}
		}
		bitsNeeded := 1
		for (1 << bitsNeeded) <= maxVal {
			bitsNeeded++
		}
		bytesPerElement := (bitsNeeded + 7) / 8
		if bytesPerElement == 0 {
			bytesPerElement = 1
		}
		bufferBytes += s.window * bytesPerElement
	}

	fmt.Println()
	fmt.Println("Stream data format (all exp-golomb encoded):")
	streamDescs := map[string]string{
		"NoteVal": "(noteIdx<<5)|inst, noteIdx freq-sorted (0=rest), inst=slot 0-30",
		"NoteDur": "gap rows before each note event",
		"FxV":     "(effect<<8)|param, sparse (0 never stored)",
		"FxD":     "[gap, duration] interleaved per fx entry",
		"TransV":  "transpose (zigzag: 0→0, -1→1, 1→2, ...)",
		"TransD":  "orders until next transpose change",
		"TblData": "delta, slot, inst[16], waveLen, wave[], arpLen, arp[], filtLen, filt[]; reset(65535): frame, row",
	}
	for _, sIdx := range streamOrder {
		if sIdx >= len(streams) {
			continue
		}
		s := streams[sIdx]
		maxV := 0
		for _, v := range s.data {
			if v > maxV {
				maxV = v
			}
		}
		// Extract base name (remove "Ch0 " prefix)
		baseName := s.name
		if len(baseName) > 4 && baseName[0:2] == "Ch" && baseName[3] == ' ' {
			baseName = baseName[4:]
		}
		desc := streamDescs[baseName]
		fmt.Printf("  %-14s max=%-6d %s\n", s.name, maxV, desc)
	}

	fmt.Println()
	fmt.Printf("Streams:    %d bytes (%d bits)\n", (totalCompBits+7)/8, totalCompBits)
	fmt.Printf("Note LUT:   %d bytes (%d entries)\n", lookupTableBytes, lookupTableBytes)
	fmt.Printf("Buffers:    %d bytes (%d streams)\n", bufferBytes, len(streams))
	fmt.Printf("Total:      %d bytes\n", totalBytes)
	fmt.Printf("Current:    26,270 bytes\n")
	fmt.Printf("Savings:    %+d bytes (%.1f%%)\n", totalBytes-26270, float64(26270-totalBytes)*100/26270)
	fmt.Printf("Time:       %.2fs\n", time.Since(mainStart).Seconds())

	if validationFailed {
		os.Exit(1)
	}
}

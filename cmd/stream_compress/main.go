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
				if effect == 0x0C && r.Param < 0x80 && r.Param > 0 {
					speed = int(r.Param)
				}
				// Effect 9 = position jump: jump to the specified order
				if effect == 9 {
					loopTarget = int(r.Param)
				}
				// Effect A = pattern break
				if effect == 0x0A {
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

type compressResult struct {
	song         int
	numOrders    int
	chCompressed [3][]byte
	chStats      [3]CompressStats
}

type intStream struct {
	data       []int
	bitWidth   int
	name       string
	isExp      bool
	baseIdx    int
	expGroup   int
}

// Vibrato lookup table (10 depths × 16 positions)
// Matches player's vibrato_table in odin_player.inc
var vibratoTable = []byte{
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // depth 0
	0x00, 0x02, 0x03, 0x05, 0x06, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x0f, 0x10, 0x10, // depth 1
	0x00, 0x03, 0x06, 0x09, 0x0c, 0x0f, 0x12, 0x14, 0x17, 0x19, 0x1b, 0x1c, 0x1e, 0x1f, 0x1f, 0x20, // depth 2
	0x00, 0x05, 0x09, 0x0e, 0x12, 0x17, 0x1b, 0x1e, 0x22, 0x25, 0x28, 0x2a, 0x2c, 0x2e, 0x2f, 0x30, // depth 3
	0x00, 0x06, 0x0c, 0x13, 0x18, 0x1e, 0x24, 0x29, 0x2d, 0x31, 0x35, 0x38, 0x3b, 0x3d, 0x3f, 0x40, // depth 4
	0x00, 0x08, 0x10, 0x17, 0x1f, 0x26, 0x2c, 0x33, 0x39, 0x3e, 0x43, 0x47, 0x4a, 0x4d, 0x4e, 0x50, // depth 5
	0x00, 0x09, 0x13, 0x1c, 0x25, 0x2d, 0x35, 0x3d, 0x44, 0x4a, 0x50, 0x55, 0x59, 0x5c, 0x5e, 0x60, // depth 6
	0x00, 0x0d, 0x19, 0x25, 0x31, 0x3c, 0x47, 0x51, 0x5b, 0x63, 0x6a, 0x71, 0x76, 0x7a, 0x7e, 0x7f, // depth 7 (old 8)
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x66, 0x71, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x9f, // depth 8 (old 10)
	0x00, 0x18, 0x2f, 0x46, 0x5c, 0x71, 0x85, 0x98, 0xaa, 0xba, 0xc8, 0xd4, 0xde, 0xe6, 0xeb, 0xef, // depth 9 (old 15)
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

		fmt.Printf("Song %d: captured %d frames\n", song, numFrames)
	}

	return sidFreqs
}

// Instrument offsets in partX.bin
const (
	INST_AD        = 0
	INST_SR        = 1
	INST_WAVESTART = 2
	INST_WAVEEND   = 3
	INST_WAVELOOP  = 4
	INST_ARPSTART  = 5
	INST_ARPEND    = 6
	INST_ARPLOOP   = 7
	INST_VIBDELAY  = 8
	INST_VIBDEPSP  = 9
	INST_SIZE      = 16
	WAVETABLE_OFF  = 0x8EA // Player patches: wave_load_addr = song_base + $08EA
	ARPTABLE_OFF   = 0x91D // Player patches: arp_load_addr = song_base + $091D
)

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
			if effect == 0x0C && param < 0x80 && param > 0 {
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

					// For portamento (effect 3), set target but don't change current freq
					if effect == 3 {
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

				// Update effect (tracker effect 4 sets vibrato params)
				if effect > 0 {
					state[ch].effect = effect
					state[ch].param = param
					if effect == 4 {
						state[ch].vibDepth = int(param >> 4)
						state[ch].vibSpeed = int(param & 0x0F)
					}
				} else if param == 0 {
					state[ch].effect = 0
					state[ch].param = 0
				}

				// Handle speed change (effect C)
				if effect == 0x0C && param < 0x80 && param > 0 {
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

			// Process arpeggio table (unless portamento effect)
			if s.effect != 3 && songIdx < len(songs) {
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

			// Apply tracker effects
			switch s.effect {
			case 1: // Slide up
				s.freq += uint16(s.param)
			case 3: // Portamento
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
			case 8: // Arpeggio (tracker effect - cycles through chord using mod3counter)
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
// Returns: vmFreqs, simFreqs, noteFreqs, isDrum, isSync, isRingMod
func runSideBySideValidation(streams [3][]byte, transpose [3][]int8, songBoundaries []int, orderBoundaries []int) ([3][]uint16, [3][]uint16, [3][]uint16, [3][]bool) {
	fmt.Println("\nRunning side-by-side VM vs Simulation validation...")

	playerData, err := os.ReadFile("build/player.bin")
	if err != nil {
		fmt.Printf("Error: could not load player: %v\n", err)
		return [3][]uint16{}, [3][]uint16{}, [3][]uint16{}, [3][]bool{}
	}

	// Load song data for instrument/wave/arp tables
	type songData struct {
		data      []byte
		instData  []byte
		waveTable []byte
		arpTable  []byte
		drumInst  [32]bool // Pre-classified drum instruments
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
		wave := make([]byte, 256)
		if len(data) > WAVETABLE_OFF+256 {
			copy(wave, data[WAVETABLE_OFF:WAVETABLE_OFF+256])
		}
		arp := make([]byte, 256)
		if len(data) > ARPTABLE_OFF+256 {
			copy(arp, data[ARPTABLE_OFF:ARPTABLE_OFF+256])
		}

		// Pre-classify drum instruments based on arp table and wave table analysis
		var drumInst [32]bool
		for i := 1; i < 32; i++ {
			off := i * INST_SIZE
			arpStart := int(inst[off+INST_ARPSTART])
			arpEnd := int(inst[off+INST_ARPEND])
			waveStart := int(inst[off+INST_WAVESTART])
			waveEnd := int(inst[off+INST_WAVEEND])

			// Analyze arp table: count abs/rel and track range of absolute notes
			absCount := 0
			relCount := 0
			minAbsNote := 255
			maxAbsNote := 0
			for j := arpStart; j <= arpEnd && j < 256; j++ {
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

			// Count noise waveforms in wave table
			noiseCount := 0
			totalWave := 0
			for j := waveStart; j <= waveEnd && j < 256; j++ {
				totalWave++
				if wave[j]&0xF0 == 0x80 {
					noiseCount++
				}
			}

			// Classify as drum if: ALL absolute notes (no relative) OR ALL noise waveform
			if absCount > 0 && relCount == 0 {
				drumInst[i] = true
			}
			if totalWave > 0 && noiseCount == totalWave {
				drumInst[i] = true
			}
			// Also classify as drum if absolute notes span > 2 octaves (24 semitones)
			if absCount > 0 && maxAbsNote-minAbsNote > 24 {
				drumInst[i] = true
			}
		}

		songs = append(songs, songData{data, inst, wave, arp, drumInst})
	}

	const playerBase = uint16(0xF000)

	var vmFreqs [3][]uint16
	var simFreqs [3][]uint16
	var noteFreqs [3][]uint16 // Base note frequencies without vibrato (for clean MIDI)
	var isDrum [3][]bool      // True when frame uses absolute arp note, sync, ring mod, or noise (drum-like)

	// SID envelope rates (frames per step) - approximation based on SID timing
	// Index is the 4-bit rate value from ADSR register
	envRates := []int{2, 4, 8, 12, 19, 28, 34, 40, 50, 125, 250, 500, 800, 1000, 3000, 5000}

	// Simulation state
	type chanState struct {
		freq        uint16
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
	}

	totalMismatches := 0
	totalFreqMismatches := 0
	totalControlMismatches := 0
	totalADMismatches := 0
	totalSRMismatches := 0

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
				}
			}

			vmFreq := [3]uint16{}
			for ch := 0; ch < 3; ch++ {
				vmFreq[ch] = uint16(vmFreqLo[ch]) | uint16(vmFreqHi[ch])<<8
				vmFreqs[ch] = append(vmFreqs[ch], vmFreq[ch])
			}

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

					// Player stores effect BEFORE processing note (see lines 600-616)
					// So note handling checks the NEW effect, not old
					if effect > 0 {
						simState[ch].effect = effect
						simState[ch].param = param
					} else if param == 0 {
						simState[ch].effect = 0
						simState[ch].param = 0
					}

					// Effect 7 = set waveform register directly (player effect09)
					if effect == 7 {
						simState[ch].waveform = param
					}

				// Player processes inst BEFORE note (see fetch_channel_row)
					// vibpos is only reset when inst changes, not for every note
					if inst > 0 {
						simState[ch].inst = inst
						// Reset waveIdx, arpIdx, vibDelay, vibPos when inst changes
						sd := songs[songIdx]
						if len(sd.instData) > inst*INST_SIZE+INST_VIBDEPSP {
							simState[ch].waveIdx = int(sd.instData[inst*INST_SIZE+INST_WAVESTART])
							simState[ch].arpIdx = int(sd.instData[inst*INST_SIZE+INST_ARPSTART])
							simState[ch].vibDelay = int(sd.instData[inst*INST_SIZE+INST_VIBDELAY])
							simState[ch].ad = sd.instData[inst*INST_SIZE+INST_AD]
							simState[ch].sr = sd.instData[inst*INST_SIZE+INST_SR]
						}
						simState[ch].vibratoPos = 0
					}

					if note > 0 && note < 0x61 {
						simState[ch].note = note - 1
						// For portamento (effect 3): only set notefreq as target (not freq)
						// This matches player's set_notefreq_only
						if simState[ch].effect == 3 {
							idx := simState[ch].note + int(trans)
							if idx >= 0 && idx < len(freqTableLo) {
								simState[ch].noteFreq = uint16(freqTableLo[idx]) | uint16(freqTableHi[idx])<<8
							}
						}
						// Reset slide when note is triggered (player does this in @notporta)
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

					if effect == 0x0C && param < 0x80 && param > 0 {
						speed = int(param)
					}
					// Effect A = pattern break (remapped from old effect D)
					// Sets firsttrackrow and forcenewpattern=$80
					if effect == 0x0A {
						firsttrackrow = int(param)
						forcenewpattern = true
					}
					// Effect 9 = position jump (remapped from old effect B)
					// Sets nextordernumber and forcenewpattern=$80
					if effect == 0x09 {
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
				if s.effect == 3 {
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

				// Process wave table - updates s.waveform (unless effect 7 overrides it)
				if s.effect != 7 && s.inst > 0 && len(sd.instData) > s.inst*INST_SIZE+INST_WAVELOOP && len(sd.waveTable) > s.waveIdx {
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

				// Process instrument (arpeggio and vibrato setup)
				if s.effect != 3 {
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

				// Apply effects
				switch s.effect {
				case 1: // Slide - modifies slideDelta by ±$20
					s.slideEnable = true
					if s.param&0x80 != 0 {
						// Slide up (param bit 7 set)
						s.slideDelta += 0x20
					} else {
						// Slide down
						s.slideDelta -= 0x20
					}
				case 3: // Portamento - slides freq toward noteFreq (like player's effect03)
					// Player uses chn_notefreq as target, which is set by:
					// - set_notefreq_only when new note with effect=3
					// - arp table lookup when effect != 3
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
				case 4: // Vibrato effect - sets depth and speed
					s.vibDepth = int(s.param & 0xF0) // high nibble as-is
					s.vibSpeed = int(s.param & 0x0F)
				case 8:
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
				case 12: // Effect C = old effect F (extended effects) - only on first frame of row
					if speedCounter == 0 && s.param >= 0x80 {
						highNibble := s.param & 0xF0
						lowNibble := s.param & 0x0F
						if highNibble == 0xB0 {
							// FBx: Slide - add lowNibble*4 to slideDelta, enable slide
							s.slideDelta += int16(lowNibble) * 4
							s.slideEnable = true
						}
						if highNibble == 0xF0 {
							// FFx: Set hard restart offset
							s.hardRestart = int(lowNibble)
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
				finFreq := s.freq
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
							finFreq = uint16(int(s.freq) + offset)
						} else {
							finFreq = uint16(int(s.freq) - offset)
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

				// For MIDI output: portamento as two notes (start/end), split at midpoint
				outputFreq := s.noteFreq
				if s.effect == 3 && s.portaStart > 0 && s.noteFreq > 0 {
					midFreq := (s.portaStart + s.noteFreq) / 2
					if (s.portaStart < s.noteFreq && s.freq < midFreq) ||
						(s.portaStart > s.noteFreq && s.freq > midFreq) {
						outputFreq = s.portaStart
					} else {
						outputFreq = s.noteFreq
					}
				}
				if s.envLevel == 0 {
					outputFreq = 0
				}

				// Store finFreq for dump
				s.freq = finFreq
				noteFreqs[ch] = append(noteFreqs[ch], outputFreq) // Base note for MIDI (0 when silent)
				isDrum[ch] = append(isDrum[ch], drumFrame[ch])
			}

			// 3) Hard restart check (runs AFTER processing, BEFORE dump)
			// Player looks ahead using hrtrackrow which is trackrow + hardrestart relative to speedcounter
			for ch := 0; ch < 3; ch++ {
				s := &simState[ch]
				// Check if we should look ahead for hard restart
				// Uses current speedCounter (after increment, reset if row processed)
				if speedCounter+s.hardRestart >= speed {
					// Calculate look-ahead row
					hrTrackRow := trackRow + 1
					hrOrder := ordernumber
					if hrTrackRow >= 64 {
						hrTrackRow = 0
						hrOrder = nextordernumber
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
								} else if effect != 3 {
									// Not portamento - hard restart
									instEffByte := instEff & 0xE0
									if instEffByte != 0x60 { // $60 = effect 3
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
				simF := s.freq

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
						isClose := vmFreq[ch] > 0 && diff < int(vmFreq[ch])/20
						fmt.Printf("\n  Song %d first FREQ mismatch at frame %d ch %d", song, frame, ch)
						if isClose {
							fmt.Printf(" [VIBRATO]")
						} else {
							fmt.Printf(" [BUG diff=%d]", diff)
						}
						fmt.Println()
						fmt.Printf("    VM:  $%04X (MIDI %d)\n", vmFreq[ch], sidFreqToMIDI(vmFreq[ch]))
						fmt.Printf("    Sim: $%04X (MIDI %d)\n", simF, sidFreqToMIDI(simF))
					}
				}

				// Control register comparison
				if simControl != vmControl[ch] {
					totalControlMismatches++
					if totalControlMismatches <= 5 {
						fmt.Printf("\n  Song %d CONTROL mismatch at frame %d ch %d: VM=$%02X Sim=$%02X (wave=$%02X gate=$%02X)\n",
							song, frame, ch, vmControl[ch], simControl, s.waveform, s.gateOn)
					}
				}

				// AD register comparison
				if s.ad != vmAD[ch] {
					totalADMismatches++
					if totalADMismatches <= 5 {
						fmt.Printf("\n  Song %d AD mismatch at frame %d ch %d: VM=$%02X Sim=$%02X\n",
							song, frame, ch, vmAD[ch], s.ad)
					}
				}

				// SR register comparison
				if s.sr != vmSR[ch] {
					totalSRMismatches++
					if totalSRMismatches <= 5 {
						fmt.Printf("\n  Song %d SR mismatch at frame %d ch %d: VM=$%02X Sim=$%02X\n",
							song, frame, ch, vmSR[ch], s.sr)
					}
				}

				// Store for output arrays
				simFreqs[ch] = append(simFreqs[ch], simF)
			}
		}

		matchPct := float64(numFrames*3-songMismatches) / float64(numFrames*3) * 100
		fmt.Printf("  Song %d: %d frames, %d freq mismatches (%.1f%% match)\n", song, numFrames, songMismatches, matchPct)
	}

	totalFrames := len(vmFreqs[0])
	matchPct := float64(totalFrames*3-totalMismatches) / float64(totalFrames*3) * 100
	fmt.Printf("\n  Total: %d frames\n", totalFrames)
	fmt.Printf("  Frequency mismatches: %d (%.1f%% match)\n", totalFreqMismatches, matchPct)
	fmt.Printf("  Control reg mismatches: %d\n", totalControlMismatches)
	fmt.Printf("  AD reg mismatches: %d\n", totalADMismatches)
	fmt.Printf("  SR reg mismatches: %d\n", totalSRMismatches)

	return vmFreqs, simFreqs, noteFreqs, isDrum
}

// writeMIDI outputs a MIDI file with detected notes
// noteFreqs contains base note frequencies (without vibrato - for clean MIDI)
// sidFreqs contains actual SID frequencies per frame (from VM emulation with vibrato)
// isDrum indicates which frames use absolute arp notes, sync, ring mod, or noise (drum-like sounds)
func writeMIDI(noteFreqs [3][]uint16, sidFreqs [3][]uint16, isDrum [3][]bool, outDir string) {
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
	// Track 3: VM frequencies (channels 10-12)

	totalFrames := len(sidFreqs[0])

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
	fmt.Printf("Wrote MIDI file: %s (4 tracks: tempo, melodic, drums, VM)\n", midiPath)
}

func main() {
	mainStart := time.Now()
	outDir := "generated/stream_compress"
	os.MkdirAll(outDir, 0755)

	// Extract streams and transpose from partX.bin files (pattern-based extraction)
	extractStart := time.Now()
	var allStreams [3][]byte
	var allTranspose [3][]int8
	var songBoundaries []int
	var orderBoundaries []int // cumulative order count per song
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
		for ch := 0; ch < 3; ch++ {
			allStreams[ch] = append(allStreams[ch], streams[ch]...)
			allTranspose[ch] = append(allTranspose[ch], transpose[ch]...)
		}
	}
	songBoundaries = append(songBoundaries, len(allStreams[0])/3)
	orderBoundaries = append(orderBoundaries, len(allTranspose[0]))

	fmt.Printf("Extraction: %.2fs\n", time.Since(extractStart).Seconds())

	// Debug: show order boundaries
	fmt.Printf("Order boundaries per song: %v\n", orderBoundaries)
	fmt.Printf("Row boundaries per song: %v\n", songBoundaries)

	// Run VM and simulation side-by-side, comparing each frame
	validationStart := time.Now()
	// noteFreqs = clean base notes (for MIDI), sidFreqs/simFreqs = with vibrato (for validation)
	sidFreqs, _, noteFreqs, isDrum := runSideBySideValidation(allStreams, allTranspose, songBoundaries, orderBoundaries)
	fmt.Printf("Validation: %.2fs\n", time.Since(validationStart).Seconds())
	fmt.Printf("Total: %d frames, %d rows, %d songs\n", len(sidFreqs[0]), len(allStreams[0])/3, len(songBoundaries)-1)

	// Count drum frames for debugging
	drumCount := 0
	for ch := 0; ch < 3; ch++ {
		for i := range isDrum[ch] {
			if isDrum[ch][i] {
				drumCount++
			}
		}
	}
	fmt.Printf("Frame counts: drum=%d (total frames=%d)\n", drumCount, len(isDrum[0])*3)

	// Output MIDI file early (before compression)
	midiStart := time.Now()
	writeMIDI(noteFreqs, sidFreqs, isDrum, outDir)
	fmt.Printf("MIDI write: %.2fs\n", time.Since(midiStart).Seconds())

	// Compute usage statistics
	statsStart := time.Now()
	usedRows := make(map[[3]byte]bool)
	usedInst := make(map[byte]bool)
	usedNote := make(map[byte]bool)
	usedEffect := make(map[byte]bool)
	usedParamPerEffect := make(map[byte]map[byte]bool)
	for i := 0; i < 16; i++ {
		usedParamPerEffect[byte(i)] = make(map[byte]bool)
	}

	for ch := 0; ch < 3; ch++ {
		stream := allStreams[ch]
		numRows := len(stream) / 3
		for row := 0; row < numRows; row++ {
			off := row * 3
			noteByte, instEff, param := stream[off], stream[off+1], stream[off+2]
			inst := instEff & 0x1F
			note := noteByte & 0x7F
			effect := (instEff >> 5) | ((noteByte >> 4) & 0x08)

			usedRows[[3]byte{noteByte, instEff, param}] = true
			usedInst[inst] = true
			usedNote[note] = true
			usedEffect[effect] = true
			if effect > 0 {
				usedParamPerEffect[effect][param] = true
			}
		}
	}

	fmt.Printf("Stats: %.2fs\n", time.Since(statsStart).Seconds())
	fmt.Println("Usage Statistics")
	fmt.Println("================")
	fmt.Printf("Rows:        %6d / 16777216 used (%.4f%%)\n", len(usedRows), float64(len(usedRows))/16777216*100)
	fmt.Printf("Instruments: %6d / 32 used\n", len(usedInst))
	fmt.Printf("Notes:       %6d / 128 used\n", len(usedNote))
	fmt.Printf("Effects:     %6d / 16 used\n", len(usedEffect))
	fmt.Println()
	fmt.Println("Params per effect:")
	for fx := byte(0); fx < 16; fx++ {
		if usedEffect[fx] && fx > 0 {
			fmt.Printf("  Effect %X: %3d / 256 params used\n", fx, len(usedParamPerEffect[fx]))
		}
	}
	fmt.Println()

	// Write each channel to a tab-separated file with decoded fields
	noteNames := []string{"C-", "C#", "D-", "D#", "E-", "F-", "F#", "G-", "G#", "A-", "A#", "B-"}
	effectNames := []string{"", "sld", "pls", "por", "vib", "AD", "SR", "wav", "arp", "jmp", "brk", "res", "Fxx"}

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
		fmt.Printf("Wrote %s (%d rows)\n", filepath.Join(outDir, fmt.Sprintf("channel_%d.csv", ch)), numRows)
	}

	// 9 streams with native bit widths: note(7), inst(5), fx+param(12)
	// Encoding: 0+literal(N bits), 1+dist+len (exp-golomb)
	const windowElements = 1024 // 1K elements per stream
	fmt.Println()
	fmt.Printf("Bit-level Compression (9 streams, %d element window)\n", windowElements)
	fmt.Println("=====================================================")
	fmt.Println()

	numRows := len(allStreams[0]) / 3

	// Extract 9 streams as integer arrays

	// Build base 9 streams
	var streams []intStream
	for ch := 0; ch < 3; ch++ {
		src := allStreams[ch]
		noteData := make([]int, 0, numRows)
		instData := make([]int, 0, numRows)
		fxData := make([]int, 0, numRows)

		for row := 0; row < numRows; row++ {
			off := row * 3
			noteByte, instEff, param := src[off], src[off+1], src[off+2]
			note := int(noteByte & 0x7F)
			inst := int(instEff & 0x1F)
			effect := int((instEff >> 5) | ((noteByte >> 4) & 0x08))
			fxParam := (effect << 8) | int(param)

			noteData = append(noteData, note)
			instData = append(instData, inst)
			fxData = append(fxData, fxParam)
		}

		streams = append(streams, intStream{noteData, 7, fmt.Sprintf("Ch%d Note", ch), false, -1, ch})
		streams = append(streams, intStream{instData, 5, fmt.Sprintf("Ch%d Inst", ch), false, -1, -1})
		streams = append(streams, intStream{fxData, 12, fmt.Sprintf("Ch%d Fx+P", ch), false, -1, -1})
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

	var totalRawBits int
	numBaseStreams := 0
	for _, s := range streams {
		if !s.isExp {
			totalRawBits += len(s.data) * s.bitWidth
			numBaseStreams++
		}
	}

	var completedChunks atomic.Int64
	var baseBits atomic.Int64      // bits for base 9 streams
	var baseChunks atomic.Int64    // chunks completed for base streams
	streamBitsAtomic := make([]atomic.Int64, numStreams)
	done := make(chan struct{})

	numWorkers := runtime.NumCPU()
	fmt.Printf("  Starting compression: %d streams × %d chunks = %d tasks, %d workers\n", numStreams, numChunks, numStreams*numChunks, numWorkers)
	fmt.Printf("  Raw bits (base only): %d (%d bytes)\n", totalRawBits, totalRawBits/8)
	fmt.Printf("  GOMAXPROCS: %d\n", runtime.GOMAXPROCS(0))

	startTime := time.Now()
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				completed := completedChunks.Load()
				total := int64(numStreams * numChunks)
				pct := float64(completed) / float64(total) * 100
				elapsed := time.Since(startTime).Seconds()
				rate := float64(completed) / elapsed
				remaining := float64(total-completed) / rate
				baseCompleted := baseChunks.Load()
				baseTotal := int64(numBaseStreams * numChunks)
				if baseCompleted > 0 {
					estBase := float64(baseBits.Load()) * float64(baseTotal) / float64(baseCompleted)
					fmt.Printf("  Progress: %.0f%% (%d/%d), %.1f/s, ~%.0fs left, est ~%.0f bytes\n", pct, completed, total, rate, remaining, estBase/8)
				} else {
					fmt.Printf("  Progress: %.0f%% (%d/%d), %.1f/s, ~%.0fs left\n", pct, completed, total, rate, remaining)
				}
			}
		}
	}()

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
					completedChunks.Add(1)
					continue
				}

				chunkData := s.data[start:end]
				prefixStart := start - windowElements
				if prefixStart < 0 {
					prefixStart = 0
				}
				prefix := s.data[prefixStart:start]

				choices := dpChunk(chunkData, prefix, s.bitWidth, windowElements)

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
				if !s.isExp {
					baseBits.Add(int64(estBits))
					baseChunks.Add(1)
				}
				completedChunks.Add(1)
			}
		}()
	}
	wg.Wait()
	close(done)
	elapsed := time.Since(startTime)

	fmt.Printf("\n  Compression complete in %.1fs (%d workers)\n", elapsed.Seconds(), numWorkers)

	// Compute total bits per stream
	streamTotalBits := make([]int, numStreams)
	for sIdx := 0; sIdx < numStreams; sIdx++ {
		for cIdx := 0; cIdx < numChunks; cIdx++ {
			streamTotalBits[sIdx] += allChunkResults[sIdx][cIdx].estBits
		}
	}

	// Find best variant for each base note stream (channels 0, 3, 6 are notes)
	type bestChoice struct {
		sIdx    int
		bits    int
		name    string
		savings int
	}
	bestForGroup := make(map[int]bestChoice) // expGroup -> best choice

	for sIdx := 0; sIdx < numStreams; sIdx++ {
		s := streams[sIdx]
		if s.expGroup < 0 {
			continue // not a note stream
		}
		bits := streamTotalBits[sIdx]
		current, exists := bestForGroup[s.expGroup]
		if !exists || bits < current.bits {
			baseBits := 0
			if s.isExp {
				baseBits = streamTotalBits[s.baseIdx]
			} else {
				baseBits = bits
			}
			bestForGroup[s.expGroup] = bestChoice{sIdx, bits, s.name, baseBits - bits}
		}
	}

	// Output results
	fmt.Println()
	fmt.Println("Stream Compression Results")
	fmt.Println("==========================")
	fmt.Println()
	fmt.Println("Stream      RawBits  CompBits  Ratio   Best Alt     Savings")
	fmt.Println("--------   -------  --------  -----   ----------   -------")

	var totalCompBits int
	var totalBestBits int
	for sIdx := 0; sIdx < numStreams; sIdx++ {
		s := streams[sIdx]
		if s.isExp {
			continue // Only base streams
		}
		rawBits := len(s.data) * s.bitWidth
		compBits := streamTotalBits[sIdx]
		ratio := float64(compBits) / float64(rawBits) * 100

		altInfo := ""
		savingsInfo := ""
		if best, ok := bestForGroup[s.expGroup]; ok && s.expGroup >= 0 {
			if best.sIdx != sIdx {
				altInfo = fmt.Sprintf("%-10s", streams[best.sIdx].name)
				savingsInfo = fmt.Sprintf("%+d", -best.savings)
				totalBestBits += best.bits
			} else {
				altInfo = "(original)"
				totalBestBits += compBits
			}
		} else {
			totalBestBits += compBits
		}

		fmt.Printf("%-8s   %7d  %8d  %4.1f%%   %s   %s\n", s.name, rawBits, compBits, ratio, altInfo, savingsInfo)
		totalCompBits += compBits
	}
	fmt.Println("--------   -------  --------  -----")
	fmt.Printf("Total      %7d  %8d  %4.1f%%\n", totalRawBits, totalCompBits, float64(totalCompBits)/float64(totalRawBits)*100)

	fmt.Println()
	fmt.Printf("Original:   %d bytes (%d bits)\n", (totalCompBits+7)/8, totalCompBits)
	fmt.Printf("With best:  %d bytes (%d bits) [%+d]\n", (totalBestBits+7)/8, totalBestBits, totalBestBits-totalCompBits)
	fmt.Printf("Current system: 26,596 bytes\n")
	fmt.Printf("Improvement:    %+d bytes\n", (totalBestBits+7)/8-26596)

	// Show experiment comparison for notes
	fmt.Println()
	fmt.Println("Note Stream Variants")
	fmt.Println("====================")
	for ch := 0; ch < 3; ch++ {
		baseIdx := ch * 3
		baseBits := streamTotalBits[baseIdx]
		rawBits := len(streams[baseIdx].data) * 7

		fmt.Printf("\nCh%d Notes (%d raw bits):\n", ch, rawBits)

		// Find all variants for this channel
		for sIdx := 0; sIdx < numStreams; sIdx++ {
			if streams[sIdx].expGroup == ch {
				bits := streamTotalBits[sIdx]
				diff := bits - baseBits
				marker := "  "
				if best, ok := bestForGroup[ch]; ok && best.sIdx == sIdx {
					marker = "* "
				}
				fmt.Printf("  %s%-10s: %6d bits (%.1f%%) %+6d\n", marker, streams[sIdx].name, bits, float64(bits)/float64(rawBits)*100, diff)
			}
		}
	}

	// By chunk position with best variants
	fmt.Println()
	fmt.Println("By position (using best variants):")
	for cIdx := 0; cIdx < numChunks; cIdx++ {
		var origBits, bestBits int
		for sIdx := 0; sIdx < numStreams; sIdx++ {
			if streams[sIdx].isExp {
				continue
			}
			origBits += allChunkResults[sIdx][cIdx].estBits
			// Use best variant for note streams
			if streams[sIdx].expGroup >= 0 {
				if best, ok := bestForGroup[streams[sIdx].expGroup]; ok {
					bestBits += allChunkResults[best.sIdx][cIdx].estBits
					continue
				}
			}
			bestBits += allChunkResults[sIdx][cIdx].estBits
		}
		origBar := ""
		for i := 0; i < origBits/500; i++ {
			origBar += "░"
		}
		bestBar := ""
		for i := 0; i < bestBits/500; i++ {
			bestBar += "█"
		}
		fmt.Printf("  Chunk %2d: %5d→%5d %s%s\n", cIdx, origBits, bestBits, bestBar, origBar[len(bestBar):])
	}

	// Identify worst-compressing chunks (base streams only)
	fmt.Println()
	fmt.Println("Worst compressing (optimization targets):")
	type chunkInfo struct {
		stream string
		chunk  int
		bits   int
		raw    int
	}
	var chunks []chunkInfo
	for sIdx := 0; sIdx < numStreams; sIdx++ {
		if streams[sIdx].isExp {
			continue
		}
		chunkSize := (len(streams[sIdx].data) + numChunks - 1) / numChunks
		for cIdx := 0; cIdx < numChunks; cIdx++ {
			start := cIdx * chunkSize
			end := start + chunkSize
			if end > len(streams[sIdx].data) {
				end = len(streams[sIdx].data)
			}
			if start >= len(streams[sIdx].data) {
				continue
			}
			rawBits := (end - start) * streams[sIdx].bitWidth
			chunks = append(chunks, chunkInfo{streams[sIdx].name, cIdx, allChunkResults[sIdx][cIdx].estBits, rawBits})
		}
	}
	// Sort by bits descending
	for i := 0; i < len(chunks)-1; i++ {
		for j := i + 1; j < len(chunks); j++ {
			if chunks[j].bits > chunks[i].bits {
				chunks[i], chunks[j] = chunks[j], chunks[i]
			}
		}
	}
	for i := 0; i < 10 && i < len(chunks); i++ {
		c := chunks[i]
		ratio := float64(c.bits) / float64(c.raw) * 100
		fmt.Printf("  %s c%02d: %5d bits (%.1f%% of raw)\n", c.stream, c.chunk, c.bits, ratio)
	}

	fmt.Printf("\nTotal time: %.2fs\n", time.Since(mainStart).Seconds())
}

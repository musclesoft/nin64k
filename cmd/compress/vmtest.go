package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// MemoryValidator tracks which memory regions are valid for reading
// and detects invalid memory accesses during decompression.
type MemoryValidator struct {
	// Buffer state tracking
	buf1000Valid [bufferSize]bool // Which bytes in $1000-$6FFF are valid
	buf7000Valid [bufferSize]bool // Which bytes in $7000-$CFFF are valid

	// Current decompression state
	currentSong   int
	selfBuffer    uint16 // $1000 or $7000
	outputPos     uint16 // Current output position within buffer

	// Violation tracking
	violations    []string
}

func NewMemoryValidator() *MemoryValidator {
	return &MemoryValidator{}
}

// InitForSong sets up the validator for decompressing a specific song
func (v *MemoryValidator) InitForSong(song int, songs map[int][]byte) {
	v.currentSong = song
	v.violations = nil

	// Determine which buffer is self vs other
	if song%2 == 1 {
		v.selfBuffer = 0x1000
	} else {
		v.selfBuffer = 0x7000
	}
	v.outputPos = 0

	// Set up buffer validity based on what's been decompressed so far
	// Songs are decompressed in order: 1,2,3,4,5,6,7,8,9
	// Buffer $1000: songs 1,3,5,7,9 (odd)
	// Buffer $7000: songs 2,4,6,8 (even)

	// Clear both buffers first
	for i := range v.buf1000Valid {
		v.buf1000Valid[i] = false
	}
	for i := range v.buf7000Valid {
		v.buf7000Valid[i] = false
	}

	// Song 1: no previous data
	if song == 1 {
		return
	}

	// Song 2: self=$7000 (empty), other=$1000 (S1)
	if song == 2 {
		for i := 0; i < len(songs[1]) && i < bufferSize; i++ {
			v.buf1000Valid[i] = true
		}
		// Protect scratch in other buffer (S1 was played, scratch corrupted)
		v.protectScratch(v.buf1000Valid[:])
		// Self buffer ($7000) is empty - no scratch to protect
		return
	}

	// Songs 3-9: both buffers have data from previous songs
	// Track HIGH WATER MARK (max bytes ever written to each buffer)
	var hwm1000, hwm7000 int
	for s := 1; s < song; s++ {
		songLen := len(songs[s])
		if s%2 == 1 {
			if songLen > hwm1000 {
				hwm1000 = songLen
			}
		} else {
			if songLen > hwm7000 {
				hwm7000 = songLen
			}
		}
	}

	// Set validity for $1000 buffer up to high water mark
	for i := 0; i < hwm1000 && i < bufferSize; i++ {
		v.buf1000Valid[i] = true
	}

	// Set validity for $7000 buffer up to high water mark
	for i := 0; i < hwm7000 && i < bufferSize; i++ {
		v.buf7000Valid[i] = true
	}

	// Protect scratch regions in BOTH buffers
	// Self buffer scratch could be read via fwdref before being overwritten
	// Other buffer scratch was corrupted by playroutine
	v.protectScratch(v.buf1000Valid[:])
	v.protectScratch(v.buf7000Valid[:])
}

// protectScratch marks scratch regions as invalid
func (v *MemoryValidator) protectScratch(valid []bool) {
	// Scratch regions (offsets relative to buffer base):
	// $0115-$0116 (2 bytes)
	// $081E-$088C (111 bytes)
	for i := 0x0115; i <= 0x0116 && i < len(valid); i++ {
		valid[i] = false
	}
	for i := 0x081E; i <= 0x088C && i < len(valid); i++ {
		valid[i] = false
	}
}

// MarkWritten marks a byte as written to output
func (v *MemoryValidator) MarkWritten(addr uint16) {
	if addr >= 0x1000 && addr < 0x1000+bufferSize {
		v.buf1000Valid[addr-0x1000] = true
	} else if addr >= 0x7000 && addr < 0x7000+bufferSize {
		v.buf7000Valid[addr-0x7000] = true
	}
	// Track output position
	if addr >= v.selfBuffer && addr < v.selfBuffer+bufferSize {
		offset := addr - v.selfBuffer
		if offset >= v.outputPos {
			v.outputPos = offset + 1
		}
	}
}

// ValidateRead checks if reading from addr is valid during copy operations
func (v *MemoryValidator) ValidateRead(addr uint16) bool {
	// Only validate reads from the decompression buffers
	if addr < 0x1000 || addr >= 0xD000 {
		return true // Not a buffer read
	}

	var valid bool
	var reason string

	if addr >= 0x1000 && addr < 0x1000+bufferSize {
		offset := int(addr - 0x1000)
		if v.selfBuffer == 0x1000 {
			// Reading from self buffer ($1000)
			// Valid if: already written (backref) OR initialized from prev song (fwdref)
			valid = v.buf1000Valid[offset]
			if !valid {
				reason = fmt.Sprintf("self buffer offset $%04X not initialized", offset)
			}
		} else {
			// Reading from other buffer ($1000) - must be initialized and not scratch
			valid = v.buf1000Valid[offset]
			if !valid {
				reason = fmt.Sprintf("other buffer ($1000) offset $%04X invalid/scratch", offset)
			}
		}
	} else if addr >= 0x7000 && addr < 0x7000+bufferSize {
		offset := int(addr - 0x7000)
		if v.selfBuffer == 0x7000 {
			// Reading from self buffer ($7000)
			valid = v.buf7000Valid[offset]
			if !valid {
				reason = fmt.Sprintf("self buffer offset $%04X not initialized", offset)
			}
		} else {
			// Reading from other buffer ($7000)
			valid = v.buf7000Valid[offset]
			if !valid {
				reason = fmt.Sprintf("other buffer ($7000) offset $%04X invalid/scratch", offset)
			}
		}
	}

	if !valid {
		v.violations = append(v.violations,
			fmt.Sprintf("Song %d: invalid read from $%04X (%s)", v.currentSong, addr, reason))
	}

	return valid
}

// HasViolations returns true if any memory access violations occurred
func (v *MemoryValidator) HasViolations() bool {
	return len(v.violations) > 0
}

// Violations returns all recorded violations
func (v *MemoryValidator) Violations() []string {
	return v.violations
}

func testDecompressor() error {
	fmt.Println("6502 Decompressor Test")
	fmt.Println("======================")

	// Load expected song data
	songs := make(map[int][]byte)
	for i := 1; i <= 9; i++ {
		data, err := os.ReadFile(filepath.Join("uncompressed", fmt.Sprintf("d%dp.raw", i)))
		if err != nil {
			return fmt.Errorf("loading song %d: %w", i, err)
		}
		normalizeSong(data)
		songs[i] = data
	}

	// Load split stream files
	streamMain, err := os.ReadFile(filepath.Join("generated", "stream_main.bin"))
	if err != nil {
		return fmt.Errorf("loading stream_main.bin: %w\n(run compressor first: go run ./cmd/compress)", err)
	}
	streamTail, err := os.ReadFile(filepath.Join("generated", "stream_tail.bin"))
	if err != nil {
		return fmt.Errorf("loading stream_tail.bin: %w\n(run compressor first: go run ./cmd/compress)", err)
	}

	// Get decompressor code
	decompCode := GetDecompressorCode()
	fmt.Printf("Decompressor size: %d bytes\n\n", len(decompCode))

	fmt.Println("Split Stream Test (main + tail)")
	fmt.Println("--------------------------------")
	fmt.Printf("Stream main: %d bytes, tail: %d bytes\n", len(streamMain), len(streamTail))

	// Memory layout:
	// - Main stream in high memory ending at $FFFF
	// - Tail stream at $663B-$6FFF (buffer A tail)
	const tailAddr = 0x663B
	mainStart := 0x10000 - len(streamMain)

	fmt.Printf("Layout: main=$%04X-$%04X, tail=$%04X-$%04X\n\n",
		mainStart, 0xFFFF, tailAddr, tailAddr+len(streamTail)-1)

	cpu := NewCPU6502()
	cpu.LoadAt(0x0D00, decompCode)

	// Load streams into memory
	cpu.LoadAt(uint16(mainStart), streamMain)
	cpu.LoadAt(tailAddr, streamTail)

	cpu.Mem[zpSrcLo] = byte(mainStart)
	cpu.Mem[zpSrcHi] = byte(mainStart >> 8)
	cpu.Mem[zpBitBuf] = 0x80
	cpu.Mem[0x0CFF] = 0x00

	// Set up memory validator
	validator := NewMemoryValidator()
	cpu.OnRead = func(addr uint16) {
		validator.ValidateRead(addr)
	}
	cpu.OnWrite = func(addr uint16) {
		validator.MarkWritten(addr)
	}

	allPassed := true
	var totalCycles uint64
	var totalViolations []string

	// Decompress songs 1-8 from main stream
	for song := 1; song <= 8; song++ {
		target := songs[song]

		// Initialize validator for this song
		validator.InitForSong(song, songs)

		var dstAddr uint16
		if song%2 == 1 {
			dstAddr = 0x1000
		} else {
			dstAddr = 0x7000
		}
		cpu.Mem[zpOutLo] = byte(dstAddr)
		cpu.Mem[zpOutHi] = byte(dstAddr >> 8)

		cpu.Mem[0x01FF] = 0x0C
		cpu.Mem[0x01FE] = 0xFE
		cpu.SP = 0xFD
		cpu.PC = 0x0D00
		cpu.Halted = false
		cpu.Cycles = 0

		err := cpu.Run(2000000)
		if err != nil {
			fmt.Printf("Song %d: RUNTIME ERROR: %v\n", song, err)
			allPassed = false
			continue
		}
		if !cpu.Halted {
			fmt.Printf("Song %d: TIMEOUT\n", song)
			allPassed = false
			continue
		}

		// Check for memory access violations
		if validator.HasViolations() {
			totalViolations = append(totalViolations, validator.Violations()...)
		}

		output := cpu.Mem[dstAddr : dstAddr+uint16(len(target))]
		if bytes.Equal(output, target) {
			srcPos := uint16(cpu.Mem[zpSrcLo]) | uint16(cpu.Mem[zpSrcHi])<<8
			fmt.Printf("Song %d: PASS (%d bytes, %d cycles) [src=$%04X]\n",
				song, len(target), cpu.Cycles, srcPos)
			totalCycles += cpu.Cycles
		} else {
			firstDiff := -1
			for i := range target {
				if output[i] != target[i] {
					firstDiff = i
					break
				}
			}
			fmt.Printf("Song %d: FAIL at offset %d\n", song, firstDiff)
			allPassed = false
		}
	}

	// Song 9: First decompress partial S9 from main (until terminator)
	// Then continue from tail stream
	target9 := songs[9]

	// Initialize validator for song 9
	validator.InitForSong(9, songs)

	cpu.Mem[zpOutLo] = 0x00
	cpu.Mem[zpOutHi] = 0x10 // $1000

	cpu.Mem[0x01FF] = 0x0C
	cpu.Mem[0x01FE] = 0xFE
	cpu.SP = 0xFD
	cpu.PC = 0x0D00
	cpu.Halted = false
	cpu.Cycles = 0

	// Run until terminator in main stream
	err = cpu.Run(2000000)
	if err != nil {
		fmt.Printf("Song 9 (main): RUNTIME ERROR: %v\n", err)
		allPassed = false
	} else if !cpu.Halted {
		fmt.Printf("Song 9 (main): TIMEOUT\n")
		allPassed = false
	} else {
		mainCycles := cpu.Cycles

		// Check how much of S9 was decompressed
		outPos := uint16(cpu.Mem[zpOutLo]) | uint16(cpu.Mem[zpOutHi])<<8
		partialLen := int(outPos - 0x1000)

		// Verify partial output matches
		partialMatch := true
		for i := 0; i < partialLen && i < len(target9); i++ {
			if cpu.Mem[0x1000+uint16(i)] != target9[i] {
				fmt.Printf("Song 9 (main): MISMATCH at offset %d\n", i)
				partialMatch = false
				allPassed = false
				break
			}
		}

		if partialMatch {
			// Continue from tail stream
			cpu.Mem[zpSrcLo] = byte(tailAddr & 0xFF)
			cpu.Mem[zpSrcHi] = byte(tailAddr >> 8)
			cpu.Mem[zpBitBuf] = 0x80

			cpu.Mem[0x01FF] = 0x0C
			cpu.Mem[0x01FE] = 0xFE
			cpu.SP = 0xFD
			cpu.PC = 0x0D00
			cpu.Halted = false
			cpu.Cycles = 0

			err = cpu.Run(2000000)
			if err != nil {
				fmt.Printf("Song 9 (tail): RUNTIME ERROR: %v\n", err)
				allPassed = false
			} else if !cpu.Halted {
				fmt.Printf("Song 9 (tail): TIMEOUT\n")
				allPassed = false
			} else {
				// Verify complete S9
				output9 := cpu.Mem[0x1000 : 0x1000+uint16(len(target9))]
				if bytes.Equal(output9, target9) {
					s9Cycles := mainCycles + cpu.Cycles
					fmt.Printf("Song 9: PASS (%d bytes, %d cycles) [main=%d + tail=%d]\n",
						len(target9), s9Cycles, partialLen, len(target9)-partialLen)
					totalCycles += s9Cycles
				} else {
					firstDiff := -1
					for i := range target9 {
						if output9[i] != target9[i] {
							firstDiff = i
							break
						}
					}
					fmt.Printf("Song 9: FAIL at offset %d (got $%02X, want $%02X)\n",
						firstDiff, output9[firstDiff], target9[firstDiff])
					allPassed = false
				}
			}
		}
	}

	// Check for memory access violations from song 9
	if validator.HasViolations() {
		totalViolations = append(totalViolations, validator.Violations()...)
	}

	fmt.Printf("\nTotal cycles: %d\n", totalCycles)

	// Report memory access violations
	if len(totalViolations) > 0 {
		fmt.Printf("\nMemory access violations: %d\n", len(totalViolations))
		for i, v := range totalViolations {
			fmt.Printf("  %s\n", v)
			if i >= 9 {
				fmt.Printf("  ... and %d more\n", len(totalViolations)-10)
				break
			}
		}
		allPassed = false
	} else {
		fmt.Println("\nMemory access validation: PASSED")
	}

	if allPassed {
		fmt.Println("\nAll tests PASSED!")
	}

	if !allPassed {
		return fmt.Errorf("some tests failed")
	}
	return nil
}

func vmTestMain() {
	if err := testDecompressor(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

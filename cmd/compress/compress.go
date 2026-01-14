package main

import (
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	kLen    = 2
	kDist   = 2
	kOffset = 2

	// Memory regions on NES
	addrLow    = 0x1000 // $1000-$6FFF - odd songs (S1, S3, S5, S7, S9)
	addrHigh   = 0x7000 // $7000-$BFFF - even songs (S2, S4, S6, S8)
	bufferSize = 0x6000 // 24KB per buffer
)

var (
	lenBitsLUT    [2048]uint8
	distBitsLUT   [16384]uint8
	offsetBitsLUT [65536]uint8
)

func init() {
	for i := range lenBitsLUT {
		lenBitsLUT[i] = uint8(expGolombBits(i, kLen))
	}
	for i := range distBitsLUT {
		distBitsLUT[i] = uint8(expGolombBits(i, kDist))
	}
	for i := range offsetBitsLUT {
		offsetBitsLUT[i] = uint8(expGolombBits(i, kOffset))
	}
}

func gammaBits(n int) int {
	return 2*bits.Len(uint(n+1)) - 1
}

func expGolombBits(n, k int) int {
	return gammaBits(n>>k) + k
}

func lenBitsFast(n int) int {
	if n < len(lenBitsLUT) {
		return int(lenBitsLUT[n])
	}
	return expGolombBits(n, kLen)
}

func distBitsFast(n int) int {
	if n < len(distBitsLUT) {
		return int(distBitsLUT[n])
	}
	return expGolombBits(n, kDist)
}

func offsetBitsFast(n int) int {
	if n < len(offsetBitsLUT) {
		return int(offsetBitsLUT[n])
	}
	return expGolombBits(n, kOffset)
}

type choice struct {
	typ     byte // 0=literal, 1=self-ref, 2=dict-self, 3=dict-other
	dist    int
	dictPos int
	length  int
}

type compressStats struct {
	literals      int
	literalBits   int
	literalUsed   [256]bool
	selfRef0      int // dist ≡ 0 (mod 3)
	selfRef0Bits  int
	selfRef1      int // dist ≡ 1 (mod 3)
	selfRef1Bits  int
	selfRef2      int // dist ≡ 2 (mod 3)
	selfRef2Bits  int
	dictSelf      int
	dictSelfBits  int
	dictOther     int
	dictOtherBits int
	maxGammaZeros int // max leading zeros in any gamma encoding
}

// Scratch regions (offsets relative to buffer base) that the playroutine corrupts.
// These must not be read via fwdref/copyother until overwritten by current decompression.
var scratchRegions = [][2]int{
	{0x0115, 0x0117}, // $0115-$0116 (2 bytes)
	{0x081E, 0x088D}, // $081E-$088C (111 bytes)
}

// normalizeSong sets unused regions to $60 (RTS) to improve compression.
// These regions are either not displayed (title) or dead code (mute routine).
func normalizeSong(data []byte) {
	for i := 0x0009; i <= 0x0028 && i < len(data); i++ {
		data[i] = 0x60
	}
	for i := 0x005C; i <= 0x0066 && i < len(data); i++ {
		data[i] = 0x60
	}
}

// MemoryMap tracks readable regions for the 48KB virtual address space.
// Buffer A (self): addresses 0 to bufferSize-1
// Buffer B (other): addresses bufferSize to 2*bufferSize-1
// Initially all memory is protected. Regions become readable when initialized
// with dictionary data or when bytes are written during decompression.
type MemoryMap struct {
	readable [2 * bufferSize]bool
	data     [2 * bufferSize]byte
}

func NewMemoryMap(selfDict, otherDict []byte) *MemoryMap {
	m := &MemoryMap{}
	for i, b := range selfDict {
		m.data[i] = b
		m.readable[i] = true
	}
	for i, b := range otherDict {
		m.data[bufferSize+i] = b
		m.readable[bufferSize+i] = true
	}
	return m
}

// ProtectOtherScratch marks the scratch regions in the other buffer as unreadable.
// Call this when the other buffer was used by a previous song (playroutine corrupts these).
func (m *MemoryMap) ProtectOtherScratch() {
	for _, region := range scratchRegions {
		for offset := region[0]; offset < region[1]; offset++ {
			m.readable[bufferSize+offset] = false
		}
	}
}

// ProtectSelfScratch marks the scratch regions in the self buffer as unreadable.
// Call this when the self buffer had a previous song played (scratch was corrupted).
// These regions become readable again once written during decompression.
func (m *MemoryMap) ProtectSelfScratch() {
	for _, region := range scratchRegions {
		for offset := region[0]; offset < region[1]; offset++ {
			m.readable[offset] = false
		}
	}
}

func (m *MemoryMap) Write(addr int, b byte) {
	if addr >= 0 && addr < len(m.data) {
		m.data[addr] = b
		m.readable[addr] = true
	}
}

func (m *MemoryMap) CanRead(addr int) bool {
	return addr >= 0 && addr < len(m.readable) && m.readable[addr]
}

func (m *MemoryMap) Read(addr int) (byte, bool) {
	if !m.CanRead(addr) {
		return 0, false
	}
	return m.data[addr], true
}

// CanReadAt returns whether addr is readable when output is at position pos.
// For self buffer (addr < bufferSize): readable only if addr >= pos (not yet overwritten)
// For other buffer (addr >= bufferSize): readable if initialized
func (m *MemoryMap) CanReadAt(addr, pos int) bool {
	if !m.CanRead(addr) {
		return false
	}
	if addr < bufferSize {
		return addr >= pos
	}
	return true
}

// ReadAt reads a byte if readable at the given output position.
func (m *MemoryMap) ReadAt(addr, pos int) (byte, bool) {
	if !m.CanReadAt(addr, pos) {
		return 0, false
	}
	return m.data[addr], true
}

// MatchLengthAt returns how many bytes match starting at addr when output is at pos.
func (m *MemoryMap) MatchLengthAt(addr, pos int, target []byte, targetPos int) int {
	maxLen := 0
	for targetPos+maxLen < len(target) {
		b, ok := m.ReadAt(addr+maxLen, pos)
		if !ok || b != target[targetPos+maxLen] {
			break
		}
		maxLen++
	}
	return maxLen
}

func compress(target, selfDict, otherDict []byte) ([]byte, int, compressStats) {
	var stats compressStats
	n := len(target)

	mem := NewMemoryMap(selfDict, otherDict)
	if len(otherDict) > 0 {
		mem.ProtectOtherScratch()
	}
	if len(selfDict) > 0 {
		mem.ProtectSelfScratch()
	}

	// Backref byte access: at position pos, going backward with distance d
	//   d ∈ [1, pos]: already-written output (target[pos-d])
	//   d ∈ (pos, pos+bufferSize]: otherDict via memory map
	getBackrefByte := func(pos, d int) int {
		if d <= 0 {
			return -1
		}
		if d <= pos {
			return int(target[pos-d])
		}
		// Distance reaches into other buffer
		addr := bufferSize + pos - d + bufferSize
		if b, ok := mem.Read(addr); ok {
			return int(b)
		}
		return -1
	}

	hashKey2 := func(b0, b1 byte) int { return int(b0)<<8 | int(b1) }

	// Build hash tables
	targetHash := make(map[int][]int)
	dictHash := make(map[int][]int) // unified hash for both buffers

	// Index target positions (for self-ref to already-written output)
	for i := 0; i < n-1; i++ {
		key := hashKey2(target[i], target[i+1])
		targetHash[key] = append(targetHash[key], i)
	}

	// Index dictionary positions from memory map (both buffers)
	for addr := 0; addr < 2*bufferSize-1; addr++ {
		b0, ok0 := mem.Read(addr)
		b1, ok1 := mem.Read(addr + 1)
		if ok0 && ok1 {
			key := hashKey2(b0, b1)
			dictHash[key] = append(dictHash[key], addr)
		}
	}

	// DP
	cost := make([]float64, n+1)
	choices := make([]choice, n)

	for pos := n - 1; pos >= 0; pos-- {
		bestCost := 10.0 + cost[pos+1]
		choices[pos] = choice{typ: 0}

		if pos < n-1 {
			key := hashKey2(target[pos], target[pos+1])
			seenDists := make(map[int]bool)

			// Backref from already-written output (target[0..pos-1])
			if positions, ok := targetHash[key]; ok {
				for _, srcPos := range positions {
					if srcPos >= pos {
						continue
					}
					dist := pos - srcPos
					if dist > 65535 || seenDists[dist] {
						continue
					}
					seenDists[dist] = true

					maxLen := 0
					for pos+maxLen < n {
						var srcByte int
						if maxLen < dist {
							srcByte = int(target[srcPos+maxLen])
						} else {
							srcByte = int(target[srcPos+(maxLen%dist)])
						}
						if srcByte != int(target[pos+maxLen]) {
							break
						}
						maxLen++
					}
					if maxLen < 2 {
						continue
					}

					rem := dist % 3
					d := dist / 3
					if rem == 0 {
						d--
					}
					prefixBits := []int{1, 3, 5}[rem]
					baseCost := float64(prefixBits + distBitsFast(d))

					for length := 2; length <= maxLen; length++ {
						c := baseCost + float64(lenBitsFast(length-2)) + cost[pos+length]
						if c < bestCost {
							bestCost = c
							choices[pos] = choice{typ: 1, dist: dist, length: length}
						}
					}
				}
			}

			// Backref from other buffer via memory map
			// Distance = pos + bufferSize - (addr - bufferSize) = pos + 2*bufferSize - addr
			if positions, ok := dictHash[key]; ok {
				for _, addr := range positions {
					if addr < bufferSize {
						continue // only other buffer for backref
					}
					dist := pos + 2*bufferSize - addr
					if dist <= 0 || dist > 65535 || seenDists[dist] {
						continue
					}
					seenDists[dist] = true

					maxLen := mem.MatchLengthAt(addr, pos, target, pos)
					if maxLen < 2 {
						continue
					}

					rem := dist % 3
					d := dist / 3
					if rem == 0 {
						d--
					}
					prefixBits := []int{1, 3, 5}[rem]
					baseCost := float64(prefixBits + distBitsFast(d))

					for length := 2; length <= maxLen; length++ {
						c := baseCost + float64(lenBitsFast(length-2)) + cost[pos+length]
						if c < bestCost {
							bestCost = c
							choices[pos] = choice{typ: 1, dist: dist, length: length}
						}
					}
				}
			}

			// Fwdref (1110): forward copy from 48K ring buffer
			// Uses memory map to check readability at current output position
			if positions, ok := dictHash[key]; ok {
				for _, addr := range positions {
					if !mem.CanReadAt(addr, pos) {
						continue
					}
					maxLen := mem.MatchLengthAt(addr, pos, target, pos)
					if maxLen < 2 {
						continue
					}
					offset := addr - pos
					if offset < 0 {
						continue // can't encode negative offset for fwdref
					}
					baseCost := float64(4 + offsetBitsFast(offset))
					for length := 2; length <= maxLen; length++ {
						c := baseCost + float64(lenBitsFast(length-2)) + cost[pos+length]
						if c < bestCost {
							bestCost = c
							choices[pos] = choice{typ: 2, dictPos: addr, length: length}
						}
					}
				}
			}

			// Copyother (11111): copy from other buffer with $6000 bias
			// encoded = addr - pos - bufferSize (for other buffer addresses)
			if positions, ok := dictHash[key]; ok {
				for _, addr := range positions {
					if addr < bufferSize {
						continue // copyother only from other buffer
					}
					if !mem.CanReadAt(addr, pos) {
						continue
					}
					maxLen := mem.MatchLengthAt(addr, pos, target, pos)
					if maxLen < 2 {
						continue
					}
					encoded := addr - pos - bufferSize
					if encoded < 0 {
						continue // can't encode negative offset
					}
					baseCost := float64(5 + offsetBitsFast(encoded))
					for length := 2; length <= maxLen; length++ {
						c := baseCost + float64(lenBitsFast(length-2)) + cost[pos+length]
						if c < bestCost {
							bestCost = c
							choices[pos] = choice{typ: 3, dictPos: addr, length: length}
						}
					}
				}
			}
		}

		// RLE check (dist 1-2)
		for dist := 1; dist <= 2; dist++ {
			if getBackrefByte(pos, dist) == -1 {
				continue
			}
			maxLen := 0
			for pos+maxLen < n {
				srcByte := getBackrefByte(pos, dist-(maxLen%dist))
				if srcByte == -1 || srcByte != int(target[pos+maxLen]) {
					break
				}
				maxLen++
			}
			if maxLen < 2 {
				continue
			}
			rem := dist % 3
			d := dist / 3
			if rem == 0 {
				d--
			}
			var prefixBits int
			switch rem {
			case 0:
				prefixBits = 1
			case 1:
				prefixBits = 3
			case 2:
				prefixBits = 5
			}
			baseCost := float64(prefixBits + distBitsFast(d))
			for length := 2; length <= maxLen; length++ {
				c := baseCost + float64(lenBitsFast(length-2)) + cost[pos+length]
				if c < bestCost {
					bestCost = c
					choices[pos] = choice{typ: 1, dist: dist, length: length}
				}
			}
		}

		cost[pos] = bestCost
	}

	// Encode
	var outBits []byte
	bitPos := 0

	writeBits := func(val, count int) {
		for i := count - 1; i >= 0; i-- {
			if bitPos%8 == 0 {
				outBits = append(outBits, 0)
			}
			if (val>>i)&1 == 1 {
				outBits[len(outBits)-1] |= 1 << (7 - bitPos%8)
			}
			bitPos++
		}
	}

	writeGamma := func(n int) {
		b := bits.Len(uint(n + 1))
		for i := 0; i < b-1; i++ {
			writeBits(0, 1)
		}
		writeBits(n+1, b)
	}

	writeExpGolomb := func(n, k int) {
		q := n >> k
		zeros := bits.Len(uint(q+1)) - 1
		if zeros > stats.maxGammaZeros {
			stats.maxGammaZeros = zeros
		}
		writeGamma(q)
		writeBits(n&((1<<k)-1), k)
	}

	pos := 0
	for pos < n {
		ch := choices[pos]
		switch ch.typ {
		case 0: // literal
			stats.literals++
			stats.literalBits += 10
			stats.literalUsed[target[pos]] = true
			writeBits(0b10, 2)
			writeBits(int(target[pos]), 8)
			pos++
		case 1: // self-ref
			rem := ch.dist % 3
			d := ch.dist / 3
			if rem == 0 {
				d--
			}
			distBits := expGolombBits(d, kDist)
			lenBits := expGolombBits(ch.length-2, kLen)
			cmdBits := 0
			switch rem {
			case 0:
				stats.selfRef0++
				cmdBits = 1 + distBits + lenBits
				stats.selfRef0Bits += cmdBits
				writeBits(0b0, 1)
			case 1:
				stats.selfRef1++
				cmdBits = 3 + distBits + lenBits
				stats.selfRef1Bits += cmdBits
				writeBits(0b110, 3)
			case 2:
				stats.selfRef2++
				cmdBits = 5 + distBits + lenBits
				stats.selfRef2Bits += cmdBits
				writeBits(0b11110, 5)
			}
			writeExpGolomb(d, kDist)
			writeExpGolomb(ch.length-2, kLen)
			pos += ch.length
		case 2: // dict-self (no bias): offset = ringPos - pos
			stats.dictSelf++
			offset := ch.dictPos - pos
			stats.dictSelfBits += 4 + expGolombBits(offset, kOffset) + expGolombBits(ch.length-2, kLen)
			writeBits(0b1110, 4)
			writeExpGolomb(offset, kOffset)
			writeExpGolomb(ch.length-2, kLen)
			pos += ch.length
		case 3: // dict-other ($6000 bias): encoded = addr - pos - bufferSize
			stats.dictOther++
			encoded := ch.dictPos - pos - bufferSize
			stats.dictOtherBits += 5 + expGolombBits(encoded, kOffset) + expGolombBits(ch.length-2, kLen)
			writeBits(0b11111, 5)
			writeExpGolomb(encoded, kOffset)
			writeExpGolomb(ch.length-2, kLen)
			pos += ch.length
		}
	}

	// Emit terminator: backref0 prefix + 12 zeros (13 bits total)
	// Data uses at most 11 zeros, so 12 zeros triggers early exit.
	// Decoder checks for 12+ zeros BEFORE reading another bit, so no trailing 1 needed.
	writeBits(0b0, 1)  // backref0 prefix
	writeBits(0, 12)   // 12 zeros (terminator signal)

	// Record bit count before padding
	totalBits := bitPos

	// Pad to byte boundary
	for bitPos%8 != 0 {
		writeBits(0, 1)
	}

	return outBits, totalBits, stats
}

type bitReader struct {
	data    []byte
	bytePos int
	bitPos  int
}

func (r *bitReader) readBit() int {
	if r.bytePos >= len(r.data) {
		return 0
	}
	bit := (r.data[r.bytePos] >> (7 - r.bitPos)) & 1
	r.bitPos++
	if r.bitPos == 8 {
		r.bitPos = 0
		r.bytePos++
	}
	return int(bit)
}

func (r *bitReader) readBits(n int) int {
	val := 0
	for i := 0; i < n; i++ {
		val = (val << 1) | r.readBit()
	}
	return val
}

func (r *bitReader) readGamma() int {
	zeros := 0
	for r.readBit() == 0 {
		zeros++
	}
	return (1 << zeros) + r.readBits(zeros) - 1
}

func (r *bitReader) readExpGolomb(k int) int {
	q := r.readGamma()
	return (q << k) + r.readBits(k)
}

func decompress(compressed, selfDict, otherDict []byte, expectedLen int) []byte {
	reader := &bitReader{data: compressed}
	output := make([]byte, 0, expectedLen)
	otherLen := len(otherDict)

	// Memory layout: selfDict at $1000, otherDict at $7000
	const otherBase = 24576 // $6000

	// Backref byte access: at position pos with distance d
	getBackrefByte := func(pos, d int) byte {
		if d <= pos {
			return output[pos-d]
		}
		idx := pos + otherBase - d
		if idx < otherLen {
			return otherDict[idx]
		}
		return 0
	}

	for len(output) < expectedLen {
		if reader.readBit() == 0 {
			d := reader.readExpGolomb(kDist)
			// Terminator: d with 12+ leading zeros in gamma (d >= 16380)
			if d >= 16380 {
				break
			}
			length := reader.readExpGolomb(kLen) + 2
			dist := 3 * (d + 1)
			for i := 0; i < length; i++ {
				output = append(output, getBackrefByte(len(output), dist))
			}
		} else if reader.readBit() == 0 {
			b := reader.readBits(8)
			output = append(output, byte(b))
		} else if reader.readBit() == 0 {
			d := reader.readExpGolomb(kDist)
			length := reader.readExpGolomb(kLen) + 2
			dist := 3*(d+1) - 2
			for i := 0; i < length; i++ {
				output = append(output, getBackrefByte(len(output), dist))
			}
		} else if reader.readBit() == 0 {
			offset := reader.readExpGolomb(kOffset)
			length := reader.readExpGolomb(kLen) + 2
			ringPos := len(output) + offset
			for i := 0; i < length; i++ {
				if ringPos+i < bufferSize {
					output = append(output, selfDict[ringPos+i])
				} else {
					output = append(output, otherDict[ringPos+i-bufferSize])
				}
			}
		} else if reader.readBit() == 0 {
			d := reader.readExpGolomb(kDist)
			length := reader.readExpGolomb(kLen) + 2
			dist := 3*(d+1) - 1
			for i := 0; i < length; i++ {
				output = append(output, getBackrefByte(len(output), dist))
			}
		} else {
			encoded := reader.readExpGolomb(kOffset)
			length := reader.readExpGolomb(kLen) + 2
			ringPos := len(output) + encoded + bufferSize
			for i := 0; i < length; i++ {
				if ringPos+i < bufferSize {
					output = append(output, selfDict[ringPos+i])
				} else {
					output = append(output, otherDict[ringPos+i-bufferSize])
				}
			}
		}
	}

	return output
}

type compressResult struct {
	song       int
	compressed []byte
	bitCount   int
	verified   bool
	stats      compressStats
}

type bitWriter struct {
	data   []byte
	bitPos int
}

func (w *bitWriter) writeBits(val, count int) {
	for i := count - 1; i >= 0; i-- {
		if w.bitPos%8 == 0 {
			w.data = append(w.data, 0)
		}
		if (val>>i)&1 == 1 {
			w.data[len(w.data)-1] |= 1 << (7 - w.bitPos%8)
		}
		w.bitPos++
	}
}

func (w *bitWriter) writeGamma(n int) {
	b := bits.Len(uint(n + 1))
	for i := 0; i < b-1; i++ {
		w.writeBits(0, 1)
	}
	w.writeBits(n+1, b)
}

func (w *bitWriter) writeExpGolomb(n, k int) {
	w.writeGamma(n >> k)
	w.writeBits(n&((1<<k)-1), k)
}

func (w *bitWriter) padToByte() {
	for w.bitPos%8 != 0 {
		w.writeBits(0, 1)
	}
}

func (w *bitWriter) totalBits() int {
	return w.bitPos
}

func (w *bitWriter) copyBits(src []byte, bitCount int) {
	for i := 0; i < bitCount; i++ {
		byteIdx := i / 8
		bitIdx := 7 - (i % 8)
		bit := (src[byteIdx] >> bitIdx) & 1
		w.writeBits(int(bit), 1)
	}
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-vmtest":
			vmTestMain()
			return
		case "-asm":
			PrintDecompressorAsm()
			return
		default:
			fmt.Fprintf(os.Stderr, "Usage: %s [option]\n", os.Args[0])
			fmt.Fprintln(os.Stderr, "Options:")
			fmt.Fprintln(os.Stderr, "  (none)    Compress songs and write to build/")
			fmt.Fprintln(os.Stderr, "  -asm      Print 6502 decompressor assembly")
			fmt.Fprintln(os.Stderr, "  -vmtest   Run decompressor VM tests")
			os.Exit(1)
		}
	}
	// Load all songs in parallel
	songs := make(map[int][]byte)
	var loadWg sync.WaitGroup
	var loadMu sync.Mutex
	for i := 1; i <= 9; i++ {
		loadWg.Add(1)
		go func(idx int) {
			defer loadWg.Done()
			data, err := os.ReadFile(filepath.Join("uncompressed", fmt.Sprintf("d%dp.raw", idx)))
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading song %d: %v\n", idx, err)
				os.Exit(1)
			}
			normalizeSong(data)
			loadMu.Lock()
			songs[idx] = data
			loadMu.Unlock()
		}(i)
	}
	loadWg.Wait()

	os.MkdirAll("build", 0755)
	os.MkdirAll("generated", 0755)

	fmt.Println("V23 Delta Compression (Go)")
	fmt.Println("==========================")
	fmt.Printf("Memory layout: $%04X (odd), $%04X (even), %d bytes each\n\n", addrLow, addrHigh, bufferSize)

	// Precompute all buffer states - buffers are deterministic from original songs
	// Buffer state before compressing song N = result of "loading" songs 1..N-1
	type bufferState struct {
		buf1000    []byte // Full 24KB buffer at $1000
		buf7000    []byte // Full 24KB buffer at $7000
		len1000    int    // Valid length for prevSong (most recent song written)
		len7000    int
		hwm1000    int    // High water mark - max bytes ever written to $1000
		hwm7000    int    // High water mark - max bytes ever written to $7000
	}

	// Compute buffer state before each song
	states := make(map[int]bufferState)

	// Initial state: S1 at $1000, S2 at $7000
	buf1000 := make([]byte, bufferSize)
	buf7000 := make([]byte, bufferSize)
	copy(buf1000, songs[1])
	copy(buf7000, songs[2])
	len1000 := len(songs[1])
	len7000 := len(songs[2])
	hwm1000 := len(songs[1]) // High water mark tracks max length ever written
	hwm7000 := len(songs[2])

	for song := 3; song <= 9; song++ {
		// Save state BEFORE this song is written
		stateBuf1000 := make([]byte, hwm1000) // Only copy up to high water mark
		stateBuf7000 := make([]byte, hwm7000)
		copy(stateBuf1000, buf1000[:hwm1000])
		copy(stateBuf7000, buf7000[:hwm7000])
		states[song] = bufferState{stateBuf1000, stateBuf7000, len1000, len7000, hwm1000, hwm7000}

		// Simulate writing this song to its buffer
		if song%2 == 1 {
			copy(buf1000, songs[song])
			len1000 = len(songs[song])
			if len1000 > hwm1000 {
				hwm1000 = len1000
			}
		} else {
			copy(buf7000, songs[song])
			len7000 = len(songs[song])
			if len7000 > hwm7000 {
				hwm7000 = len7000
			}
		}
	}

	// Compress all songs in parallel
	var wg sync.WaitGroup
	results := make(chan compressResult, 9)

	// Song 1: no dictionaries available (first song)
	wg.Add(1)
	go func() {
		defer wg.Done()
		target := songs[1]
		emptyDict := []byte{}
		compressed, bitCount, stats := compress(target, emptyDict, emptyDict)
		decompressed := decompress(compressed, emptyDict, emptyDict, len(target))
		verified := len(decompressed) == len(target)
		if verified {
			for i := range target {
				if decompressed[i] != target[i] {
					verified = false
					break
				}
			}
		}
		results <- compressResult{1, compressed, bitCount, verified, stats}
	}()

	// Song 2: otherDict = song1 (already decompressed at $1000)
	wg.Add(1)
	go func() {
		defer wg.Done()
		target := songs[2]
		emptyDict := []byte{}
		compressed, bitCount, stats := compress(target, emptyDict, songs[1])
		decompressed := decompress(compressed, emptyDict, songs[1], len(target))
		verified := len(decompressed) == len(target)
		if verified {
			for i := range target {
				if decompressed[i] != target[i] {
					verified = false
					break
				}
			}
		}
		results <- compressResult{2, compressed, bitCount, verified, stats}
	}()

	// Songs 3-9: use pre-computed buffer states
	for song := 3; song <= 9; song++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			state := states[s]
			target := songs[s]

			var selfDict, otherDict []byte
			if s%2 == 1 {
				selfDict = state.buf1000
				otherDict = state.buf7000
			} else {
				selfDict = state.buf7000
				otherDict = state.buf1000
			}

			compressed, bitCount, stats := compress(target, selfDict, otherDict)

			// Verify by decompressing
			decompressed := decompress(compressed, selfDict, otherDict, len(target))
			verified := len(decompressed) == len(target)
			if verified {
				for i := range target {
					if decompressed[i] != target[i] {
						verified = false
						break
					}
				}
			}

			results <- compressResult{s, compressed, bitCount, verified, stats}
		}(song)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results and write files
	resultMap := make(map[int]compressResult)
	for r := range results {
		resultMap[r.song] = r
		outPath := filepath.Join("build", fmt.Sprintf("d%d_delta.bin", r.song))
		os.WriteFile(outPath, r.compressed, 0644)
	}

	// Print results in order
	totalOriginal := 0
	totalCompressed := 0
	allVerified := true
	var totalStats compressStats
	for song := 1; song <= 9; song++ {
		r := resultMap[song]
		totalOriginal += len(songs[song])
		totalCompressed += len(r.compressed)
		totalStats.literals += r.stats.literals
		totalStats.literalBits += r.stats.literalBits
		totalStats.selfRef0 += r.stats.selfRef0
		totalStats.selfRef0Bits += r.stats.selfRef0Bits
		totalStats.selfRef1 += r.stats.selfRef1
		totalStats.selfRef1Bits += r.stats.selfRef1Bits
		totalStats.selfRef2 += r.stats.selfRef2
		totalStats.selfRef2Bits += r.stats.selfRef2Bits
		totalStats.dictSelf += r.stats.dictSelf
		totalStats.dictSelfBits += r.stats.dictSelfBits
		totalStats.dictOther += r.stats.dictOther
		totalStats.dictOtherBits += r.stats.dictOtherBits
		for i := 0; i < 256; i++ {
			if r.stats.literalUsed[i] {
				totalStats.literalUsed[i] = true
			}
		}
		if r.stats.maxGammaZeros > totalStats.maxGammaZeros {
			totalStats.maxGammaZeros = r.stats.maxGammaZeros
		}
		destAddr := addrLow
		if song%2 == 0 {
			destAddr = addrHigh
		}
		status := "OK"
		if !r.verified {
			status = "FAIL"
			allVerified = false
		}
		fmt.Printf("Song %d -> $%04X: %d -> %d bytes (%d bits) [%s]\n", song, destAddr, len(songs[song]), len(r.compressed), r.bitCount, status)
	}

	fmt.Printf("\nTotal: %d -> %d bytes (%.1f%%)\n", totalOriginal, totalCompressed,
		100*float64(totalCompressed)/float64(totalOriginal))

	fmt.Println("\nCommand usage:")
	fmt.Printf("  backref0 (0):      %5d  %6d bits  %5d bytes\n", totalStats.selfRef0, totalStats.selfRef0Bits, totalStats.selfRef0Bits/8)
	fmt.Printf("  literal (10):      %5d  %6d bits  %5d bytes\n", totalStats.literals, totalStats.literalBits, totalStats.literalBits/8)
	fmt.Printf("  backref1 (110):    %5d  %6d bits  %5d bytes\n", totalStats.selfRef1, totalStats.selfRef1Bits, totalStats.selfRef1Bits/8)
	fmt.Printf("  fwdref (1110):     %5d  %6d bits  %5d bytes\n", totalStats.dictSelf, totalStats.dictSelfBits, totalStats.dictSelfBits/8)
	fmt.Printf("  backref2 (11110):  %5d  %6d bits  %5d bytes\n", totalStats.selfRef2, totalStats.selfRef2Bits, totalStats.selfRef2Bits/8)
	fmt.Printf("  copyother (11111): %5d  %6d bits  %5d bytes\n", totalStats.dictOther, totalStats.dictOtherBits, totalStats.dictOtherBits/8)
	totalCmds := totalStats.literals + totalStats.selfRef0 + totalStats.selfRef1 + totalStats.selfRef2 + totalStats.dictSelf + totalStats.dictOther
	totalBits := totalStats.literalBits + totalStats.selfRef0Bits + totalStats.selfRef1Bits + totalStats.selfRef2Bits + totalStats.dictSelfBits + totalStats.dictOtherBits
	fmt.Printf("  total:             %5d  %6d bits  %5d bytes\n", totalCmds, totalBits, totalBits/8)
	fmt.Printf("\nMax leading zeros in gamma: %d (terminator uses %d)\n", totalStats.maxGammaZeros, TerminatorZeros)
	if totalStats.maxGammaZeros >= TerminatorZeros {
		fmt.Fprintf(os.Stderr, "\nERROR: max gamma zeros (%d) >= terminator zeros (%d)\n", totalStats.maxGammaZeros, TerminatorZeros)
		fmt.Fprintf(os.Stderr, "Increase TerminatorZeros in decompress6502.go to at least %d\n", totalStats.maxGammaZeros+1)
		os.Exit(1)
	}

	var unusedLits []string
	for i := 0; i < 256; i++ {
		if !totalStats.literalUsed[i] {
			unusedLits = append(unusedLits, fmt.Sprintf("$%02X", i))
		}
	}
	if len(unusedLits) > 0 {
		fmt.Printf("\nUnused literal bytes: %s\n", strings.Join(unusedLits, " "))
	}

	// Generate concatenated bitstream by copying bits from already-compressed data
	// Each song's terminator includes the gamma terminating 1, so songs are self-contained
	w := &bitWriter{}
	for song := 1; song <= 9; song++ {
		r := resultMap[song]
		w.copyBits(r.compressed, r.bitCount)
	}

	w.padToByte()
	concatPath := filepath.Join("build", "all_songs.bin")
	os.WriteFile(concatPath, w.data, 0644)
	fmt.Printf("\nConcatenated bitstream: %d bits (%d bytes) -> %s\n", w.totalBits(), len(w.data), concatPath)

	// Split concatenated stream into main + tail (2,501 bytes)
	// Find command boundary in S9 where ~2501 bytes remain
	const tailTargetBytes = 2501
	s9Bits := resultMap[9].bitCount

	// Find command boundary by parsing S9's bitstream
	s9Data := resultMap[9].compressed
	reader := &bitReader{data: s9Data}
	var cmdBoundaries []int // bit positions where commands start
	cmdBoundaries = append(cmdBoundaries, 0)

	for reader.bytePos*8+reader.bitPos < s9Bits-13 { // -13 for terminator
		startBit := reader.bytePos*8 + reader.bitPos
		if reader.readBit() == 0 {
			d := reader.readExpGolomb(kDist)
			if d >= 16380 {
				break // terminator
			}
			reader.readExpGolomb(kLen)
		} else if reader.readBit() == 0 {
			reader.readBits(8)
		} else if reader.readBit() == 0 {
			reader.readExpGolomb(kDist)
			reader.readExpGolomb(kLen)
		} else if reader.readBit() == 0 {
			reader.readExpGolomb(kOffset)
			reader.readExpGolomb(kLen)
		} else if reader.readBit() == 0 {
			reader.readExpGolomb(kDist)
			reader.readExpGolomb(kLen)
		} else {
			reader.readExpGolomb(kOffset)
			reader.readExpGolomb(kLen)
		}
		cmdBoundaries = append(cmdBoundaries, reader.bytePos*8+reader.bitPos)
		_ = startBit
	}

	// Find earliest boundary that ensures tail fits in tailTargetBytes after byte padding
	// This maximizes tail usage while staying within the limit.
	// tail bytes = ceil((s9Bits - boundary) / 8)
	// We need: (s9Bits - boundary + 7) / 8 <= tailTargetBytes
	bestBoundary := 0
	for _, boundary := range cmdBoundaries {
		tailBits := s9Bits - boundary
		tailBytes := (tailBits + 7) / 8
		if tailBytes <= tailTargetBytes {
			bestBoundary = boundary
			break // Take the first (earliest) valid boundary
		}
	}

	// Calculate bits before S9 in concatenated stream
	bitsBeforeS9 := 0
	for song := 1; song <= 8; song++ {
		bitsBeforeS9 += resultMap[song].bitCount
	}

	// Build main stream: S1-S8 + S9[0:boundary] + terminator
	mainWriter := &bitWriter{}
	for song := 1; song <= 8; song++ {
		r := resultMap[song]
		mainWriter.copyBits(r.compressed, r.bitCount)
	}
	mainWriter.copyBits(s9Data, bestBoundary)
	mainWriter.writeBits(0b0, 1)  // terminator prefix
	mainWriter.writeBits(0, 12)   // terminator signal
	mainWriter.padToByte()

	// Build tail stream: S9[boundary:end] (already has terminator)
	tailWriter := &bitWriter{}
	tailReader := &bitReader{data: s9Data, bytePos: bestBoundary / 8, bitPos: bestBoundary % 8}
	tailBits := s9Bits - bestBoundary
	for i := 0; i < tailBits; i++ {
		tailWriter.writeBits(tailReader.readBit(), 1)
	}
	tailWriter.padToByte()

	mainPath := filepath.Join("generated", "stream_main.bin")
	tailPath := filepath.Join("generated", "stream_tail.bin")
	asmPath := filepath.Join("generated", "decompress.asm")
	os.WriteFile(mainPath, mainWriter.data, 0644)
	os.WriteFile(tailPath, tailWriter.data, 0644)
	WriteDecompressorAsm(asmPath)

	fmt.Printf("\nSplit stream: main %d bytes + tail %d bytes (target tail: %d)\n",
		len(mainWriter.data), len(tailWriter.data), tailTargetBytes)
	fmt.Printf("  S9 split at command boundary: bit %d of %d (%d bytes into S9)\n",
		bestBoundary, s9Bits, bestBoundary/8)

	if allVerified {
		fmt.Println("\nVerification: ALL PASSED")
	} else {
		fmt.Println("\nVerification: FAILED")
		os.Exit(1)
	}

	// Output checksums for selftest
	fmt.Println("\nSelftest checksums (16-bit additive):")
	fmt.Println("selftest_checksums:")
	for song := 1; song <= 9; song++ {
		var csum uint16
		for _, b := range songs[song] {
			csum += uint16(b)
		}
		fmt.Printf("        .word   $%04X               ; Song %d\n", csum, song)
	}
	fmt.Println("\nStream checksums:")
	var mainCsum uint16
	for _, b := range mainWriter.data {
		mainCsum += uint16(b)
	}
	var tailCsum uint16
	for _, b := range tailWriter.data {
		tailCsum += uint16(b)
	}
	fmt.Printf("selftest_stream_main_csum:  .word $%04X\n", mainCsum)
	fmt.Printf("selftest_stream_tail_csum:  .word $%04X\n", tailCsum)
}

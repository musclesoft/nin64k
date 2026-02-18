package simulate

import (
	"fmt"

	"forge/encode"
	"forge/serialize"
	"forge/transform"
)

// Frequency table for notes (matches odin_player.inc freqtable_lo/hi)
// Notes 0-95 are valid, notes 96-102 unused (0x0000), note 103 is remapped from 127
var freqTable = []uint16{
	0x0112, 0x0000, 0x0000, 0x0146, 0x015A, 0x016E, 0x0184, 0x019B, 0x01B3, 0x01CD, 0x01E9, 0x0206, // 0-11
	0x0225, 0x0245, 0x0268, 0x028C, 0x02B3, 0x02DC, 0x0308, 0x0336, 0x0367, 0x039B, 0x03D2, 0x040C, // 12-23
	0x0449, 0x048B, 0x04D0, 0x0519, 0x0567, 0x05B9, 0x0610, 0x066C, 0x06CE, 0x0735, 0x07A3, 0x0817, // 24-35
	0x0893, 0x0915, 0x099F, 0x0A32, 0x0ACD, 0x0B72, 0x0C20, 0x0CD8, 0x0D9C, 0x0E6B, 0x0F46, 0x102F, // 36-47
	0x1125, 0x122A, 0x133F, 0x1464, 0x159A, 0x16E3, 0x183F, 0x19B1, 0x1B38, 0x1CD6, 0x1E8D, 0x205E, // 48-59
	0x224B, 0x2455, 0x267E, 0x28C8, 0x2B34, 0x2DC6, 0x307F, 0x3361, 0x366F, 0x39AC, 0x3D1A, 0x40BC, // 60-71
	0x4495, 0x48A9, 0x4CFC, 0x518F, 0x5669, 0x5B8C, 0x60FE, 0x66C2, 0x6CDF, 0x7358, 0x7A34, 0x8178, // 72-83
	0x892B, 0x9153, 0x99F7, 0xA31F, 0xACD2, 0xB719, 0xC1FC, 0xCD85, 0xD9BD, 0xE6B0, 0xF467, 0xFFFF, // 84-95
	0x0000, 0x0000, 0x0000, 0x0000, 0x0000, 0x0000, 0x0000, 0x302F, // 96-103 (96-102 unused, 103 remapped from 127)
}

// Vibrato table (9 rows of 16 values each, depth 0 not stored)
// Index = (vibPos & 0x1F) where values 0-15 are used directly, 16-31 are mirrored
var vibratoTable = [][]byte{
	{0x00, 0x06, 0x0c, 0x13, 0x18, 0x1e, 0x24, 0x29, 0x2d, 0x31, 0x35, 0x38, 0x3b, 0x3d, 0x3f, 0x40}, // depth 1
	{0x00, 0x03, 0x06, 0x09, 0x0c, 0x0f, 0x12, 0x14, 0x17, 0x19, 0x1b, 0x1c, 0x1e, 0x1f, 0x1f, 0x20}, // depth 2
	{0x00, 0x05, 0x09, 0x0e, 0x12, 0x17, 0x1b, 0x1e, 0x22, 0x25, 0x28, 0x2a, 0x2c, 0x2e, 0x2f, 0x30}, // depth 3
	{0x00, 0x02, 0x03, 0x05, 0x06, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x0f, 0x10, 0x10}, // depth 4
	{0x00, 0x09, 0x13, 0x1c, 0x25, 0x2d, 0x35, 0x3d, 0x44, 0x4a, 0x50, 0x55, 0x59, 0x5c, 0x5e, 0x60}, // depth 5
	{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x66, 0x71, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x9f}, // depth 6
	{0x00, 0x08, 0x10, 0x17, 0x1f, 0x26, 0x2c, 0x33, 0x39, 0x3e, 0x43, 0x47, 0x4a, 0x4d, 0x4e, 0x50}, // depth 7
	{0x00, 0x0d, 0x19, 0x25, 0x31, 0x3c, 0x47, 0x51, 0x5b, 0x63, 0x6a, 0x71, 0x76, 0x7a, 0x7e, 0x7f}, // depth 8
	{0x00, 0x18, 0x2f, 0x46, 0x5c, 0x71, 0x85, 0x98, 0xaa, 0xba, 0xc8, 0xd4, 0xde, 0xe6, 0xeb, 0xef}, // depth 9
}

// VirtualPlayer emulates the ASM player behavior for validation.
// It processes converted song data and produces SID register writes.
type VirtualPlayer struct {
	songName string // For debug identification

	// Song data (from serialized output)
	fullData    []byte    // Complete song data (for reading patterns from gaps)
	instData    []byte    // 16 bytes per instrument
	numInst     int
	bitstream   []byte
	filterTable []byte
	arpTable    []byte
	transBase   byte
	deltaBase   byte
	rowDict     [3][]byte // Split dictionary: dict0, dict1, dict2
	packedPtrs  []uint16  // Pattern pointers (absolute offsets into fullData)
	gapCodes    []byte    // Gap codes for each pattern

	// Global tables (from player)
	deltaTable     []byte
	transposeTable []byte
	waveTable      []byte
	startConst     int

	// Channel state
	chn [3]channelState

	// Global state
	speed           int
	speedCounter    int
	mod3counter     int  // Cycles 2,1,0 every frame (for arpeggio)
	order           int
	nextOrder       int  // Next order for pattern break
	row             int
	hrRow           int  // Row for HR lookahead
	forceNewPattern bool // Pattern break flag

	// Filter state
	filterCutoff    byte
	filterResonance byte
	filterMode      byte
	globalVolume    byte
	filterIdx       byte
	filterEnd       byte
	filterLoop      byte

	// Output
	writes       []SIDWrite
	currentFrame int
}

type channelState struct {
	// Pattern decoding
	patIdx       int  // Current pattern index
	srcOff       int  // Offset into packed pattern data
	rleCount     int  // RLE repeat counter
	gapRemaining int  // Gap zeros remaining
	gap          int  // Gap value from gap code
	prevRow      [3]byte
	decodedRow   int  // Last decoded row number
	lastWasGap   bool // True if last decoded row was a gap zero

	// Playback state
	note        byte  // Row note (can be 0 for continuation)
	playingNote byte  // Currently sounding note (persists when row note is 0)
	inst        byte
	effect      byte
	param       byte
	transpose   int8
	trackptr    byte // Current pattern index for this order
	patArp      byte // Pattern arpeggio param (0 = none)
	permArp     byte // Permanent arpeggio param (0 = none, persists across NOP rows)

	// Instrument state
	instPtr    int  // Pointer to current instrument data (-1 = no instrument)
	instActive bool // Whether instrument is active
	waveIdx    byte
	arpIdx     byte
	filterIdx  byte

	// Pulse modulation state
	pulseSpeed    byte
	pulseLimitUp  byte // 4-bit high nibble limit
	pulseLimitDown byte // 4-bit high nibble limit
	pulseDir      byte // 0 = up, $80 = down

	// Vibrato state
	vibDelay byte
	vibDepth byte
	vibSpeed byte
	vibPos   byte

	// Slide state
	slideEnable  byte
	slideDeltaLo byte
	slideDeltaHi byte

	// Output registers
	freqLo     byte
	freqHi     byte
	noteFreqLo byte // Target frequency for portamento
	noteFreqHi byte
	finFreqLo  byte // Final frequency after vibrato
	finFreqHi  byte
	pulseLo   byte
	pulseHi   byte
	waveform  byte
	ad        byte
	sr        byte
	gateon    byte
	hardRestart byte
	hrActive    bool // True when HR is active (skip wave/ADSR processing)
}

// NewVirtualPlayer creates a player from converted song data and global tables.
func NewVirtualPlayer(
	songName string,
	songData []byte,
	deltaTable, transposeTable, waveTable []byte,
	transformed transform.TransformedSong,
	encoded encode.EncodedSong,
	startConst int,
) *VirtualPlayer {
	vp := &VirtualPlayer{
		songName:        songName,
		deltaTable:      deltaTable,
		transposeTable:  transposeTable,
		waveTable:       waveTable,
		startConst:      startConst,
		speed:           6,
		speedCounter:    5, // speed - 1, so first frame triggers row processing
		forceNewPattern: true, // First frame goes to newpattern which sets row=0
		globalVolume:    0x0F, // Max volume
	}

	// Parse song data layout
	vp.parseSongData(songData, len(encoded.PatternOffsets))

	// Initialize channels
	for ch := 0; ch < 3; ch++ {
		vp.chn[ch].gateon = 0xFE    // Gate off but waveform passes
		vp.chn[ch].decodedRow = -1
		vp.chn[ch].hardRestart = 2  // Default HR timing (can be changed by effect 4)
		vp.chn[ch].instPtr = -1     // No instrument active initially
	}

	return vp
}

func (vp *VirtualPlayer) parseSongData(data []byte, numPatterns int) {
	// Use layout constants from serialize package
	DictArraySize := serialize.DictArraySize
	PackedPtrsOffset := serialize.PackedPtrsOffset()

	// Store full data for reading patterns from gaps
	vp.fullData = data

	// Parse instrument data
	vp.instData = data[serialize.InstOffset:serialize.BitstreamOffset]
	vp.numInst = len(vp.instData) / 16

	// Parse bitstream
	bitstreamEnd := serialize.FilterOffset
	if bitstreamEnd > len(data) {
		bitstreamEnd = len(data)
	}
	vp.bitstream = data[serialize.BitstreamOffset:bitstreamEnd]

	// Parse filter and arp tables
	if serialize.FilterOffset < len(data) && serialize.ArpOffset <= len(data) {
		vp.filterTable = data[serialize.FilterOffset:serialize.ArpOffset]
	}
	if serialize.ArpOffset < len(data) && serialize.TransBaseOffset <= len(data) {
		vp.arpTable = data[serialize.ArpOffset:serialize.TransBaseOffset]
	}

	// Parse base values
	if serialize.TransBaseOffset < len(data) {
		vp.transBase = data[serialize.TransBaseOffset]
	}
	if serialize.DeltaBaseOffset < len(data) {
		vp.deltaBase = data[serialize.DeltaBaseOffset]
	}

	// Parse row dictionary (3 arrays of DictArraySize bytes each)
	if serialize.RowDictOffset+DictArraySize*3 <= len(data) {
		vp.rowDict[0] = data[serialize.RowDictOffset : serialize.RowDictOffset+DictArraySize]
		vp.rowDict[1] = data[serialize.RowDictOffset+DictArraySize : serialize.RowDictOffset+DictArraySize*2]
		vp.rowDict[2] = data[serialize.RowDictOffset+DictArraySize*2 : serialize.RowDictOffset+DictArraySize*3]
	}

	// Parse packed pattern pointers (keep as absolute offsets into fullData)
	if PackedPtrsOffset+numPatterns*2 <= len(data) {
		vp.packedPtrs = make([]uint16, numPatterns)
		vp.gapCodes = make([]byte, numPatterns)
		for i := 0; i < numPatterns; i++ {
			lo := data[PackedPtrsOffset+i*2]
			hi := data[PackedPtrsOffset+i*2+1]
			absPtr := uint16(lo) | uint16(hi&0x1F)<<8
			// Keep as absolute offset - patterns may be in gaps
			vp.packedPtrs[i] = absPtr
			vp.gapCodes[i] = hi >> 5
		}
	}

	if vpDebug {
		patternDataStart := PackedPtrsOffset + numPatterns*2
		fmt.Printf("  parseSongData: numPatterns=%d patternDataStart=$%04X dataLen=$%04X\n",
			numPatterns, patternDataStart, len(data))
		if len(vp.packedPtrs) > 0 {
			fmt.Printf("  first 3 pattern ptrs: $%04X $%04X $%04X\n",
				vp.packedPtrs[0], vp.packedPtrs[min(1, len(vp.packedPtrs)-1)], vp.packedPtrs[min(2, len(vp.packedPtrs)-1)])
		}
	}
}

// RunFrames runs the player for the specified number of frames.
func (vp *VirtualPlayer) RunFrames(frames int) []SIDWrite {
	vp.writes = nil

	// ASM: ordernumber=0, nextordernumber=0 at init
	// On first frame, forceNewPattern triggers newpattern:
	// ordernumber = nextordernumber (0), nextordernumber++ (1)
	vp.order = 0
	vp.nextOrder = 0
	vp.row = 0

	for frame := 0; frame < frames; frame++ {
		vp.currentFrame = frame
		vp.playFrame()
	}

	return vp.writes
}

func (vp *VirtualPlayer) initOrder(orderNum int) {
	vp.order = orderNum

	// Decode bitstream for this order
	bsOff := orderNum * 4
	if bsOff+4 > len(vp.bitstream) {
		return
	}

	bs0 := vp.bitstream[bsOff]
	bs1 := vp.bitstream[bsOff+1]
	bs2 := vp.bitstream[bsOff+2]
	bs3 := vp.bitstream[bsOff+3]

	// Extract transpose indices (4 bits each)
	trans0 := bs0 & 0x0F
	trans1 := bs0 >> 4
	trans2 := bs1 & 0x0F

	// Extract trackptr indices (5 bits each, packed)
	delta0 := ((bs1 >> 4) & 0x0F) | ((bs2 & 0x01) << 4)
	delta1 := (bs2 >> 1) & 0x1F
	delta2 := ((bs2 >> 6) & 0x03) | ((bs3 & 0x07) << 2)

	// Look up actual values from tables
	for ch := 0; ch < 3; ch++ {
		var transIdx, deltaIdx byte
		switch ch {
		case 0:
			transIdx, deltaIdx = trans0, delta0
		case 1:
			transIdx, deltaIdx = trans1, delta1
		case 2:
			transIdx, deltaIdx = trans2, delta2
		}

		// Get transpose value
		tIdx := int(vp.transBase) + int(transIdx)
		if tIdx < len(vp.transposeTable) {
			vp.chn[ch].transpose = int8(vp.transposeTable[tIdx])
		}

		// Get delta and compute trackptr
		dIdx := int(vp.deltaBase) + int(deltaIdx)
		if dIdx < len(vp.deltaTable) {
			delta := int8(vp.deltaTable[dIdx])
			if orderNum == 0 {
				// First order uses start constant
				vp.chn[ch].trackptr = byte(vp.startConst + int(delta))
			} else {
				vp.chn[ch].trackptr = byte(int(vp.chn[ch].trackptr) + int(delta))
			}
		}

		// Initialize pattern decoder for this channel
		vp.initPattern(ch, int(vp.chn[ch].trackptr))
	}
}

func (vp *VirtualPlayer) initPattern(ch, patIdx int) {
	if patIdx < 0 || patIdx >= len(vp.packedPtrs) {
		if vpDebug {
			fmt.Printf("  initPattern ch%d: patIdx %d out of range (max %d)\n", ch, patIdx, len(vp.packedPtrs))
		}
		return
	}

	vp.chn[ch].patIdx = patIdx
	vp.chn[ch].srcOff = 0
	vp.chn[ch].rleCount = 0
	vp.chn[ch].gapRemaining = 0
	vp.chn[ch].decodedRow = -1
	vp.chn[ch].prevRow = [3]byte{}
	vp.chn[ch].lastWasGap = false

	// Get gap value from gap code
	gapCode := vp.gapCodes[patIdx]
	gapValues := []int{0, 1, 3, 7, 15, 31, 63}
	if int(gapCode) < len(gapValues) {
		vp.chn[ch].gap = gapValues[gapCode]
	}

	if vpDebug {
		ptrOff := vp.packedPtrs[patIdx]
		firstBytes := "n/a"
		if int(ptrOff)+3 <= len(vp.fullData) {
			firstBytes = fmt.Sprintf("%02X %02X %02X", vp.fullData[ptrOff], vp.fullData[ptrOff+1], vp.fullData[ptrOff+2])
		}
		fmt.Printf("  initPattern ch%d: patIdx=%d ptr=$%04X gap=%d firstBytes=%s\n",
			ch, patIdx, ptrOff, vp.chn[ch].gap, firstBytes)
	}
}

func (vp *VirtualPlayer) playFrame() {
	// Update mod3counter (cycles 2,1,0 every frame for arpeggio)
	vp.mod3counter--
	if vp.mod3counter < 0 {
		vp.mod3counter = 2
	}

	// ASM increments speedcounter FIRST, then checks if == speed
	vp.speedCounter++

	// Process new row if speed counter reached speed (and reset to 0)
	if vp.speedCounter >= vp.speed {
		vp.speedCounter = 0

		// ASM advances row BEFORE processing (except for forcenewpattern which sets row=0)
		if vp.forceNewPattern {
			// Pattern break or first frame - jump to next order, row 0
			vp.row = 0
			vp.order = vp.nextOrder
			vp.nextOrder++
			vp.forceNewPattern = false
			vp.initOrder(vp.order)
		} else {
			// Normal row advance
			vp.row++
			if vp.row >= 64 {
				// End of pattern, go to next order
				vp.row = 0
				vp.order = vp.nextOrder
				vp.nextOrder++
				vp.initOrder(vp.order)
			}
		}

		// Process the row we're now at
		vp.processRow()
	}

	// Process instruments (wave table, arp, etc.)
	for ch := 0; ch < 3; ch++ {
		vp.processInstrument(ch)
	}

	// Process filter table
	vp.processFilter()

	// Check HR timing for each channel (happens AFTER instrument processing in ASM)
	// Uses speedCounter value AFTER potential reset to 0
	for ch := 0; ch < 3; ch++ {
		shouldCheck := vp.speedCounter+int(vp.chn[ch].hardRestart) >= vp.speed
		if shouldCheck {
			vp.checkHardRestart(ch)
		}
	}

	// Dump registers to SID
	vp.dumpRegisters()
}

var vpDebug = false
var vpDebugSong = ""
var vpDebugFrame = 0

func debugNearFrame(frame int) bool {
	return vpDebugFrame > 0 && frame >= vpDebugFrame-5 && frame <= vpDebugFrame+5
}

func (vp *VirtualPlayer) processRow() {
	for ch := 0; ch < 3; ch++ {
		// Decode row for this channel
		row := vp.decodeRow(ch, vp.row)

		note := row[0] & 0x7F
		inst := row[1] & 0x1F
		effect := (row[1] >> 5) | ((row[0] >> 4) & 8)
		param := row[2]

		if vpDebug && (vp.currentFrame < 8 || (vp.currentFrame >= 360 && vp.currentFrame <= 420) || (ch == 1 && vp.currentFrame >= 9875 && vp.currentFrame <= 9895) || debugNearFrame(vp.currentFrame)) {
			fmt.Printf("  [f%d] ch%d row%d: note=%02X inst=%d eff=%d param=%02X raw=%02X %02X %02X order=%d arpIdx=%d\n",
				vp.currentFrame, ch, vp.row, note, inst, effect, param, row[0], row[1], row[2], vp.order, vp.chn[ch].arpIdx)
		}

		// Store current row data
		vp.chn[ch].note = note
		vp.chn[ch].inst = inst
		vp.chn[ch].effect = effect
		vp.chn[ch].param = param
		c := &vp.chn[ch]

		// ASM: First load instrument params if inst > 0 (regardless of note/effect)
		if inst > 0 {
			vp.triggerInstrument(ch, int(inst))
			if vpDebug && vp.currentFrame < 2 {
				fmt.Printf("    -> ch%d triggered inst %d, pulseLo=%02X\n", ch, inst, c.pulseLo)
			}
		}

		// Handle note on
		// Note format: $00=none, $01-$60=notes 0-95, $61=key off
		// ASM: chn_note is set for ALL notes, including portamento
		// ASM always resets wave/arp/slide on ANY note, even portamento
		if note > 0 && note < 0x61 {
			c.playingNote = note // Always set playingNote (chn_note in ASM)
			if effect == 2 {
				// Portamento: set target frequency
				// If there's also an instrument trigger, set gate on
				if inst > 0 {
					c.gateon = 0xFF
				}
				targetNote := int(note) - 1 + int(c.transpose)
				if targetNote >= 0 && targetNote < len(freqTable) {
					freq := freqTable[targetNote]
					c.noteFreqLo = byte(freq)
					c.noteFreqHi = byte(freq >> 8)
					if vpDebug && ch == 2 {
						fmt.Printf("    [f%d] ch2 noteFreq set: note=%02X trans=%d -> targetNote=%d freq=%04X\n",
							vp.currentFrame, note, c.transpose, targetNote, freq)
					}
				}
			} else {
				// Non-portamento: set gate, end HR phase
				c.gateon = 0xFF
				c.hrActive = false // End HR phase when note plays
			}
			// ASM always resets wave, arp, and slide for ANY note (including portamento)
			if c.instPtr >= 0 && c.instPtr+8 < len(vp.instData) {
				c.waveIdx = vp.instData[c.instPtr+2] // WaveStart
				c.arpIdx = vp.instData[c.instPtr+5]  // ArpStart
			}
			c.slideDeltaLo = 0
			c.slideDeltaHi = 0
			c.slideEnable = 0
			// Note: permArp is NOT cleared on note - ASM player preserves permArp across notes
		} else if note == 0x61 { // Key off ($61 = note off)
			c.gateon = 0xFE
			if vpDebug && ch == 2 && vp.currentFrame >= 360 && vp.currentFrame <= 420 {
				fmt.Printf("    [f%d] ch2 key-off: gateon set to FE\n", vp.currentFrame)
			}
		}

		// Handle effects
		vp.processEffect(ch, effect, param)
	}
}

func (vp *VirtualPlayer) decodeRow(ch, targetRow int) [3]byte {
	c := &vp.chn[ch]

	// If already decoded this row, return cached value
	if c.decodedRow == targetRow {
		if c.lastWasGap {
			return [3]byte{}
		}
		return c.prevRow
	}

	// Need to decode from current position to target
	for c.decodedRow < targetRow {
		c.decodedRow++
		vp.decodeAdvanceRow(ch)
	}

	// Return zeros if this row was a gap row
	if c.lastWasGap {
		return [3]byte{}
	}
	return c.prevRow
}

func (vp *VirtualPlayer) decodeAdvanceRow(ch int) {
	c := &vp.chn[ch]

	// Check for gap zeros
	if c.gapRemaining > 0 {
		c.gapRemaining--
		c.lastWasGap = true
		return
	}

	// Check for RLE
	if c.rleCount > 0 {
		c.rleCount--
		c.lastWasGap = false // RLE repeats the previous row data
		if c.gap > 0 {
			c.gapRemaining = c.gap
		}
		return
	}

	// Read packed pattern data - this is a real row, not a gap
	c.lastWasGap = false

	if c.patIdx < 0 || c.patIdx >= len(vp.packedPtrs) {
		return
	}

	ptrOff := int(vp.packedPtrs[c.patIdx])
	srcOff := ptrOff + c.srcOff
	if srcOff >= len(vp.fullData) {
		// End of pattern data - treat as zeros (no note/effect)
		c.prevRow = [3]byte{}
		c.lastWasGap = true
		return
	}

	b := vp.fullData[srcOff]

	showDecode := vpDebug && (c.decodedRow < 2 || (ch == 2 && c.decodedRow >= 14 && c.decodedRow <= 20))
	if showDecode {
		fmt.Printf("    decodeAdvance ch%d row%d: byte=$%02X at srcOff=%d\n", ch, c.decodedRow, b, c.srcOff)
	}

	if b < 0x10 {
		// $00-$0F: dict[0] (zeros) with RLE 0-15
		c.prevRow = [3]byte{}
		c.rleCount = int(b)
		c.srcOff++
		if showDecode {
			fmt.Printf("      -> zeros with RLE %d\n", c.rleCount)
		}
	} else if b < 0xEF {
		// $10-$EE: dict[1-223]
		idx := int(b) - 0x0F // $10->1, $11->2, etc.
		c.prevRow = vp.lookupDict(idx)
		c.srcOff++
		if showDecode {
			fmt.Printf("      -> dict[%d] = %02X %02X %02X\n", idx, c.prevRow[0], c.prevRow[1], c.prevRow[2])
		}
	} else if b < 0xFE {
		// $EF-$FD: RLE 1-15
		c.rleCount = int(b) - 0xEF
		c.srcOff++
		if showDecode {
			fmt.Printf("      -> RLE %d (repeat prev)\n", c.rleCount)
		}
	} else if b == 0xFE {
		// $FE: note-only (keep inst/eff/param, change note)
		c.srcOff++
		if srcOff+1 < len(vp.fullData) {
			noteByte := vp.fullData[srcOff+1]
			c.prevRow[0] = noteByte // Only update note byte, keep inst/eff/param
			c.srcOff++
			if showDecode {
				fmt.Printf("      -> note-only: %02X (inst/eff unchanged)\n", noteByte)
			}
		}
	} else {
		// $FF: extended dict index
		c.srcOff++
		if srcOff+1 < len(vp.fullData) {
			extByte := vp.fullData[srcOff+1]
			idx := 224 + int(extByte)
			c.prevRow = vp.lookupDict(idx)
			c.srcOff++
			if showDecode {
				fmt.Printf("      -> ext dict[%d] = %02X %02X %02X\n", idx, c.prevRow[0], c.prevRow[1], c.prevRow[2])
			}
		}
	}

	// Apply gap zeros for subsequent rows
	if c.gap > 0 {
		c.gapRemaining = c.gap
	}
}

func (vp *VirtualPlayer) lookupDict(idx int) [3]byte {
	if idx <= 0 {
		return [3]byte{}
	}
	// Dictionary is 1-indexed, arrays are 0-indexed
	arrIdx := idx - 1
	if arrIdx >= len(vp.rowDict[0]) {
		return [3]byte{}
	}
	return [3]byte{
		vp.rowDict[0][arrIdx],
		vp.rowDict[1][arrIdx],
		vp.rowDict[2][arrIdx],
	}
}

func (vp *VirtualPlayer) triggerInstrument(ch, inst int) {
	if inst <= 0 || inst > vp.numInst {
		return
	}

	c := &vp.chn[ch]
	base := (inst - 1) * 16

	c.instPtr = base
	c.instActive = true
	c.ad = vp.instData[base+0]
	c.sr = vp.instData[base+1]
	c.waveIdx = vp.instData[base+2]   // WaveStart
	c.arpIdx = vp.instData[base+5]    // ArpStart
	c.filterIdx = vp.instData[base+13] // FilterStart
	// Note: hardRestart is NOT reset on instrument trigger - only by effect 4

	// Initialize pulse from instrument
	// Byte 10 is nibble-swapped PulseWidth: original $XY -> stored $YX
	// ASM splits directly: high nibble -> pulse_lo, low nibble -> pulse_hi
	pw := vp.instData[base+10]
	c.pulseLo = pw & 0xF0
	c.pulseHi = pw & 0x0F
	c.pulseSpeed = vp.instData[base+11]
	// Byte 12 has limits: high nibble = limit down, low nibble = limit up
	limits := vp.instData[base+12]
	c.pulseLimitDown = limits >> 4
	c.pulseLimitUp = limits & 0x0F
	c.pulseDir = 0 // Start modulating up (0 = up, $80 = down)

	// Initialize vibrato from instrument
	c.vibDelay = vp.instData[base+8] // INST_VIBDELAY
	c.vibPos = 0

	if vpDebug && vp.currentFrame < 5 {
		arpEnd := vp.instData[base+6]
		arpLoop := vp.instData[base+7]
		fmt.Printf("      inst%d base=$%04X: AD=%02X SR=%02X waveIdx=%d arpIdx=%d arpEnd=%d arpLoop=%d pw=%02X\n",
			inst, base, c.ad, c.sr, c.waveIdx, c.arpIdx, arpEnd, arpLoop, c.pulseLo)
		// Show arp table values around the index
		if int(c.arpIdx) < len(vp.arpTable) {
			fmt.Printf("        arpTable[%d..%d]: ", c.arpIdx, min(int(c.arpIdx)+5, len(vp.arpTable)-1))
			for i := int(c.arpIdx); i < int(c.arpIdx)+6 && i < len(vp.arpTable); i++ {
				fmt.Printf("%02X ", vp.arpTable[i])
			}
			fmt.Println()
		}
	}
}

func (vp *VirtualPlayer) processInstrument(ch int) {
	c := &vp.chn[ch]

	if vpDebug && ch == 2 && vp.currentFrame >= 20249 && vp.currentFrame <= 20261 {
		fmt.Printf("  [f%d] procInst ch2: effect=%d instActive=%v instPtr=%d\n",
			vp.currentFrame, c.effect, c.instActive, c.instPtr)
	}

	// Only process if instrument is active
	if !c.instActive {
		c.finFreqLo = c.freqLo
		c.finFreqHi = c.freqHi
		return
	}

	if c.instPtr < 0 {
		c.finFreqLo = c.freqLo
		c.finFreqHi = c.freqHi
		return
	}

	// Wave table processing - runs first (matching odin_player order)
	// Wave table advances waveidx and sets waveform
	if c.waveIdx != 255 && int(c.waveIdx) < len(vp.waveTable) {
		oldWave := c.waveform
		c.waveform = vp.waveTable[c.waveIdx]
		if vpDebug && ch == 2 && vp.currentFrame >= 405 && vp.currentFrame <= 412 {
			fmt.Printf("    [f%d] ch2 wave: idx=%d -> waveform=%02X (was %02X)\n",
				vp.currentFrame, c.waveIdx, c.waveform, oldWave)
		}
		c.waveIdx++

		// Check wave end
		waveEnd := vp.instData[c.instPtr+3]
		if c.waveIdx >= waveEnd {
			c.waveIdx = vp.instData[c.instPtr+4] // Wave loop
		}
	} else if vpDebug && ch == 2 && vp.currentFrame >= 405 && vp.currentFrame <= 412 {
		fmt.Printf("    [f%d] ch2 wave: skipped (eff=%d waveIdx=%d), waveform=%02X\n",
			vp.currentFrame, c.effect, c.waveIdx, c.waveform)
	}

	// PlayerEffectWave (set waveform) runs AFTER wave table - overrides waveform but doesn't disable wave table
	// Original GT effect 9 behavior: just override waveform, let wave table keep advancing
	// This allows wave table to resume when effect changes to 0
	if c.effect == PlayerEffectWave {
		c.waveform = c.param
		if vpDebug && ch == 2 && vp.currentFrame >= 405 && vp.currentFrame <= 412 {
			fmt.Printf("    [f%d] ch2 eff7: overriding waveform=%02X from param\n", vp.currentFrame, c.param)
		}
	}

	// ASM: Skip arp/freq calculation if effect is PlayerEffectPorta (portamento)
	// Portamento keeps current freq and slides it toward target
	if c.effect == PlayerEffectPorta {
		goto skipArpFreq
	}

	// Calculate frequency from playing note + transpose + instrument arp
	// Note values are 1-indexed: $01-$60 = notes 0-95, so subtract 1
	// Use playingNote (not row note) because note=0 means "continue playing"
	{
		note := int(c.playingNote)
		if note > 0 && note < 0x61 {
			// Get arp offset from instrument arp table
			// ASM logic: if bit 7 set, value is absolute note; else relative offset
			var finalNote int
			origArpIdx := c.arpIdx
			var origArpVal byte
			if c.arpIdx < 255 && int(c.arpIdx) < len(vp.arpTable) {
				origArpVal = vp.arpTable[c.arpIdx]
				arpVal := origArpVal
				if arpVal&0x80 != 0 {
					// Absolute note value (bit 7 set) - note 127 will clamp to 103
					finalNote = int(arpVal & 0x7F)
				} else {
					// Relative offset (bit 7 clear)
					finalNote = (note - 1) + int(c.transpose) + int(int8(arpVal))
				}

				// Advance arp index
				arpEnd := vp.instData[c.instPtr+6]
				c.arpIdx++
				if c.arpIdx >= arpEnd {
					c.arpIdx = vp.instData[c.instPtr+7] // Arp loop
				}
			} else {
				finalNote = (note - 1) + int(c.transpose)
			}


			if vpDebug && (vp.currentFrame < 6 || (vp.currentFrame >= 25 && vp.currentFrame <= 30) || (vp.currentFrame >= 20249 && vp.currentFrame <= 20261) || (ch == 1 && vp.currentFrame >= 9891 && vp.currentFrame <= 9895) || debugNearFrame(vp.currentFrame)) {
				arpDbg := "none"
				if origArpIdx < 255 && int(origArpIdx) < len(vp.arpTable) {
					arpDbg = fmt.Sprintf("idx=%d val=%02X", origArpIdx, origArpVal)
				}
				arpEnd := byte(0)
				arpLoop := byte(0)
				arpStart := byte(0)
				if c.instPtr >= 0 && c.instPtr+8 < len(vp.instData) {
					arpStart = vp.instData[c.instPtr+5]
					arpEnd = vp.instData[c.instPtr+6]
					arpLoop = vp.instData[c.instPtr+7]
				}
				fmt.Printf("    [f%d] freq ch%d: note=%d trans=%d arp=%s start=%d end=%d loop=%d -> final=%d\n",
					vp.currentFrame, ch, note, c.transpose, arpDbg, arpStart, arpEnd, arpLoop, finalNote)
				// Dump arp table around current index
				if origArpIdx < 255 {
					fmt.Printf("      arpTable[%d-%d]: ", max(0, int(origArpIdx)-2), min(len(vp.arpTable)-1, int(origArpIdx)+5))
					for i := max(0, int(origArpIdx)-2); i <= min(len(vp.arpTable)-1, int(origArpIdx)+5); i++ {
						marker := ""
						if i == int(origArpIdx) {
							marker = "*"
						}
						fmt.Printf("%s%02X ", marker, vp.arpTable[i])
					}
					fmt.Println()
				}
			}
			if finalNote < 0 {
				finalNote = 0
			}
			if finalNote >= len(freqTable) {
				finalNote = len(freqTable) - 1
			}
			freq := freqTable[finalNote]
			c.freqLo = byte(freq)
			c.freqHi = byte(freq >> 8)
			// ASM also sets notefreq for non-portamento (lines 563-567 of odin_player)
			c.noteFreqLo = byte(freq)
			c.noteFreqHi = byte(freq >> 8)
		}
	}

skipArpFreq:

	// ASM: PlayerEffectArp (pattern arpeggio) runs AFTER instrument arp and OVERWRITES frequency
	// This ensures arpIdx continues to advance even during pattern arpeggio
	// Trigger on PlayerEffectArp (regular arp), PlayerEffectPermArp2 (perm arp), or effect 0 with active permArp
	if c.effect == PlayerEffectArp || c.effect == PlayerEffectPermArp2 || (c.effect == PlayerEffectSpecial && c.patArp != 0) {
		note := int(c.playingNote)
		if note > 0 && note < 0x61 {
			// Pattern arpeggio: mod3counter 0=low, 1=high, >=2=base
			// ASM: beq @arp_val2 (mod3=0), dey/bne @arp_val0 (mod3=1 goes to val2, mod3=2 goes to val0)
			// So: mod3=0 -> param low nibble, mod3=1 -> param high nibble, mod3=2 -> base note
			var arpOffset int
			switch vp.mod3counter {
			case 0:
				arpOffset = int(c.patArp & 0x0F) // Low nibble
			case 1:
				arpOffset = int(c.patArp >> 4) // High nibble
			default:
				arpOffset = 0 // Base note
			}
			finalNote := (note - 1) + arpOffset + int(c.transpose)
			if vpDebug && ch == 1 && vp.currentFrame >= 9875 && vp.currentFrame <= 9895 {
				fmt.Printf("    [f%d] patArp ch%d: note=%d arpOff=%d trans=%d mod3=%d -> final=%d\n",
					vp.currentFrame, ch, note, arpOffset, c.transpose, vp.mod3counter, finalNote)
			}
			if finalNote < 0 {
				finalNote = 0
			}
			if finalNote >= len(freqTable) {
				finalNote = len(freqTable) - 1
			}
			freq := freqTable[finalNote]
			c.freqLo = byte(freq)
			c.freqHi = byte(freq >> 8)
			c.noteFreqLo = byte(freq)
			c.noteFreqHi = byte(freq >> 8)
		}
	}

	// Vibrato processing - clear depth at start, then set from instrument if delay expired
	c.vibDepth = 0
	if c.vibDelay > 0 {
		c.vibDelay--
	} else {
		// Load vibrato params from instrument
		vibDepSp := vp.instData[c.instPtr+9] // INST_VIBDEPSP
		c.vibDepth = vibDepSp & 0xF0         // High nibble
		if c.vibDepth != 0 {
			c.vibSpeed = vibDepSp & 0x0F // Low nibble
		}
	}

	// Pulse modulation - matches ASM pi_pulse logic
	// Only runs if pulseSpeed != 0 AND effect is not PlayerEffectPulse (effect 8 overrides)
	if c.pulseSpeed != 0 && c.effect != PlayerEffectPulse {
		if c.pulseDir == 0 {
			// Going up
			newLo := int(c.pulseLo) + int(c.pulseSpeed)
			carry := byte(0)
			if newLo > 255 {
				carry = 1
			}
			c.pulseLo = byte(newLo)
			newHi := int(c.pulseHi) + int(carry)

			// Compare pulseHi with limitUp (signed comparison: bmi/beq means <=)
			if newHi >= int(c.pulseLimitUp) && newHi > int(c.pulseLimitUp) {
				// Exceeded limit - clamp and reverse
				c.pulseDir = 0x80
				c.pulseLo = 0xFF
				c.pulseHi = c.pulseLimitUp
			} else {
				c.pulseHi = byte(newHi)
			}
		} else {
			// Going down
			newLo := int(c.pulseLo) - int(c.pulseSpeed)
			borrow := byte(0)
			if newLo < 0 {
				borrow = 1
				newLo += 256
			}
			c.pulseLo = byte(newLo)
			newHi := int(c.pulseHi) - int(borrow)

			// Compare pulseHi with limitDown (signed comparison: bpl means >=)
			if newHi < int(c.pulseLimitDown) {
				// Below limit - clamp and reverse
				c.pulseDir = 0
				c.pulseLo = 0
				c.pulseHi = c.pulseLimitDown
			} else {
				c.pulseHi = byte(newHi)
			}
		}
	}

	// PlayerEffectPulse (pulse width) runs every frame - overrides pulse modulation
	// odin_player: param!=0 -> hi=$08,lo=$00; param==0 -> hi=$00,lo=$00
	if c.effect == PlayerEffectPulse {
		if c.param != 0 {
			c.pulseHi = 0x08
		} else {
			c.pulseHi = 0x00
		}
		c.pulseLo = 0x00
	}

	// PlayerEffectSpecial param PlayerParam0VibOff (GT vibrato disable) runs AFTER vibdepth is loaded from instrument
	// This matches ASM order: pi_hasinst loads vibdepth, then pi_noinst effect 0/1 clears it
	if c.effect == PlayerEffectSpecial && c.param == PlayerParam0VibOff {
		c.vibDepth = 0
		c.vibSpeed = 0
	}

	// Portamento (PlayerEffectPorta) - slides current freq toward noteFreq every frame
	if c.effect == PlayerEffectPorta {
		// Param is nibble-swapped: $XY original -> $YX stored
		// Speed: low byte = $Y0, high byte = $0X -> 16-bit speed = $0XY0
		speedLo := c.param & 0xF0
		speedHi := c.param & 0x0F

		// Compare current freq to target noteFreq
		currFreq := int(c.freqLo) | (int(c.freqHi) << 8)
		targetFreq := int(c.noteFreqLo) | (int(c.noteFreqHi) << 8)
		speed := int(speedLo) | (int(speedHi) << 8)

		if vpDebug && vp.currentFrame >= 1918 && vp.currentFrame <= 1924 && ch == 1 {
			fmt.Printf("    [f%d] porta ch1: curr=%04X target=%04X speed=%04X param=%02X\n",
				vp.currentFrame, currFreq, targetFreq, speed, c.param)
		}
		if vpDebug && vp.currentFrame >= 20249 && vp.currentFrame <= 20261 && ch == 2 {
			fmt.Printf("    [f%d] porta ch2: curr=%04X target=%04X speed=%04X param=%02X -> newFreq=%04X\n",
				vp.currentFrame, currFreq, targetFreq, speed, c.param, currFreq+speed)
		}

		if currFreq < targetFreq {
			// Slide up
			newFreq := currFreq + speed
			if newFreq >= targetFreq {
				// Overshoot - snap to target
				c.freqLo = c.noteFreqLo
				c.freqHi = c.noteFreqHi
			} else {
				c.freqLo = byte(newFreq)
				c.freqHi = byte(newFreq >> 8)
			}
		} else if currFreq > targetFreq {
			// Slide down
			newFreq := currFreq - speed
			if newFreq <= targetFreq {
				// Overshoot - snap to target
				c.freqLo = c.noteFreqLo
				c.freqHi = c.noteFreqHi
			} else {
				c.freqLo = byte(newFreq)
				c.freqHi = byte(newFreq >> 8)
			}
		}
	}

	// Slide effect - accumulate delta and apply to frequency
	// ASM: PlayerEffectSlide accumulates $20 to delta every frame the effect is active
	if c.effect == PlayerEffectSlide {
		c.slideEnable = 0x80
		if c.param == 0 {
			// Slide up: add $20 to slideDelta
			newLo := int(c.slideDeltaLo) + 0x20
			if newLo > 255 {
				c.slideDeltaHi++
			}
			c.slideDeltaLo = byte(newLo)
		} else {
			// Slide down: subtract $20 from slideDelta
			newLo := int(c.slideDeltaLo) - 0x20
			if newLo < 0 {
				c.slideDeltaHi--
			}
			c.slideDeltaLo = byte(newLo)
		}
	}

	// Apply slide to frequency (unsigned 16-bit addition matching ASM)
	// ASM: adc chn_slidedelta_lo,x / adc chn_slidedelta_hi,x
	if c.slideEnable != 0 {
		newLo := int(c.freqLo) + int(c.slideDeltaLo)
		carry := 0
		if newLo > 255 {
			carry = 1
		}
		c.freqLo = byte(newLo)
		newHi := int(c.freqHi) + int(c.slideDeltaHi) + carry
		c.freqHi = byte(newHi)
	}

	// Apply vibrato to get final frequency
	if c.vibDepth == 0 {
		// No vibrato - finFreq equals base freq
		c.finFreqLo = c.freqLo
		c.finFreqHi = c.freqHi
	} else {
		// Calculate vibrato offset
		pos := c.vibPos & 0x1F
		if pos >= 0x10 {
			pos = pos ^ 0x1F // Mirror for 16-31
		}
		// Combine position with depth to index vibrato table
		depthRow := int(c.vibDepth>>4) - 1 // Convert depth nibble to row index (1->0, 2->1, etc)
		if depthRow < 0 || depthRow >= len(vibratoTable) {
			depthRow = 0
		}
		vibOffset := int(vibratoTable[depthRow][pos]) * 2 // Table value * 2

		// Apply offset based on phase (bit 5 of vibPos)
		// ASM uses 16-bit arithmetic that wraps on overflow (no clamping)
		freq := uint16(c.freqLo) | (uint16(c.freqHi) << 8)
		if c.vibPos&0x20 != 0 {
			// Add phase - allow overflow wrap
			freq += uint16(vibOffset)
		} else {
			// Subtract phase - allow underflow wrap
			freq -= uint16(vibOffset)
		}
		c.finFreqLo = byte(freq)
		c.finFreqHi = byte(freq >> 8)

		// Advance vibrato position
		c.vibPos += c.vibSpeed
	}

	if vpDebug && ch == 2 && vp.currentFrame >= 20249 && vp.currentFrame <= 20261 {
		fmt.Printf("    [f%d] ch2 freq: freqLo=%02X freqHi=%02X finFreqLo=%02X finFreqHi=%02X vibDepth=%02X\n",
			vp.currentFrame, c.freqLo, c.freqHi, c.finFreqLo, c.finFreqHi, c.vibDepth)
	}
}

func (vp *VirtualPlayer) processFilter() {
	// Filter table processing (runs every frame)
	// ASM: if filter_idx != 0, load from filter table and advance
	if vp.filterIdx == 0 {
		return
	}

	// Load cutoff from filter table
	if int(vp.filterIdx) < len(vp.filterTable) {
		vp.filterCutoff = vp.filterTable[vp.filterIdx]
	}

	// Advance filter index
	vp.filterIdx++
	if vp.filterIdx >= vp.filterEnd {
		vp.filterIdx = vp.filterLoop
	}
}

func (vp *VirtualPlayer) processEffect(ch int, effect, param byte) {
	c := &vp.chn[ch]

	// Clear permArp for effects other than Special (0) and Arp (1)
	// Special handles permArp clearing based on param; Arp sets permArp
	if effect != PlayerEffectSpecial && effect != PlayerEffectArp {
		c.permArp = 0
	}

	switch effect {
	case PlayerEffectSpecial:
		// No effect or special param
		if c.permArp != 0 && param == PlayerParam0Nop {
			c.patArp = c.permArp // Apply permanent arp on NOP rows
		} else {
			c.patArp = 0 // Clear pattern arpeggio
			if param != PlayerParam0Nop {
				c.permArp = 0 // Non-NOP clears permarp (including NOP(HARD))
			}
		}
		if param == PlayerParam0VibOff {
			// GT vibrato - disable vibrato
			c.vibDepth = 0
			c.vibSpeed = 0
		} else if param == PlayerParam0Break {
			// Pattern break - skip to next order
			vp.forceNewPattern = true
		} else if param == PlayerParam0FineSlide {
			// Fine slide - add $04 to slide delta once per row
			if vp.speedCounter == 0 {
				newLo := int(c.slideDeltaLo) + 0x04
				if newLo > 255 {
					c.slideDeltaHi++
				}
				c.slideDeltaLo = byte(newLo)
				c.slideEnable = 0x80
			}
		}
	case PlayerEffectArp:
		// Pattern arpeggio - sets permanent arp for permarp optimization
		c.permArp = param
		c.patArp = param
	case PlayerEffectPorta:
		// Portamento - sliding handled in processInstrument
		// Nothing to do here on row change
	case PlayerEffectSpeed:
		// Speed - ASM sets speed unconditionally when speedcounter==0 (including speed=0)
		if vp.speedCounter == 0 {
			vp.speed = int(param)
		}
	case PlayerEffectHrdRest:
		// Hard restart timing
		if vp.speedCounter == 0 {
			if vpDebug && ch == 0 {
				fmt.Printf("    eff4 ch0: hardRestart set to %d at f%d row%d order%d (was %d)\n", param, vp.currentFrame, vp.row, vp.order, c.hardRestart)
			}
			c.hardRestart = param
		}
	case PlayerEffectFiltTrig:
		// Filter trigger - load filter params from instrument
		// Param is instrument number * 16 (pre-shifted)
		if vp.speedCounter == 0 && param != 0 {
			// Instrument N is at offset (N-1)*16, param = N*16
			instBase := int(param) - 16
			if instBase >= 0 && instBase+16 <= len(vp.instData) {
				vp.filterIdx = vp.instData[instBase+13]  // INST_FILTSTART
				vp.filterEnd = vp.instData[instBase+14]  // INST_FILTEND
				vp.filterLoop = vp.instData[instBase+15] // INST_FILTLOOP
			}
		}
	case PlayerEffectSR:
		// Set SR
		c.sr = param
	case PlayerEffectWave:
		// Set waveform directly
		// NOTE: Don't set waveIdx=255 - let wave table continue advancing
		// The waveform override happens every frame in processInstrument
		c.waveform = param
	case PlayerEffectPulse:
		// Pulse width (hardcoded values in odin_player)
		// param != 0: pulseHi = 0x08, pulseLo = 0x00
		// param == 0: pulseHi = 0x00, pulseLo = 0x00
		if param != 0 {
			c.pulseHi = 0x08
		} else {
			c.pulseHi = 0x00
		}
		c.pulseLo = 0x00
	case PlayerEffectAD:
		// Set AD
		c.ad = param
	case PlayerEffectReso:
		// Filter resonance - stores param directly to filter_resonance
		vp.filterResonance = param
	case PlayerEffectSlide:
		// Slide - sets up slide mode
		// The actual delta accumulation happens every frame in processInstrument
		// param 0 = up, param != 0 = down (stored for processInstrument to use)
		c.slideEnable = 0x80
	case PlayerEffectGlobVol:
		// Global volume
		if vp.speedCounter == 0 {
			vp.globalVolume = param & 0x0F
		}
	case PlayerEffectFiltMode:
		// Filter mode - param is pre-shifted, stores directly to filter_mode
		if vp.speedCounter == 0 {
			vp.filterMode = param
		}
	case 15:
		// Permanent arpeggio (new effect F)
		// Stores param for use on subsequent NOP rows
		c.permArp = param
		c.patArp = param
	}
}

func (vp *VirtualPlayer) checkHardRestart(ch int) {
	// Look ahead to HR row
	var hrRow int
	var hrOrder int
	if vp.forceNewPattern {
		// Pattern break - next row is row 0 of next order
		hrRow = 0
		hrOrder = vp.nextOrder
	} else {
		hrRow = vp.row + 1
		hrOrder = vp.order
		if hrRow >= 64 {
			hrRow = 0
			hrOrder = vp.nextOrder // Next order
		}
	}

	// Get the row data for HR lookahead
	// For same order: save decoder state, decode, restore
	// For different order: use decodeRowForOrder
	var row [3]byte
	if hrOrder == vp.order {
		// Save decoder state
		c := &vp.chn[ch]
		savedDecodedRow := c.decodedRow
		savedPrevRow := c.prevRow
		savedLastWasGap := c.lastWasGap
		savedRleCount := c.rleCount
		savedGapRemaining := c.gapRemaining
		savedSrcOff := c.srcOff

		// Decode the lookahead row
		row = vp.decodeRow(ch, hrRow)

		// Restore decoder state
		c.decodedRow = savedDecodedRow
		c.prevRow = savedPrevRow
		c.lastWasGap = savedLastWasGap
		c.rleCount = savedRleCount
		c.gapRemaining = savedGapRemaining
		c.srcOff = savedSrcOff
	} else {
		// Different order - use stateless decoder
		row = vp.decodeRowForOrder(ch, hrOrder, hrRow)
	}

	byte0 := row[0]
	note := byte0 & 0x7F

	if vpDebug && ch == 0 && (vp.currentFrame >= 268 && vp.currentFrame <= 272 || vp.currentFrame >= 380 && vp.currentFrame <= 384) {
		fmt.Printf("    HR decode ch0: row=%d hrRow=%d hrOrder=%d byte0=%02X note=%02X fnp=%v\n",
			vp.row, hrRow, hrOrder, byte0, note, vp.forceNewPattern)
	}
	if vpDebug && ch == 0 && (vp.currentFrame >= 27749 && vp.currentFrame <= 27755 || vp.currentFrame >= 28025 && vp.currentFrame <= 28037) {
		fmt.Printf("    [f%d] HR decode ch0: byte0=%02X byte1=%02X byte2=%02X note=%02X effect=%d patIdx=%d\n",
			vp.currentFrame, row[0], row[1], row[2], note, row[1]>>5, vp.chn[ch].trackptr)
	}
	// Skip HR if no note or key off ($61)
	if note == 0 || note == 0x61 {
		if vpDebug && ch == 0 && (vp.currentFrame >= 27749 && vp.currentFrame <= 27755 || vp.currentFrame >= 28025 && vp.currentFrame <= 28037) {
			fmt.Printf("    [f%d] -> HR skipped (no note or keyoff)\n", vp.currentFrame)
		}
		return
	}

	// ASM: if byte0 bit 7 is set (effect bit 3), do HR immediately
	if byte0&0x80 != 0 {
		if vpDebug && ch == 0 && (vp.currentFrame >= 27749 && vp.currentFrame <= 27755 || vp.currentFrame >= 28025 && vp.currentFrame <= 28037) {
			fmt.Printf("    [f%d] -> HR triggered (bit7 set)\n", vp.currentFrame)
		}
		vp.doHardRestart(ch)
		return
	}

	// Check effect bits 0-2 from byte 1
	effect := row[1] >> 5

	// Skip HR if portamento (effect 2 in new format = tone portamento)
	if effect == 2 {
		if vpDebug && ch == 0 && (vp.currentFrame >= 27749 && vp.currentFrame <= 27755 || vp.currentFrame >= 28025 && vp.currentFrame <= 28037) {
			fmt.Printf("    [f%d] -> HR skipped (portamento)\n", vp.currentFrame)
		}
		return
	}

	// Do HR - zero envelope and waveform
	vp.doHardRestart(ch)
}

// decodeRowForOrder decodes a row from a different order's pattern without modifying state
func (vp *VirtualPlayer) decodeRowForOrder(ch, orderNum, rowNum int) [3]byte {
	// Get pattern index for this order
	bsOff := orderNum * 4
	if bsOff+4 > len(vp.bitstream) {
		return [3]byte{}
	}

	bs1 := vp.bitstream[bsOff+1]
	bs2 := vp.bitstream[bsOff+2]
	bs3 := vp.bitstream[bsOff+3]

	// Extract delta index for this channel
	var deltaIdx byte
	switch ch {
	case 0:
		deltaIdx = ((bs1 >> 4) & 0x0F) | ((bs2 & 0x01) << 4)
	case 1:
		deltaIdx = (bs2 >> 1) & 0x1F
	case 2:
		deltaIdx = ((bs2 >> 6) & 0x03) | ((bs3 & 0x07) << 2)
	}

	// Compute pattern index: current trackptr + delta from table
	dIdx := int(vp.deltaBase) + int(deltaIdx)
	if dIdx >= len(vp.deltaTable) {
		return [3]byte{}
	}
	delta := int8(vp.deltaTable[dIdx])
	patIdx := int(vp.chn[ch].trackptr) + int(delta)

	if patIdx < 0 || patIdx >= len(vp.packedPtrs) {
		return [3]byte{}
	}

	// Temporarily decode from this pattern
	// Create a temporary decoder state
	gapCode := vp.gapCodes[patIdx]
	gapValues := []int{0, 1, 3, 7, 15, 31, 63}
	gap := 0
	if int(gapCode) < len(gapValues) {
		gap = gapValues[gapCode]
	}

	srcOff := 0
	rleCount := 0
	gapRemaining := 0
	prevRow := [3]byte{}
	ptrOff := int(vp.packedPtrs[patIdx])

	// Decode rows until we reach rowNum
	for decoded := 0; decoded <= rowNum; decoded++ {
		// Check for gap zeros
		if gapRemaining > 0 {
			gapRemaining--
			if decoded == rowNum {
				return [3]byte{} // Gap row returns zeros
			}
			continue
		}

		// Check for RLE
		if rleCount > 0 {
			rleCount--
			if gap > 0 {
				gapRemaining = gap
			}
			if decoded == rowNum {
				return prevRow
			}
			continue
		}

		// Read packed pattern data
		dataSrcOff := ptrOff + srcOff
		if dataSrcOff >= len(vp.fullData) {
			return [3]byte{}
		}

		b := vp.fullData[dataSrcOff]

		if b < 0x10 {
			// $00-$0F: dict[0] (zeros) with RLE 0-15
			prevRow = [3]byte{}
			rleCount = int(b)
			srcOff++
		} else if b < 0xEF {
			// $10-$EE: dict[1-223]
			idx := int(b) - 0x0F
			prevRow = vp.lookupDict(idx)
			srcOff++
		} else if b < 0xFE {
			// $EF-$FD: RLE 1-15
			rleCount = int(b) - 0xEF
			srcOff++
		} else if b == 0xFE {
			// $FE: note-only (keep inst/eff/param, change note)
			srcOff++
			if dataSrcOff+1 < len(vp.fullData) {
				noteByte := vp.fullData[dataSrcOff+1]
				prevRow[0] = noteByte // Only update note byte
				srcOff++
			}
		} else {
			// $FF: extended dict index
			srcOff++
			if dataSrcOff+1 < len(vp.fullData) {
				extByte := vp.fullData[dataSrcOff+1]
				idx := 224 + int(extByte)
				prevRow = vp.lookupDict(idx)
				srcOff++
			}
		}

		// Apply gap zeros for subsequent rows
		if gap > 0 {
			gapRemaining = gap
		}

		if decoded == rowNum {
			return prevRow
		}
	}

	return prevRow
}

func (vp *VirtualPlayer) doHardRestart(ch int) {
	if vpDebug && ch == 0 && vp.currentFrame >= 28025 && vp.currentFrame <= 28037 {
		fmt.Printf("    [f%d] doHardRestart ch0: zeroing waveform (was %02X)\n", vp.currentFrame, vp.chn[ch].waveform)
	}
	vp.chn[ch].waveform = 0
	vp.chn[ch].ad = 0
	vp.chn[ch].sr = 0
	vp.chn[ch].hrActive = true
}

func (vp *VirtualPlayer) dumpRegisters() {
	sidBase := uint16(0xD400)

	// GT player writes: pulse, freq, control, AD, SR for each channel
	for ch := 0; ch < 3; ch++ {
		c := &vp.chn[ch]
		chBase := sidBase + uint16(ch*7)

		if vpDebug && vp.currentFrame < 6 && ch == 1 {
			fmt.Printf("  [f%d] dump ch%d: freqLo=%02X freqHi=%02X note=%02X playingNote=%02X trans=%d instActive=%v\n",
				vp.currentFrame, ch, c.freqLo, c.freqHi, c.note, c.playingNote, c.transpose, c.instActive)
		}
		if vpDebug && (vp.currentFrame >= 268 && vp.currentFrame <= 272 || debugNearFrame(vp.currentFrame)) && ch == 0 {
			fmt.Printf("  [f%d] dump ch0: wave=%02X gateon=%02X result=%02X hrActive=%v waveIdx=%d note=%02X playNote=%02X freqLo=%02X finFreqLo=%02X slideEn=%02X slideLo=%02X slideHi=%02X vibD=%02X\n",
				vp.currentFrame, c.waveform, c.gateon, c.waveform&c.gateon, c.hrActive, c.waveIdx, c.note, c.playingNote, c.freqLo, c.finFreqLo, c.slideEnable, c.slideDeltaLo, c.slideDeltaHi, c.vibDepth)
		}
		if vpDebug && ((vp.currentFrame >= 380 && vp.currentFrame <= 420) || (vp.currentFrame >= 20253 && vp.currentFrame <= 20260)) && ch == 2 {
			fmt.Printf("  [f%d] dump ch2: wave=%02X gateon=%02X result=%02X note=%02X effect=%d playNote=%02X hrActive=%v\n",
				vp.currentFrame, c.waveform, c.gateon, c.waveform&c.gateon, c.note, c.effect, c.playingNote, c.hrActive)
		}
		vp.writeSID(chBase+2, c.pulseLo)
		vp.writeSID(chBase+3, c.pulseHi)
		vp.writeSID(chBase+0, c.finFreqLo)
		vp.writeSID(chBase+1, c.finFreqHi)
		vp.writeSID(chBase+4, c.waveform&c.gateon)
		vp.writeSID(chBase+5, c.ad)
		vp.writeSID(chBase+6, c.sr)
	}

	// Filter registers
	vp.writeSID(0xD416, vp.filterCutoff)
	vp.writeSID(0xD417, vp.filterResonance)
	vp.writeSID(0xD418, vp.globalVolume|vp.filterMode)
}

func (vp *VirtualPlayer) writeSID(addr uint16, val byte) {
	vp.writes = append(vp.writes, SIDWrite{
		Addr:  addr,
		Value: val,
		Frame: vp.currentFrame,
	})
}

// SetVPDebugSong enables debug output for a specific song name
func SetVPDebugSong(name string) {
	vpDebugSong = name
}

func SetVPDebugFrame(frame int) {
	vpDebugFrame = frame
}

// CompareVirtual compares virtual player output against original GT player output.
// Returns true if outputs match, along with mismatch details.
func CompareVirtual(
	songName string,
	origWrites []SIDWrite,
	songData []byte,
	deltaTable, transposeTable, waveTable []byte,
	transformed transform.TransformedSong,
	encoded encode.EncodedSong,
	frames int,
	startConst int,
) (bool, int, string) {
	return CompareVirtualDebug(songName, origWrites, songData, deltaTable, transposeTable, waveTable, transformed, encoded, frames, startConst, false)
}

func CompareVirtualDebug(
	songName string,
	origWrites []SIDWrite,
	songData []byte,
	deltaTable, transposeTable, waveTable []byte,
	transformed transform.TransformedSong,
	encoded encode.EncodedSong,
	frames int,
	startConst int,
	debug bool,
) (bool, int, string) {
	vpDebug = debug || vpDebugSong != ""
	vp := NewVirtualPlayer(songName, songData, deltaTable, transposeTable, waveTable, transformed, encoded, startConst)
	vpWrites := vp.RunFrames(frames)

	// Debug: show writes around mismatch
	showDebugWrites := func(mismatchIdx int) {
		if !debug {
			return
		}
		// Show 10 writes before and 5 after mismatch
		startIdx := mismatchIdx - 10
		if startIdx < 0 {
			startIdx = 0
		}
		endIdx := mismatchIdx + 5
		if endIdx > len(origWrites) {
			endIdx = len(origWrites)
		}
		if endIdx > len(vpWrites) {
			endIdx = len(vpWrites)
		}
		fmt.Printf("\n=== Writes %d-%d (mismatch at %d) ===\n", startIdx, endIdx-1, mismatchIdx)
		for i := startIdx; i < endIdx; i++ {
			marker := "  "
			if i == mismatchIdx {
				marker = "->"
			}
			origStr := "---"
			vpStr := "---"
			if i < len(origWrites) {
				origStr = fmt.Sprintf("$%04X=%02X f%d", origWrites[i].Addr, origWrites[i].Value, origWrites[i].Frame)
			}
			if i < len(vpWrites) {
				vpStr = fmt.Sprintf("$%04X=%02X f%d", vpWrites[i].Addr, vpWrites[i].Value, vpWrites[i].Frame)
			}
			match := " "
			if i < len(origWrites) && i < len(vpWrites) && origWrites[i] != vpWrites[i] {
				match = "X"
			}
			fmt.Printf("%s %3d: orig=%-16s vp=%-16s %s\n", marker, i, origStr, vpStr, match)
		}
	}

	// Compare write counts first
	if len(vpWrites) != len(origWrites) {
		showDebugWrites(-1)
		return false, 0, fmt.Sprintf("write count: vp=%d orig=%d", len(vpWrites), len(origWrites))
	}

	// Compare each write
	for i := 0; i < len(origWrites); i++ {
		if vpWrites[i] != origWrites[i] {
			// Show writes around mismatch - 48 writes = 2 frames before
			if debug || vpDebugSong != "" {
				start := i - 48
				if start < 0 {
					start = 0
				}
				fmt.Printf("\n=== Writes around mismatch (write %d) ===\n", i)
				for j := start; j < i+24 && j < len(origWrites); j++ {
					marker := "  "
					if j == i {
						marker = "->"
					}
					match := " "
					if vpWrites[j] != origWrites[j] {
						match = "X"
					}
					fmt.Printf("%s %5d: orig=$%04X=%02X f%d vp=$%04X=%02X f%d %s\n",
						marker, j,
						origWrites[j].Addr, origWrites[j].Value, origWrites[j].Frame,
						vpWrites[j].Addr, vpWrites[j].Value, vpWrites[j].Frame, match)
				}
			}
			return false, i, fmt.Sprintf("vp=$%04X=%02X orig=$%04X=%02X f=%d",
				vpWrites[i].Addr, vpWrites[i].Value, origWrites[i].Addr, origWrites[i].Value, origWrites[i].Frame)
		}
	}

	return true, len(origWrites), ""
}

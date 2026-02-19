package simulate

import "forge/serialize"

// MinimalPlayer is a clean, minimal implementation of the SID player.
// It tracks only essential state needed for playback.
type MinimalPlayer struct {
	// Song data
	fullData   []byte   // Complete song data (for absolute pattern offsets)
	instData   []byte   // Instrument parameters
	bitstream  []byte   // Order bitstream (4 bytes per order)
	dict       [3][]byte // Row dictionary [note, inst, effect]
	patternPtr []uint16 // Pattern offsets (absolute into fullData)
	patternGap []byte   // Gap codes per pattern
	filterTbl  []byte
	arpTbl     []byte
	waveTbl    []byte
	transTbl   []byte
	deltaTbl   []byte
	transBase  int
	deltaBase  int
	startConst int
	numInst    int

	// Global state
	order           int
	nextOrder       int
	row             int
	speed           int
	speedCtr        int
	mod3            int
	forceNewPattern bool

	// Filter
	filterIdx  int
	filterEnd  int
	filterLoop int
	filterCut  byte
	filterRes  byte
	filterMode byte
	volume     byte

	// Channels
	ch [3]minChan

	// Output
	writes []SIDWrite
	frame  int
}

type minChan struct {
	// Pattern stream - always one row ahead
	src      int  // offset into pattern data
	prevIdx  int  // previous dict index (-1 = zero row)
	prevNote byte // note override (for $FE note-only)
	rleCount int  // RLE repeats remaining
	gap      int  // gap value (0,1,3,7,15,31,63)
	gapRem   int  // gap zeros remaining
	nextIdx  int  // next dict index (read ahead for peek)
	nextNote byte // note override for next row

	// Row data
	playing byte // currently sounding note
	inst    byte // instrument (0 = none loaded yet)
	effect  byte // effect 0-15
	param   byte // effect parameter
	permArp byte // permanent arpeggio (persists across NOP rows)

	// Instrument playback (positions advance, limits derived from inst)
	wavePos  int
	arpPos   int
	vibDelay byte
	vibPos   byte
	pulseDir byte // 0 = up, $80 = down

	// SID output state
	trans    int8     // transpose
	ad, sr   byte
	wave     byte
	gate     byte     // 0xFF=on, 0xFE=off
	plsLo      byte
	plsHi      byte
	freqLo     byte
	freqHi     byte
	noteFreqLo byte // Target for portamento
	noteFreqHi byte
	finFreqLo  byte // Final after vibrato
	finFreqHi  byte
	// HR
	hrTime byte

	// Slide
	slideEnable  byte
	slideDeltaLo byte
	slideDeltaHi byte

	// Trackptr
	trackptr int
}

var gapValues = [7]int{0, 1, 3, 7, 15, 31, 63}

func NewMinimalPlayer(
	songData []byte,
	numPatterns int,
	deltaTbl, transTbl, waveTbl []byte,
	startConst int,
) *MinimalPlayer {
	p := &MinimalPlayer{
		speed:      6,
		speedCtr:   5,  // triggers row on frame 0
		mod3:       0,  // matches ASM player (0 -> -1 -> 2 on first frame)
		volume:     0x0F,
		row:        -1, // so first advanceRow() goes to row 0
		transTbl:   transTbl,
		deltaTbl:   deltaTbl,
		waveTbl:    waveTbl,
		startConst: startConst,
	}

	p.parseSongData(songData, numPatterns)

	// Init channels
	for i := range p.ch {
		p.ch[i].gate = 0xFE
		p.ch[i].hrTime = 2
	}

	// Load first order (sets up decoders)
	p.order = 0
	p.nextOrder = 1
	p.loadOrder(0)

	return p
}

func (p *MinimalPlayer) parseSongData(data []byte, numPatterns int) {
	dictSize := serialize.DictArraySize
	ptrsOff := serialize.PackedPtrsOffset()

	// Store full data for absolute pattern offsets
	p.fullData = data

	// Instruments: at offset 0, 16 bytes each, size determined by bitstream start
	p.instData = data[serialize.InstOffset:serialize.BitstreamOffset]
	p.numInst = len(p.instData) / 16

	// Bitstream
	p.bitstream = data[serialize.BitstreamOffset:serialize.FilterOffset]

	// Filter table
	p.filterTbl = data[serialize.FilterOffset:serialize.ArpOffset]

	// Arp table
	p.arpTbl = data[serialize.ArpOffset:serialize.TransBaseOffset]

	// Bases
	p.transBase = int(data[serialize.TransBaseOffset])
	p.deltaBase = int(data[serialize.DeltaBaseOffset])

	// Dictionary (3 separate arrays: note, inst, effect)
	p.dict[0] = data[serialize.RowDictOffset : serialize.RowDictOffset+dictSize]
	p.dict[1] = data[serialize.RowDictOffset+dictSize : serialize.RowDictOffset+dictSize*2]
	p.dict[2] = data[serialize.RowDictOffset+dictSize*2 : serialize.RowDictOffset+dictSize*3]

	// Pattern pointers (absolute offsets into fullData)
	p.patternPtr = make([]uint16, numPatterns)
	p.patternGap = make([]byte, numPatterns)
	for i := 0; i < numPatterns; i++ {
		lo := data[ptrsOff+i*2]
		hi := data[ptrsOff+i*2+1]
		p.patternPtr[i] = uint16(lo) | (uint16(hi&0x1F) << 8)
		p.patternGap[i] = hi >> 5
	}
}

func (p *MinimalPlayer) loadOrder(orderNum int) {
	if orderNum*4+3 >= len(p.bitstream) {
		return
	}

	bs := p.bitstream[orderNum*4:]

	// Decode transpose indices
	trans := [3]byte{
		bs[0] & 0x0F,
		bs[0] >> 4,
		bs[1] & 0x0F,
	}

	// Decode trackptr delta indices
	delta := [3]byte{
		(bs[1] >> 4) | ((bs[2] & 0x01) << 4),
		(bs[2] >> 1) & 0x1F,
		(bs[2] >> 6) | ((bs[3] & 0x07) << 2),
	}

	// Apply to channels
	for ch := 0; ch < 3; ch++ {
		// Transpose
		tIdx := p.transBase + int(trans[ch])
		if tIdx < len(p.transTbl) {
			p.ch[ch].trans = int8(p.transTbl[tIdx])
		}

		// Trackptr delta
		dIdx := p.deltaBase + int(delta[ch])
		if dIdx < len(p.deltaTbl) {
			d := int8(p.deltaTbl[dIdx])
			if orderNum == 0 {
				p.ch[ch].trackptr = p.startConst + int(d)
			} else {
				p.ch[ch].trackptr += int(d)
			}
		}

		// Init pattern decoder
		p.initDecoder(ch, p.ch[ch].trackptr)
	}
}

func (p *MinimalPlayer) initDecoder(ch, patIdx int) {
	c := &p.ch[ch]
	if patIdx < 0 || patIdx >= len(p.patternPtr) {
		return
	}

	c.src = int(p.patternPtr[patIdx])
	gapCode := int(p.patternGap[patIdx])
	if gapCode < len(gapValues) {
		c.gap = gapValues[gapCode]
	} else {
		c.gap = 0
	}
	c.prevIdx = -1
	c.prevNote = 0
	c.rleCount = 0
	c.gapRem = 0
	c.nextIdx, c.nextNote = p.advanceStream(ch) // Read first row ahead
}

// rowFromIdx returns the 3-byte row for a dict index (-1 = zero row).
func (p *MinimalPlayer) rowFromIdx(idx int, noteOverride byte) [3]byte {
	if idx < 0 {
		return [3]byte{noteOverride, 0, 0}
	}
	note := p.dict[0][idx]
	if noteOverride != 0 {
		note = noteOverride
	}
	return [3]byte{note, p.dict[1][idx], p.dict[2][idx]}
}

// peekNextRow returns the next row without advancing.
func (p *MinimalPlayer) peekNextRow(ch int) [3]byte {
	c := &p.ch[ch]
	return p.rowFromIdx(c.nextIdx, c.nextNote)
}

// consumeRow returns the current row and advances to the next.
func (p *MinimalPlayer) consumeRow(ch int) [3]byte {
	c := &p.ch[ch]
	row := p.rowFromIdx(c.nextIdx, c.nextNote)
	c.nextIdx, c.nextNote = p.advanceStream(ch)
	return row
}

// advanceStream advances through the pattern stream and returns the next dict index.
// Returns (idx, noteOverride) where idx=-1 means zero row.
func (p *MinimalPlayer) advanceStream(ch int) (int, byte) {
	c := &p.ch[ch]

	// Gap zeros
	if c.gapRem > 0 {
		c.gapRem--
		return -1, 0
	}

	// RLE
	if c.rleCount > 0 {
		c.rleCount--
		if c.gap > 0 {
			c.gapRem = c.gap
		}
		return c.prevIdx, c.prevNote
	}

	// Read encoded byte
	if c.src >= len(p.fullData) {
		return -1, 0
	}
	b := p.fullData[c.src]
	c.src++

	switch {
	case b <= 0x0F:
		c.prevIdx = -1
		c.prevNote = 0
		c.rleCount = int(b)
	case b >= 0x10 && b <= 0xEE:
		c.prevIdx = int(b - 0x10)
		c.prevNote = 0
	case b >= 0xEF && b <= 0xFD:
		c.rleCount = int(b - 0xEF)
	case b == 0xFE:
		// Note-only: keep prevIdx, override note
		if c.src < len(p.fullData) {
			c.prevNote = p.fullData[c.src]
			c.src++
		}
	case b == 0xFF:
		if c.src < len(p.fullData) {
			c.prevIdx = 224 + int(p.fullData[c.src]) - 1
			c.prevNote = 0
			c.src++
		}
	}

	if c.gap > 0 {
		c.gapRem = c.gap
	}
	return c.prevIdx, c.prevNote
}

func (p *MinimalPlayer) Tick() []SIDWrite {
	p.writes = nil

	// Mod3 counter (for pulse width modulation)
	p.mod3--
	if p.mod3 < 0 {
		p.mod3 = 2
	}

	// Speed counter
	p.speedCtr++
	if p.speedCtr >= p.speed {
		p.speedCtr = 0
		p.advanceRow()
	}

	// Per-frame effects (wave, arp, pulse modulation)
	p.processFrame()

	// HR lookahead (after processFrame, matching ASM player order)
	p.hrLookahead()

	// Output to SID
	p.outputSID()

	p.frame++
	return p.writes
}

func (p *MinimalPlayer) advanceRow() {
	if p.forceNewPattern {
		// Pattern break - jump to next order, row 0
		p.row = 0
		p.order = p.nextOrder
		p.nextOrder = p.order + 1
		p.forceNewPattern = false
		p.loadOrder(p.order)
	} else {
		p.row++
		if p.row >= 64 {
			p.row = 0
			p.order = p.nextOrder
			p.nextOrder = p.order + 1
			// TODO: handle loop
			p.loadOrder(p.order)
		}
	}

	// Consume pre-decoded row for each channel and apply it
	for ch := 0; ch < 3; ch++ {
		row := p.consumeRow(ch)
		p.applyRow(ch, row)
	}
}

func (p *MinimalPlayer) applyRow(ch int, row [3]byte) {
	c := &p.ch[ch]

	// Decode row bytes
	note := row[0] & 0x7F
	hasEffBit3 := (row[0] & 0x80) != 0
	inst := row[1] & 0x1F
	effect := row[1] >> 5
	if hasEffBit3 {
		effect |= 0x08
	}
	param := row[2]

	c.effect = effect
	c.param = param

	// Load instrument first if inst > 0 (regardless of note - matches ASM)
	if inst > 0 && int(inst) <= p.numInst {
		c.inst = inst
		p.loadInstrument(ch, int(inst)-1)
	}

	// Note-off
	if note == 0x61 {
		c.gate = 0xFE
	}

	// New note
	if note > 0 && note != 0x61 {
		c.playing = note

		// Reset slide on new note
		c.slideDeltaLo = 0
		c.slideDeltaHi = 0
		c.slideEnable = 0

		// Reset wave and arp indices on new note (even without new inst)
		if c.inst > 0 {
			instBase := (int(c.inst) - 1) * 16
			if instBase+8 <= len(p.instData) {
				c.wavePos = int(p.instData[instBase+2])
				c.arpPos = int(p.instData[instBase+5])
			}
		}

		// Portamento: set target frequency but don't trigger gate
		if effect == 2 {
			targetNote := int(note) - 1 + int(c.trans)
			if targetNote < 0 {
				targetNote = 0
			}
			if targetNote >= len(freqTable) {
				targetNote = len(freqTable) - 1
			}
			freq := freqTable[targetNote]
			c.noteFreqLo = byte(freq)
			c.noteFreqHi = byte(freq >> 8)
			// If there's also an instrument trigger, set gate on
			if inst > 0 {
				c.gate = 0xFF
			}
		} else {
			c.gate = 0xFF
		}
	}

	// Apply effect
	p.applyEffect(ch)
}

func (p *MinimalPlayer) loadInstrument(ch, instIdx int) {
	c := &p.ch[ch]
	base := instIdx * 16
	if base+16 > len(p.instData) {
		return
	}

	c.ad = p.instData[base+0]
	c.sr = p.instData[base+1]
	c.wavePos = int(p.instData[base+2])
	c.arpPos = int(p.instData[base+5])
	c.vibDelay = p.instData[base+8]
	c.vibPos = 0

	// Initialize pulse width (nibble-swapped: original $XY -> stored $YX)
	pw := p.instData[base+10]
	c.plsLo = pw & 0xF0
	c.plsHi = pw & 0x0F
	c.pulseDir = 0
}

// instBase returns the instrument data offset, or -1 if invalid
func (p *MinimalPlayer) instBase(ch int) int {
	inst := p.ch[ch].inst
	if inst == 0 {
		return -1
	}
	base := (int(inst) - 1) * 16
	if base+16 > len(p.instData) {
		return -1
	}
	return base
}

func (p *MinimalPlayer) applyEffect(ch int) {
	c := &p.ch[ch]

	// Effect numbers from effects.go:
	// 0=Special, 1=Arp, 2=TonePorta, 3=Speed, 4=HRTiming, 5=FiltTrig
	// 6=SR, 7=Wave, 8=Pulse, 9=AD, 10=Reso, 11=Slide, 12=GlobVol, 13=FiltMode

	// Clear permArp for effects other than Special (0) and Arp (1)
	if c.effect != 0 && c.effect != 1 {
		c.permArp = 0
	}

	switch c.effect {
	case 0: // Special
		if c.param != 0 {
			c.permArp = 0 // Non-NOP clears permarp
		}
		// Pattern break (param 2)
		if c.param == 2 {
			p.forceNewPattern = true
		}
		// Fine slide (param 3) - add $04 to slide delta once per row
		if c.param == 3 && p.speedCtr == 0 {
			c.slideEnable = 0x80
			newLo := int(c.slideDeltaLo) + 0x04
			if newLo > 255 {
				c.slideDeltaHi++
			}
			c.slideDeltaLo = byte(newLo)
		}
	case 1: // Pattern arpeggio
		c.permArp = c.param
	case 2: // Tone portamento (uses c.param directly in processChannelFrame)
	case 3: // Speed
		p.speed = int(c.param)
	case 4: // HR timing
		c.hrTime = c.param
	case 5: // Filter trigger - load filter params from instrument
		// Param is instrument number * 16 (pre-shifted)
		if c.param != 0 {
			instBase := int(c.param) - 16
			if instBase >= 0 && instBase+16 <= len(p.instData) {
				p.filterIdx = int(p.instData[instBase+13])  // INST_FILTSTART
				p.filterEnd = int(p.instData[instBase+14])  // INST_FILTEND
				p.filterLoop = int(p.instData[instBase+15]) // INST_FILTLOOP
			}
		}
	case 6: // SR
		c.sr = c.param
	case 8: // Pulse width - effect applied every frame in processChannelFrame
		// Nothing to do here - handled in processChannelFrame
	case 9: // AD
		c.ad = c.param
	case 10: // Filter resonance
		p.filterRes = c.param
	case 11: // Slide - sets up slide mode (param 0=up, nonzero=down)
		c.slideEnable = 0x80
	case 12: // Global volume
		p.volume = c.param & 0x0F
	case 13: // Filter mode
		p.filterMode = c.param
	case 15: // Permanent arpeggio
		c.permArp = c.param
	}
}

func (p *MinimalPlayer) hrLookahead() {
	var hrRow int
	var hrOrder int
	if p.forceNewPattern {
		// Pattern break - next row is row 0 of next order
		hrRow = 0
		hrOrder = p.nextOrder
	} else {
		hrRow = p.row + 1
		hrOrder = p.order
		if hrRow >= 64 {
			hrRow = 0
			hrOrder = p.nextOrder
		}
	}

	for ch := 0; ch < 3; ch++ {
		c := &p.ch[ch]

		// Check timing
		shouldCheck := p.speedCtr+int(c.hrTime) >= p.speed
		if !shouldCheck {
			continue
		}

		// At boundary: use HR skip mask
		if hrRow == 0 {
			hrSkip := p.getHRSkip(p.order)
			if hrSkip&(1<<ch) != 0 {
				continue // Skip HR for this channel
			}
			p.doHR(ch)
			continue
		}

		// Within pattern: check next row
		row := p.peekRow(ch, hrOrder, hrRow)
		note := row[0] & 0x7F

		// Skip HR for: no note, note-off (must check BEFORE bit 7)
		if note == 0 || note == 0x61 {
			continue
		}

		// Bit 7 set = effect bit 3 = immediate HR
		if row[0]&0x80 != 0 {
			p.doHR(ch)
			continue
		}

		// Skip HR for porta
		effect := row[1] >> 5
		if effect == 2 {
			continue // Porta
		}

		p.doHR(ch)
	}
}

func (p *MinimalPlayer) getHRSkip(orderNum int) byte {
	if orderNum*4+3 >= len(p.bitstream) {
		return 0
	}
	return (p.bitstream[orderNum*4+3] >> 3) & 0x07
}

func (p *MinimalPlayer) peekRow(ch, orderNum, rowNum int) [3]byte {
	// Same order: return pre-decoded next row
	if orderNum == p.order {
		return p.peekNextRow(ch)
	}
	// Different order uses HR skip mask, not peek
	return [3]byte{0, 0, 0}
}

func (p *MinimalPlayer) doHR(ch int) {
	c := &p.ch[ch]
	c.wave = 0
	c.ad = 0
	c.sr = 0
}

func (p *MinimalPlayer) processFrame() {
	// Filter table processing (runs every frame if filterIdx != 0)
	if p.filterIdx != 0 && p.filterIdx < len(p.filterTbl) {
		p.filterCut = p.filterTbl[p.filterIdx]
		p.filterIdx++
		if p.filterEnd > 0 && p.filterIdx >= p.filterEnd {
			p.filterIdx = p.filterLoop
		}
	}

	// Per-channel
	for ch := 0; ch < 3; ch++ {
		p.processChannelFrame(ch)
	}
}

func (p *MinimalPlayer) processChannelFrame(ch int) {
	c := &p.ch[ch]

	// Skip if no instrument loaded yet
	if c.inst == 0 {
		c.finFreqLo = c.freqLo
		c.finFreqHi = c.freqHi
		return
	}

	// Wavetable (1 byte per entry, 255 = inactive)
	if c.wavePos != 255 && c.wavePos < len(p.waveTbl) {
		c.wave = p.waveTbl[c.wavePos]
		c.wavePos++
		if base := p.instBase(ch); base >= 0 && c.wavePos >= int(p.instData[base+3]) {
			c.wavePos = int(p.instData[base+4])
		}
	}

	// Effect 7 (Wave) overrides waveform every frame
	if c.effect == 7 {
		c.wave = c.param
	}

	// Frequency/arp processing
	// Portamento (effect 2) skips arp/freq calculation - just slides toward target
	if c.effect == 2 {
		// Slide current freq toward noteFreq
		speedLo := c.param & 0xF0
		speedHi := c.param & 0x0F
		currFreq := int(c.freqLo) | (int(c.freqHi) << 8)
		targetFreq := int(c.noteFreqLo) | (int(c.noteFreqHi) << 8)
		speed := int(speedLo) | (int(speedHi) << 8)

		if currFreq < targetFreq {
			newFreq := currFreq + speed
			if newFreq >= targetFreq {
				c.freqLo = c.noteFreqLo
				c.freqHi = c.noteFreqHi
			} else {
				c.freqLo = byte(newFreq)
				c.freqHi = byte(newFreq >> 8)
			}
		} else if currFreq > targetFreq {
			newFreq := currFreq - speed
			if newFreq <= targetFreq {
				c.freqLo = c.noteFreqLo
				c.freqHi = c.noteFreqHi
			} else {
				c.freqLo = byte(newFreq)
				c.freqHi = byte(newFreq >> 8)
			}
		}
	} else {
		// Normal arp/freq calculation
		var arpOffset int
		if c.arpPos != 255 && c.arpPos < len(p.arpTbl) {
			arpVal := p.arpTbl[c.arpPos]
			if arpVal&0x80 != 0 {
				arpOffset = int(arpVal&0x7F) - (int(c.playing) - 1 + int(c.trans))
			} else {
				arpOffset = int(arpVal)
			}
			c.arpPos++
			if base := p.instBase(ch); base >= 0 && c.arpPos >= int(p.instData[base+6]) {
				c.arpPos = int(p.instData[base+7])
			}
		}

		note := int(c.playing) - 1 + int(c.trans) + arpOffset
		if note < 0 {
			note = 0
		}
		if note >= len(freqTable) {
			note = len(freqTable) - 1
		}
		freq := freqTable[note]
		c.freqLo = byte(freq)
		c.freqHi = byte(freq >> 8)
		c.noteFreqLo = byte(freq)
		c.noteFreqHi = byte(freq >> 8)

		// Pattern arpeggio OVERWRITES frequency - effect 1/15 use param, effect 0 NOP uses permArp
		if c.effect == 1 || c.effect == 15 || (c.effect == 0 && c.param == 0 && c.permArp != 0) {
			playNote := int(c.playing)
			if playNote > 0 && playNote < 0x61 {
				arpVal := c.param
				if c.effect == 0 {
					arpVal = c.permArp
				}
				var patArpOffset int
				switch p.mod3 {
				case 0:
					patArpOffset = int(arpVal & 0x0F)
				case 1:
					patArpOffset = int(arpVal >> 4)
				default:
					patArpOffset = 0
				}
				finalNote := (playNote - 1) + patArpOffset + int(c.trans)
				if finalNote < 0 {
					finalNote = 0
				}
				if finalNote >= len(freqTable) {
					finalNote = len(freqTable) - 1
				}
				patFreq := freqTable[finalNote]
				c.freqLo = byte(patFreq)
				c.freqHi = byte(patFreq >> 8)
				c.noteFreqLo = byte(patFreq)
				c.noteFreqHi = byte(patFreq >> 8)
			}
		}
	}

	// Slide effect - accumulate delta every frame if effect is 11
	if c.effect == 11 {
		c.slideEnable = 0x80
		if c.param == 0 {
			newLo := int(c.slideDeltaLo) + 0x20
			if newLo > 255 {
				c.slideDeltaHi++
			}
			c.slideDeltaLo = byte(newLo)
		} else {
			newLo := int(c.slideDeltaLo) - 0x20
			if newLo < 0 {
				c.slideDeltaHi--
			}
			c.slideDeltaLo = byte(newLo)
		}
	}

	// Apply slide to frequency if enabled
	if c.slideEnable != 0 {
		newLo := int(c.freqLo) + int(c.slideDeltaLo)
		carry := 0
		if newLo > 255 {
			carry = 1
		}
		c.freqLo = byte(newLo)
		c.freqHi = byte(int(c.freqHi) + int(c.slideDeltaHi) + carry)
	}

	// Vibrato processing - load from instrument if delay expired
	var vibDepth, vibSpeed byte
	if c.vibDelay > 0 {
		c.vibDelay--
	} else if c.inst > 0 {
		instBase := (int(c.inst) - 1) * 16
		if instBase+16 <= len(p.instData) {
			vibDS := p.instData[instBase+9]
			vibDepth = vibDS & 0xF0
			vibSpeed = vibDS & 0x0F
		}
	}

	// Effect 0 param 1 (VibOff) disables vibrato
	if c.effect == 0 && c.param == 1 {
		vibDepth = 0
	}

	// Apply vibrato to get final frequency
	if vibDepth == 0 {
		c.finFreqLo = c.freqLo
		c.finFreqHi = c.freqHi
	} else {
		pos := c.vibPos & 0x1F
		if pos >= 0x10 {
			pos = pos ^ 0x1F
		}
		depthRow := int(vibDepth>>4) - 1
		if depthRow < 0 || depthRow >= len(vibratoTable) {
			depthRow = 0
		}
		vibOffset := int(vibratoTable[depthRow][pos]) * 2

		freq := uint16(c.freqLo) | (uint16(c.freqHi) << 8)
		if c.vibPos&0x20 != 0 {
			freq += uint16(vibOffset)
		} else {
			freq -= uint16(vibOffset)
		}
		c.finFreqLo = byte(freq)
		c.finFreqHi = byte(freq >> 8)

		c.vibPos += vibSpeed
	}

	// Pulse modulation - runs if pulseSpeed != 0 AND effect != 8 (pulse override)
	if base := p.instBase(ch); base >= 0 && c.effect != 8 {
		pulseSpeed := p.instData[base+11]
		if pulseSpeed != 0 {
			limits := p.instData[base+12]
			limitUp := limits & 0x0F
			limitDown := limits >> 4
			if c.pulseDir == 0 {
				newLo := int(c.plsLo) + int(pulseSpeed)
				carry := byte(0)
				if newLo > 255 {
					carry = 1
				}
				c.plsLo = byte(newLo)
				newHi := int(c.plsHi) + int(carry)
				if newHi > int(limitUp) {
					c.pulseDir = 0x80
					c.plsLo = 0xFF
					c.plsHi = limitUp
				} else {
					c.plsHi = byte(newHi)
				}
			} else {
				newLo := int(c.plsLo) - int(pulseSpeed)
				borrow := byte(0)
				if newLo < 0 {
					borrow = 1
					newLo += 256
				}
				c.plsLo = byte(newLo)
				newHi := int(c.plsHi) - int(borrow)
				if newHi < int(limitDown) {
					c.pulseDir = 0
					c.plsLo = 0
					c.plsHi = limitDown
				} else {
					c.plsHi = byte(newHi)
				}
			}
		}
	}

	// Effect 8 (Pulse) - overrides pulse width every frame
	if c.effect == 8 {
		if c.param != 0 {
			c.plsHi = 0x08
		} else {
			c.plsHi = 0x00
		}
		c.plsLo = 0x00
	}
}

func (p *MinimalPlayer) outputSID() {
	// Channel registers (order matches ASM: pulse, freq, wave, ad, sr)
	for ch := 0; ch < 3; ch++ {
		c := &p.ch[ch]
		base := uint16(0xD400 + ch*7)

		p.write(base+2, c.plsLo)
		p.write(base+3, c.plsHi)
		p.write(base+0, c.finFreqLo)
		p.write(base+1, c.finFreqHi)
		p.write(base+4, c.wave&c.gate) // waveform AND gate
		p.write(base+5, c.ad)
		p.write(base+6, c.sr)
	}

	// Filter (matches ASM player: D416=cutoff, D417=resonance, D418=volume|mode)
	p.write(0xD416, p.filterCut)
	p.write(0xD417, p.filterRes)
	p.write(0xD418, p.volume|p.filterMode)
}

func (p *MinimalPlayer) write(addr uint16, val byte) {
	p.writes = append(p.writes, SIDWrite{Addr: addr, Value: val, Frame: p.frame})
}

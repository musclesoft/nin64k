package parse

type Row struct {
	Note   byte
	Inst   byte
	Effect byte
	Param  byte
}

type Pattern struct {
	Address uint16
	Rows    [64]Row
}

type OrderEntry struct {
	PatternAddr uint16
	Transpose   int8
}

type Instrument struct {
	AD, SR                             byte
	WaveStart, WaveEnd, WaveLoop       byte
	ArpStart, ArpEnd, ArpLoop          byte
	PulseWidthLo, PulseWidthHi         byte
	PulseSpeed                         byte
	VibDepthSpeed, VibDelay            byte
	FilterStart, FilterEnd, FilterLoop byte
}

type ParsedSong struct {
	BaseAddr    uint16
	Instruments []Instrument
	Patterns    map[uint16]Pattern
	Orders      [3][]OrderEntry
	WaveTable   []byte
	ArpTable    []byte
	FilterTable []byte
	StartOrder  int
	NumOrders   int
	Addrs       TableAddresses
}

func Parse(raw []byte) ParsedSong {
	addrs := ExtractAddresses(raw)
	rawLen := len(raw)

	song := ParsedSong{
		BaseAddr:   uint16(addrs.BaseAddr),
		Patterns:   make(map[uint16]Pattern),
		StartOrder: int(raw[addrs.SongStart]),
		NumOrders:  addrs.NumOrders,
		Addrs:      addrs,
	}

	song.Instruments = parseInstruments(raw, addrs)
	song.WaveTable = parseTable(raw, addrs.Wavetable, addrs.Arptable)
	song.ArpTable = parseTable(raw, addrs.Arptable, addrs.Filtertable)

	filterEnd := addrs.Filtertable + 256
	if filterEnd > rawLen {
		filterEnd = rawLen
	}
	if addrs.Filtertable < rawLen {
		song.FilterTable = make([]byte, filterEnd-addrs.Filtertable)
		copy(song.FilterTable, raw[addrs.Filtertable:filterEnd])
	}

	for ch := 0; ch < 3; ch++ {
		song.Orders[ch] = make([]OrderEntry, addrs.NumOrders)
		for i := 0; i < addrs.NumOrders; i++ {
			if addrs.TrackLo[ch]+i >= rawLen || addrs.TrackHi[ch]+i >= rawLen {
				continue
			}
			lo := raw[addrs.TrackLo[ch]+i]
			hi := raw[addrs.TrackHi[ch]+i]
			addr := uint16(lo) | uint16(hi)<<8

			var transpose int8
			if addrs.Transpose[ch]+i < rawLen {
				transpose = int8(raw[addrs.Transpose[ch]+i])
			}

			song.Orders[ch][i] = OrderEntry{
				PatternAddr: addr,
				Transpose:   transpose,
			}

			srcOff := int(addr) - addrs.BaseAddr
			if srcOff >= 0 && srcOff+192 <= rawLen {
				if _, exists := song.Patterns[addr]; !exists {
					song.Patterns[addr] = parsePattern(raw, srcOff, addr)
				}
			}
		}
	}

	return song
}

func parseInstruments(raw []byte, addrs TableAddresses) []Instrument {
	numInst := addrs.NumInstruments
	instruments := make([]Instrument, numInst)

	for i := 0; i < numInst; i++ {
		inst := Instrument{}
		instOff := addrs.InstAD

		getParam := func(param int) byte {
			idx := instOff + param*numInst + i
			if idx < len(raw) {
				return raw[idx]
			}
			return 0
		}

		inst.AD = getParam(0)
		inst.SR = getParam(1)
		inst.WaveStart = getParam(2)
		inst.WaveEnd = getParam(3)
		inst.WaveLoop = getParam(4)
		inst.ArpStart = getParam(5)
		inst.ArpEnd = getParam(6)
		inst.ArpLoop = getParam(7)
		inst.PulseWidthLo = getParam(8)
		inst.PulseWidthHi = getParam(9)
		inst.PulseSpeed = getParam(10)
		inst.VibDepthSpeed = getParam(11)
		inst.VibDelay = getParam(12)
		inst.FilterStart = getParam(13)
		inst.FilterEnd = getParam(14)
		inst.FilterLoop = getParam(15)

		instruments[i] = inst
	}

	return instruments
}

func parsePattern(raw []byte, srcOff int, addr uint16) Pattern {
	pat := Pattern{Address: addr}
	for row := 0; row < 64; row++ {
		off := srcOff + row*3
		b0 := raw[off]
		b1 := raw[off+1]
		b2 := raw[off+2]

		pat.Rows[row] = Row{
			Note:   b0 & 0x7F,
			Inst:   b1 & 0x1F,
			Effect: (b1 >> 5) | ((b0 >> 4) & 8),
			Param:  b2,
		}
	}
	return pat
}

func parseTable(raw []byte, start, end int) []byte {
	if start < 0 || end < start || start >= len(raw) {
		return nil
	}
	if end > len(raw) {
		end = len(raw)
	}
	tbl := make([]byte, end-start)
	copy(tbl, raw[start:end])
	return tbl
}

func GetRawPatternData(raw []byte, addrs TableAddresses, addr uint16) []byte {
	srcOff := int(addr) - addrs.BaseAddr
	if srcOff >= 0 && srcOff+192 <= len(raw) {
		result := make([]byte, 192)
		copy(result, raw[srcOff:srcOff+192])
		return result
	}
	return nil
}

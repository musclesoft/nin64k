package parse

const (
	CodeSongStart   = 0x003B
	CodeTranspose0  = 0x00BA
	CodeTrackLo0    = 0x00C0
	CodeTrackHi0    = 0x00C3
	CodeTranspose1  = 0x00CE
	CodeTrackLo1    = 0x00D4
	CodeTrackHi1    = 0x00D7
	CodeTranspose2  = 0x00E2
	CodeTrackLo2    = 0x00E8
	CodeTrackHi2    = 0x00EB
	CodeInstAD      = 0x0520
	CodeInstSR      = 0x0526
	CodeWavetable   = 0x025F
	CodeArptable    = 0x0281
	CodeFiltertable = 0x015B
)

func ReadWord(data []byte, offset int) uint16 {
	return uint16(data[offset]) | uint16(data[offset+1])<<8
}

func WriteWord(data []byte, offset int, val uint16) {
	data[offset] = byte(val)
	data[offset+1] = byte(val >> 8)
}

type TableAddresses struct {
	BaseAddr       int
	SongStart      int
	Transpose      [3]int
	TrackLo        [3]int
	TrackHi        [3]int
	InstAD         int
	InstSR         int
	NumInstruments int
	Wavetable      int
	Arptable       int
	Filtertable    int
	NumOrders      int
}

func ExtractAddresses(raw []byte) TableAddresses {
	baseAddr := int(raw[2]) << 8

	addrs := TableAddresses{
		BaseAddr:  baseAddr,
		SongStart: int(ReadWord(raw, CodeSongStart)) - baseAddr,
	}

	addrs.Transpose[0] = int(ReadWord(raw, CodeTranspose0)) - baseAddr
	addrs.Transpose[1] = int(ReadWord(raw, CodeTranspose1)) - baseAddr
	addrs.Transpose[2] = int(ReadWord(raw, CodeTranspose2)) - baseAddr

	addrs.TrackLo[0] = int(ReadWord(raw, CodeTrackLo0)) - baseAddr
	addrs.TrackLo[1] = int(ReadWord(raw, CodeTrackLo1)) - baseAddr
	addrs.TrackLo[2] = int(ReadWord(raw, CodeTrackLo2)) - baseAddr

	addrs.TrackHi[0] = int(ReadWord(raw, CodeTrackHi0)) - baseAddr
	addrs.TrackHi[1] = int(ReadWord(raw, CodeTrackHi1)) - baseAddr
	addrs.TrackHi[2] = int(ReadWord(raw, CodeTrackHi2)) - baseAddr

	instADAddr := ReadWord(raw, CodeInstAD)
	instSRAddr := ReadWord(raw, CodeInstSR)
	addrs.InstAD = int(instADAddr) - baseAddr
	addrs.InstSR = int(instSRAddr) - baseAddr
	addrs.NumInstruments = int(instSRAddr) - int(instADAddr)

	addrs.Wavetable = int(ReadWord(raw, CodeWavetable)) - baseAddr
	addrs.Arptable = int(ReadWord(raw, CodeArptable)) - baseAddr
	addrs.Filtertable = int(ReadWord(raw, CodeFiltertable)) - baseAddr

	addrs.NumOrders = addrs.TrackLo[0] - addrs.Transpose[0]
	if addrs.NumOrders <= 0 || addrs.NumOrders > 255 {
		addrs.NumOrders = 255
	}

	return addrs
}

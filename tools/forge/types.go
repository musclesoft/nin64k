package main

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
}

type SongAnalysis struct {
	EffectUsage       map[byte]int
	EffectParams      map[byte]map[byte]int
	FSubUsage         map[string]int
	ReachableOrders   []int
	OrderMap          map[int]int
	PatternAddrs      map[uint16]bool
	PatternBreaks     map[uint16]int
	PatternJumps      map[uint16]int
	UsedInstruments   []int
	InstrumentFreq    map[int]int
	FilterTriggerInst map[int]bool
	TruncateLimits    map[uint16]int
}

type TransformedRow struct {
	Note   byte
	Inst   byte
	Effect byte
	Param  byte
}

type TransformedPattern struct {
	OriginalAddr uint16
	CanonicalIdx int
	Rows         []TransformedRow
	TruncateAt   int
}

type TransformedOrder struct {
	PatternIdx int
	Transpose  int8
}

type TransformedSong struct {
	Instruments    []Instrument
	Patterns       []TransformedPattern
	Orders         [3][]TransformedOrder
	WaveTable      []byte
	ArpTable       []byte
	FilterTable    []byte
	EffectRemap    [16]byte
	FSubRemap      map[int]byte
	InstRemap      []int
	WaveRemap      []int
	ArpRemap       []int
	FilterRemap    []int
	PatternRemap   map[uint16]uint16
	TransposeDelta map[uint16]int
	OrderMap       map[int]int
	MaxUsedSlot    int
}

type EncodedSong struct {
	RowDict          []byte
	RowToIdx         map[string]int
	EquivMap         map[int]int
	PatternData      [][]byte
	PatternOffsets   []uint16
	PatternGapCodes  []byte
	PackedPatterns   []byte
	PrimaryCount     int
	ExtendedCount    int
	OrderBitstream   []byte
	DeltaTable       []byte
	DeltaBases       []int
	StartConst       int
	TransposeTable   []byte
	TransposeBases   []int
	InstrumentData   []byte
	FilterData       []byte
	ArpData          []byte
	TrackStarts      [3]byte
}

type ConversionStats struct {
	OrigOrders       int
	NewOrders        int
	OrigWaveSize     int
	OrigArpSize      int
	UniquePatterns   int
	CanonicalPats    int
	TransposeEquiv   int
	DictSize         int
	PrimaryIndices   int
	ExtendedIndices  int
	DeltaSet         []int
	TrackStarts      [3]byte
	TempTranspose    [3][]byte
	TempTrackptr     [3][]byte
}

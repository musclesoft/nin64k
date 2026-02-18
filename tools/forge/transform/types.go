package transform

import "forge/parse"

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

type TransformOptions struct {
	PermanentArp     bool
	MaxPermArpRows   int
	PersistPorta     bool
	PortaUpEffect    byte
	PortaDownEffect  byte
	PersistTonePorta bool
	TonePortaEffect  byte
	EquivMap         map[string]string
	OptimizeInst     bool
	TransposeEquiv   *TransposeEquivResult // Pre-computed transpose equivalence (optional)
}

type TransformedSong struct {
	Instruments    []parse.Instrument
	Patterns       []TransformedPattern
	Orders         [3][]TransformedOrder
	WaveTable      []byte
	ArpTable       []byte
	FilterTable    []byte
	EffectRemap    [16]byte
	FSubRemap      map[int]byte
	InstRemap      []int
	PatternRemap   map[uint16]uint16
	TransposeDelta map[uint16]int
	OrderMap       map[int]int
	MaxUsedSlot    int
	PatternOrder   []uint16
}

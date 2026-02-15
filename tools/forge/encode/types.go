package encode

type EncodedSong struct {
	RowDict          []byte
	RowToIdx         map[string]int
	RawPatterns      [][]byte
	RawPatternsEquiv [][]byte
	TruncateLimits   []int
	PatternData      [][]byte
	PatternOffsets   []uint16
	PatternGapCodes  []byte
	PackedPatterns   []byte
	CanonPatterns    [][]byte
	CanonGapCodes    []byte
	PatternCanon     []int
	PrimaryCount     int
	ExtendedCount    int
	OrderBitstream   []byte
	DeltaTable       []byte
	DeltaBases       []int
	StartConst       int
	TransposeTable   []byte
	TransposeBases   []int
	InstrumentData   []byte
	TrackStarts      [3]byte
	TempTranspose    [3][]byte
	TempTrackptr     [3][]byte
}

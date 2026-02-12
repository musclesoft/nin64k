package serialize

import (
	"forge/encode"
	"forge/transform"
)

func Serialize(song transform.TransformedSong, encoded encode.EncodedSong) []byte {
	output := make([]byte, OutputSize)

	copy(output[InstOffset:], encoded.InstrumentData)

	numOrders := len(song.Orders[0])
	bitstream := encode.PackOrderBitstream(numOrders, encoded.TempTranspose, encoded.TempTrackptr)
	copy(output[BitstreamOffset:], bitstream)

	filterSize := len(song.FilterTable)
	if filterSize > MaxFilterSize {
		filterSize = MaxFilterSize
	}
	copy(output[FilterOffset:], song.FilterTable[:filterSize])

	arpSize := len(song.ArpTable)
	if arpSize > MaxArpSize {
		arpSize = MaxArpSize
	}
	copy(output[ArpOffset:], song.ArpTable[:arpSize])

	output[TransBaseOffset] = 0
	output[DeltaBaseOffset] = 0

	numDictEntries := len(encoded.RowDict) / 3
	for i := 1; i < numDictEntries && i <= DictArraySize; i++ {
		output[RowDictOffset+i-1] = encoded.RowDict[i*3]
		output[RowDictOffset+DictArraySize+i-1] = encoded.RowDict[i*3+1]
		output[RowDictOffset+DictArraySize*2+i-1] = encoded.RowDict[i*3+2]
	}

	patternDataStart := PackedPtrsOffset
	copy(output[patternDataStart:], encoded.PackedPatterns)

	return output
}

package encode

import (
	"forge/transform"
)

func encodeOrders(song transform.TransformedSong) ([3][]byte, [3][]byte, [3]byte) {
	numOrders := len(song.Orders[0])
	if numOrders == 0 {
		return [3][]byte{}, [3][]byte{}, [3]byte{}
	}

	var transpose [3][]byte
	var trackptr [3][]byte
	var trackStarts [3]byte

	for ch := 0; ch < 3; ch++ {
		transpose[ch] = make([]byte, numOrders)
		trackptr[ch] = make([]byte, numOrders)
	}

	for ch := 0; ch < 3; ch++ {
		if len(song.Orders[ch]) > 0 {
			trackStarts[ch] = byte(song.Orders[ch][0].PatternIdx)
		}

		prevPtr := 0
		if len(song.Orders[ch]) > 0 {
			prevPtr = song.Orders[ch][0].PatternIdx
		}
		prevTranspose := int8(0)
		if len(song.Orders[ch]) > 0 {
			prevTranspose = song.Orders[ch][0].Transpose
		}

		for i, order := range song.Orders[ch] {
			if i == 0 {
				transpose[ch][i] = 0
				trackptr[ch][i] = 0
				continue
			}

			transDelta := int(order.Transpose) - int(prevTranspose)
			ptrDelta := order.PatternIdx - prevPtr

			transpose[ch][i] = byte(transDelta) & 0x0F
			trackptr[ch][i] = byte(ptrDelta) & 0x1F

			prevTranspose = order.Transpose
			prevPtr = order.PatternIdx
		}
	}

	return transpose, trackptr, trackStarts
}

func PackOrderBitstream(numOrders int, transpose [3][]byte, trackptr [3][]byte) []byte {
	out := make([]byte, numOrders*4)
	for i := 0; i < numOrders; i++ {
		ch0Tr := transpose[0][i] & 0x0F
		ch1Tr := transpose[1][i] & 0x0F
		ch2Tr := transpose[2][i] & 0x0F
		ch0Tp := trackptr[0][i] & 0x1F
		ch1Tp := trackptr[1][i] & 0x1F
		ch2Tp := trackptr[2][i] & 0x1F

		out[i*4+0] = ch0Tr | (ch1Tr << 4)
		out[i*4+1] = ch2Tr | ((ch0Tp & 0x0F) << 4)
		out[i*4+2] = (ch0Tp >> 4) | (ch1Tp << 1) | ((ch2Tp & 0x03) << 6)
		out[i*4+3] = ch2Tp >> 2
	}
	return out
}

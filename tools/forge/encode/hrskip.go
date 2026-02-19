package encode

import "forge/transform"

// ComputeHRSkipMask computes the 3-bit HR skip mask for each order.
// For each channel, if row 0 of the next pattern should skip HR, the bit is set.
//
// HR is skipped when:
// - No note (note == 0)
// - Key off (note == 0x61)
// - Portamento (effect == 2)
// - Immediate HR flag set (effect bit 3, stored as bit 7 in note byte)
//
// The mask is encoded as: bit 0 = ch0, bit 1 = ch1, bit 2 = ch2
func ComputeHRSkipMask(song transform.TransformedSong) []byte {
	numOrders := len(song.Orders[0])
	if numOrders == 0 {
		return nil
	}

	masks := make([]byte, numOrders)

	for orderIdx := 0; orderIdx < numOrders; orderIdx++ {
		var mask byte

		for ch := 0; ch < 3; ch++ {
			// Get next order
			nextOrderIdx := orderIdx + 1
			if nextOrderIdx >= numOrders {
				// At end of song, no next pattern - do HR
				continue
			}

			// Get pattern index for next order from song.Orders
			if nextOrderIdx >= len(song.Orders[ch]) {
				continue
			}
			patIdx := song.Orders[ch][nextOrderIdx].PatternIdx

			// Get pattern
			if patIdx < 0 || patIdx >= len(song.Patterns) {
				continue
			}
			pattern := song.Patterns[patIdx]

			// Check row 0
			if len(pattern.Rows) == 0 {
				// Empty pattern - skip HR
				mask |= 1 << ch
				continue
			}

			row := pattern.Rows[0]
			note := row.Note & 0x7F
			effect := row.Effect

			// Determine if we should skip HR
			// Logic must match VPlayer hrLookahead:
			// 1. No note or key off -> skip HR
			// 2. Immediate HR (effect bit 3) -> do HR (don't skip)
			// 3. Portamento (effect 2) -> skip HR
			// 4. Otherwise -> do HR (don't skip)
			skipHR := false

			if note == 0 || note == 0x61 {
				// No note or key off -> skip HR
				skipHR = true
			} else if effect&0x08 != 0 {
				// Note present with immediate HR -> do HR (don't skip)
				skipHR = false
			} else if effect == 2 {
				// Note present with portamento -> skip HR
				skipHR = true
			} else {
				// Note present, normal -> do HR (don't skip)
				skipHR = false
			}

			if skipHR {
				mask |= 1 << ch
			}
		}

		masks[orderIdx] = mask
	}

	return masks
}

// PackOrderBitstreamWithHR packs the order bitstream with HR skip masks.
// Byte 3 format: bits 0-2 = trackptr bits 2-4, bits 3-5 = HR skip mask
func PackOrderBitstreamWithHR(numOrders int, transpose [3][]byte, trackptr [3][]byte, hrSkip []byte) []byte {
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
		out[i*4+3] = (ch2Tp >> 2) | ((hrSkip[i] & 0x07) << 3)
	}
	return out
}

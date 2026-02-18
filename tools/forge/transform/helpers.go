package transform

import "forge/parse"

func encodeB0(note, effect byte) byte {
	return (note & 0x7F) | ((effect & 8) << 4)
}

func encodeB1(inst, effect byte) byte {
	return (inst & 0x1F) | ((effect & 7) << 5)
}

func DeepCopyPatterns(patterns []TransformedPattern) []TransformedPattern {
	result := make([]TransformedPattern, len(patterns))
	for i, pat := range patterns {
		result[i] = TransformedPattern{
			OriginalAddr: pat.OriginalAddr,
			CanonicalIdx: pat.CanonicalIdx,
			TruncateAt:   pat.TruncateAt,
			Rows:         make([]TransformedRow, len(pat.Rows)),
		}
		copy(result[i].Rows, pat.Rows)
	}
	return result
}

func remapInstruments(instruments []parse.Instrument, instRemap []int, maxUsedSlot int) []parse.Instrument {
	if maxUsedSlot <= 0 {
		return nil
	}

	result := make([]parse.Instrument, maxUsedSlot+1)

	for oldIdx, newIdx := range instRemap {
		if newIdx > 0 && newIdx <= maxUsedSlot && oldIdx < len(instruments) {
			result[newIdx] = instruments[oldIdx]
		}
	}

	return result
}

package encode

import "forge/parse"

func encodeInstruments(instruments []parse.Instrument, maxUsedSlot int) []byte {
	return encodeInstrumentsFromSource(instruments, maxUsedSlot, nil, 0, 0)
}

func encodeInstrumentsFromSource(instruments []parse.Instrument, maxUsedSlot int, raw []byte, srcInstOff, numInst int) []byte {
	if maxUsedSlot <= 0 {
		return nil
	}

	vibDepthRemap := [16]byte{0, 4, 2, 3, 1, 7, 5, 0, 8, 0, 6, 0, 0, 0, 0, 9}

	data := make([]byte, maxUsedSlot*16)

	for i := 1; i <= maxUsedSlot && i < len(instruments); i++ {
		base := (i - 1) * 16

		var params [16]byte
		if raw != nil && numInst > 0 && i < numInst {
			for p := 0; p < 16; p++ {
				idx := srcInstOff + p*numInst + i
				if idx < len(raw) {
					params[p] = raw[idx]
				}
			}
		} else {
			inst := instruments[i]
			params[0] = inst.AD
			params[1] = inst.SR
			params[2] = inst.WaveStart
			params[3] = inst.WaveEnd
			params[4] = inst.WaveLoop
			params[5] = inst.ArpStart
			params[6] = inst.ArpEnd
			params[7] = inst.ArpLoop
			params[8] = inst.VibDelay
			params[9] = inst.VibDepthSpeed
			params[10] = inst.PulseWidth
			params[11] = inst.PulseSpeed
			params[12] = inst.PulseLimits
			params[13] = inst.FilterStart
			params[14] = inst.FilterEnd
			params[15] = inst.FilterLoop
		}

		data[base+0] = params[0]
		data[base+1] = params[1]
		data[base+2] = params[2]
		if params[3] < 255 {
			data[base+3] = params[3] + 1
		} else {
			data[base+3] = params[3]
		}
		data[base+4] = params[4]
		data[base+5] = params[5]
		if params[6] < 255 {
			data[base+6] = params[6] + 1
		} else {
			data[base+6] = params[6]
		}
		data[base+7] = params[7]
		data[base+8] = params[8]

		oldDepth := params[9] >> 4
		speed := params[9] & 0x0F
		data[base+9] = (vibDepthRemap[oldDepth] << 4) | speed

		data[base+10] = (params[10] << 4) | (params[10] >> 4)

		data[base+11] = params[11]
		data[base+12] = params[12]
		data[base+13] = params[13]
		if params[14] < 255 {
			data[base+14] = params[14] + 1
		} else {
			data[base+14] = params[14]
		}
		data[base+15] = params[15]
	}

	return data
}

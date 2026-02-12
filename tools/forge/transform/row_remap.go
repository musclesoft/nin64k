package transform

func remapRowBytes(b0, b1, b2 byte, remap [16]byte, fSubRemap map[int]byte, instRemap []int) (byte, byte, byte) {
	oldEffect := (b1 >> 5) | ((b0 >> 4) & 8)
	var newEffect byte
	var newParam byte = b2

	switch oldEffect {
	case 0:
		newEffect = 0
		newParam = 0
	case 1:
		newEffect = remap[1]
		if b2&0x80 != 0 {
			newParam = 0
		} else {
			newParam = 1
		}
	case 2:
		newEffect = remap[2]
		if b2 == 0x80 {
			newParam = 1
		} else {
			newParam = 0
		}
	case 3:
		newEffect = remap[3]
		newParam = ((b2 & 0x0F) << 4) | ((b2 & 0xF0) >> 4)
	case 4:
		newEffect = 0
		newParam = 1
	case 7:
		newEffect = remap[7]
		newParam = b2
	case 8:
		newEffect = remap[8]
		newParam = b2
	case 9:
		newEffect = remap[9]
		newParam = b2
	case 0xA:
		newEffect = remap[0xA]
		newParam = b2
	case 0xB:
		newEffect = remap[0xB]
		newParam = b2
	case 0xD:
		newEffect = 0
		newParam = 2
	case 0xE:
		newEffect = remap[0xE]
		newParam = b2
	case 0xF:
		if b2 < 0x80 {
			newEffect = fSubRemap[0x10]
			newParam = b2
		} else {
			hiNib := b2 & 0xF0
			loNib := b2 & 0x0F
			switch hiNib {
			case 0xB0:
				newEffect = 0
				newParam = 3
			case 0xF0:
				newEffect = fSubRemap[0x11]
				newParam = loNib
			case 0xE0:
				newEffect = fSubRemap[0x12]
				instIdx := int(loNib)
				if instRemap != nil && instIdx > 0 && instIdx < len(instRemap) && instRemap[instIdx] > 0 {
					remapped := instRemap[instIdx]
					if remapped <= 15 {
						instIdx = remapped
					}
				}
				newParam = byte(instIdx << 4)
			case 0x80:
				newEffect = fSubRemap[0x13]
				newParam = loNib
			case 0x90:
				newEffect = fSubRemap[0x14]
				newParam = loNib << 4
			default:
				newEffect = 0
				newParam = 0
			}
		}
	default:
		newEffect = remap[oldEffect]
		newParam = b2
	}

	newB0 := (b0 & 0x7F) | ((newEffect & 8) << 4)

	inst := int(b1 & 0x1F)
	if instRemap != nil && inst > 0 && inst < len(instRemap) && instRemap[inst] > 0 {
		inst = instRemap[inst]
	}
	newB1 := byte(inst&0x1F) | ((newEffect & 7) << 5)

	return newB0, newB1, newParam
}

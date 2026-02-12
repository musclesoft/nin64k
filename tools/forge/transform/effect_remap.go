package transform

import (
	"forge/analysis"
	"sort"
)

type effectFreq struct {
	name  string
	code  int
	count int
}

func buildEffectRemap(anal analysis.SongAnalysis) ([16]byte, map[int]byte) {
	var usedEffects []effectFreq

	for i := 1; i < 16; i++ {
		if i == 4 || i == 0xD || i == 0xF {
			continue
		}
		if count, ok := anal.EffectUsage[byte(i)]; ok && count > 0 {
			usedEffects = append(usedEffects, effectFreq{
				code:  i,
				count: count,
			})
		}
	}

	fSubNames := []struct {
		name string
		code int
	}{
		{"speed", 0x10},
		{"hrdrest", 0x11},
		{"filttrig", 0x12},
		{"globalvol", 0x13},
		{"filtmode", 0x14},
	}
	for _, fs := range fSubNames {
		if c := anal.FSubUsage[fs.name]; c > 0 {
			usedEffects = append(usedEffects, effectFreq{
				name:  fs.name,
				code:  fs.code,
				count: c,
			})
		}
	}

	sort.Slice(usedEffects, func(i, j int) bool {
		return usedEffects[i].count > usedEffects[j].count
	})

	effectRemap := [16]byte{}
	fSubRemap := make(map[int]byte)

	for newIdx, ef := range usedEffects {
		newEffect := byte(newIdx + 1)
		if ef.code < 0x10 {
			effectRemap[ef.code] = newEffect
		} else {
			fSubRemap[ef.code] = newEffect
		}
	}

	effectRemap[4] = 0
	effectRemap[0xD] = 0
	effectRemap[0xF] = 0

	return effectRemap, fSubRemap
}

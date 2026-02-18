package transform

// BuildGlobalEffectRemap builds hardcoded effect remapping.
// These values are assigned by frequency (frame-based analysis, 2024-02):
//
//	GT $A (arp):      3483 -> 1
//	GT $3 (toneporta): 2974 -> 2
//	F speed:          1260 -> 3
//	F hrdrest:         921 -> 4
//	F filttrig:        722 -> 5
//	GT $8 (SR):        446 -> 6
//	GT $9 (waveform):  387 -> 7
//	GT $2 (portadown): 312 -> 8
//	GT $7 (AD):        219 -> 9
//	GT $E (slide):     108 -> 10
//	GT $1 (portaup):    96 -> 11
//	F globalvol:        66 -> 12
//	F filtmode:         40 -> 13
//
// Returns effectRemap, fSubRemap, portaUpEffect, portaDownEffect, tonePortaEffect.
func BuildGlobalEffectRemap() ([16]byte, map[int]byte, byte, byte, byte) {
	effectRemap := [16]byte{}
	fSubRemap := make(map[int]byte)

	// Regular effects (by frequency)
	effectRemap[GTEffectArp] = 1        // GT A: 3483
	effectRemap[GTEffectTonePorta] = 2  // GT 3: 2974
	effectRemap[GTEffectSR] = 6         // GT 8: 446
	effectRemap[GTEffectWaveform] = 7   // GT 9: 387
	effectRemap[GTEffectPortaDown] = 8  // GT 2: 312
	effectRemap[GTEffectAD] = 9         // GT 7: 219
	effectRemap[GTEffectMulti] = 10     // GT E: 108
	effectRemap[GTEffectPortaUp] = 11   // GT 1: 96

	// Special cases -> effect 0
	effectRemap[GTEffectVibOff] = PlayerEffectSpecial  // GT 4: 64
	effectRemap[GTEffectPosJump] = PlayerEffectSpecial // GT B: 4
	effectRemap[GTEffectBreak] = PlayerEffectSpecial   // GT D: 19
	effectRemap[GTEffectSub] = PlayerEffectSpecial     // GT F: 3025 (sub-effects)

	// F sub-effects (by frequency)
	fSubRemap[GTSubCodeSpeed] = 3     // 1260
	fSubRemap[GTSubCodeHrdRest] = 4   // 921
	fSubRemap[GTSubCodeFiltTrig] = 5  // 722
	fSubRemap[GTSubCodeGlobalVol] = 12 // 66
	fSubRemap[GTSubCodeFiltMode] = 13  // 40

	portaUpEffect := effectRemap[GTEffectPortaUp]
	portaDownEffect := effectRemap[GTEffectPortaDown]
	tonePortaEffect := effectRemap[GTEffectTonePorta]

	return effectRemap, fSubRemap, portaUpEffect, portaDownEffect, tonePortaEffect
}

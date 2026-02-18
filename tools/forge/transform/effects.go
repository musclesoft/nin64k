package transform

// GT (GoatTracker) source effect numbers - before remapping
const (
	GTEffectSpecial   = 0x0 // Special (vibrato off in param)
	GTEffectPortaUp   = 0x1 // Portamento up
	GTEffectPortaDown = 0x2 // Portamento down
	GTEffectTonePorta = 0x3 // Tone portamento (slide to note)
	GTEffectVibOff    = 0x4 // Vibrato off
	GTEffectAD        = 0x7 // Set AD envelope
	GTEffectSR        = 0x8 // Set SR envelope
	GTEffectWaveform  = 0x9 // Set waveform
	GTEffectArp       = 0xA // Arpeggio
	GTEffectPosJump   = 0xB // Position jump
	GTEffectBreak     = 0xD // Pattern break
	GTEffectMulti     = 0xE // Multi-speed (unused)
	GTEffectSub       = 0xF // Sub-effects (speed, hrdrest, etc.)
)

// GT sub-effect internal codes for remapping
const (
	GTSubCodeSpeed     = 0x10 // Speed (Fxx, xx < E0)
	GTSubCodeHrdRest   = 0x11 // Hard restart timing
	GTSubCodeFiltTrig  = 0x12 // Filter trigger
	GTSubCodeGlobalVol = 0x13 // Global volume
	GTSubCodeFiltMode  = 0x14 // Filter mode
)

// Player effect numbers - after remapping (as shown in odin_player.inc)
const (
	PlayerEffectSpecial  = 0  // 0=nop, 1=vib off, 2=break, 3=fineslide
	PlayerEffectArp      = 1  // Pattern arpeggio
	PlayerEffectPorta    = 2  // Tone portamento (slide to note)
	PlayerEffectSpeed    = 3  // Set speed
	PlayerEffectHrdRest  = 4  // Hard restart timing
	PlayerEffectFiltTrig = 5  // Filter trigger
	PlayerEffectSR       = 6  // Set SR envelope
	PlayerEffectWave     = 7  // Set waveform
	PlayerEffectPulse    = 8  // Set pulse width
	PlayerEffectAD       = 9  // Set AD envelope
	PlayerEffectReso     = 10 // Filter resonance
	PlayerEffectSlide    = 11 // Pitch slide (accumulates delta)
	PlayerEffectGlobVol  = 12 // Global volume
	PlayerEffectFiltMode = 13 // Filter mode
)

// Player effect 0 param values
const (
	PlayerParam0Nop       = 0 // NOP - do nothing
	PlayerParam0VibOff    = 1 // Disable vibrato
	PlayerParam0Break     = 2 // Pattern break
	PlayerParam0FineSlide = 3 // Fine pitch slide
	PlayerParam0NopHard   = 4 // NOP that clears permarp
)

// PersistentPlayerEffects returns the player effects that truly persist through NOP
func PersistentPlayerEffects() []byte {
	return []byte{
		PlayerEffectHrdRest,
		PlayerEffectReso,
		PlayerEffectGlobVol,
		PlayerEffectFiltMode,
	}
}

// PlayerEffectName returns the human-readable name for a player effect
func PlayerEffectName(eff byte) string {
	switch eff {
	case PlayerEffectSpecial:
		return "special"
	case PlayerEffectArp:
		return "arp"
	case PlayerEffectPorta:
		return "porta"
	case PlayerEffectSpeed:
		return "speed"
	case PlayerEffectHrdRest:
		return "hrdrest"
	case PlayerEffectFiltTrig:
		return "filttrig"
	case PlayerEffectSR:
		return "SR"
	case PlayerEffectWave:
		return "wave"
	case PlayerEffectPulse:
		return "pulse"
	case PlayerEffectAD:
		return "AD"
	case PlayerEffectReso:
		return "reso"
	case PlayerEffectSlide:
		return "slide"
	case PlayerEffectGlobVol:
		return "globalvol"
	case PlayerEffectFiltMode:
		return "filtmode"
	default:
		return "unknown"
	}
}

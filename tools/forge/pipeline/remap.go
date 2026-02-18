package pipeline

import (
	"fmt"
	"os"

	"forge/analysis"
	"forge/encode"
	"forge/parse"
	"forge/transform"
	"forge/verify"
)

type RemapResult struct {
	EffectRemap    [16]byte
	FSubRemap      map[int]byte
	TransformOpts  transform.TransformOptions
	EquivMaps      []map[string]string
	Transformed    []transform.TransformedSong
}

func BuildRemapAndTransform(
	cfg *Config,
	songNames []string,
	rawData [9][]byte,
	parsedSongs [9]parse.ParsedSong,
	analyses [9]analysis.SongAnalysis,
) RemapResult {
	fmt.Println("\n=== Build global effect remap ===")
	globalEffectRemap, globalFSubRemap, portaUpEffect, _, tonePortaEffect := transform.BuildGlobalEffectRemap()
	transformOpts := transform.TransformOptions{
		PermanentArp:   true,
		MaxPermArpRows: 0,
		PersistPorta:     false,
		PortaUpEffect:    portaUpEffect,
		PortaDownEffect:  0,
		PersistTonePorta: false,
		TonePortaEffect:  tonePortaEffect,
		OptimizeInst:     false,
	}
	fmt.Println("  Effect remap (orig -> new):")
	for orig := 0; orig < 16; orig++ {
		if globalEffectRemap[orig] != 0 || orig == 0 {
			fmt.Printf("    %X -> %d\n", orig, globalEffectRemap[orig])
		}
	}
	fmt.Println("  F sub-effect remap:")
	for code, newEff := range globalFSubRemap {
		fmt.Printf("    0x%X -> %d\n", code, newEff)
	}

	// Find transpose-equivalent patterns
	transposeEquiv := FindTransposeEquiv(songNames, rawData, parsedSongs, analyses)

	fmt.Println("\n=== Build equiv maps ===")
	equivMaps := make([]map[string]string, len(songNames))
	for i, name := range songNames {
		if rawData[i] == nil {
			continue
		}
		patterns, truncateLimits := transform.ExtractRawPatternsAsBytes(parsedSongs[i], analyses[i], rawData[i])
		equivMaps[i] = encode.BuildEquivHexMap(cfg.ProjectRoot, i+1, patterns, truncateLimits)
		if len(equivMaps[i]) > 0 {
			fmt.Printf("  %s: %d mappings\n", name, len(equivMaps[i]))
		}
	}

	fmt.Println("\n=== Apply equiv (pre-transform fixup) ===")
	hasEquiv := false
	for i, name := range songNames {
		if rawData[i] == nil {
			continue
		}
		if len(equivMaps[i]) > 0 {
			fmt.Printf("  %s: %d row substitutions\n", name, len(equivMaps[i]))
			hasEquiv = true
		}
	}
	if !hasEquiv {
		fmt.Println("  (none)")
	}

	fmt.Println("\n=== Transform (effect + inst remap) ===")
	transformedSongs := make([]transform.TransformedSong, len(songNames))
	for i, name := range songNames {
		if rawData[i] == nil {
			continue
		}
		songOpts := transformOpts
		songOpts.EquivMap = equivMaps[i]
		songOpts.TransposeEquiv = &transposeEquiv.Equiv[i]
		transformedSongs[i] = transform.TransformWithGlobalEffects(
			parsedSongs[i], analyses[i], rawData[i],
			globalEffectRemap, globalFSubRemap, songOpts,
		)
		usedOrig := len(analyses[i].UsedInstruments)
		fmt.Printf("  %s: %d patterns, %d instruments (was %d)\n",
			name, len(transformedSongs[i].Patterns), transformedSongs[i].MaxUsedSlot, usedOrig)
	}

	fmt.Println("\n=== Selective persistent FX optimization ===")
	for _, eff := range transform.PersistentPlayerEffects() {
		converted := 0
		for i := range songNames {
			if rawData[i] == nil {
				continue
			}
			var c int
			transformedSongs[i].Patterns, c = transform.OptimizePersistentFXSelective(
				transformedSongs[i].Patterns, transformedSongs[i].Orders, eff)
			converted += c
		}
		if converted > 0 {
			fmt.Printf("  %s: %d rows -> NOP\n", transform.PlayerEffectName(eff), converted)
		}
	}

	// Permarp optimization runs AFTER persistent FX optimization to avoid
	// creating NOPs that incorrectly inherit permarp
	if transformOpts.PermanentArp {
		fmt.Println("\n=== Permanent arpeggio optimization ===")
		arpEffect := globalEffectRemap[0xA]
		if arpEffect != 0 {
			totalCross := 0
			// Store original patterns for verification (before any permarp optimization)
			origPatternsForVerify := make([][]transform.TransformedPattern, len(songNames))
			for i := range songNames {
				if rawData[i] == nil {
					continue
				}
				origPatternsForVerify[i] = transform.DeepCopyPatterns(transformedSongs[i].Patterns)

				// Within-pattern optimization
				transformedSongs[i].Patterns = transform.OptimizeArpToPermanent(
					transformedSongs[i].Patterns, arpEffect, nil)

				// Cross-pattern optimization (includes boundary protection)
				var crossConverted int
				transformedSongs[i].Patterns, crossConverted = transform.OptimizeCrossPatternArp(
					transformedSongs[i].Patterns, transformedSongs[i].Orders, arpEffect)
				totalCross += crossConverted
			}
			fmt.Printf("  Cross-pattern: %d rows converted\n", totalCross)

			// Verify after ALL permarp optimizations (including boundary protection)
			for i, name := range songNames {
				if rawData[i] == nil {
					continue
				}
				if err := transform.VerifyFullSongPermarp(origPatternsForVerify[i], transformedSongs[i].Patterns, transformedSongs[i].Orders, arpEffect); err != nil {
					fmt.Printf("FATAL: %s permarp verification failed:\n%v\n", name, err)
					os.Exit(1)
				}
			}
		}
	}

	fmt.Println("\n=== Verify remapped patterns ===")
	for i, name := range songNames {
		if rawData[i] == nil {
			continue
		}
		if err := verify.Transform(parsedSongs[i], analyses[i], transformedSongs[i], rawData[i]); err != nil {
			fmt.Printf("FATAL: %s verification failed:\n%v\n", name, err)
			os.Exit(1)
		}
	}
	fmt.Println("  All verified")

	return RemapResult{
		EffectRemap:   globalEffectRemap,
		FSubRemap:     globalFSubRemap,
		TransformOpts: transformOpts,
		EquivMaps:     equivMaps,
		Transformed:   transformedSongs,
	}
}

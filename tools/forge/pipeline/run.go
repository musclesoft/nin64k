package pipeline

import (
	"fmt"
	"os"
	"sync"

	"forge/build"
	"forge/simulate"
	"forge/solve"
	"forge/transform"
)

func RunValidation(
	cfg *Config,
	songs [9]*ProcessedSong,
	outputs [][]byte,
	tables TablesResult,
	globalEffectRemap [16]byte,
	globalFSubRemap map[int]byte,
	transformOpts transform.TransformOptions,
) {
	fmt.Println("\n=== Validate with VM ===")
	if err := build.RebuildPlayer(cfg.ProjectPath("tools/odin_convert"), cfg.ProjectPath("build")); err != nil {
		fmt.Printf("  Warning: could not rebuild player: %v\n", err)
	}

	playerData, err := os.ReadFile(cfg.ProjectPath("build/player.bin"))
	if err != nil {
		fmt.Printf("  Skipping validation: %v\n", err)
		return
	}

	if err := build.VerifyTablesInPlayer(tables.DeltaResult.Table, tables.TransposeResult.Table, tables.GlobalWave.Data, playerData); err != nil {
		fmt.Printf("FATAL: tables not correctly embedded in player binary:\n  %v\n", err)
		os.Exit(1)
	}
	fmt.Println("  Tables verified in player binary")

	runVPValidation(cfg, songs, outputs, tables, playerData, globalEffectRemap, globalFSubRemap, transformOpts)
	runASMValidation(cfg, songs, outputs, tables, playerData)
}

func runVPValidation(
	cfg *Config,
	songs [9]*ProcessedSong,
	outputs [][]byte,
	tables TablesResult,
	playerData []byte,
	globalEffectRemap [16]byte,
	globalFSubRemap map[int]byte,
	transformOpts transform.TransformOptions,
) {
	fmt.Println("\n=== Transformed VP Validation ===")
	vpPassed, vpFailed := 0, 0

	for i, ps := range songs {
		if ps == nil || outputs[i] == nil {
			continue
		}

		testFrames := cfg.PartTimes[i]
		origWrites := simulate.GetOriginalWrites(ps.Raw, i+1, testFrames)

		deltaBytes := make([]byte, len(tables.DeltaResult.Table))
		for j, v := range tables.DeltaResult.Table {
			if v == solve.DeltaEmpty {
				deltaBytes[j] = 0
			} else {
				deltaBytes[j] = byte(v)
			}
		}
		transposeBytes := make([]byte, len(tables.TransposeResult.Table))
		for j, v := range tables.TransposeResult.Table {
			transposeBytes[j] = byte(v)
		}

		ok, writes, msg := simulate.CompareVirtual(
			origWrites,
			outputs[i],
			deltaBytes,
			transposeBytes,
			tables.GlobalWave.Data,
			len(ps.Encoded.PatternOffsets),
			testFrames,
			tables.DeltaResult.StartConst,
		)
		if ok {
			fmt.Printf("  %s: PASS (%d writes)\n", ps.Name, writes)
			vpPassed++
		} else {
			fmt.Printf("  %s: VFAIL - %s at write %d\n", ps.Name, msg, writes)
			vpFailed++
			badEntries := bisectEquivEntries(cfg,
				i+1, ps, origWrites,
				deltaBytes, transposeBytes, tables.GlobalWave.Data,
				tables.DeltaToIdx[i], tables.TransposeToIdx[i],
				tables.DeltaResult.Bases[i], tables.TransposeResult.Bases[i],
				tables.GlobalWave.Remap[i], tables.DeltaResult.StartConst,
				testFrames,
			)
			if len(badEntries) > 0 {
				fmt.Printf("    Found %d bad equiv entries:\n", len(badEntries))
				for _, entry := range badEntries {
					fmt.Printf("      %s\n", entry)
				}
			}
		}
	}
	fmt.Printf("Virtual validation: %d passed, %d failed\n", vpPassed, vpFailed)

	fmt.Println("\n=== Verify Excluded Equiv Entries ===")
	hasOptionalExclusions := checkExcludedEntries(cfg, songs, outputs, tables.DeltaResult, tables.TransposeResult, tables.GlobalWave, tables.DeltaToIdx, tables.TransposeToIdx, globalEffectRemap, globalFSubRemap, transformOpts, playerData)
	if hasOptionalExclusions {
		fmt.Println("\nFATAL: Optional exclusions found - these should be removed from equiv_cache.json")
		os.Exit(1)
	}
}

func runASMValidation(
	cfg *Config,
	songs [9]*ProcessedSong,
	outputs [][]byte,
	tables TablesResult,
	playerData []byte,
) {
	fmt.Println("\n=== ASM Player Validation ===")

	type asmResult struct {
		ok     bool
		writes int
		stats  *simulate.ASMStats
	}
	asmResults := make([]asmResult, len(songs))
	var wg sync.WaitGroup

	for i, ps := range songs {
		if ps == nil || outputs[i] == nil {
			continue
		}
		wg.Add(1)
		go func(idx int, ps *ProcessedSong) {
			defer wg.Done()
			ok, writes, stats := TestSong(cfg, idx+1, ps.Raw, outputs[idx], playerData, ps.Transformed, ps.Encoded, true)
			asmResults[idx] = asmResult{ok, writes, stats}
		}(i, ps)
	}
	wg.Wait()

	passed, failed := 0, 0
	allStats := make([]*simulate.ASMStats, len(songs))
	for i := range songs {
		if songs[i] == nil || outputs[i] == nil {
			continue
		}
		r := asmResults[i]
		allStats[i] = r.stats

		cyclesRatio := float64(r.stats.TotalCycles) / float64(r.stats.OrigCycles)
		maxRatio := float64(r.stats.MaxFrameCycles) / float64(r.stats.OrigMaxCycles)
		sizeRatio := float64(r.stats.NewSize) / float64(r.stats.OrigSize)

		status := "PASS"
		if !r.ok {
			status = "FAIL"
			failed++
		} else {
			passed++
		}
		fmt.Printf("  Song %d: %s cycles: %.2fx, max: %.2fx, size: %.2fx, dict: %d, len: $%X (%d writes)\n",
			i+1, status, cyclesRatio, maxRatio, sizeRatio, r.stats.DictSize, r.stats.NewSize, r.writes)
	}
	fmt.Printf("\nASM validation: %d passed, %d failed\n", passed, failed)

	simulate.ReportASMStats(allStats, playerData)
}

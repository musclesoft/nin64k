package pipeline

import (
	"fmt"

	"forge/encode"
	"forge/serialize"
	"forge/simulate"
	"forge/solve"
	"forge/transform"
	"forge/verify"
)

func checkExcludedEntries(
	cfg *Config,
	songs [9]*ProcessedSong,
	outputs [][]byte,
	deltaResult solve.DeltaTableResult,
	transposeResult solve.TransposeTableResult,
	globalWave *solve.GlobalWaveTable,
	deltaToIdx [9]map[int]byte,
	transposeToIdx [9]map[int8]byte,
	globalEffectRemap [16]byte,
	globalFSubRemap map[int]byte,
	transformOpts transform.TransformOptions,
	playerData []byte,
) bool {
	hasOptional := false
	for i, ps := range songs {
		if ps == nil || outputs[i] == nil {
			continue
		}
		songNum := i + 1
		excluded := encode.GetExcludedOrig(cfg.ProjectRoot, songNum)
		if len(excluded) == 0 {
			continue
		}

		var optional []string
		for _, entry := range excluded {
			newExcluded := make([]string, 0, len(excluded)-1)
			for _, e := range excluded {
				if e != entry {
					newExcluded = append(newExcluded, e)
				}
			}

			encode.UseOverrideExcluded = true
			encode.OverrideExcluded = newExcluded

			patterns, truncateLimits := transform.ExtractRawPatternsAsBytes(
				ps.Song, ps.Anal, ps.Raw,
			)
			equivMap := encode.BuildEquivHexMap(
				cfg.ProjectRoot, songNum,
				patterns, truncateLimits,
			)

			songOpts := transformOpts
			songOpts.EquivMap = equivMap
			transformed := transform.TransformWithGlobalEffects(ps.Song, ps.Anal, ps.Raw, globalEffectRemap, globalFSubRemap, songOpts)

			encoded := encode.Encode(transformed)

			output := serialize.SerializeWithWaveRemap(
				transformed,
				encoded,
				deltaToIdx[i],
				transposeToIdx[i],
				deltaResult.Bases[i],
				transposeResult.Bases[i],
				globalWave.Remap[i],
				deltaResult.StartConst,
			)
			encode.UseOverrideExcluded = false

			ok, _, _ := TestSong(cfg, songNum, ps.Raw, output, playerData, transformed, encoded, false)
			if ok {
				optional = append(optional, entry)
			}
		}

		if len(optional) > 0 {
			fmt.Printf("  Song %d: %d/%d exclusions optional: %v\n", songNum, len(optional), len(excluded), optional)
			hasOptional = true
		}
	}
	if !hasOptional {
		fmt.Println("  All exclusions required")
	}
	return hasOptional
}

func TestSong(cfg *Config, songNum int, rawData, convertedData, playerData []byte, transformed transform.TransformedSong, encoded encode.EncodedSong, verbose bool) (bool, int, *simulate.ASMStats) {
	testFrames := cfg.PartTimes[songNum-1]

	var bufferBase uint16
	if songNum%2 == 1 {
		bufferBase = 0x1000
	} else {
		bufferBase = 0x7000
	}
	playAddr := bufferBase + 3
	playerBase := uint16(0xF000)

	cpuBuiltin := simulate.NewCPU()
	copy(cpuBuiltin.Memory[bufferBase:], rawData)
	cpuBuiltin.A = 0
	cpuBuiltin.Call(bufferBase)

	cpuBuiltin.SIDWrites = nil
	cpuBuiltin.Cycles = 0
	var origMaxCycles uint64
	origPrevCycles := cpuBuiltin.Cycles
	for i := 0; i < testFrames; i++ {
		cpuBuiltin.CurrentFrame = i
		cpuBuiltin.Call(playAddr)
		frameCycles := cpuBuiltin.Cycles - origPrevCycles
		if frameCycles > origMaxCycles {
			origMaxCycles = frameCycles
		}
		origPrevCycles = cpuBuiltin.Cycles
	}
	builtinWrites := cpuBuiltin.SIDWrites
	origTotalCycles := cpuBuiltin.Cycles

	cpuNew := simulate.NewCPU()
	cpuNew.Coverage = make(map[uint16]bool)
	cpuNew.DataCoverage = make(map[uint16]bool)
	cpuNew.DataBase = playerBase
	cpuNew.DataSize = len(playerData)
	cpuNew.RedundantCLC = make(map[uint16]int)
	cpuNew.RedundantSEC = make(map[uint16]int)
	cpuNew.TotalCLC = make(map[uint16]int)
	cpuNew.TotalSEC = make(map[uint16]int)
	cpuNew.CheckpointAddr = playerBase + uint16(len(playerData)) - 1
	copy(cpuNew.Memory[bufferBase:], convertedData)
	copy(cpuNew.Memory[playerBase:], playerData)
	cpuNew.A = 0
	cpuNew.X = byte(bufferBase >> 8)
	simulate.DebugSpeedAddr = 0
	simulate.DebugSIDAddr = 0
	simulate.DebugReadAddr = 0
	simulate.DebugReadRange = 0
	simulate.DebugMemAddr = 0
	cpuNew.Call(playerBase)

	var maxFrameCycles uint64
	prevCycles := cpuNew.Cycles
	for i := 0; i < testFrames; i++ {
		cpuNew.CurrentFrame = i
		cpuNew.Call(playerBase + 3)
		frameCycles := cpuNew.Cycles - prevCycles
		if frameCycles > maxFrameCycles {
			maxFrameCycles = frameCycles
		}
		prevCycles = cpuNew.Cycles
	}
	newWrites := cpuNew.SIDWrites

	stats := &simulate.ASMStats{
		Coverage:       cpuNew.Coverage,
		DataCoverage:   cpuNew.DataCoverage,
		DataBase:       cpuNew.DataBase,
		DataSize:       cpuNew.DataSize,
		RedundantCLC:   cpuNew.RedundantCLC,
		RedundantSEC:   cpuNew.RedundantSEC,
		TotalCLC:       cpuNew.TotalCLC,
		TotalSEC:       cpuNew.TotalSEC,
		CheckpointGap:  cpuNew.CheckpointGap,
		CheckpointFrom: cpuNew.CheckpointGapFrom,
		CheckpointTo:   cpuNew.CheckpointGapTo,
		TotalCycles:    cpuNew.Cycles,
		MaxFrameCycles: maxFrameCycles,
		OrigCycles:     origTotalCycles,
		OrigMaxCycles:  origMaxCycles,
		OrigSize:       len(rawData),
		NewSize:        len(convertedData),
		DictSize:       len(encoded.RowDict) / 3,
		SongLength:     testFrames,
	}

	if simulate.CompareRuns(builtinWrites, newWrites) {
		return true, len(builtinWrites), stats
	}

	if verbose {
		frameMap := verify.BuildFrameMap(transformed, encoded, testFrames+1)

		for i := 0; i < len(builtinWrites) && i < len(newWrites); i++ {
			if builtinWrites[i] != newWrites[i] {
				frame := builtinWrites[i].Frame
				posInfo := verify.DescribeFrame(frameMap, frame)
				origFrame := newWrites[i].Frame
				fmt.Printf("    Mismatch at write %d: orig=$%04X=%02X (frame %d), new=$%04X=%02X (frame %d)\n",
					i,
					builtinWrites[i].Addr, builtinWrites[i].Value, builtinWrites[i].Frame,
					newWrites[i].Addr, newWrites[i].Value, newWrites[i].Frame)
				if builtinWrites[i].Frame != newWrites[i].Frame {
					fmt.Printf("    *** TIMING DRIFT: orig frame %d vs new frame %d (diff=%d)\n",
						builtinWrites[i].Frame, origFrame, int(newWrites[i].Frame)-int(builtinWrites[i].Frame))
				}
				fmt.Printf("    Position: %s\n", posInfo)

				start := i - 15
				if start < 0 {
					start = 0
				}
				fmt.Printf("    Last %d writes:\n", i-start+1)
				for j := start; j <= i && j < len(builtinWrites) && j < len(newWrites); j++ {
					match := " "
					if builtinWrites[j] != newWrites[j] {
						match = "*"
					}
					fmt.Printf("      %s[%d] f=%d: orig $%04X=%02X, new $%04X=%02X\n",
						match, j, builtinWrites[j].Frame,
						builtinWrites[j].Addr, builtinWrites[j].Value,
						newWrites[j].Addr, newWrites[j].Value)
				}

				if frame < len(frameMap) {
					pos := frameMap[frame]
					verify.DumpRowAtPosition(transformed, encoded, pos.Order, pos.Row)
				}

				cpuDebug := simulate.NewCPU()
				copy(cpuDebug.Memory[bufferBase:], convertedData)
				copy(cpuDebug.Memory[playerBase:], playerData)
				cpuDebug.A = 0
				cpuDebug.X = byte(bufferBase >> 8)
				cpuDebug.Call(playerBase)
				cpuDebug.RunUntilFrame(playerBase+3, frame)

				fmt.Printf("    Player state at frame %d:\n", frame)
				for ch := 0; ch < 3; ch++ {
					gateon := cpuDebug.Memory[0xFA2C+ch]
					waveform := cpuDebug.Memory[0xFA4A+ch]
					inst := cpuDebug.Memory[0xFA3B+ch]
					effect := cpuDebug.Memory[0xFA44+ch]
					effectpar := cpuDebug.Memory[0xFA47+ch]
					waveidx := cpuDebug.Memory[0xFA6B+ch]
					fmt.Printf("      ch%d: gateon=%02X waveform=%02X inst=%d effect=%02X param=%02X waveidx=%02X\n",
						ch, gateon, waveform, inst, effect, effectpar, waveidx)
				}
				speed := cpuDebug.Memory[0xFA94]
				speedcounter := cpuDebug.Memory[0xFA95]
				trackrow := cpuDebug.Memory[0xFA96]
				hrtrackrow := cpuDebug.Memory[0xFA97]
				fmt.Printf("    Timing: speed=%d speedcounter=%d trackrow=%d hrtrackrow=%d\n",
					speed, speedcounter, trackrow, hrtrackrow)

				fmt.Printf("    chn_hardrestart: [%d, %d, %d]\n",
					cpuDebug.Memory[0xFA29], cpuDebug.Memory[0xFA29+1], cpuDebug.Memory[0xFA29+2])

				if int(speedcounter)+int(cpuDebug.Memory[0xFA29+2]) >= int(speed) {
					fmt.Printf("    HR check would trigger for ch2!\n")
					fmt.Printf("    chn_trackptr_cur: [%d, %d, %d]\n",
						cpuDebug.Memory[0xFAA3], cpuDebug.Memory[0xFAA3+1], cpuDebug.Memory[0xFAA3+2])
					fmt.Printf("    chn_src_off: [%d, %d, %d] decode_row=%d\n",
						cpuDebug.Memory[0xFABE], cpuDebug.Memory[0xFABE+1], cpuDebug.Memory[0xFABE+2],
						cpuDebug.Memory[0xFA98])
					fmt.Printf("    chn_prev_row_0 @FACA: %02X %02X %02X\n",
						cpuDebug.Memory[0xFACA], cpuDebug.Memory[0xFACA+1], cpuDebug.Memory[0xFACA+2])
					fmt.Printf("    chn_prev_row_1 @FACD: %02X %02X %02X\n",
						cpuDebug.Memory[0xFACD], cpuDebug.Memory[0xFACD+1], cpuDebug.Memory[0xFACD+2])
					fmt.Printf("    chn_prev_row_2 @FAD0: %02X %02X %02X\n",
						cpuDebug.Memory[0xFAD0], cpuDebug.Memory[0xFAD0+1], cpuDebug.Memory[0xFAD0+2])
					for ch := 0; ch < 3; ch++ {
						bufLo := uint16(cpuDebug.Memory[0xFADC+ch])
						bufHi := uint16(cpuDebug.Memory[0xFADF+ch])
						addr := bufHi<<8 | bufLo
						pat := cpuDebug.Memory[0xFAB8+ch]
						row := cpuDebug.Memory[0xFABB+ch]
						prev0 := cpuDebug.Memory[0xFACA+ch]
						prev1 := cpuDebug.Memory[0xFACD+ch]
						prev2 := cpuDebug.Memory[0xFAD0+ch]
						fmt.Printf("    ch%d: pat=%d row=%d prev=[%02X %02X %02X] buf=[%02X %02X %02X]\n",
							ch, pat, row,
							prev0, prev1, prev2,
							cpuDebug.Memory[addr], cpuDebug.Memory[addr+1], cpuDebug.Memory[addr+2])
						if int(pat) < len(encoded.RawPatternsEquiv) && int(row) < 64 {
							p := encoded.RawPatternsEquiv[pat]
							off := int(row) * 3
							if off+2 < len(p) {
								fmt.Printf("        expected: [%02X %02X %02X]\n", p[off], p[off+1], p[off+2])
							}
						}
						if int(pat) < len(encoded.TruncateLimits) {
							fmt.Printf("        truncate=%d", encoded.TruncateLimits[pat])
							if int(pat) < len(encoded.PatternGapCodes) {
								fmt.Printf(" gap_code=%d", encoded.PatternGapCodes[pat])
							}
							fmt.Println()
						}
					}
				}
				break
			}
		}
	}
	return false, len(builtinWrites), stats
}

func bisectEquivEntries(
	cfg *Config,
	songNum int,
	ps *ProcessedSong,
	origWrites []simulate.SIDWrite,
	deltaBytes, transposeBytes []byte,
	globalWaveData []byte,
	deltaToIdx map[int]byte,
	transposeToIdx map[int8]byte,
	deltaBase, transposeBase int,
	waveRemap map[int][3]int,
	startConst int,
	testFrames int,
) []string {
	sources := encode.GetEquivSources(cfg.ProjectRoot, songNum)
	if len(sources) == 0 {
		return nil
	}

	fmt.Printf("    Bisecting %d equiv entries...\n", len(sources))

	encode.TestExclusions = sources
	passWithAllExcluded := testEquivConfig(cfg, songNum, ps, origWrites, deltaBytes, transposeBytes, globalWaveData,
		deltaToIdx, transposeToIdx, deltaBase, transposeBase, waveRemap, startConst, testFrames)
	fmt.Printf("    Test with all %d sources excluded: %v\n", len(sources), passWithAllExcluded)
	if !passWithAllExcluded {
		encode.TestExclusions = nil
		fmt.Printf("    Still fails with all equiv disabled - not an equiv issue\n")
		return nil
	}

	var badEntries []string
	fmt.Printf("    Greedy search for bad entries...\n")

	excluded := make(map[string]bool)
	for _, s := range sources {
		excluded[s] = true
	}

	for _, src := range sources {
		var testExcl []string
		for s := range excluded {
			if s != src {
				testExcl = append(testExcl, s)
			}
		}
		encode.TestExclusions = testExcl

		if testEquivConfig(cfg, songNum, ps, origWrites, deltaBytes, transposeBytes, globalWaveData,
			deltaToIdx, transposeToIdx, deltaBase, transposeBase, waveRemap, startConst, testFrames) {
			delete(excluded, src)
		} else {
			badEntries = append(badEntries, src)
			fmt.Printf("      Found bad entry: %s\n", src)
		}
	}

	encode.TestExclusions = nil
	return badEntries
}

func testEquivConfig(
	cfg *Config,
	songNum int,
	ps *ProcessedSong,
	origWrites []simulate.SIDWrite,
	deltaBytes, transposeBytes []byte,
	globalWaveData []byte,
	deltaToIdx map[int]byte,
	transposeToIdx map[int8]byte,
	deltaBase, transposeBase int,
	waveRemap map[int][3]int,
	startConst int,
	testFrames int,
) bool {
	encoded := encode.EncodeWithEquiv(ps.Transformed, songNum, cfg.ProjectRoot)
	fmt.Printf("      [test] re-encoded dict size: %d\n", len(encoded.RowDict)/3)

	output := serialize.SerializeWithWaveRemap(
		ps.Transformed,
		encoded,
		deltaToIdx,
		transposeToIdx,
		deltaBase,
		transposeBase,
		waveRemap,
		startConst,
	)

	ok, _, _ := simulate.CompareVirtual(
		origWrites,
		output,
		deltaBytes,
		transposeBytes,
		globalWaveData,
		len(encoded.PatternOffsets),
		testFrames,
		startConst,
	)
	return ok
}

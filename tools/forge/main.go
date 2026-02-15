package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"

	"forge/analysis"
	"forge/encode"
	"forge/parse"
	"forge/serialize"
	"forge/solve"
	"forge/transform"
	"forge/validate"
	"forge/verify"
)

var partTimes = []int{
	0xBB44, 0x7234, 0x57C0, 0x88D0, 0xC0A4, 0x79F6, 0x491A, 0x7BF0, 0x6D80,
}

var projectRoot string

func init() {
	projectRoot = findProjectRoot()
}

func findProjectRoot() string {
	if wd, err := os.Getwd(); err == nil {
		for d := wd; d != "/" && d != "."; d = filepath.Dir(d) {
			if _, err := os.Stat(filepath.Join(d, "src/odin_player.inc")); err == nil {
				return d
			}
		}
	}
	return "../.."
}

func projectPath(rel string) string {
	return filepath.Join(projectRoot, rel)
}

type processedSong struct {
	name        string
	raw         []byte
	song        parse.ParsedSong
	anal        analysis.SongAnalysis
	transformed transform.TransformedSong
	encoded     encode.EncodedSong
}

type ASMStats struct {
	Coverage        map[uint16]bool
	DataCoverage    map[uint16]bool
	DataBase        uint16
	DataSize        int
	RedundantCLC    map[uint16]int
	RedundantSEC    map[uint16]int
	TotalCLC        map[uint16]int
	TotalSEC        map[uint16]int
	CheckpointGap   uint64
	CheckpointFrom  uint16
	CheckpointTo    uint16
	TotalCycles     uint64
	MaxFrameCycles  uint64
	OrigCycles      uint64
	OrigMaxCycles   uint64
	OrigSize        int
	NewSize         int
	DictSize        int
	SongLength      int
}

func main() {
	// Default to batch mode
	if len(os.Args) < 2 || os.Args[1] == "batch" {
		outputDir := filepath.Join(projectRoot, "generated", "parts")
		if len(os.Args) > 2 {
			outputDir = os.Args[2]
		}
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			fmt.Printf("Error creating output directory: %v\n", err)
			os.Exit(1)
		}
		runBatch(outputDir)
		return
	}

	runSingle(os.Args[1], "")
	if len(os.Args) > 2 {
		runSingle(os.Args[1], os.Args[2])
	}
}

func runSingle(inputPath, outputPath string) {
	raw, err := os.ReadFile(inputPath)
	if err != nil {
		fmt.Printf("Error reading input: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Converting: %s (%d bytes)\n", inputPath, len(raw))

	song := parse.Parse(raw)
	if err := verify.Parse(song); err != nil {
		fmt.Printf("FATAL: parse verification failed:\n%v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Base: $%04X, Instruments: %d, Patterns: %d\n",
		song.BaseAddr, len(song.Instruments), len(song.Patterns))

	anal := analysis.Analyze(song, raw)
	if err := verify.Analysis(song, anal); err != nil {
		fmt.Printf("FATAL: analysis verification failed:\n%v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Reachable orders: %d (from %d)\n",
		len(anal.ReachableOrders), song.NumOrders)
	fmt.Printf("  Used instruments: %d, Filter trigger: %d\n",
		len(anal.UsedInstruments), len(anal.FilterTriggerInst))

	transformOpts := transform.TransformOptions{
		PermanentArp: false, // Disabled for single file mode
	}
	transformed := transform.Transform(song, anal, raw, transformOpts)
	if err := verify.Transform(song, anal, transformed, raw); err != nil {
		fmt.Printf("FATAL: transform verification failed:\n%v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Canonical patterns: %d\n", len(transformed.Patterns))
	fmt.Printf("  Max used slot: %d\n", transformed.MaxUsedSlot)

	encoded := encode.Encode(transformed)
	if err := verify.Encode(transformed, encoded); err != nil {
		fmt.Printf("FATAL: encode verification failed:\n%v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Dictionary: %d entries\n", len(encoded.RowDict)/3)
	fmt.Printf("  Primary indices: %d, Extended: %d\n",
		encoded.PrimaryCount, encoded.ExtendedCount)
	fmt.Printf("  Packed patterns: %d bytes\n", len(encoded.PackedPatterns))

	output := serialize.Serialize(transformed, encoded)
	if err := verify.Serialize(transformed, encoded, output); err != nil {
		fmt.Printf("FATAL: serialize verification failed:\n%v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Output: %d bytes\n", len(output))

	if outputPath != "" {
		if err := os.WriteFile(outputPath, output, 0644); err != nil {
			fmt.Printf("Error writing output: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Wrote: %s\n", outputPath)
	}
}

func runBatch(outputDir string) {
	songNames := []string{
		"d1p", "d2p", "d3p", "d4p", "d5p", "d6p", "d7p", "d8p", "d9p",
	}

	var songs [9]*processedSong
	var rawData [9][]byte
	var parsedSongs [9]parse.ParsedSong
	var analyses [9]analysis.SongAnalysis

	fmt.Println("=== Phase 1: Parse and analyze all songs ===")
	var allAnalyses []analysis.SongAnalysis
	for i, name := range songNames {
		inputPath := projectPath(fmt.Sprintf("uncompressed/%s.raw", name))
		raw, err := os.ReadFile(inputPath)
		if err != nil {
			fmt.Printf("  %s: skipped (not found)\n", name)
			continue
		}

		fmt.Printf("  %s: %d bytes\n", name, len(raw))

		song := parse.Parse(raw)
		if err := verify.Parse(song); err != nil {
			fmt.Printf("FATAL: %s parse verification failed:\n%v\n", name, err)
			os.Exit(1)
		}

		anal := analysis.Analyze(song, raw)
		if err := verify.Analysis(song, anal); err != nil {
			fmt.Printf("FATAL: %s analysis verification failed:\n%v\n", name, err)
			os.Exit(1)
		}

		rawData[i] = raw
		parsedSongs[i] = song
		analyses[i] = anal
		allAnalyses = append(allAnalyses, anal)
	}

	fmt.Println("\n=== Phase 2: Build global effect remap ===")
	globalEffectRemap, globalFSubRemap, permArpEffect, portaUpEffect, portaDownEffect, tonePortaEffect := transform.BuildGlobalEffectRemap(allAnalyses)
	transformOpts := transform.TransformOptions{
		PermanentArp:     false, // DISABLED - requires player changes
		PermArpEffect:    permArpEffect,
		MaxPermArpRows:   0, // unlimited
		PersistPorta:     false, // DISABLED - requires player changes
		PortaUpEffect:    portaUpEffect,
		PortaDownEffect:  portaDownEffect,
		PersistTonePorta: false, // DISABLED - requires player changes
		TonePortaEffect:  tonePortaEffect,
		OptimizeInst:     false, // DISABLED - requires player changes
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

	fmt.Println("\n=== Phase 3: Build equiv maps ===")
	equivMaps := make([]map[string]string, len(songNames))
	for i, name := range songNames {
		if rawData[i] == nil {
			continue
		}
		patterns, truncateLimits := transform.ExtractRawPatternsAsBytes(parsedSongs[i], analyses[i], rawData[i])
		equivMaps[i] = encode.BuildEquivHexMap(projectRoot, i+1, patterns, truncateLimits)
		if len(equivMaps[i]) > 0 {
			fmt.Printf("  %s: %d mappings\n", name, len(equivMaps[i]))
		}
	}

	fmt.Println("\n=== Phase 4: Apply equiv and remap ===")
	transformedSongs := make([]transform.TransformedSong, len(songNames))
	for i, name := range songNames {
		if rawData[i] == nil {
			continue
		}
		songOpts := transformOpts
		songOpts.EquivMap = equivMaps[i]
		transformedSongs[i] = transform.TransformWithGlobalEffects(
			parsedSongs[i], analyses[i], rawData[i],
			globalEffectRemap, globalFSubRemap, songOpts,
		)
		fmt.Printf("  %s: %d patterns, %d instruments\n",
			name, len(transformedSongs[i].Patterns), transformedSongs[i].MaxUsedSlot)
	}

	fmt.Println("\n=== Phase 5: Verify remapped patterns ===")
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

	fmt.Println("\n=== Phase 6: Build dictionaries and pack patterns ===")
	encodedSongs := make([]encode.EncodedSong, len(songNames))
	for i, name := range songNames {
		if rawData[i] == nil {
			continue
		}
		encodedSongs[i] = encode.Encode(transformedSongs[i])
		fmt.Printf("  %s: dict=%d\n", name, len(encodedSongs[i].RowDict)/3)
	}

	fmt.Println("\n=== Phase 7: Verify packed patterns ===")
	for i, name := range songNames {
		if rawData[i] == nil {
			continue
		}
		if err := verify.Encode(transformedSongs[i], encodedSongs[i]); err != nil {
			fmt.Printf("FATAL: %s encode verification failed:\n%v\n", name, err)
			os.Exit(1)
		}
		if err := verify.PatternSemantics(transformedSongs[i], encodedSongs[i]); err != nil {
			fmt.Printf("FATAL: %s semantic verification failed:\n%v\n", name, err)
			os.Exit(1)
		}
		if err := verify.DictionaryInstruments(transformedSongs[i], encodedSongs[i]); err != nil {
			fmt.Printf("FATAL: %s dictionary verification failed:\n%v\n", name, err)
			os.Exit(1)
		}
		if err := verify.FilterTableRemap(transformedSongs[i], encodedSongs[i]); err != nil {
			fmt.Printf("FATAL: %s filter verification failed:\n%v\n", name, err)
			os.Exit(1)
		}
		if err := verify.ArpTableRemap(transformedSongs[i], encodedSongs[i]); err != nil {
			fmt.Printf("FATAL: %s arp verification failed:\n%v\n", name, err)
			os.Exit(1)
		}
	}
	fmt.Println("  All verified")

	// Build processedSong structs
	for i, name := range songNames {
		if rawData[i] == nil {
			continue
		}
		songs[i] = &processedSong{
			name:        name,
			raw:         rawData[i],
			song:        parsedSongs[i],
			anal:        analyses[i],
			transformed: transformedSongs[i],
			encoded:     encodedSongs[i],
		}
	}

	fmt.Println("\n=== Phase 8: Collect delta and transpose sets ===")
	var baseDeltaSets [9][]int
	var trackStarts [9][3]byte
	var transposeSets [9][]int8

	for i, ps := range songs {
		if ps == nil {
			continue
		}

		numOrders := len(ps.anal.ReachableOrders)
		baseDeltaSets[i] = encode.ComputeDeltaSet(ps.encoded.TempTrackptr, numOrders)
		trackStarts[i] = ps.encoded.TrackStarts
		transposeSets[i] = encode.ComputeTransposeSet(ps.encoded.TempTranspose, numOrders)

		fmt.Printf("  %s: base_deltas=%d, transposes=%d, starts=[%d,%d,%d]\n",
			ps.name, len(baseDeltaSets[i]), len(transposeSets[i]),
			trackStarts[i][0], trackStarts[i][1], trackStarts[i][2])
	}

	fmt.Println("\n=== Phase 9: Find optimal start constant ===")
	var baseUnion [256]bool
	for s := 0; s < 9; s++ {
		for _, d := range baseDeltaSets[s] {
			baseUnion[byte(d)] = true
		}
	}

	type constScore struct {
		c, union int
	}
	scores := make([]constScore, 256)
	for c := 0; c < 256; c++ {
		var seen [256]bool
		copy(seen[:], baseUnion[:])
		for s := 0; s < 9; s++ {
			for ch := 0; ch < 3; ch++ {
				d := int(trackStarts[s][ch]) - c
				if d > 127 {
					d -= 256
				} else if d < -128 {
					d += 256
				}
				seen[byte(d)] = true
			}
		}
		count := 0
		for _, v := range seen {
			if v {
				count++
			}
		}
		scores[c] = constScore{c, count}
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i].union < scores[j].union })

	type constResult struct {
		c, size int
	}
	constResults := make(chan constResult, 10)
	for i := 0; i < 10; i++ {
		go func(c int) {
			var testSets [9][]int
			for s := 0; s < 9; s++ {
				var seen [256]bool
				for _, d := range baseDeltaSets[s] {
					seen[byte(d)] = true
				}
				for ch := 0; ch < 3; ch++ {
					d := int(trackStarts[s][ch]) - c
					if d > 127 {
						d -= 256
					} else if d < -128 {
						d += 256
					}
					seen[byte(d)] = true
				}
				set := make([]int, 0, 35)
				for d := 0; d < 256; d++ {
					if seen[d] {
						if d > 127 {
							set = append(set, d-256)
						} else {
							set = append(set, d)
						}
					}
				}
				testSets[s] = set
			}
			result := solve.SolveDeltaTable(testSets)
			constResults <- constResult{c, len(result.Table)}
		}(scores[i].c)
	}
	bestConst, bestSize := 0, 9999
	for i := 0; i < 10; i++ {
		r := <-constResults
		if r.size < bestSize || (r.size == bestSize && r.c < bestConst) {
			bestSize, bestConst = r.size, r.c
		}
	}
	fmt.Printf("  Best const %d: %d bytes\n", bestConst, bestSize)

	var deltaSets [9][]int
	for s := 0; s < 9; s++ {
		var seen [256]bool
		for _, d := range baseDeltaSets[s] {
			seen[byte(d)] = true
		}
		for ch := 0; ch < 3; ch++ {
			d := int(trackStarts[s][ch]) - bestConst
			if d > 127 {
				d -= 256
			} else if d < -128 {
				d += 256
			}
			seen[byte(d)] = true
		}
		set := make([]int, 0, 35)
		for d := 0; d < 256; d++ {
			if seen[d] {
				if d > 127 {
					set = append(set, d-256)
				} else {
					set = append(set, d)
				}
			}
		}
		deltaSets[s] = set
		sort.Ints(deltaSets[s])
	}

	unionDeltas := make(map[int]bool)
	for _, ds := range deltaSets {
		for _, d := range ds {
			unionDeltas[d] = true
		}
	}
	fmt.Printf("  Total unique deltas (union): %d\n", len(unionDeltas))

	fmt.Println("\n=== Phase 10: Solve global tables ===")
	deltaResult := solve.SolveDeltaTable(deltaSets)
	deltaResult.StartConst = bestConst
	fmt.Printf("  Delta table: %d bytes\n", len(deltaResult.Table))

	if err := verify.DeltaTable(deltaResult, deltaSets, 32); err != nil {
		fmt.Printf("FATAL: %v\n", err)
		os.Exit(1)
	}

	transposeResult := solve.SolveTransposeTable(transposeSets)
	fmt.Printf("  Transpose table: %d bytes\n", len(transposeResult.Table))

	if err := verify.TransposeTable(transposeResult, transposeSets, 16); err != nil {
		fmt.Printf("FATAL: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("  Delta bases: %v\n", deltaResult.Bases)
	fmt.Printf("  Transpose bases: %v\n", transposeResult.Bases)

	fmt.Println("\n=== Phase 11: Build lookup maps ===")
	var deltaToIdx [9]map[int]byte
	var transposeToIdx [9]map[int8]byte

	for songIdx := 0; songIdx < 9; songIdx++ {
		deltaToIdx[songIdx] = make(map[int]byte)
		base := deltaResult.Bases[songIdx]
		for i := 0; i < 32 && base+i < len(deltaResult.Table); i++ {
			v := deltaResult.Table[base+i]
			if v != solve.DeltaEmpty {
				deltaToIdx[songIdx][int(v)] = byte(i)
			}
		}

		transposeToIdx[songIdx] = make(map[int8]byte)
		tbase := transposeResult.Bases[songIdx]
		for i := 0; i < 16 && tbase+i < len(transposeResult.Table); i++ {
			v := transposeResult.Table[tbase+i]
			if _, exists := transposeToIdx[songIdx][v]; !exists {
				transposeToIdx[songIdx][v] = byte(i)
			}
		}
	}

	if err := verify.DeltaLookupMaps(deltaToIdx, deltaSets); err != nil {
		fmt.Printf("FATAL: %v\n", err)
		os.Exit(1)
	}

	if err := verify.TransposeLookupMaps(transposeToIdx, transposeSets); err != nil {
		fmt.Printf("FATAL: %v\n", err)
		os.Exit(1)
	}

	if err := verify.DeltaTableConsistency(deltaResult.Table, deltaToIdx, deltaResult.Bases); err != nil {
		fmt.Printf("FATAL: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\n=== Phase 12: Build global wave table ===")
	var waveTables [][]byte
	var waveInstruments [][]solve.WaveInstrumentInfo

	for _, ps := range songs {
		if ps == nil {
			waveTables = append(waveTables, nil)
			waveInstruments = append(waveInstruments, nil)
			continue
		}
		waveTables = append(waveTables, ps.song.WaveTable)

		var instInfo []solve.WaveInstrumentInfo
		for _, inst := range ps.transformed.Instruments {
			instInfo = append(instInfo, solve.WaveInstrumentInfo{
				Start: int(inst.WaveStart),
				End:   int(inst.WaveEnd),
				Loop:  int(inst.WaveLoop),
			})
		}
		waveInstruments = append(waveInstruments, instInfo)
	}

	globalWave := solve.BuildGlobalWaveTable(waveTables, waveInstruments)
	fmt.Printf("  Global wave table: %d bytes (%d unique snippets)\n",
		len(globalWave.Data), len(globalWave.Snippets))

	fmt.Println("\n=== Phase 13: Serialize with global tables ===")
	outputs := make([][]byte, 9)
	for i, ps := range songs {
		if ps == nil {
			continue
		}

		ps.encoded.DeltaTable = make([]byte, len(deltaResult.Table))
		for j, v := range deltaResult.Table {
			if v == solve.DeltaEmpty {
				ps.encoded.DeltaTable[j] = 0
			} else {
				ps.encoded.DeltaTable[j] = byte(v)
			}
		}
		ps.encoded.DeltaBases = deltaResult.Bases[:]
		ps.encoded.TransposeTable = make([]byte, len(transposeResult.Table))
		for j, v := range transposeResult.Table {
			ps.encoded.TransposeTable[j] = byte(v)
		}
		ps.encoded.TransposeBases = transposeResult.Bases[:]

		output := serialize.SerializeWithWaveRemap(
			ps.transformed,
			ps.encoded,
			deltaToIdx[i],
			transposeToIdx[i],
			deltaResult.Bases[i],
			transposeResult.Bases[i],
			globalWave.Remap[i],
			deltaResult.StartConst,
		)

		if err := verify.SerializeWithWaveRemap(ps.transformed, ps.encoded, output, globalWave.Remap[i]); err != nil {
			fmt.Printf("FATAL: %s serialize verification failed:\n%v\n", ps.name, err)
			os.Exit(1)
		}

		if err := verify.SerializedDictionary(ps.encoded, output); err != nil {
			fmt.Printf("FATAL: %s dictionary serialization failed:\n%v\n", ps.name, err)
			os.Exit(1)
		}

		if err := verify.PackedPatterns(ps.transformed, ps.encoded, output); err != nil {
			fmt.Printf("FATAL: %s packed patterns verification failed:\n%v\n", ps.name, err)
			os.Exit(1)
		}

		if err := verify.BitstreamRoundtrip(
			ps.encoded.TempTrackptr,
			ps.encoded.TempTranspose,
			deltaToIdx[i],
			transposeToIdx[i],
			deltaResult.Table,
			transposeResult.Table,
			deltaResult.Bases[i],
			transposeResult.Bases[i],
			deltaResult.StartConst,
		); err != nil {
			fmt.Printf("FATAL: %s bitstream roundtrip failed:\n%v\n", ps.name, err)
			os.Exit(1)
		}

		if err := verify.PlaybackStream(
			ps.transformed,
			ps.encoded,
			output,
			deltaResult.Table,
			transposeResult.Table,
			deltaResult.Bases[i],
			transposeResult.Bases[i],
			deltaResult.StartConst,
		); err != nil {
			fmt.Printf("FATAL: %s playback stream verification failed:\n%v\n", ps.name, err)
			os.Exit(1)
		}

		outputs[i] = output

		outputPath := filepath.Join(outputDir, fmt.Sprintf("part%d.bin", i+1))
		if err := os.WriteFile(outputPath, output, 0644); err != nil {
			fmt.Printf("  %s: error writing: %v\n", ps.name, err)
			continue
		}
		fmt.Printf("  %s: %d bytes -> %s\n", ps.name, len(output), outputPath)
	}

	tablesPath := projectPath("generated/tables.inc")
	writeTablesInc(deltaResult, transposeResult, globalWave, tablesPath)
	fmt.Printf("\nWrote tables: %s\n", tablesPath)

	wavetablePath := projectPath("generated/wavetable.inc")
	writeWavetableInc(globalWave, wavetablePath)
	fmt.Printf("Wrote wavetable: %s\n", wavetablePath)

	fmt.Println("\n=== Phase 14: Validate with VM ===")
	if err := rebuildPlayer(); err != nil {
		fmt.Printf("  Warning: could not rebuild player: %v\n", err)
	}

	playerData, err := os.ReadFile(projectPath("build/player.bin"))
	if err != nil {
		fmt.Printf("  Skipping validation: %v\n", err)
		return
	}

	if err := verifyTablesInPlayer(deltaResult.Table, transposeResult.Table, globalWave.Data, playerData); err != nil {
		fmt.Printf("FATAL: tables not correctly embedded in player binary:\n  %v\n", err)
		os.Exit(1)
	}
	fmt.Println("  Tables verified in player binary")

	// Phase 11: Original format VP validation (tests core playback logic)
	fmt.Println("\n=== Phase 15: Original Format VP Validation ===")
	origVPPassed, origVPFailed := 0, 0
	var origWritesCache [9][]validate.SIDWrite
	for i, ps := range songs {
		if ps == nil {
			continue
		}

		// Get original writes from GT player
		testFrames := partTimes[i]
		var bufferBase uint16
		if (i+1)%2 == 1 {
			bufferBase = 0x1000
		} else {
			bufferBase = 0x7000
		}

		cpuOrig := validate.NewCPU()
		copy(cpuOrig.Memory[bufferBase:], ps.raw)
		cpuOrig.A = 0
		cpuOrig.Call(bufferBase)

		origWrites := cpuOrig.RunFrames(bufferBase+3, testFrames)
		origWritesCache[i] = origWrites

		// Test original VP against GT player output
		debugOVP := (ps.name == "d5p")
		ok, writes, msg := validate.CompareOriginal(origWrites, &ps.song, testFrames, debugOVP)
		if ok {
			fmt.Printf("  %s: OPASS (%d writes)\n", ps.name, writes)
			origVPPassed++
		} else {
			fmt.Printf("  %s: OFAIL - %s at write %d\n", ps.name, msg, writes)
			origVPFailed++
		}
	}
	fmt.Printf("Original VP validation: %d passed, %d failed\n", origVPPassed, origVPFailed)

	// Phase 12: Virtual player validation (tests conversion, not ASM)
	fmt.Println("\n=== Phase 16: Transformed VP Validation ===")
	vpPassed, vpFailed := 0, 0
	for i, ps := range songs {
		if ps == nil || outputs[i] == nil {
			continue
		}

		testFrames := partTimes[i]
		origWrites := origWritesCache[i]
		if origWrites == nil {
			// Get original writes from GT player (fallback if not cached)
			var bufferBase uint16
			if (i+1)%2 == 1 {
				bufferBase = 0x1000
			} else {
				bufferBase = 0x7000
			}
			cpuOrig := validate.NewCPU()
			copy(cpuOrig.Memory[bufferBase:], ps.raw)
			cpuOrig.A = 0
			cpuOrig.Call(bufferBase)
			origWrites = cpuOrig.RunFrames(bufferBase+3, testFrames)
		}

		// Build delta and transpose byte tables
		deltaBytes := make([]byte, len(deltaResult.Table))
		for j, v := range deltaResult.Table {
			if v == solve.DeltaEmpty {
				deltaBytes[j] = 0
			} else {
				deltaBytes[j] = byte(v)
			}
		}
		transposeBytes := make([]byte, len(transposeResult.Table))
		for j, v := range transposeResult.Table {
			transposeBytes[j] = byte(v)
		}

		_ = globalWave
		validate.SetVPDebugSong("")
		validate.SetVPDebugFrame(0)

		ok, writes, msg := validate.CompareVirtual(
			ps.name,
			origWrites,
			outputs[i],
			deltaBytes,
			transposeBytes,
			globalWave.Data,
			ps.transformed,
			ps.encoded,
			testFrames,
		)
		if ok {
			fmt.Printf("  %s: VPASS (%d writes)\n", ps.name, writes)
			vpPassed++
		} else {
			fmt.Printf("  %s: VFAIL - %s at write %d\n", ps.name, msg, writes)
			vpFailed++
			// Try to find bad equiv entries by bisection
			badEntries := bisectEquivEntries(
				i+1, ps, origWrites,
				deltaBytes, transposeBytes, globalWave.Data,
				deltaToIdx[i], transposeToIdx[i],
				deltaResult.Bases[i], transposeResult.Bases[i],
				globalWave.Remap[i], deltaResult.StartConst,
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

	// Phase 13: Check which excluded equiv entries are actually needed
	fmt.Println("\n=== Phase 17: Verify Excluded Equiv Entries ===")
	hasOptionalExclusions := checkExcludedEntries(songs, outputs, deltaResult, transposeResult, globalWave, deltaToIdx, transposeToIdx, globalEffectRemap, globalFSubRemap, transformOpts, playerData)
	if hasOptionalExclusions {
		fmt.Println("\nFATAL: Optional exclusions found - these should be removed from equiv_cache.json")
		os.Exit(1)
	}

	// Phase 14: ASM player validation (parallel)
	fmt.Println("\n=== Phase 18: ASM Player Validation ===")

	type asmResult struct {
		ok     bool
		writes int
		stats  *ASMStats
	}
	asmResults := make([]asmResult, len(songs))
	var wg sync.WaitGroup

	for i, ps := range songs {
		if ps == nil || outputs[i] == nil {
			continue
		}
		wg.Add(1)
		go func(idx int, ps *processedSong) {
			defer wg.Done()
			ok, writes, stats := testSong(idx+1, ps.raw, outputs[idx], playerData, ps.transformed, ps.encoded)
			asmResults[idx] = asmResult{ok, writes, stats}
		}(i, ps)
	}
	wg.Wait()

	passed, failed := 0, 0
	allStats := make([]*ASMStats, len(songs))
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

	// Phase 15: Report stats
	reportASMStats(allStats, playerData)
}

func checkExcludedEntries(
	songs [9]*processedSong,
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
		excluded := encode.GetExcludedOrig(projectRoot, songNum)
		if len(excluded) == 0 {
			continue
		}

		var optional []string
		for _, entry := range excluded {
			// Test with this entry NOT excluded (i.e., active)
			newExcluded := make([]string, 0, len(excluded)-1)
			for _, e := range excluded {
				if e != entry {
					newExcluded = append(newExcluded, e)
				}
			}

			// Re-build equiv map with new exclusions
			encode.UseOverrideExcluded = true
			encode.OverrideExcluded = newExcluded

			patterns, truncateLimits := transform.ExtractRawPatternsAsBytes(
				ps.song, ps.anal, ps.raw,
			)
			equivMap := encode.BuildEquivHexMap(
				projectRoot, songNum,
				patterns, truncateLimits,
			)

			// Re-transform with new equiv map
			songOpts := transformOpts
			songOpts.EquivMap = equivMap
			transformed := transform.TransformWithGlobalEffects(ps.song, ps.anal, ps.raw, globalEffectRemap, globalFSubRemap, songOpts)

			// Re-encode
			encoded := encode.Encode(transformed)

			// Re-serialize
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

			// ASM validate
			ok, _, _ := testSong(songNum, ps.raw, output, playerData, transformed, encoded)
			if ok {
				optional = append(optional, entry)
			}
		}

		if len(optional) > 0 {
			fmt.Printf("  Song %d: %d/%d exclusions optional: %v\n", songNum, len(optional), len(excluded), optional)
			hasOptional = true
		} else {
			fmt.Printf("  Song %d: all %d exclusions required\n", songNum, len(excluded))
		}
	}
	return hasOptional
}

func getOrigWrites(rawData []byte, songNum int, testFrames int) []validate.SIDWrite {
	var bufferBase uint16
	if songNum%2 == 1 {
		bufferBase = 0x1000
	} else {
		bufferBase = 0x7000
	}
	playAddr := bufferBase + 3

	cpu := validate.NewCPU()
	copy(cpu.Memory[bufferBase:], rawData)
	cpu.A = 0
	cpu.Call(bufferBase)
	return cpu.RunFrames(playAddr, testFrames)
}

func reportASMStats(allStats []*ASMStats, playerData []byte) {
	playerBase := uint16(0xF000)

	// Merge coverage from all songs
	mergedCoverage := make(map[uint16]bool)
	mergedDataCoverage := make(map[int]bool)
	mergedRedundantCLC := make(map[uint16]int)
	mergedRedundantSEC := make(map[uint16]int)
	mergedTotalCLC := make(map[uint16]int)
	mergedTotalSEC := make(map[uint16]int)
	var worstGap uint64
	var worstGapFrom, worstGapTo uint16

	for _, stats := range allStats {
		if stats == nil {
			continue
		}
		for addr := range stats.Coverage {
			mergedCoverage[addr] = true
		}
		for addr := range stats.DataCoverage {
			mergedDataCoverage[int(addr-stats.DataBase)] = true
		}
		for addr, count := range stats.RedundantCLC {
			mergedRedundantCLC[addr] += count
		}
		for addr, count := range stats.RedundantSEC {
			mergedRedundantSEC[addr] += count
		}
		for addr, count := range stats.TotalCLC {
			mergedTotalCLC[addr] += count
		}
		for addr, count := range stats.TotalSEC {
			mergedTotalSEC[addr] += count
		}
		if stats.CheckpointGap > worstGap {
			worstGap = stats.CheckpointGap
			worstGapFrom = stats.CheckpointFrom
			worstGapTo = stats.CheckpointTo
		}
	}

	// Code coverage
	instrStarts := validate.FindInstructionStarts(playerData, playerBase)
	var uncovered []uint16
	for _, addr := range instrStarts {
		if !mergedCoverage[addr] {
			uncovered = append(uncovered, addr)
		}
	}
	fmt.Printf("\nCode coverage: %d/%d instructions executed\n", len(instrStarts)-len(uncovered), len(instrStarts))
	if len(uncovered) > 0 && len(uncovered) <= 10 {
		fmt.Printf("Uncovered instructions:")
		for _, addr := range uncovered {
			fmt.Printf(" $%04X", addr)
		}
		fmt.Println()
	}

	// Data coverage
	codeEnd := len(instrStarts)
	dataStart := 0
	if codeEnd > 0 {
		lastInstr := instrStarts[codeEnd-1]
		lastLen := 1
		if int(lastInstr-playerBase) < len(playerData) {
			opcode := playerData[lastInstr-playerBase]
			if l, ok := validate.InstrLengths()[opcode]; ok {
				lastLen = l
			}
		}
		dataStart = int(lastInstr-playerBase) + lastLen
	}
	dataEnd := len(playerData)
	dataCovered := 0
	for off := dataStart; off < dataEnd; off++ {
		if mergedDataCoverage[off] {
			dataCovered++
		}
	}
	dataTotal := dataEnd - dataStart
	if dataTotal > 0 {
		fmt.Printf("Data coverage: %d/%d bytes (%.0f%%)\n", dataCovered, dataTotal, 100*float64(dataCovered)/float64(dataTotal))
	}

	// Uncovered data bytes
	var uncoveredData []string
	for off := dataStart; off < dataEnd; off++ {
		if !mergedDataCoverage[off] {
			uncoveredData = append(uncoveredData, fmt.Sprintf("%d:$%02X", off-dataStart, playerData[off]))
		}
	}
	if len(uncoveredData) > 0 && len(uncoveredData) <= 30 {
		fmt.Printf("Uncovered data: %s\n", join(uncoveredData, ", "))
	}

	// Redundant flag operations
	type flagOp struct {
		addr      uint16
		redundant int
		total     int
		isCLC     bool
	}
	var redundantOps []flagOp
	for addr, count := range mergedRedundantCLC {
		if count == mergedTotalCLC[addr] && count > 0 {
			redundantOps = append(redundantOps, flagOp{addr, count, mergedTotalCLC[addr], true})
		}
	}
	for addr, count := range mergedRedundantSEC {
		if count == mergedTotalSEC[addr] && count > 0 {
			redundantOps = append(redundantOps, flagOp{addr, count, mergedTotalSEC[addr], false})
		}
	}
	if len(redundantOps) > 0 {
		sort.Slice(redundantOps, func(i, j int) bool { return redundantOps[i].addr < redundantOps[j].addr })
		var clcAddrs, secAddrs []string
		for _, op := range redundantOps {
			if op.isCLC {
				clcAddrs = append(clcAddrs, fmt.Sprintf("$%04X", op.addr))
			} else {
				secAddrs = append(secAddrs, fmt.Sprintf("$%04X", op.addr))
			}
		}
		fmt.Printf("\nRedundant flags: %d CLC %v, %d SEC %v\n", len(clcAddrs), clcAddrs, len(secAddrs), secAddrs)
	}

	// Checkpoint timing
	if worstGap > 0 {
		fmt.Printf("\nSlowest checkpoint: %d cycles (from $%04X to $%04X)\n", worstGap, worstGapFrom, worstGapTo)
	}
}

func join(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for _, s := range strs[1:] {
		result += sep + s
	}
	return result
}

func writeTablesInc(deltaResult solve.DeltaTableResult, transposeResult solve.TransposeTableResult, globalWave *solve.GlobalWaveTable, path string) {
	f, err := os.Create(path)
	if err != nil {
		fmt.Printf("Error creating tables.inc: %v\n", err)
		return
	}
	defer f.Close()

	fmt.Fprintf(f, "; Auto-generated lookup tables - DO NOT EDIT\n\n")

	fmt.Fprintf(f, "; Delta table: %d bytes\n", len(deltaResult.Table))
	fmt.Fprintf(f, "delta_table:\n")
	for i := 0; i < len(deltaResult.Table); i += 16 {
		fmt.Fprintf(f, "\t.byte\t")
		end := i + 16
		if end > len(deltaResult.Table) {
			end = len(deltaResult.Table)
		}
		for j := i; j < end; j++ {
			v := deltaResult.Table[j]
			if v == solve.DeltaEmpty {
				v = 0
			}
			fmt.Fprintf(f, "$%02X", byte(v))
			if j < end-1 {
				fmt.Fprintf(f, ", ")
			}
		}
		fmt.Fprintf(f, "\t; %d\n", i)
	}
	fmt.Fprintf(f, "\nTRACKPTR_START = %d\n", deltaResult.StartConst)

	fmt.Fprintf(f, "\n; Transpose table: %d bytes\n", len(transposeResult.Table))
	fmt.Fprintf(f, "transpose_table:\n")
	for i := 0; i < len(transposeResult.Table); i += 16 {
		fmt.Fprintf(f, "\t.byte\t")
		end := i + 16
		if end > len(transposeResult.Table) {
			end = len(transposeResult.Table)
		}
		for j := i; j < end; j++ {
			fmt.Fprintf(f, "$%02X", byte(transposeResult.Table[j]))
			if j < end-1 {
				fmt.Fprintf(f, ", ")
			}
		}
		fmt.Fprintf(f, "\t; %d\n", i)
	}

}

func writeWavetableInc(globalWave *solve.GlobalWaveTable, path string) {
	f, err := os.Create(path)
	if err != nil {
		fmt.Printf("Error creating wavetable.inc: %v\n", err)
		return
	}
	defer f.Close()

	fmt.Fprintf(f, "; Auto-generated wavetable - DO NOT EDIT\n\n")
	fmt.Fprintf(f, "; Global wave table: %d bytes\n", len(globalWave.Data))
	fmt.Fprintf(f, "global_wavetable:\n")
	for i := 0; i < len(globalWave.Data); i += 16 {
		fmt.Fprintf(f, "\t.byte\t")
		end := i + 16
		if end > len(globalWave.Data) {
			end = len(globalWave.Data)
		}
		for j := i; j < end; j++ {
			fmt.Fprintf(f, "$%02X", globalWave.Data[j])
			if j < end-1 {
				fmt.Fprintf(f, ", ")
			}
		}
		fmt.Fprintf(f, "\t; %d\n", i)
	}
}

func rebuildPlayer() error {
	toolsDir := projectPath("tools/odin_convert")
	buildDir := projectPath("build")

	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return fmt.Errorf("mkdir build: %w", err)
	}

	asmCmd := exec.Command("ca65", "-o", filepath.Join(buildDir, "player.o"),
		"-l", filepath.Join(buildDir, "player.lst"),
		filepath.Join(toolsDir, "player_standalone.asm"))
	asmCmd.Dir = toolsDir
	if out, err := asmCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ca65: %w\n%s", err, out)
	}

	linkCmd := exec.Command("ld65", "-C", filepath.Join(toolsDir, "player.cfg"),
		"-o", filepath.Join(buildDir, "player.bin"),
		"--dbgfile", filepath.Join(buildDir, "player.dbg"),
		filepath.Join(buildDir, "player.o"))
	linkCmd.Dir = toolsDir
	if out, err := linkCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ld65: %w\n%s", err, out)
	}

	return nil
}

func verifyTablesInPlayer(deltaTable []int8, transposeTable []int8, waveTable []byte, playerData []byte) error {
	deltaBytes := make([]byte, len(deltaTable))
	for i, v := range deltaTable {
		if v == solve.DeltaEmpty {
			deltaBytes[i] = 0
		} else {
			deltaBytes[i] = byte(v)
		}
	}

	transposeBytes := make([]byte, len(transposeTable))
	for i, v := range transposeTable {
		transposeBytes[i] = byte(v)
	}

	deltaOffset := bytes.Index(playerData, deltaBytes)
	if deltaOffset < 0 {
		return fmt.Errorf("delta_table not found in player binary (first bytes: %02X %02X %02X...)",
			deltaBytes[0], deltaBytes[1], deltaBytes[2])
	}

	transposeOffset := bytes.Index(playerData, transposeBytes)
	if transposeOffset < 0 {
		return fmt.Errorf("transpose_table not found in player binary")
	}

	if transposeOffset != deltaOffset+len(deltaBytes) {
		return fmt.Errorf("transpose_table not immediately after delta_table (delta@%d, transpose@%d, expected %d)",
			deltaOffset, transposeOffset, deltaOffset+len(deltaBytes))
	}

	waveOffset := bytes.Index(playerData, waveTable)
	if waveOffset < 0 {
		return fmt.Errorf("global_wavetable not found in player binary (len=%d, first bytes: %02X %02X %02X...)",
			len(waveTable), waveTable[0], waveTable[1], waveTable[2])
	}

	return nil
}

func testSong(songNum int, rawData, convertedData, playerData []byte, transformed transform.TransformedSong, encoded encode.EncodedSong) (bool, int, *ASMStats) {
	testFrames := partTimes[songNum-1]

	var bufferBase uint16
	if songNum%2 == 1 {
		bufferBase = 0x1000
	} else {
		bufferBase = 0x7000
	}
	playAddr := bufferBase + 3
	playerBase := uint16(0xF000)

	cpuBuiltin := validate.NewCPU()
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

	cpuNew := validate.NewCPU()
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
	validate.DebugSpeedAddr = 0
	validate.DebugSIDAddr = 0
	validate.DebugReadAddr = 0
	validate.DebugReadRange = 0
	validate.DebugMemAddr = 0
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

	stats := &ASMStats{
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

	if validate.CompareRuns(builtinWrites, newWrites) {
		return true, len(builtinWrites), stats
	}

	// Build frame map for position tracking
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

			// Show context: writes before the mismatch
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

			// Dump row details
			if frame < len(frameMap) {
				pos := frameMap[frame]
				verify.DumpRowAtPosition(transformed, encoded, pos.Order, pos.Row)
			}

			// Debug: dump player variables at the exact mismatch frame
			// Re-run VM to exactly that frame to get state
			cpuDebug := validate.NewCPU()
			copy(cpuDebug.Memory[bufferBase:], convertedData)
			copy(cpuDebug.Memory[playerBase:], playerData)
			cpuDebug.A = 0
			cpuDebug.X = byte(bufferBase >> 8)
			cpuDebug.Call(playerBase)
			cpuDebug.RunUntilFrame(playerBase+3, frame)

			// Player loaded at $F000, offsets from player.lst:
			// chn_hardrestart: $0A29, chn_gateon: $0A2C, chn_inst: $0A3B
			// chn_effect: $0A44, chn_effectpar: $0A47, chn_waveform: $0A4A, chn_waveidx: $0A6B
			// speed: $0A94, speedcounter: $0A95, trackrow: $0A96, hrtrackrow: $0A97
			// decode_row: $0A98, chn_trackptr_cur: $0AA3, chn_src_off: $0ABE
			// chn_prev_row_0: $0ACA, chn_prev_row_1: $0ACD, chn_prev_row_2: $0AD0
			// chn_decoded_pat: $0AB8, chn_decoded_row: $0ABB
			// decode_buffer_lo: $0ADC, decode_buffer_hi: $0ADF
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
			// Dump timing state
			speed := cpuDebug.Memory[0xFA94]
			speedcounter := cpuDebug.Memory[0xFA95]
			trackrow := cpuDebug.Memory[0xFA96]
			hrtrackrow := cpuDebug.Memory[0xFA97]
			fmt.Printf("    Timing: speed=%d speedcounter=%d trackrow=%d hrtrackrow=%d\n",
				speed, speedcounter, trackrow, hrtrackrow)

			// Show chn_hardrestart values
			fmt.Printf("    chn_hardrestart: [%d, %d, %d]\n",
				cpuDebug.Memory[0xFA29], cpuDebug.Memory[0xFA29+1], cpuDebug.Memory[0xFA29+2])

			// Check if HR would trigger for ch2
			if int(speedcounter)+int(cpuDebug.Memory[0xFA29+2]) >= int(speed) {
				fmt.Printf("    HR check would trigger for ch2!\n")
				// Show trackptr_cur and src_off for each channel
				fmt.Printf("    chn_trackptr_cur: [%d, %d, %d]\n",
					cpuDebug.Memory[0xFAA3], cpuDebug.Memory[0xFAA3+1], cpuDebug.Memory[0xFAA3+2])
				fmt.Printf("    chn_src_off: [%d, %d, %d] decode_row=%d\n",
					cpuDebug.Memory[0xFABE], cpuDebug.Memory[0xFABE+1], cpuDebug.Memory[0xFABE+2],
					cpuDebug.Memory[0xFA98])
				// Show all decode buffers and prev_row state
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
					// Show expected pattern data from encoder
					if int(pat) < len(encoded.RawPatternsEquiv) && int(row) < 64 {
						p := encoded.RawPatternsEquiv[pat]
						off := int(row) * 3
						if off+2 < len(p) {
							fmt.Printf("        expected: [%02X %02X %02X]\n", p[off], p[off+1], p[off+2])
						}
					}
					// Show truncation limit and gap code
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
	return false, len(builtinWrites), stats
}

// bisectEquivEntries finds bad equiv entries by testing subsets
func bisectEquivEntries(
	songNum int,
	ps *processedSong,
	origWrites []validate.SIDWrite,
	deltaBytes, transposeBytes []byte,
	globalWaveData []byte,
	deltaToIdx map[int]byte,
	transposeToIdx map[int8]byte,
	deltaBase, transposeBase int,
	waveRemap map[int][3]int,
	startConst int,
	testFrames int,
) []string {
	sources := encode.GetEquivSources(projectRoot, songNum)
	if len(sources) == 0 {
		return nil
	}

	fmt.Printf("    Bisecting %d equiv entries...\n", len(sources))

	// First test: exclude ALL sources - should pass if equiv is the problem
	encode.TestExclusions = sources
	passWithAllExcluded := testEquivConfig(songNum, ps, origWrites, deltaBytes, transposeBytes, globalWaveData,
		deltaToIdx, transposeToIdx, deltaBase, transposeBase, waveRemap, startConst, testFrames)
	fmt.Printf("    Test with all %d sources excluded: %v\n", len(sources), passWithAllExcluded)
	if !passWithAllExcluded {
		// Still fails with all equiv disabled - not an equiv problem
		encode.TestExclusions = nil
		fmt.Printf("    Still fails with all equiv disabled - not an equiv issue\n")
		return nil
	}

	// Greedy search: start with all excluded (passes), try removing each exclusion.
	// If test still passes, that entry is safe (remove from exclusion list).
	// If test fails, that entry is bad (keep excluded).
	var badEntries []string
	fmt.Printf("    Greedy search for bad entries...\n")

	excluded := make(map[string]bool)
	for _, s := range sources {
		excluded[s] = true
	}

	for _, src := range sources {
		// Try removing this entry from exclusions (making it active)
		var testExcl []string
		for s := range excluded {
			if s != src {
				testExcl = append(testExcl, s)
			}
		}
		encode.TestExclusions = testExcl

		if testEquivConfig(songNum, ps, origWrites, deltaBytes, transposeBytes, globalWaveData,
			deltaToIdx, transposeToIdx, deltaBase, transposeBase, waveRemap, startConst, testFrames) {
			// Test passes with this entry active - it's safe, remove from exclusions
			delete(excluded, src)
		} else {
			// Test fails with this entry active - it's bad, keep excluded
			badEntries = append(badEntries, src)
			fmt.Printf("      Found bad entry: %s\n", src)
		}
	}

	encode.TestExclusions = nil
	return badEntries
}

func testEquivConfig(
	songNum int,
	ps *processedSong,
	origWrites []validate.SIDWrite,
	deltaBytes, transposeBytes []byte,
	globalWaveData []byte,
	deltaToIdx map[int]byte,
	transposeToIdx map[int8]byte,
	deltaBase, transposeBase int,
	waveRemap map[int][3]int,
	startConst int,
	testFrames int,
) bool {
	// Re-encode with current TestExclusions
	encoded := encode.EncodeWithEquiv(ps.transformed, songNum, projectRoot)
	fmt.Printf("      [test] re-encoded dict size: %d\n", len(encoded.RowDict)/3)

	// Re-serialize with proper global tables
	output := serialize.SerializeWithWaveRemap(
		ps.transformed,
		encoded,
		deltaToIdx,
		transposeToIdx,
		deltaBase,
		transposeBase,
		waveRemap,
		startConst,
	)

	// Test VP
	ok, _, _ := validate.CompareVirtual(
		ps.name,
		origWrites,
		output,
		deltaBytes,
		transposeBytes,
		globalWaveData,
		ps.transformed,
		encoded,
		testFrames,
	)
	return ok
}

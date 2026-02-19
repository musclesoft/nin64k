package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"forge/serialize"
	"forge/solve"
	"forge/verify"
)

func SerializeAndWrite(cfg *Config, songs [9]*ProcessedSong, tables TablesResult) [][]byte {
	fmt.Println("\n=== Serialize with global tables ===")
	outputs := make([][]byte, 9)

	for i, ps := range songs {
		if ps == nil {
			continue
		}

		ps.Encoded.DeltaTable = make([]byte, len(tables.DeltaResult.Table))
		for j, v := range tables.DeltaResult.Table {
			if v == solve.DeltaEmpty {
				ps.Encoded.DeltaTable[j] = 0
			} else {
				ps.Encoded.DeltaTable[j] = byte(v)
			}
		}
		ps.Encoded.DeltaBases = tables.DeltaResult.Bases[:]
		ps.Encoded.TransposeTable = make([]byte, len(tables.TransposeResult.Table))
		for j, v := range tables.TransposeResult.Table {
			ps.Encoded.TransposeTable[j] = byte(v)
		}
		ps.Encoded.TransposeBases = tables.TransposeResult.Bases[:]

		output := serialize.SerializeWithWaveRemap(
			ps.Transformed,
			ps.Encoded,
			tables.DeltaToIdx[i],
			tables.TransposeToIdx[i],
			tables.DeltaResult.Bases[i],
			tables.TransposeResult.Bases[i],
			tables.GlobalWave.Remap[i],
			tables.DeltaResult.StartConst,
			ps.Anal.DuplicateOrder,
			ps.Anal.DuplicateSource,
		)

		if err := verify.SerializeWithWaveRemap(ps.Transformed, ps.Encoded, output, tables.GlobalWave.Remap[i]); err != nil {
			fmt.Printf("FATAL: %s serialize verification failed:\n%v\n", ps.Name, err)
			os.Exit(1)
		}

		if err := verify.SerializedDictionary(ps.Encoded, output); err != nil {
			fmt.Printf("FATAL: %s dictionary serialization failed:\n%v\n", ps.Name, err)
			os.Exit(1)
		}

		if err := verify.PackedPatterns(ps.Transformed, ps.Encoded, output); err != nil {
			fmt.Printf("FATAL: %s packed patterns verification failed:\n%v\n", ps.Name, err)
			os.Exit(1)
		}

		if err := verify.BitstreamRoundtrip(
			ps.Encoded.TempTrackptr,
			ps.Encoded.TempTranspose,
			tables.DeltaToIdx[i],
			tables.TransposeToIdx[i],
			tables.DeltaResult.Table,
			tables.TransposeResult.Table,
			tables.DeltaResult.Bases[i],
			tables.TransposeResult.Bases[i],
			tables.DeltaResult.StartConst,
		); err != nil {
			fmt.Printf("FATAL: %s bitstream roundtrip failed:\n%v\n", ps.Name, err)
			os.Exit(1)
		}

		if err := verify.PlaybackStream(
			ps.Transformed,
			ps.Encoded,
			output,
			tables.DeltaResult.Table,
			tables.TransposeResult.Table,
			tables.DeltaResult.Bases[i],
			tables.TransposeResult.Bases[i],
			tables.DeltaResult.StartConst,
		); err != nil {
			fmt.Printf("FATAL: %s playback stream verification failed:\n%v\n", ps.Name, err)
			os.Exit(1)
		}

		outputs[i] = output

		outputPath := filepath.Join(cfg.OutputDir, fmt.Sprintf("part%d.bin", i+1))
		if err := os.WriteFile(outputPath, output, 0644); err != nil {
			fmt.Printf("  %s: error writing: %v\n", ps.Name, err)
			continue
		}
		fmt.Printf("  %s: %d bytes, gaps %d/%d -> %s\n", ps.Name, len(output), serialize.GapStats.Used, serialize.GapStats.Available, outputPath)
	}

	tablesPath := cfg.ProjectPath("generated/tables.inc")
	if err := serialize.WriteTablesInc(tables.DeltaResult, tables.TransposeResult, tablesPath); err != nil {
		fmt.Printf("Error: %v\n", err)
	} else {
		fmt.Printf("\nWrote tables: %s\n", tablesPath)
	}

	wavetablePath := cfg.ProjectPath("generated/wavetable.inc")
	if err := serialize.WriteWavetableInc(tables.GlobalWave, wavetablePath); err != nil {
		fmt.Printf("Error: %v\n", err)
	} else {
		fmt.Printf("Wrote wavetable: %s\n", wavetablePath)
	}

	return outputs
}

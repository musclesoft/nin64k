package main

import (
	"fmt"
	"os"
	"path/filepath"

	"forge/analysis"
	"forge/encode"
	"forge/parse"
	"forge/serialize"
	"forge/transform"
)

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

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: forge <songfile> [output]")
		os.Exit(1)
	}

	inputPath := os.Args[1]
	outputPath := ""
	if len(os.Args) > 2 {
		outputPath = os.Args[2]
	}

	raw, err := os.ReadFile(inputPath)
	if err != nil {
		fmt.Printf("Error reading input: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Converting: %s (%d bytes)\n", inputPath, len(raw))

	song := parse.Parse(raw)
	fmt.Printf("  Base: $%04X, Instruments: %d, Patterns: %d\n",
		song.BaseAddr, len(song.Instruments), len(song.Patterns))

	anal := analysis.Analyze(song, raw)
	fmt.Printf("  Reachable orders: %d (from %d)\n",
		len(anal.ReachableOrders), song.NumOrders)
	fmt.Printf("  Used instruments: %d, Filter trigger: %d\n",
		len(anal.UsedInstruments), len(anal.FilterTriggerInst))

	transformed := transform.Transform(song, anal, raw)
	fmt.Printf("  Canonical patterns: %d\n", len(transformed.Patterns))
	fmt.Printf("  Max used slot: %d\n", transformed.MaxUsedSlot)

	encoded := encode.Encode(transformed)
	fmt.Printf("  Dictionary: %d entries\n", len(encoded.RowDict)/3)
	fmt.Printf("  Primary indices: %d, Extended: %d\n",
		encoded.PrimaryCount, encoded.ExtendedCount)
	fmt.Printf("  Packed patterns: %d bytes\n", len(encoded.PackedPatterns))

	output := serialize.Serialize(transformed, encoded)
	fmt.Printf("  Output: %d bytes\n", len(output))

	if outputPath != "" {
		if err := os.WriteFile(outputPath, output, 0644); err != nil {
			fmt.Printf("Error writing output: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Wrote: %s\n", outputPath)
	}
}

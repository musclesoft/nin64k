# Nine Inch Ninjas - Single PRG

Disassembly and rebuild of SounDemoN's "[Nine Inch Ninjas](https://csdb.dk/release/?id=62433)" (2000) C64 music demo. Fits all nine songs into a single PRG file with no disk loading.

## Architecture

Dual-buffer system with sequential decompression:

- **$2000-$3FFF** - Buffer A: odd songs (1, 3, 5, 7, 9)
- **$4000-$5FFF** - Buffer B: even songs (2, 4, 6, 8)
- **High memory** - Single compressed stream

Songs are stored as data-only (no embedded player). A standalone player handles all songs.

## Building

Requires [cc65](https://cc65.github.io/) (ca65/ld65) and [VICE](https://vice-emu.sourceforge.io/).

```bash
go run ./cmd/compress           # Generate compressed stream
go run ./cmd/compress -vmtest   # Run 6502 VM verification
make                            # Build PRG
make run                        # Run in VICE (set VICE_BIN first)
```

## Files

- `cmd/compress/` - Compressor with 6502 decompressor generator
- `tools/odin_convert/` - Converts original songs to data-only format
- `src/nin64selftest.asm` - Self-test build (verifies decompression)
- `src/odin_player.inc` - Standalone player (extracted from original)

## Compression

Exp-Golomb (k=2) with DP optimal parsing. Cross-buffer references allow each song to reference the previous song's data.

```
0     + expgol(d)   + expgol(len):  backref0 - dist = 3*(d+1), len += 2
10    + 8bits:                      literal
110   + expgol(d)   + expgol(len):  backref1 - dist = 3*(d+1) - 2
1110  + expgol(o)   + expgol(len):  fwdref - forward copy from same buffer
11110 + expgol(d)   + expgol(len):  backref2 - dist = 3*(d+1) - 1
11111 + expgol(o)   + expgol(len):  copyother - copy from other buffer
Terminator: 0 + 12 zeros
```

Key optimizations:
- **3x distance encoding**: `dist = 3*(d+1) + adj` exploits 3-byte alignment in song data (~1KB savings)
- **Uniform k=2**: Single Exp-Golomb k value simplifies decoder
- **DP optimal parsing**: Globally optimal vs greedy local decisions
- **Cross-buffer backref**: Can reach into previous song's buffer end

## Memory Layout

After init (stream copied to high memory):

```
$0801-$1FFF   Code, player, decompressor
$2000-$3FFF   Buffer A (8KB) - odd songs (1,3,5,7,9)
$4000-$5FFF   Buffer B (8KB) - even songs (2,4,6,8)
$6000-$D000   Compressed stream (~26KB)
```

Sequential decompression: S1 to buffer A, S2 to buffer B (referencing S1), S3 to buffer A (referencing S2), etc. The stream is consumed faster than output is written, allowing in-place decompression.

## Development

After modifying the song format:

```bash
make test                       # Rebuild converter and generate parts
go run cmd/compress/*.go        # Regenerate stream, outputs checksums
# Copy checksums to src/nin64selftest.asm
make                            # Rebuild PRG/SID
make selftest && make run-selftest  # Verify
```

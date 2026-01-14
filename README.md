# Nine Inch Ninjas - Single PRG

Disassembly and rebuild of SounDemoN's "[Nine Inch Ninjas](https://csdb.dk/release/?id=62433)" (2000) C64 music demo. The compression format is tailored to this specific data set.

## Goal

Fit all nine music parts into a single PRG file, eliminating disk loading between parts.

## Architecture

Dual-buffer system with patching:

- **$1000** - Odd songs (1, 3, 5, 7, 9)
- **$7000** - Even songs (2, 4, 6, 8)

## Compression Strategy

All songs compressed using delta encoding. Songs reference current buffer contents:

```
S1 -> $1000 (references no buffer)
S2 -> $7000 (references S1)
S3 -> $1000 (references S2, potentially S1)
S4 -> $7000 (references S3, potentially S2)
...
```

## Building

Requires [cc65](https://cc65.github.io/) (ca65/ld65) and [VICE](https://vice-emu.sourceforge.io/) (c1541/x64sc).

```bash
go build ./cmd/compress  # Build compressor
./compress               # Generate delta files
./compress -asm          # Output decompressor as ca65 assembly
./compress -vmtest       # Run 6502 VM verification tests
make                     # Build PRG and D64
make run                 # Run in VICE
make clean               # Remove build artifacts
```

Set `VICE_BIN` to your VICE bin directory (required):

```bash
export VICE_BIN=~/path/to/vice/bin
make run
```

## Files

- `cmd/compress/` - Delta compressor (V23 Exp-Golomb, DP optimal parsing)
- `src/nin64k.asm` - Main loader/player
- `src/c64.cfg` - Linker configuration
- `uncompressed/d*p.raw` - Extracted song files with player

## Delta Compression (V23)

### Encoding Scheme

```
0     + expgol(d)   + expgol(len):  backref0 - dist = 3*(d+1), len += 2
10    + 8bits:                      literal
110   + expgol(d)   + expgol(len):  backref1 - dist = 3*(d+1) - 2
1110  + expgol(o)   + expgol(len):  fwdref - forward copy from same buffer
11110 + expgol(d)   + expgol(len):  backref2 - dist = 3*(d+1) - 1
11111 + expgol(o)   + expgol(len):  copyother - copy from other buffer

Terminator: 0 + 12 zeros (13 bits total)
```

Exp-Golomb: `expgol(n) = gamma(n>>2) + 2 low bits`

### Key Optimizations

- **DP optimal parsing**: Dynamic programming finds globally optimal encoding (vs greedy)
- **Cross-song backref**: Backref can reach into previous song's buffer end
- **3x distance encoding**: `dist = 3*(d+1) + rem` saves ~1,125 bytes
- **Uniform Exp-Golomb k=2**: Simplifies decoder (single k value for all parameters)

### Results

| Data      | Size             |
| --------- | ---------------- |
| S1+S2     | 7,730 bytes      |
| S3-S9     | 17,820 bytes     |
| **Total** | **25,550 bytes** |

## 6502 Decompressor

Optimized for size (speed is irrelevant; only runs during song transitions)

```bash
go run ./cmd/compress -vmtest   # Verify against Go reference implementation
go run ./cmd/compress -asm      # Output as ca65 assembly
```

## In-Memory Sequential Decompression Plan

Goal: Fit the entire compressed stream in memory alongside decompression buffers using in-place overlap.

### Why Songs 3-9 Are Optimized for Size

The **memory high water mark** occurs after S1 and S2 are decompressed - both output buffers are occupied, leaving only the permanently free regions for the remaining compressed stream:

- S1 occupies $1000-$625C (buffer A)
- S2 occupies $7000-$C37E (buffer B)
- S3-S9 compressed must fit in: buffer A tail ($663B-$6FFF) + buffer B tail ($C37F-$FFFF) = 17,990 bytes

Since S3-S9 fits with ~170 bytes to spare, it works. S1+S2 doesn't need to fit at high water—it's consumed during S1/S2 decompression while more space is available.

### Decompression Model

In-place decompression: output can overwrite input bytes that have already been consumed. The write pointer trails the read pointer through shared memory.

```
Stream and output overlap:
┌────────────────────────────────────────┐
│ consumed ← read_ptr ... write_ptr →    │
│ (safe to   (unconsumed  (output        │
│  overwrite) stream)      region)       │
└────────────────────────────────────────┘
```

Songs are read from a **continuous compressed stream** (songs 1-9 concatenated) and decompressed one at a time, alternating between buffers:

```
Decompression sequence:
1. Decompress S1 → $1000
2. Decompress S2 → $7000
3. Decompress S3 → $1000 (overwrites S1, can reference S2)
4. Decompress S4 → $7000 (overwrites S2, can reference S3)
5. Decompress S5 → $1000 (overwrites S3, can reference S4)
... and so on through S9
```

### Constraints

1. **Read/write ordering**: Write pointer must never overtake read pointer. With ~8:1 compression ratio on deltas, each output byte consumes only ~1 bit of input—safe margin.

2. **Playroutine scratch memory**: The playroutine uses buffer offsets `$0115-$0116` and `$081E-$088C` as working memory (i.e., `$1115-$1116`/`$181E-$188C` in buffer A, `$7115-$7116`/`$781E-$788C` in buffer B). Forward references (`fwdref`, `copyother`) must not source from these regions until overwritten—they contain undefined data from a previous song's playroutine. Backward references (`backref`) are unaffected since they read from already-written output.

### Data Optimization

The compressor normalizes unused regions to `$60` (RTS opcode) before compression:

- **Title area**: `$0009-$0028` (32 bytes) - PETSCII song title, not displayed
- **Mute routine**: `$005C-$0066` (11 bytes) - the loader patches `$005C` to force early return from play init, making this dead code

Benefits:

1. **No exclusion needed for `$005C`**: The patch location already contains `$60`
2. **Better compression**: Identical regions across songs enable cross-references

### Memory Layout

```
Region          Range           Size      Purpose
─────────────────────────────────────────────────────
Odd buffer      $1000-$6FFF     24,576    S1,S3,S5,S7,S9 output
Even buffer     $7000-$CFFF     24,576    S2,S4,S6,S8 output
I/O             $D000-$DFFF      4,096    bank out for temp storage
ROM shadow      $E000-$FFFF      8,192    bank out for temp storage
Screen          $0400-$07FF      1,024    temp storage during S1→S2
─────────────────────────────────────────────────────
```

### Compressed Sizes

```
Song   Uncompressed  Compressed  Ratio   Stream offset
─────────────────────────────────────────────────────
S1        21,085       4,998      24%    $0000
S2        21,375       2,732      13%    $1386
S3        19,464       2,153      11%    $1E32
S4        22,889       2,611      11%    $269B
S5        22,075       3,150      14%    $30CE
S6        20,300       2,340      12%    $3D1C
S7        14,423       2,032      14%    $4640
S8        20,707       2,635      13%    $4E30
S9        21,620       2,899      13%    $587B
─────────────────────────────────────────────────────
Total    183,938      25,550      14%    end: $63CE
```

### Song Output Ranges

```
Song   Buffer   Size      Output range    Notes
──────────────────────────────────────────────────────
S1     A        21,085    $1000-$625C
S2     B        21,375    $7000-$C37E
S3     A        19,464    $1000-$5C07
S4     B        22,889    $7000-$C968     largest even
S5     A        22,075    $1000-$663A     largest odd
S6     B        20,300    $7000-$BF4B
S7     A        14,423    $1000-$4856
S8     B        20,707    $7000-$C0D2
S9     A        21,620    $1000-$6473
```

### Stream Placement Strategy

The compressed stream must coexist with decompression output. The solution splits the stream into two parts with different survival requirements:

**S3-S9** must survive until needed, so it goes in permanently free regions—memory that no song output ever touches:

```
Buffer A tail:  $663B-$6FFF   2,501 bytes  (largest odd song S5 ends at $663A)
Buffer B tail:  $C37F-$FFFD  15,487 bytes  (S2 ends at $C37E; S4 is larger but
                                            S3-S4 compressed is consumed before
                                            S4 output reaches $C37F)
IRQ vectors:    $FFFE-$FFFF       2 bytes  (reserved for RAM IRQ handler)
─────────────────────────────────────────
Total:                       17,988 bytes available
```

**Note**: When decompressing in all-RAM mode ($30), the KERNAL ROM is banked out and $FFFE-$FFFF become the hardware IRQ vector. The stream ends at $FFFD to avoid conflict.

**S1+S2** only needs to survive until consumed. It sits just before the S3-S9 region. During S2 decompression, output starts at $7000 and races toward S1+S2—but decompression consumes ~1 input byte per 8 output bytes, so the stream is fully read before being overwritten.

**Final layout** (stream placed as late as possible to maximize low memory):

```
STREAM_START  = $10000 - (S3_S9_SIZE - 2501) - S1_S2_SIZE
WRAP_POINT    = $10000 - (S3_S9_SIZE - 2501)

STREAM_START-WRAP_POINT-1   S1+S2 compressed   (temporary)
WRAP_POINT-$FFFF            S3-S9 compressed   (permanent)
$663B-$6FFF                 S3-S9 tail         (permanent, wraps here)
```

### Execution Plan

Decompress SN+1 while SN plays.

```
Memory: 64K (1 column = 1K)
            1   2   3   4   5   6   7   8   9   A   B   C   D   E   F  F
            048C048C048C048C048C048C048C048C048C048C048C048C048C048C048C
                                  9+             1     2  3 4 5  6 7 8 9
All zipped  ······················░░·········░░░░░░░░░▒▒▒░░▒▒░░░▒▒░░▒▒▒░
                  Unzip 1         9+                   2  3 4 5  6 7 8 9
            ▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉·░░··················▒▒▒░░▒▒░░░▒▒░░▒▒▒░
                     1            9+      Unzip 2         3 4 5  6 7 8 9
High water  █████████████████████·░░▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉░░▒▒░░░▒▒░░▒▒▒░
                  Unzip 3         9+         2              4 5  6 7 8 9
            ▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉···░░█████████████████████··▒▒░░░▒▒░░▒▒▒░
                     3            9+      Unzip 4             5  6 7 8 9
            ███████████████████···░░▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉··░░░▒▒░░▒▒▒░
                  Unzip 5         9+         4                   6 7 8 9
Min gap A   ▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉░░███████████████████████·····▒▒░░▒▒▒░
                     5            9+      Unzip 6                  7 8 9
            ██████████████████████░░▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉··········░░▒▒▒░
                  Unzip 7         9+         6                       8 9
            ▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉·······░░████████████████████············▒▒▒░
                     7            9+      Unzip 8                      9
            ███████████████·······░░▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉··············░
               Unzip 9 and 9+                8
            ▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉▉··█████████████████████···············
                     9
            ██████████████████████······································
            1   2   3   4   5   6   7   8   9   A   B   C   D   E   F  F
            048C048C048C048C048C048C048C048C048C048C048C048C048C048C048C

██ playing   ▉▉ ready   ░░ odd compressed   ▒▒ even compressed   ·· free
```

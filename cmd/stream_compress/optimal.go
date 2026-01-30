package main

import (
	"math/bits"
)

const (
	optKLen    = 3
	optKDist   = 4
	optKOffset = 2
	optWindow  = 8192
)

type optChoice struct {
	typ    byte // 0=literal, 1=backref
	dist   int
	length int
}

type OptimalCompressor struct {
	target    []byte
	n         int
	window    int
	kDist     int
	kLen      int
	cost      []float64
	choices   []optChoice
	hashTable map[int][]int
}

func NewOptimalCompressor(target []byte) *OptimalCompressor {
	return NewOptimalCompressorWithWindow(target, optWindow)
}

// Simple row-aligned compressor with 1-bit prefix
// 0 = literal row (24 bits: 3 bytes)
// 1 = backref (distRows-1 + lenRows-2, both exp-golomb)
func CompressRowAligned(target []byte) ([]byte, int) {
	n := len(target)
	if n == 0 || n%3 != 0 {
		return nil, 0
	}
	numRows := n / 3

	// Build hash table for row triples (first 2 bytes of each row)
	hashKey := func(pos int) int {
		return int(target[pos*3])<<8 | int(target[pos*3+1])
	}
	hashTable := make(map[int][]int)
	for i := 0; i < numRows; i++ {
		key := hashKey(i)
		hashTable[key] = append(hashTable[key], i)
	}

	// Cost in bits for a backref: 1 + dist + len
	// Encoding: 1-bit prefix + (distRows-1) + (lenRows-2)
	backrefCost := func(distRows, lenRows int) float64 {
		d := distRows - 1
		l := lenRows - 2
		return float64(1 + expGolombBits(d, optKDist) + expGolombBits(l, optKLen))
	}

	// DP arrays (per row)
	cost := make([]float64, numRows+1)
	type choice struct {
		distRows, lenRows int
	}
	choices := make([]choice, numRows)

	// Fill DP from end to start
	maxWindowRows := optWindow / 3
	for row := numRows - 1; row >= 0; row-- {
		// Default: literal row (1 prefix + 24 data = 25 bits)
		bestCost := 25.0 + cost[row+1]
		bestChoice := choice{}

		// Try row-aligned backrefs
		key := hashKey(row)
		if positions, ok := hashTable[key]; ok {
			minRow := row - maxWindowRows
			if minRow < 0 {
				minRow = 0
			}

			// Binary search for first valid position
			lo, hi := 0, len(positions)
			for lo < hi {
				mid := (lo + hi) / 2
				if positions[mid] < minRow {
					lo = mid + 1
				} else {
					hi = mid
				}
			}

			checked := 0
			for i := lo; i < len(positions) && checked < 128; i++ {
				srcRow := positions[i]
				if srcRow >= row {
					break
				}
				checked++

				// Count matching rows
				distRows := row - srcRow
				maxLenRows := 0
				for row+maxLenRows < numRows {
					srcIdx := srcRow + (maxLenRows % distRows)
					match := true
					for b := 0; b < 3; b++ {
						if target[srcIdx*3+b] != target[(row+maxLenRows)*3+b] {
							match = false
							break
						}
					}
					if !match {
						break
					}
					maxLenRows++
				}

				if maxLenRows >= 2 {
					// Try lengths 2, 3, 4, maxLen
					lengths := []int{2}
					if maxLenRows >= 3 {
						lengths = append(lengths, 3)
					}
					if maxLenRows >= 4 {
						lengths = append(lengths, 4)
					}
					if maxLenRows > 4 {
						lengths = append(lengths, maxLenRows)
					}
					for _, lenRows := range lengths {
						c := backrefCost(distRows, lenRows) + cost[row+lenRows]
						if c < bestCost {
							bestCost = c
							bestChoice = choice{distRows, lenRows}
						}
					}
				}
			}
		}

		// RLE check (dist 1 and 2 rows)
		for distRows := 1; distRows <= 2 && distRows <= row; distRows++ {
			maxLenRows := 0
			for row+maxLenRows < numRows {
				srcIdx := row - distRows + (maxLenRows % distRows)
				match := true
				for b := 0; b < 3; b++ {
					if target[srcIdx*3+b] != target[(row+maxLenRows)*3+b] {
						match = false
						break
					}
				}
				if !match {
					break
				}
				maxLenRows++
			}
			if maxLenRows >= 2 {
				lengths := []int{2}
				if maxLenRows >= 3 {
					lengths = append(lengths, 3)
				}
				if maxLenRows >= 4 {
					lengths = append(lengths, 4)
				}
				if maxLenRows > 4 {
					lengths = append(lengths, maxLenRows)
				}
				for _, lenRows := range lengths {
					c := backrefCost(distRows, lenRows) + cost[row+lenRows]
					if c < bestCost {
						bestCost = c
						bestChoice = choice{distRows, lenRows}
					}
				}
			}
		}

		cost[row] = bestCost
		choices[row] = bestChoice
	}

	// Encode
	var out []byte
	bitPos := 0

	writeBits := func(val, count int) {
		for i := count - 1; i >= 0; i-- {
			if bitPos%8 == 0 {
				out = append(out, 0)
			}
			if (val>>i)&1 == 1 {
				out[len(out)-1] |= 1 << (7 - bitPos%8)
			}
			bitPos++
		}
	}

	writeGamma := func(n int) {
		b := bits.Len(uint(n + 1))
		for i := 0; i < b-1; i++ {
			writeBits(0, 1)
		}
		writeBits(n+1, b)
	}

	writeExpGolomb := func(n, k int) {
		writeGamma(n >> k)
		writeBits(n&((1<<k)-1), k)
	}

	row := 0
	for row < numRows {
		ch := choices[row]
		if ch.lenRows == 0 {
			// Literal row: 0 + 24 bits
			writeBits(0, 1)
			writeBits(int(target[row*3]), 8)
			writeBits(int(target[row*3+1]), 8)
			writeBits(int(target[row*3+2]), 8)
			row++
		} else {
			// Backref: 1 + distRows-1 + lenRows-2
			writeBits(1, 1)
			writeExpGolomb(ch.distRows-1, optKDist)
			writeExpGolomb(ch.lenRows-2, optKLen)
			row += ch.lenRows
		}
	}

	// Terminator: backref with dist=0 is invalid, use as EOF marker
	// 1 + expGolomb(large) signals end
	writeBits(1, 1)
	writeExpGolomb(4095, optKDist) // large value = EOF

	totalBits := bitPos
	for bitPos%8 != 0 {
		writeBits(0, 1)
	}

	return out, totalBits
}

func expGolombBits(n, k int) int {
	return 2*bits.Len(uint((n>>k)+1)) - 1 + k
}

func NewOptimalCompressorWithWindow(target []byte, window int) *OptimalCompressor {
	return NewOptimalCompressorWithK(target, window, optKDist, optKLen)
}

func NewOptimalCompressorWithK(target []byte, window, kDist, kLen int) *OptimalCompressor {
	n := len(target)
	c := &OptimalCompressor{
		target:    target,
		n:         n,
		window:    window,
		kDist:     kDist,
		kLen:      kLen,
		cost:      make([]float64, n+1),
		choices:   make([]optChoice, n),
		hashTable: make(map[int][]int),
	}

	// Build hash table for 2-byte sequences
	for i := 0; i < n-1; i++ {
		key := c.hashKey(i)
		c.hashTable[key] = append(c.hashTable[key], i)
	}

	return c
}

func (c *OptimalCompressor) hashKey(pos int) int {
	return int(c.target[pos])<<8 | int(c.target[pos+1])
}

func (c *OptimalCompressor) expGolombBits(n, k int) int {
	return 2*bits.Len(uint((n>>k)+1)) - 1 + k
}

func (c *OptimalCompressor) backrefCost(dist, length int) float64 {
	// Simple 1-bit prefix: 1 + dist-1 + len-2
	return float64(1 + c.expGolombBits(dist-1, c.kDist) + c.expGolombBits(length-2, c.kLen))
}

func (c *OptimalCompressor) matchLength(srcPos, dstPos int) int {
	dist := dstPos - srcPos
	maxLen := 0
	for dstPos+maxLen < c.n {
		var srcByte byte
		if maxLen < dist {
			srcByte = c.target[srcPos+maxLen]
		} else {
			srcByte = c.target[srcPos+(maxLen%dist)]
		}
		if srcByte != c.target[dstPos+maxLen] {
			break
		}
		maxLen++
	}
	return maxLen
}

func (c *OptimalCompressor) Compress() ([]byte, int, CompressStats) {
	if c.n == 0 {
		return nil, 0, CompressStats{}
	}

	// DP: fill from end to start
	for pos := c.n - 1; pos >= 0; pos-- {
		// Default: literal (9 bits)
		bestCost := 9.0 + c.cost[pos+1]
		bestChoice := optChoice{typ: 0}

		// Try backrefs from hash table
		if pos < c.n-1 {
			key := c.hashKey(pos)
			if positions, ok := c.hashTable[key]; ok {
				// Binary search to find first position within window
				lo, hi := 0, len(positions)
				minPos := pos - c.window
				if minPos < 0 {
					minPos = 0
				}
				for lo < hi {
					mid := (lo + hi) / 2
					if positions[mid] < minPos {
						lo = mid + 1
					} else {
						hi = mid
					}
				}

				// Check positions within window (limit to 128 for speed)
				checked := 0
				for i := lo; i < len(positions) && checked < 128; i++ {
					srcPos := positions[i]
					if srcPos >= pos {
						break
					}
					checked++

					maxLen := c.matchLength(srcPos, pos)
					if maxLen < 2 {
						continue
					}

					dist := pos - srcPos
					// Full DP: try all lengths from 2 to maxLen
					for length := 2; length <= maxLen; length++ {
						cost := c.backrefCost(dist, length) + c.cost[pos+length]
						if cost < bestCost {
							bestCost = cost
							bestChoice = optChoice{typ: 1, dist: dist, length: length}
						}
					}
				}
			}
		}

		// RLE check (dist 1 and 2) - these are common and cheap
		for dist := 1; dist <= 2 && dist <= pos; dist++ {
			maxLen := 0
			for pos+maxLen < c.n && c.target[pos-dist+(maxLen%dist)] == c.target[pos+maxLen] {
				maxLen++
			}
			if maxLen >= 2 {
				for length := 2; length <= maxLen; length++ {
					cost := c.backrefCost(dist, length) + c.cost[pos+length]
					if cost < bestCost {
						bestCost = cost
						bestChoice = optChoice{typ: 1, dist: dist, length: length}
					}
				}
			}
		}

		c.cost[pos] = bestCost
		c.choices[pos] = bestChoice
	}

	// Encode
	out, bits, stats := c.encode()
	return out, bits, stats
}

type CompressStats struct {
	LiteralBits int
	BackrefBits int
	LiteralCnt  int
	BackrefCnt  int
	DistHist    map[int]int
	LenHist     map[int]int
}

func (c *OptimalCompressor) encode() ([]byte, int, CompressStats) {
	var out []byte
	bitPos := 0
	stats := CompressStats{
		DistHist: make(map[int]int),
		LenHist:  make(map[int]int),
	}

	writeBits := func(val, count int) {
		for i := count - 1; i >= 0; i-- {
			if bitPos%8 == 0 {
				out = append(out, 0)
			}
			if (val>>i)&1 == 1 {
				out[len(out)-1] |= 1 << (7 - bitPos%8)
			}
			bitPos++
		}
	}

	writeGamma := func(n int) {
		b := bits.Len(uint(n + 1))
		for i := 0; i < b-1; i++ {
			writeBits(0, 1)
		}
		writeBits(n+1, b)
	}

	writeExpGolomb := func(n, k int) {
		writeGamma(n >> k)
		writeBits(n&((1<<k)-1), k)
	}

	pos := 0
	for pos < c.n {
		ch := c.choices[pos]
		if ch.typ == 0 {
			// Literal: 0 + 8 bits
			startBits := bitPos
			writeBits(0, 1)
			writeBits(int(c.target[pos]), 8)
			stats.LiteralBits += bitPos - startBits
			stats.LiteralCnt++
			pos++
		} else {
			// Backref: 1 + dist-1 + len-2
			startBits := bitPos
			writeBits(1, 1)
			writeExpGolomb(ch.dist-1, c.kDist)
			writeExpGolomb(ch.length-2, c.kLen)
			stats.BackrefBits += bitPos - startBits
			stats.BackrefCnt++
			stats.DistHist[ch.dist]++
			stats.LenHist[ch.length]++
			pos += ch.length
		}
	}

	// Terminator: backref0 prefix + 12 zeros
	writeBits(0b10, 2)
	writeBits(0, 12)

	totalBits := bitPos

	// Pad to byte
	for bitPos%8 != 0 {
		writeBits(0, 1)
	}

	return out, totalBits, stats
}

// CompressOptimal is a convenience function
func CompressOptimal(data []byte) ([]byte, int, CompressStats) {
	c := NewOptimalCompressor(data)
	return c.Compress()
}

// DPWithPrefix runs DP on data with prefix available for backrefs
// Returns choices for the data portion only (not prefix)
func DPWithPrefix(data []byte, prefix []byte, window int) []optChoice {
	combined := append(prefix, data...)
	prefixLen := len(prefix)

	n := len(combined)
	c := &OptimalCompressor{
		target:    combined,
		n:         n,
		window:    window,
		kDist:     optKDist,
		kLen:      optKLen,
		cost:      make([]float64, n+1),
		choices:   make([]optChoice, n),
		hashTable: make(map[int][]int),
	}

	for i := 0; i < n-1; i++ {
		key := c.hashKey(i)
		c.hashTable[key] = append(c.hashTable[key], i)
	}

	for pos := n - 1; pos >= prefixLen; pos-- {
		bestCost := 9.0 + c.cost[pos+1]
		bestChoice := optChoice{typ: 0}

		minPos := pos - window
		if minPos < 0 {
			minPos = 0
		}

		if pos < n-1 {
			key := c.hashKey(pos)
			if positions, ok := c.hashTable[key]; ok {
				lo, hi := 0, len(positions)
				for lo < hi {
					mid := (lo + hi) / 2
					if positions[mid] < minPos {
						lo = mid + 1
					} else {
						hi = mid
					}
				}

				checked := 0
				for i := lo; i < len(positions) && checked < 128; i++ {
					srcPos := positions[i]
					if srcPos >= pos {
						break
					}
					checked++

					maxLen := c.matchLength(srcPos, pos)
					if maxLen < 2 {
						continue
					}

					dist := pos - srcPos
					lengths := []int{2}
					if maxLen >= 3 {
						lengths = append(lengths, 3)
					}
					if maxLen >= 4 {
						lengths = append(lengths, 4)
					}
					if maxLen > 4 {
						lengths = append(lengths, maxLen)
					}
					for _, length := range lengths {
						cost := c.backrefCost(dist, length) + c.cost[pos+length]
						if cost < bestCost {
							bestCost = cost
							bestChoice = optChoice{typ: 1, dist: dist, length: length}
						}
					}
				}
			}
		}

		for dist := 1; dist <= 2 && dist <= pos; dist++ {
			maxLen := 0
			for pos+maxLen < n && c.target[pos-dist+(maxLen%dist)] == c.target[pos+maxLen] {
				maxLen++
			}
			if maxLen >= 2 {
				lengths := []int{2}
				if maxLen >= 3 {
					lengths = append(lengths, 3)
				}
				if maxLen >= 4 {
					lengths = append(lengths, 4)
				}
				if maxLen > 4 {
					lengths = append(lengths, maxLen)
				}
				for _, length := range lengths {
					cost := c.backrefCost(dist, length) + c.cost[pos+length]
					if cost < bestCost {
						bestCost = cost
						bestChoice = optChoice{typ: 1, dist: dist, length: length}
					}
				}
			}
		}

		c.cost[pos] = bestCost
		c.choices[pos] = bestChoice
	}

	return c.choices[prefixLen:]
}

// EncodeChoices encodes pre-computed choices into a compressed stream
func EncodeChoices(data []byte, choices []optChoice) ([]byte, int, CompressStats) {
	var out []byte
	bitPos := 0
	stats := CompressStats{
		DistHist: make(map[int]int),
		LenHist:  make(map[int]int),
	}

	writeBits := func(val, count int) {
		for i := count - 1; i >= 0; i-- {
			if bitPos%8 == 0 {
				out = append(out, 0)
			}
			if (val>>i)&1 == 1 {
				out[len(out)-1] |= 1 << (7 - bitPos%8)
			}
			bitPos++
		}
	}

	writeGamma := func(n int) {
		b := bits.Len(uint(n + 1))
		for i := 0; i < b-1; i++ {
			writeBits(0, 1)
		}
		writeBits(n+1, b)
	}

	writeExpGolomb := func(n, k int) {
		writeGamma(n >> k)
		writeBits(n&((1<<k)-1), k)
	}

	pos := 0
	for pos < len(data) {
		ch := choices[pos]
		if ch.typ == 0 {
			startBits := bitPos
			writeBits(0, 1)
			writeBits(int(data[pos]), 8)
			stats.LiteralBits += bitPos - startBits
			stats.LiteralCnt++
			pos++
		} else {
			startBits := bitPos
			writeBits(1, 1)
			writeExpGolomb(ch.dist-1, optKDist)
			writeExpGolomb(ch.length-2, optKLen)
			stats.BackrefBits += bitPos - startBits
			stats.BackrefCnt++
			stats.DistHist[ch.dist]++
			stats.LenHist[ch.length]++
			pos += ch.length
		}
	}

	// Terminator
	writeBits(0b10, 2)
	writeBits(0, 12)

	totalBits := bitPos
	for bitPos%8 != 0 {
		writeBits(0, 1)
	}

	return out, totalBits, stats
}

// CompressWithPrefix compresses data with ability to reference prefix data
// Prefix is available for backrefs but not included in output
func CompressWithPrefix(data []byte, prefix []byte, window int) ([]byte, int, CompressStats) {
	combined := append(prefix, data...)
	prefixLen := len(prefix)

	n := len(combined)
	c := &OptimalCompressor{
		target:    combined,
		n:         n,
		window:    window,
		kDist:     optKDist,
		kLen:      optKLen,
		cost:      make([]float64, n+1),
		choices:   make([]optChoice, n),
		hashTable: make(map[int][]int),
	}

	// Build hash table for entire combined data
	for i := 0; i < n-1; i++ {
		key := c.hashKey(i)
		c.hashTable[key] = append(c.hashTable[key], i)
	}

	// DP from end to prefix boundary (only compress the non-prefix part)
	for pos := n - 1; pos >= prefixLen; pos-- {
		bestCost := 9.0 + c.cost[pos+1]
		bestChoice := optChoice{typ: 0}

		// Within-channel window
		minPos := pos - window
		if minPos < 0 {
			minPos = 0
		}

		// Try backrefs from hash table
		if pos < n-1 {
			key := c.hashKey(pos)
			if positions, ok := c.hashTable[key]; ok {
				lo, hi := 0, len(positions)
				for lo < hi {
					mid := (lo + hi) / 2
					if positions[mid] < minPos {
						lo = mid + 1
					} else {
						hi = mid
					}
				}

				checked := 0
				for i := lo; i < len(positions) && checked < 128; i++ {
					srcPos := positions[i]
					if srcPos >= pos {
						break
					}
					checked++

					maxLen := c.matchLength(srcPos, pos)
					if maxLen < 2 {
						continue
					}

					dist := pos - srcPos
					lengths := []int{2}
					if maxLen >= 3 {
						lengths = append(lengths, 3)
					}
					if maxLen >= 4 {
						lengths = append(lengths, 4)
					}
					if maxLen > 4 {
						lengths = append(lengths, maxLen)
					}
					for _, length := range lengths {
						cost := c.backrefCost(dist, length) + c.cost[pos+length]
						if cost < bestCost {
							bestCost = cost
							bestChoice = optChoice{typ: 1, dist: dist, length: length}
						}
					}
				}
			}
		}

		// RLE check
		for dist := 1; dist <= 2 && dist <= pos; dist++ {
			maxLen := 0
			for pos+maxLen < n && c.target[pos-dist+(maxLen%dist)] == c.target[pos+maxLen] {
				maxLen++
			}
			if maxLen >= 2 {
				lengths := []int{2}
				if maxLen >= 3 {
					lengths = append(lengths, 3)
				}
				if maxLen >= 4 {
					lengths = append(lengths, 4)
				}
				if maxLen > 4 {
					lengths = append(lengths, maxLen)
				}
				for _, length := range lengths {
					cost := c.backrefCost(dist, length) + c.cost[pos+length]
					if cost < bestCost {
						bestCost = cost
						bestChoice = optChoice{typ: 1, dist: dist, length: length}
					}
				}
			}
		}

		c.cost[pos] = bestCost
		c.choices[pos] = bestChoice
	}

	// Encode only the non-prefix part
	return c.encodeFrom(prefixLen)
}

func (c *OptimalCompressor) encodeFrom(startPos int) ([]byte, int, CompressStats) {
	var out []byte
	bitPos := 0
	stats := CompressStats{
		DistHist: make(map[int]int),
		LenHist:  make(map[int]int),
	}

	writeBits := func(val, count int) {
		for i := count - 1; i >= 0; i-- {
			if bitPos%8 == 0 {
				out = append(out, 0)
			}
			if (val>>i)&1 == 1 {
				out[len(out)-1] |= 1 << (7 - bitPos%8)
			}
			bitPos++
		}
	}

	writeGamma := func(n int) {
		b := bits.Len(uint(n + 1))
		for i := 0; i < b-1; i++ {
			writeBits(0, 1)
		}
		writeBits(n+1, b)
	}

	writeExpGolomb := func(n, k int) {
		writeGamma(n >> k)
		writeBits(n&((1<<k)-1), k)
	}

	pos := startPos
	for pos < c.n {
		ch := c.choices[pos]
		if ch.typ == 0 {
			startBits := bitPos
			writeBits(0, 1)
			writeBits(int(c.target[pos]), 8)
			stats.LiteralBits += bitPos - startBits
			stats.LiteralCnt++
			pos++
		} else {
			startBits := bitPos
			writeBits(1, 1)
			writeExpGolomb(ch.dist-1, c.kDist)
			writeExpGolomb(ch.length-2, c.kLen)
			stats.BackrefBits += bitPos - startBits
			stats.BackrefCnt++
			stats.DistHist[ch.dist]++
			stats.LenHist[ch.length]++
			pos += ch.length
		}
	}

	// Terminator
	writeBits(0b10, 2)
	writeBits(0, 12)

	totalBits := bitPos
	for bitPos%8 != 0 {
		writeBits(0, 1)
	}

	return out, totalBits, stats
}

// CrossChannelCompressor compresses with cross-channel references
// Encoding: 0+byte=literal, 10+dist+len=same-ch, 11+chDelta+rowDist+lenRows=cross-ch
type CrossChannelCompressor struct {
	streams    [3][]byte
	numRows    int
	costs      [3][]float64
	choices    [3][]crossChoice
	windowRows int
	hashTables [3]map[int][]int // hash tables per channel
}

type crossChoice struct {
	typ      byte // 0=literal row, 1=same-ch backref, 2=cross-ch backref
	dist     int  // rows for both
	length   int  // rows for both
	srcCh    int  // source channel for cross-ch
}

func NewCrossChannelCompressor(streams [3][]byte, windowRows int) *CrossChannelCompressor {
	numRows := len(streams[0]) / 3
	c := &CrossChannelCompressor{
		streams:    streams,
		numRows:    numRows,
		windowRows: windowRows,
	}
	for ch := 0; ch < 3; ch++ {
		c.costs[ch] = make([]float64, numRows+1)
		c.choices[ch] = make([]crossChoice, numRows)
		// Build hash table: first 2 bytes of row -> row indices
		c.hashTables[ch] = make(map[int][]int)
		for row := 0; row < numRows; row++ {
			off := row * 3
			key := int(streams[ch][off])<<8 | int(streams[ch][off+1])
			c.hashTables[ch][key] = append(c.hashTables[ch][key], row)
		}
	}
	return c
}

func (c *CrossChannelCompressor) rowsMatch(ch1, row1, ch2, row2 int) bool {
	off1, off2 := row1*3, row2*3
	return c.streams[ch1][off1] == c.streams[ch2][off2] &&
		c.streams[ch1][off1+1] == c.streams[ch2][off2+1] &&
		c.streams[ch1][off1+2] == c.streams[ch2][off2+2]
}

func (c *CrossChannelCompressor) hashKey(ch, row int) int {
	off := row * 3
	return int(c.streams[ch][off])<<8 | int(c.streams[ch][off+1])
}

func (c *CrossChannelCompressor) Compress() ([3][]byte, [3]CompressStats) {
	// DP for each channel
	for ch := 0; ch < 3; ch++ {
		for row := c.numRows - 1; row >= 0; row-- {
			// Default: literal row (25 bits: 1 + 24)
			bestCost := 25.0 + c.costs[ch][row+1]
			bestChoice := crossChoice{typ: 0}

			key := c.hashKey(ch, row)
			minRow := row - c.windowRows
			if minRow < 0 {
				minRow = 0
			}

			// Same-channel row-aligned backrefs
			if positions, ok := c.hashTables[ch][key]; ok {
				checked := 0
				for _, srcRow := range positions {
					if srcRow >= row || srcRow < minRow {
						continue
					}
					if checked++; checked > 64 {
						break
					}
					if !c.rowsMatch(ch, srcRow, ch, row) {
						continue
					}
					distRows := row - srcRow
					maxLen := 1
					for row+maxLen < c.numRows && c.rowsMatch(ch, srcRow+(maxLen%distRows), ch, row+maxLen) {
						maxLen++
					}
					if maxLen >= 2 {
						for _, lenRows := range []int{2, 3, 4, maxLen} {
							if lenRows > maxLen {
								continue
							}
							cost := 2.0 + float64(expGolombBits(distRows-1, optKDist)+expGolombBits(lenRows-2, optKLen)) + c.costs[ch][row+lenRows]
							if cost < bestCost {
								bestCost = cost
								bestChoice = crossChoice{typ: 1, dist: distRows, length: lenRows}
							}
						}
					}
				}
			}

			// Cross-channel refs (only for ch1 and ch2)
			for srcCh := 0; srcCh < ch; srcCh++ {
				if positions, ok := c.hashTables[srcCh][key]; ok {
					checked := 0
					for _, srcRow := range positions {
						if srcRow > row || srcRow < minRow {
							continue
						}
						if checked++; checked > 64 {
							break
						}
						if !c.rowsMatch(srcCh, srcRow, ch, row) {
							continue
						}
						rowDist := row - srcRow
						maxLen := 1
						for row+maxLen < c.numRows && srcRow+maxLen < c.numRows && c.rowsMatch(srcCh, srcRow+maxLen, ch, row+maxLen) {
							maxLen++
						}
						if maxLen >= 2 {
							chDelta := ch - srcCh - 1
							for _, lenRows := range []int{2, 3, 4, maxLen} {
								if lenRows > maxLen {
									continue
								}
								cost := 3.0 + float64(expGolombBits(chDelta, 0)+expGolombBits(rowDist, optKDist)+expGolombBits(lenRows-2, optKLen)) + c.costs[ch][row+lenRows]
								if cost < bestCost {
									bestCost = cost
									bestChoice = crossChoice{typ: 2, dist: rowDist, length: lenRows, srcCh: srcCh}
								}
							}
						}
					}
				}
			}

			c.costs[ch][row] = bestCost
			c.choices[ch][row] = bestChoice
		}
	}

	// Encode each channel
	var results [3][]byte
	var stats [3]CompressStats
	for ch := 0; ch < 3; ch++ {
		results[ch], stats[ch] = c.encodeChannel(ch)
	}
	return results, stats
}

func (c *CrossChannelCompressor) encodeChannel(ch int) ([]byte, CompressStats) {
	var out []byte
	bitPos := 0
	stats := CompressStats{
		DistHist: make(map[int]int),
		LenHist:  make(map[int]int),
	}

	writeBits := func(val, count int) {
		for i := count - 1; i >= 0; i-- {
			if bitPos%8 == 0 {
				out = append(out, 0)
			}
			if (val>>i)&1 == 1 {
				out[len(out)-1] |= 1 << (7 - bitPos%8)
			}
			bitPos++
		}
	}

	writeGamma := func(n int) {
		b := bits.Len(uint(n + 1))
		for i := 0; i < b-1; i++ {
			writeBits(0, 1)
		}
		writeBits(n+1, b)
	}

	writeExpGolomb := func(n, k int) {
		writeGamma(n >> k)
		writeBits(n&((1<<k)-1), k)
	}

	row := 0
	for row < c.numRows {
		choice := c.choices[ch][row]
		switch choice.typ {
		case 0: // Literal row
			writeBits(0, 1)
			off := row * 3
			writeBits(int(c.streams[ch][off]), 8)
			writeBits(int(c.streams[ch][off+1]), 8)
			writeBits(int(c.streams[ch][off+2]), 8)
			stats.LiteralBits += 25
			stats.LiteralCnt++
			row++
		case 1: // Same-channel backref
			writeBits(0b10, 2)
			writeExpGolomb(choice.dist-1, optKDist)
			writeExpGolomb(choice.length-2, optKLen)
			stats.BackrefBits += 2 + expGolombBits(choice.dist-1, optKDist) + expGolombBits(choice.length-2, optKLen)
			stats.BackrefCnt++
			stats.DistHist[choice.dist]++
			stats.LenHist[choice.length]++
			row += choice.length
		case 2: // Cross-channel backref
			writeBits(0b11, 2)
			chDelta := ch - choice.srcCh - 1
			writeExpGolomb(chDelta, 0)
			writeExpGolomb(choice.dist, optKDist)
			writeExpGolomb(choice.length-2, optKLen)
			stats.BackrefBits += 2 + expGolombBits(chDelta, 0) + expGolombBits(choice.dist, optKDist) + expGolombBits(choice.length-2, optKLen)
			stats.BackrefCnt++
			stats.DistHist[choice.dist+10000]++ // Mark as cross-channel
			stats.LenHist[choice.length]++
			row += choice.length
		}
	}

	// Terminator
	writeBits(0b10, 2)
	writeExpGolomb(4095, optKDist)

	for bitPos%8 != 0 {
		writeBits(0, 1)
	}

	return out, stats
}

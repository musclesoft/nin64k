package simulate

import (
	"testing"
)

// TestDecoder tests the minimal player decoder against known patterns
func TestDecoder(t *testing.T) {
	// Dictionary is split by component:
	// dict[0] = all notes, dict[1] = all inst|effect, dict[2] = all params
	// Entry 1 is at index 0, entry 2 at index 1, etc.

	// Test gap=0 (no gaps, all 64 rows encoded)
	t.Run("gap0", func(t *testing.T) {
		// Pattern: $10 (dict[1]) $EF (RLE 1) $11 (dict[2])
		// Dict entry 1: note=$10, inst=$20, param=$30
		// Dict entry 2: note=$11, inst=$21, param=$31
		dictNotes := []byte{0x10, 0x11}  // entry 1, entry 2
		dictInsts := []byte{0x20, 0x21}
		dictParams := []byte{0x30, 0x31}

		patData := []byte{0x10, 0xEF, 0x11} // dict[1], RLE 1, dict[2]

		p := &MinimalPlayer{
			dict:        [3][]byte{dictNotes, dictInsts, dictParams},
			patternPtr:  []uint16{0},
			patternGap:  []byte{0}, // gap=0
			fullData: patData,
		}

		p.initDecoder(0, 0)

		// Row 0: dict[1] = {$10, $20, $30}
		row := p.consumeRow(0)
		if row != [3]byte{0x10, 0x20, 0x30} {
			t.Errorf("row 0: got %v, want [10 20 30]", row)
		}

		// Row 1: RLE (still dict[1])
		row = p.consumeRow(0)
		if row != [3]byte{0x10, 0x20, 0x30} {
			t.Errorf("row 1 (RLE): got %v, want [10 20 30]", row)
		}

		// Row 2: dict[2] = {$11, $21, $31}
		row = p.consumeRow(0)
		if row != [3]byte{0x11, 0x21, 0x31} {
			t.Errorf("row 2: got %v, want [11 21 31]", row)
		}
	})

	// Test gap=1 (every other row is zeros)
	t.Run("gap1", func(t *testing.T) {
		// Pattern: $10 (dict[1]) $11 (dict[2])
		// With gap=1: row0=dict[1], row1=zeros, row2=dict[2], row3=zeros
		dictNotes := []byte{0x10, 0x11}
		dictInsts := []byte{0x20, 0x21}
		dictParams := []byte{0x30, 0x31}

		patData := []byte{0x10, 0x11}

		p := &MinimalPlayer{
			dict:        [3][]byte{dictNotes, dictInsts, dictParams},
			patternPtr:  []uint16{0},
			patternGap:  []byte{1}, // gap=1
			fullData: patData,
		}

		p.initDecoder(0, 0)

		// Row 0: dict[1]
		row := p.consumeRow(0)
		if row != [3]byte{0x10, 0x20, 0x30} {
			t.Errorf("row 0: got %v, want [10 20 30]", row)
		}

		// Row 1: gap zeros
		row = p.consumeRow(0)
		if row != [3]byte{0, 0, 0} {
			t.Errorf("row 1 (gap): got %v, want [0 0 0]", row)
		}

		// Row 2: dict[2]
		row = p.consumeRow(0)
		if row != [3]byte{0x11, 0x21, 0x31} {
			t.Errorf("row 2: got %v, want [11 21 31]", row)
		}

		// Row 3: gap zeros
		row = p.consumeRow(0)
		if row != [3]byte{0, 0, 0} {
			t.Errorf("row 3 (gap): got %v, want [0 0 0]", row)
		}
	})

	// Test gap=1 with RLE
	t.Run("gap1_rle", func(t *testing.T) {
		// Pattern: $10 (dict[1]) $F0 (RLE 2)
		// With gap=1: row0=dict[1], row1=zeros, row2=dict[1] (RLE), row3=zeros,
		//             row4=dict[1] (RLE), row5=zeros
		dictNotes := []byte{0x10}
		dictInsts := []byte{0x20}
		dictParams := []byte{0x30}

		patData := []byte{0x10, 0xF0} // dict[1], RLE 2 (repeat twice more)

		p := &MinimalPlayer{
			dict:        [3][]byte{dictNotes, dictInsts, dictParams},
			patternPtr:  []uint16{0},
			patternGap:  []byte{1}, // gap=1
			fullData: patData,
		}

		p.initDecoder(0, 0)

		rows := make([][3]byte, 6)
		for i := 0; i < 6; i++ {
			rows[i] = p.consumeRow(0)
		}

		expected := [][3]byte{
			{0x10, 0x20, 0x30}, // row 0: dict[1]
			{0, 0, 0},          // row 1: gap
			{0x10, 0x20, 0x30}, // row 2: RLE
			{0, 0, 0},          // row 3: gap
			{0x10, 0x20, 0x30}, // row 4: RLE
			{0, 0, 0},          // row 5: gap
		}

		for i, exp := range expected {
			if rows[i] != exp {
				t.Errorf("row %d: got %v, want %v", i, rows[i], exp)
			}
		}
	})

	// Test zeros with RLE ($00-$0F)
	t.Run("zeros_rle", func(t *testing.T) {
		// Pattern: $02 (zeros with RLE 2 more)
		// Should decode: zeros, zeros, zeros (3 total)
		patData := []byte{0x02}

		p := &MinimalPlayer{
			dict:        [3][]byte{{}, {}, {}},
			patternPtr:  []uint16{0},
			patternGap:  []byte{0}, // gap=0
			fullData: patData,
		}

		p.initDecoder(0, 0)

		for i := 0; i < 3; i++ {
			row := p.consumeRow(0)
			if row != [3]byte{0, 0, 0} {
				t.Errorf("row %d: got %v, want [0 0 0]", i, row)
			}
		}
	})

	// Test note-only ($FE)
	t.Run("note_only", func(t *testing.T) {
		// Pattern: $10 (dict[1]) $FE $25 (note-only with note=$25)
		// Should decode: dict[1], then {$25, $20, $30} (note changed, rest same)
		dictNotes := []byte{0x10}
		dictInsts := []byte{0x20}
		dictParams := []byte{0x30}

		patData := []byte{0x10, 0xFE, 0x25}

		p := &MinimalPlayer{
			dict:        [3][]byte{dictNotes, dictInsts, dictParams},
			patternPtr:  []uint16{0},
			patternGap:  []byte{0},
			fullData: patData,
		}

		p.initDecoder(0, 0)

		// Row 0: dict[1]
		row := p.consumeRow(0)
		if row != [3]byte{0x10, 0x20, 0x30} {
			t.Errorf("row 0: got %v, want [10 20 30]", row)
		}

		// Row 1: note-only
		row = p.consumeRow(0)
		if row != [3]byte{0x25, 0x20, 0x30} {
			t.Errorf("row 1 (note-only): got %v, want [25 20 30]", row)
		}
	})
}


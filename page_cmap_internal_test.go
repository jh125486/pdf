// Whitebox: this file exercises hasOperands, readCmap's begincodespacerange/
// beginbfchar/beginbfrange operand-count and shape guards, and cmap.Decode's
// bfrange scaling/indexing and codespace bound check, all in page.go. cmap,
// bfrange, bfchar, byteRange, hasOperands and readCmap are unexported, so
// only package pdf itself can reach them.
//
// This is a mutation-coverage workstream (WS9) kept in its own file so it can
// be reviewed and merged independently of other test work in this package.

package pdf

import "testing"

// wsCmapStack returns a *Stack holding n zero Values, for exercising
// hasOperands directly without going through Interpret.
func wsCmapStack(n int) *Stack {
	var stk Stack
	for range n {
		stk.Push(Value{})
	}
	return &stk
}

// TestWSCmapHasOperands covers hasOperands directly: n declared entries of
// per operands each must fit exactly within the stack's depth.
func TestWSCmapHasOperands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		stackLen int
		per      int
		n        int
		want     bool
	}{
		{"exact fit, per=2", 4, 2, 2, true},
		{"one over, per=2", 4, 2, 3, false},
		{"exact fit, per=3", 9, 3, 3, true},
		{"one over, per=3", 9, 3, 4, false},
		{"zero declared, empty stack", 0, 2, 0, true},
		{"zero declared, nonempty stack", 4, 2, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stk := wsCmapStack(tt.stackLen)
			if got := hasOperands(stk, tt.n, tt.per); got != tt.want {
				t.Errorf("hasOperands(n=%d, per=%d) with %d-value stack = %v, want %v",
					tt.n, tt.per, tt.stackLen, got, tt.want)
			}
		})
	}
}

// TestWSReadCmapCodespaceOperandCount covers begincodespacerange/
// endcodespacerange's operand-count guard: a declared count exactly matching
// the supplied <lo> <hi> pairs is accepted and every range is registered and
// usable; one more than supplied rejects the whole cmap.
func TestWSReadCmapCodespaceOperandCount(t *testing.T) {
	t.Parallel()

	t.Run("exact count accepted, both ranges usable", func(t *testing.T) {
		t.Parallel()

		const cm = "2 begincodespacerange <00> <7f> <80> <ff> endcodespacerange " +
			"2 beginbfchar <10> <0041> <90> <0042> endbfchar"

		m := readCmap(rawStream(cm))
		if m == nil {
			t.Fatal("readCmap returned nil for a well-formed codespace")
		}
		if len(m.space[0]) != 2 {
			t.Errorf("space[0] = %d entries, want 2", len(m.space[0]))
		}
		if got := m.Decode("\x10"); got != "A" {
			t.Errorf(`Decode(0x10) = %q, want "A"`, got)
		}
		if got := m.Decode("\x90"); got != "B" {
			t.Errorf(`Decode(0x90) = %q, want "B"`, got)
		}
	})

	t.Run("count one over rejects whole cmap", func(t *testing.T) {
		t.Parallel()

		const cm = "3 begincodespacerange <00> <7f> <80> <ff> endcodespacerange"

		if m := readCmap(rawStream(cm)); m != nil {
			t.Errorf("readCmap = %+v, want nil", m)
		}
	})
}

// TestWSReadCmapBfcharOperandCount mirrors the codespace case for
// beginbfchar/endbfchar, whose per-entry width is 2 (orig, repl).
func TestWSReadCmapBfcharOperandCount(t *testing.T) {
	t.Parallel()

	t.Run("exact count accepted", func(t *testing.T) {
		t.Parallel()

		const cm = "1 begincodespacerange <00> <ff> endcodespacerange " +
			"1 beginbfchar <41> <0058> endbfchar"

		m := readCmap(rawStream(cm))
		if m == nil {
			t.Fatal("readCmap returned nil for a well-formed bfchar")
		}
		if got := m.Decode("A"); got != "X" {
			t.Errorf(`Decode("A") = %q, want "X"`, got)
		}
	})

	t.Run("count one over rejects whole cmap", func(t *testing.T) {
		t.Parallel()

		const cm = "1 begincodespacerange <00> <ff> endcodespacerange " +
			"2 beginbfchar <41> <0058> endbfchar"

		if m := readCmap(rawStream(cm)); m != nil {
			t.Errorf("readCmap = %+v, want nil", m)
		}
	})
}

// TestWSReadCmapBfrangeOperandCount mirrors the codespace case for
// beginbfrange/endbfrange, whose per-entry width is 3 (srcLo, srcHi, dst).
func TestWSReadCmapBfrangeOperandCount(t *testing.T) {
	t.Parallel()

	t.Run("exact count accepted", func(t *testing.T) {
		t.Parallel()

		const cm = "1 begincodespacerange <00> <ff> endcodespacerange " +
			"1 beginbfrange <41> <42> <0058> endbfrange"

		m := readCmap(rawStream(cm))
		if m == nil {
			t.Fatal("readCmap returned nil for a well-formed bfrange")
		}
		if len(m.bfrange) != 1 {
			t.Errorf("bfrange = %d entries, want 1", len(m.bfrange))
		}
	})

	t.Run("count one over rejects whole cmap", func(t *testing.T) {
		t.Parallel()

		const cm = "1 begincodespacerange <00> <ff> endcodespacerange " +
			"2 beginbfrange <41> <42> <0058> endbfrange"

		if m := readCmap(rawStream(cm)); m != nil {
			t.Errorf("readCmap = %+v, want nil", m)
		}
	})
}

// TestWSReadCmapNegativeCount covers the n < 0 guard on begincodespacerange
// and beginbfrange declared counts. (beginbfchar's negative case is already
// covered by TestMalformedCmap.)
func TestWSReadCmapNegativeCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cm   string
	}{
		{"codespace negative count", "-3 begincodespacerange <00> <ff> endcodespacerange"},
		{
			"bfrange negative count",
			"1 begincodespacerange <00> <ff> endcodespacerange -2 beginbfrange <41> <42> <0058> endbfrange",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if m := readCmap(rawStream(tt.cm)); m != nil {
				t.Errorf("readCmap = %+v, want nil", m)
			}
		})
	}
}

// TestWSReadCmapCodespaceMaxWidth covers the codespace-width guard's boundary:
// m.space is indexed [4]byteRange, so a 4-byte-wide lo/hi is the maximum
// accepted width.
func TestWSReadCmapCodespaceMaxWidth(t *testing.T) {
	t.Parallel()

	const cm = "1 begincodespacerange <01020304> <01020305> endcodespacerange " +
		"1 beginbfchar <01020304> <0058> endbfchar"

	m := readCmap(rawStream(cm))
	if m == nil {
		t.Fatal("readCmap returned nil for a 4-byte-wide codespace range")
	}
	if len(m.space[3]) != 1 {
		t.Errorf("space[3] = %d entries, want 1", len(m.space[3]))
	}
	if got := m.Decode("\x01\x02\x03\x04"); got != "X" {
		t.Errorf(`Decode(0x01020304) = %q, want "X"`, got)
	}
}

// TestWSCmapDecodeBfrangeStringScaling covers cmap.Decode's bfrange
// String-destination path: the code at the start of the range uses the
// destination verbatim, and a code partway through the range scales the
// destination's last byte by the same offset.
func TestWSCmapDecodeBfrangeStringScaling(t *testing.T) {
	t.Parallel()

	// <41>-<45> is A..E; the destination <0058> is "X" (0x58). C is A+2, so
	// its decode scales the last byte to 0x5A ('Z'); E is A+4, scaling to
	// 0x5C ('\').
	const cm = "1 begincodespacerange <00> <ff> endcodespacerange " +
		"1 beginbfrange <41> <45> <0058> endbfrange"

	m := readCmap(rawStream(cm))
	if m == nil {
		t.Fatal("readCmap returned nil for a well-formed bfrange")
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"range start, no scaling", "A", "X"},
		{"partway through range, scaled", "C", "Z"},
		{"range end, scaled", "E", "\\"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := m.Decode(tt.in); got != tt.want {
				t.Errorf("Decode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestWSCmapDecodeBfrangeEmptyStringOffset covers cmap.Decode's guard against
// scaling an empty String destination: decoding a code partway through the
// range must not panic and must produce no runes for that code (utf16Decode
// of an empty string is empty).
func TestWSCmapDecodeBfrangeEmptyStringOffset(t *testing.T) {
	t.Parallel()

	const cm = "1 begincodespacerange <00> <ff> endcodespacerange " +
		"1 beginbfrange <41> <45> () endbfrange"

	m := readCmap(rawStream(cm))
	if m == nil {
		t.Fatal("readCmap returned nil for a well-formed bfrange")
	}

	mustNotCrash(t, func() {
		if got := m.Decode("C"); got != "" {
			t.Errorf(`Decode("C") = %q, want ""`, got)
		}
	})
}

// TestWSCmapDecodeBfrangeArrayBounds covers cmap.Decode's bfrange
// Array-destination path: decoding at index 0 and the last valid index
// returns the matching array element's rune, and decoding one past the
// array's end falls back to noRune without panicking.
func TestWSCmapDecodeBfrangeArrayBounds(t *testing.T) {
	t.Parallel()

	// <41>-<43> is A,B,C (3 codes) but the array has only 2 elements, so C
	// indexes one past the end.
	const cm = "1 begincodespacerange <00> <ff> endcodespacerange " +
		"1 beginbfrange <41> <43> [<0058> <0059>] endbfrange"

	m := readCmap(rawStream(cm))
	if m == nil {
		t.Fatal("readCmap returned nil for a well-formed bfrange")
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"index 0", "A", "X"},
		{"last valid index", "B", "Y"},
		{"one past end falls back to noRune", "C", string(noRune)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mustNotCrash(t, func() {
				if got := m.Decode(tt.in); got != tt.want {
					t.Errorf("Decode(%q) = %q, want %q", tt.in, got, tt.want)
				}
			})
		})
	}
}

// TestWSCmapDecodeBfcharExactLength covers cmap.Decode's exact-length bfchar
// matching: a 1-byte code must not match a 2-byte bfchar entry or vice versa.
// The codespace ranges are chosen so their lead bytes cannot overlap (1-byte
// codes 0x80-0xff, 2-byte codes 0x0000-0x7fff), so the width that matches is
// unambiguous.
func TestWSCmapDecodeBfcharExactLength(t *testing.T) {
	t.Parallel()

	const cm = "2 begincodespacerange <80> <ff> <0000> <7fff> endcodespacerange " +
		"2 beginbfchar <90> <0058> <0041> <0059> endbfchar"

	m := readCmap(rawStream(cm))
	if m == nil {
		t.Fatal("readCmap returned nil for a well-formed bfchar")
	}
	if got := m.Decode("\x90"); got != "X" {
		t.Errorf(`Decode(0x90) = %q, want "X"`, got)
	}
	if got := m.Decode("\x00\x41"); got != "Y" {
		t.Errorf(`Decode(0x0041) = %q, want "Y"`, got)
	}
}

// TestWSCmapDecodeCodespaceInclusiveBound covers cmap.Decode's codespace
// range check being inclusive at both ends: lo and hi themselves decode via
// their bfchar entries, while lo-1 and hi+1 match no codespace range and fall
// back to noRune.
func TestWSCmapDecodeCodespaceInclusiveBound(t *testing.T) {
	t.Parallel()

	const cm = "1 begincodespacerange <41> <45> endcodespacerange " +
		"2 beginbfchar <41> <0058> <45> <0059> endbfchar"

	m := readCmap(rawStream(cm))
	if m == nil {
		t.Fatal("readCmap returned nil for a well-formed cmap")
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"lo-1 falls outside range", "\x40", string(noRune)},
		{"lo is in range", "\x41", "X"},
		{"hi is in range", "\x45", "Y"},
		{"hi+1 falls outside range", "\x46", string(noRune)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mustNotCrash(t, func() {
				if got := m.Decode(tt.in); got != tt.want {
					t.Errorf("Decode(%q) = %q, want %q", tt.in, got, tt.want)
				}
			})
		})
	}
}

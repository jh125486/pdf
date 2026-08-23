// Whitebox: builds Page/Reader/dict values directly from unexported fields
// (Reader.f/end/trailer, dict, objptr, stream, cmap internals) to reach
// content-stream and cmap edge cases without a full on-disk PDF, and to
// assert on cmap.bfchar/bfrange/space directly.

package pdf

import (
	"bytes"
	"strings"
	"testing"
)

// TestMalformedContentStream covers the content stream operators.
func TestMalformedContentStream(t *testing.T) {
	t.Parallel()

	// Q with nothing saved indexed gstack[-1].
	unbalanced := []string{"Q", "q Q Q", "Q Q Q", "q q Q Q Q"}
	for _, content := range unbalanced {
		t.Run("unbalanced "+content, func(t *testing.T) {
			t.Parallel()

			mustNotCrash(t, func() { pageWithContent(content).Content() })
		})
	}

	// Operators given the wrong number of operands panic by design. They
	// must be reported through the APIs that return an error, not escape
	// them.
	badOperands := []string{"1 2 Tm", "Tm", "1 2 3 cm", "re", "1 Tj", "TJ"}
	for _, content := range badOperands {
		t.Run("bad operands "+content, func(t *testing.T) {
			t.Parallel()

			mustNotCrash(t, func() {
				if _, err := pageWithContent(content).GetPlainText(nil); err != nil {
					t.Logf("reported: %v", err)
				}
			})
			mustNotCrash(t, func() {
				if _, err := pageWithContent(content).GetTextByRow(); err != nil {
					t.Logf("reported: %v", err)
				}
			})
			mustNotCrash(t, func() {
				if _, err := pageWithContent(content).GetTextByColumn(); err != nil {
					t.Logf("reported: %v", err)
				}
			})
		})
	}
}

// TestGetTextByRowColumnReportErrors pins the named-return fix: a panic has
// to surface as an error, not as a silent empty result.
func TestGetTextByRowColumnReportErrors(t *testing.T) {
	t.Parallel()

	// "Tm" with no operands panics inside walkTextBlocks.
	p := pageWithContent("Tm")

	if _, err := p.GetTextByRow(); err == nil {
		t.Error("GetTextByRow: got nil error, want the recovered panic")
	}
	if _, err := p.GetTextByColumn(); err == nil {
		t.Error("GetTextByColumn: got nil error, want the recovered panic")
	}
}

// TestQuoteOperatorHandlesThreeOperands covers the `"` operator (set
// spacing, move to next line, show text), which takes 3 operands (aw ac
// string). It must trim args down to the trailing string before falling
// through into the `'`/`Tj` cases, which expect exactly 1 arg -- otherwise a
// well-formed `"` operator panics as "bad ' operator" instead of being
// handled. Covers both GetPlainText and the walkTextBlocks-based
// GetTextByRow/GetTextByColumn paths.
func TestQuoteOperatorHandlesThreeOperands(t *testing.T) {
	t.Parallel()

	const content = `BT 1 2 (hello) " ET`
	p := pageWithContent(content)

	text, err := p.GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText: unexpected error: %v", err)
	}
	if !strings.Contains(text, "hello") {
		t.Errorf("GetPlainText: got %q, want it to contain %q", text, "hello")
	}

	rows, err := p.GetTextByRow()
	if err != nil {
		t.Fatalf("GetTextByRow: unexpected error: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("GetTextByRow: got no rows, want at least one")
	}
}

// TestCyclicReferences covers the traversals over object graphs a file can
// point back at themselves. The outline case previously exhausted the
// goroutine stack, which is a fatal error no caller can recover from.
func TestCyclicReferences(t *testing.T) {
	t.Parallel()

	newReader := func() *Reader { return &Reader{f: bytes.NewReader(nil), end: 0} }

	t.Run("parent chain", func(t *testing.T) {
		t.Parallel()

		d := dict{}
		d[name("Parent")] = d
		mustNotCrash(t, func() {
			Page{V: Value{newReader(), objptr{}, d}}.Resources()
		})
	})

	t.Run("parent chain long", func(t *testing.T) {
		t.Parallel()

		// A chain longer than the cap but not cyclic still terminates.
		leaf := dict{name("Resources"): dict{}}
		cur := leaf
		for range 500 {
			cur = dict{name("Parent"): cur}
		}
		mustNotCrash(t, func() {
			Page{V: Value{newReader(), objptr{}, cur}}.Resources()
		})
	})

	t.Run("page kids", func(t *testing.T) {
		t.Parallel()

		node := dict{name("Type"): name("Pages"), name("Count"): int64(10)}
		node[name("Kids")] = array{node}
		r := newReader()
		r.trailer = dict{name("Root"): dict{name("Pages"): node}}
		mustNotCrash(t, func() { r.Page(5) })
	})

	t.Run("outline first", func(t *testing.T) {
		t.Parallel()

		d := dict{name("Title"): "t"}
		d[name("First")] = d
		r := newReader()
		r.trailer = dict{name("Root"): dict{name("Outlines"): d}}
		mustNotCrash(t, func() { r.Outline() })
	})

	t.Run("outline next", func(t *testing.T) {
		t.Parallel()

		child := dict{name("Title"): "c"}
		child[name("Next")] = child
		d := dict{name("Title"): "t", name("First"): child}
		r := newReader()
		r.trailer = dict{name("Root"): dict{name("Outlines"): d}}
		mustNotCrash(t, func() { r.Outline() })
	})
}

// TestMalformedCmap covers the ToUnicode CMap parser.
func TestMalformedCmap(t *testing.T) {
	t.Parallel()

	const space = "1 begincodespacerange <00> <ff> endcodespacerange "

	tests := []struct {
		name    string
		content string
	}{
		// A codespace range wider than 4 bytes indexed cmap.space[4].
		{"codespace too wide", "1 begincodespacerange <0102030405> <0102030406> endcodespacerange"},
		{"codespace mismatched", "1 begincodespacerange <00> <ffff> endcodespacerange"},
		{"codespace empty", "1 begincodespacerange <> <> endcodespacerange"},
		{"codespace count over", "9 begincodespacerange <00> <ff> endcodespacerange"},
		{"codespace no begin", "endcodespacerange"},

		// Declared counts used to be trusted, so a few digits appended
		// hundreds of millions of empty entries.
		{"bfchar count over", space + "268435456 beginbfchar endbfchar"},
		{"bfchar count huge", space + "9223372036854775807 beginbfchar endbfchar"},
		{"bfchar count negative", space + "-1 beginbfchar endbfchar"},
		{"bfchar no begin", space + "endbfchar"},
		{"bfrange count over", space + "268435456 beginbfrange endbfrange"},
		{"bfrange count huge", space + "9223372036854775807 beginbfrange endbfrange"},
		{"bfrange no begin", space + "endbfrange"},

		// Interpret's own panics must not escape readCmap.
		{"end without begin", "end"},
		{"begin non dict", "1 begin"},
		{"def without dict", "/k 1 def"},
		{"currentdict", "currentdict"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mustNotCrash(t, func() {
				if m := readCmap(rawStream(tt.content)); m != nil {
					// Whatever was salvaged must also be safe to use.
					m.Decode("A")
					if n := len(m.bfchar) + len(m.bfrange); n > 1024 {
						t.Errorf("built %d entries from %d bytes of input", n, len(tt.content))
					}
				}
			})
		})
	}
}

// TestMalformedCmapDecode covers using a cmap that parsed successfully but
// holds hostile values.
func TestMalformedCmapDecode(t *testing.T) {
	t.Parallel()

	const space = "1 begincodespacerange <00> <ff> endcodespacerange "

	tests := []struct {
		name    string
		content string
		decode  string
	}{
		// An odd-length replacement read one byte past the end in utf16Decode.
		{"odd length bfchar", space + "1 beginbfchar <41> <414243> endbfchar", "A"},
		{"odd length bfrange", space + "1 beginbfrange <41> <5a> <414243> endbfrange", "A"},
		// An empty destination has no low byte to scale, and indexed b[-1].
		{"empty bfrange dst", space + "1 beginbfrange <41> <5a> () endbfrange", "B"},
		{"empty bfchar repl", space + "1 beginbfchar <41> () endbfchar", "A"},
		{"bfrange array short", space + "1 beginbfrange <41> <5a> [<0041>] endbfrange", "Z"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mustNotCrash(t, func() {
				if m := readCmap(rawStream(tt.content)); m != nil {
					m.Decode(tt.decode)
				}
			})
		})
	}
}

// TestValidCmapStillParses guards against the CMap operand check rejecting
// well-formed cmaps.
func TestValidCmapStillParses(t *testing.T) {
	t.Parallel()

	const cm = `/CIDInit /ProcSet findresource begin
12 dict begin
begincmap
2 begincodespacerange
<00> <ff>
<0000> <ffff>
endcodespacerange
2 beginbfchar
<41> <0041>
<42> <0042>
endbfchar
1 beginbfrange
<43> <45> <0043>
endbfrange
endcmap
end
end`

	m := readCmap(rawStream(cm))
	if m == nil {
		t.Fatal("readCmap returned nil for a well-formed cmap")
	}
	if len(m.bfchar) != 2 {
		t.Errorf("bfchar = %d entries, want 2", len(m.bfchar))
	}
	if len(m.bfrange) != 1 {
		t.Errorf("bfrange = %d entries, want 1", len(m.bfrange))
	}
	if len(m.space[0]) != 1 || len(m.space[1]) != 1 {
		t.Errorf("codespace ranges = %d one-byte, %d two-byte, want 1 and 1",
			len(m.space[0]), len(m.space[1]))
	}
	if got := m.Decode("A"); got != "A" {
		t.Errorf("Decode(A) = %q, want %q", got, "A")
	}
	if got := m.Decode("D"); got != "D" {
		t.Errorf("Decode(D) = %q, want %q", got, "D")
	}
}

// TestCmapDecodeBfrangeArray covers cmap.Decode's bfrange-with-array-
// destination success path: each code in the range indexes into the
// destination array, and an entry that is itself a String is used directly.
func TestCmapDecodeBfrangeArray(t *testing.T) {
	t.Parallel()

	const cm = `1 begincodespacerange
<00> <ff>
endcodespacerange
1 beginbfrange
<41> <42> [<0058> <0059>]
endbfrange`

	m := readCmap(rawStream(cm))
	if m == nil {
		t.Fatal("readCmap returned nil for a well-formed cmap")
	}
	if got := m.Decode("A"); got != "X" {
		t.Errorf(`Decode("A") = %q, want %q`, got, "X")
	}
	if got := m.Decode("B"); got != "Y" {
		t.Errorf(`Decode("B") = %q, want %q`, got, "Y")
	}
}

// TestCmapDecodeUnmapped covers cmap.Decode's two "give up" paths: a byte
// sequence that matches a declared codespace range but has no bfchar or
// bfrange entry, and a byte that matches no codespace range at all (so
// Decode falls back to consuming it one byte at a time).
func TestCmapDecodeUnmapped(t *testing.T) {
	t.Parallel()

	t.Run("codespace matched, no bfchar or bfrange", func(t *testing.T) {
		t.Parallel()

		const cm = "1 begincodespacerange\n<00> <ff>\nendcodespacerange"
		m := readCmap(rawStream(cm))
		if m == nil {
			t.Fatal("readCmap returned nil")
		}
		if got := m.Decode("A"); got != string(noRune) {
			t.Errorf("Decode(A) = %q, want the replacement char", got)
		}
	})

	t.Run("no codespace range matches", func(t *testing.T) {
		t.Parallel()

		// A codespace of 2-byte codes only; decoding a 1-byte string matches
		// no range at all.
		const cm = "1 begincodespacerange\n<0000> <ffff>\nendcodespacerange"
		m := readCmap(rawStream(cm))
		if m == nil {
			t.Fatal("readCmap returned nil")
		}
		if got := m.Decode("A"); got != string(noRune) {
			t.Errorf("Decode(A) = %q, want the replacement char", got)
		}
	})
}

// TestReadCmapDefineresource covers readCmap's "defineresource" operator,
// part of the standard CMap resource-registration boilerplate
// ("currentdict /CMap defineresource pop") that real ToUnicode CMaps emit
// after endcmap.
func TestReadCmapDefineresource(t *testing.T) {
	t.Parallel()

	const cm = `/CIDInit /ProcSet findresource begin
12 dict begin
begincmap
1 begincodespacerange
<00> <ff>
endcodespacerange
1 beginbfchar
<41> <0042>
endbfchar
endcmap
CMapName currentdict /CMap defineresource pop
end
end`

	m := readCmap(rawStream(cm))
	if m == nil {
		t.Fatal("readCmap returned nil for a cmap using defineresource")
	}
	if got := m.Decode("A"); got != "B" {
		t.Errorf(`Decode("A") = %q, want %q`, got, "B")
	}
}

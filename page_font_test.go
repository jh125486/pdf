// Mutation-coverage workstream (WS10), kept separate so it can merge
// independently of other in-flight test PRs.
//
// Covers Font.Width's boundary/malformed-range handling and dictEncoder's
// Decode method (the /Encoding << /Differences [...] >> glyph-name
// encoder), both in page.go.
package pdf_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/jh125486/pdf"
)

// wsFontWidthPDF builds a page with four fonts exercising Font.Width's
// boundary and malformed-range branches: a normal three-glyph range, a
// single-entry range starting at code 0, a font with no /Widths entry at
// all, and a font whose /FirstChar is greater than its /LastChar.
func wsFontWidthPDF() []byte {
	const content = "BT ET\n"

	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] " +
			"/Resources << /Font << /F1 5 0 R /F2 6 0 R /F3 7 0 R /F4 8 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
		// F1: normal range, FirstChar < LastChar.
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 65 /LastChar 67 /Widths [500 600 700] >>",
		// F2: single-entry range starting at code 0.
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 0 /LastChar 0 /Widths [123] >>",
		// F3: no /Widths entry at all, but a valid FirstChar/LastChar range.
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 65 /LastChar 70 >>",
		// F4: malformed range, FirstChar > LastChar.
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 67 /LastChar 65 /Widths [500 600 700] >>",
	}
	return buildPDF(objs)
}

// wsFontDifferencesPDF builds a page with six fonts exercising
// dictEncoder.Decode's handling of an /Encoding << /Differences [...] >>
// array: a normal run of consecutive Names after a leading Integer, a
// second Integer that must reset the running index rather than continue
// it, an array with no leading Integer at all, an invented glyph name with
// no Unicode mapping, an Integer at the edge of int64 alongside entries
// that are neither Integer nor Name, and an empty Differences array.
func wsFontDifferencesPDF() []byte {
	const content = "BT ET\n"

	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] " +
			"/Resources << /Font << /D1 5 0 R /D2 6 0 R /D3 7 0 R /D4 8 0 R /D5 9 0 R /D6 10 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
		// D1: consecutive Names after one leading Integer.
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding << /Differences [65 /A /B /C] >> >>",
		// D2: a second Integer (200) must reset the running index rather
		// than continue counting from where /A left off. /zero (-> '0',
		// 0x30) is used instead of a second Latin letter because glyph
		// names 'A'/'B'/'C' map to their own ASCII code points, which
		// would make a mis-mapped result indistinguishable from the
		// raw-byte-passthrough fallback.
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding << /Differences [65 /A 200 /zero] >> >>",
		// D3: no leading Integer at all.
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding << /Differences [/A /B] >> >>",
		// D4: an invented glyph name with no entry in nameToRune.
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding << /Differences [65 /NotARealGlyphName] >> >>",
		// D5: an Integer at the edge of int64's range, plus an Array and a
		// Real entry, neither of which is Integer nor Name.
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding << /Differences [9223372036854775807 /A [1 2] 1.5 /B] >> >>",
		// D6: an empty Differences array.
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding << /Differences [] >> >>",
	}
	return buildPDF(objs)
}

// TestWSFontWidth covers Font.Width's in-range, boundary, missing-/Widths,
// and malformed-range (/FirstChar > /LastChar) branches.
func TestWSFontWidth(t *testing.T) {
	t.Parallel()

	data := wsFontWidthPDF()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	p := r.Page(1)

	tests := []struct {
		name string
		font string
		code int
		want float64
	}{
		// Case 1: normal three-glyph range, both boundary pairs.
		{"below first char", "F1", 64, 0},
		{"first char", "F1", 65, 500},
		{"middle char", "F1", 66, 600},
		{"last char", "F1", 67, 700},
		{"above last char", "F1", 68, 0},
		// Case 2: single-entry range starting at code 0.
		{"single-entry range, in range", "F2", 0, 123},
		{"single-entry range, below", "F2", -1, 0},
		{"single-entry range, above", "F2", 1, 0},
		// Case 3: no /Widths entry at all.
		{"no Widths, at FirstChar", "F3", 65, 0},
		{"no Widths, mid-range", "F3", 67, 0},
		{"no Widths, at LastChar", "F3", 70, 0},
		// Case 4: malformed range, FirstChar (67) > LastChar (65).
		{"malformed range, below both", "F4", 60, 0},
		{"malformed range, between", "F4", 66, 0},
		{"malformed range, above both", "F4", 70, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := p.Font(tt.font).Width(tt.code); got != tt.want {
				t.Errorf("Font(%s).Width(%d) = %v, want %v", tt.font, tt.code, got, tt.want)
			}
		})
	}
}

// TestWSDictEncoderDecode covers dictEncoder.Decode: consecutive Names
// after a leading Integer, a second Integer resetting the running index,
// an array with no leading Integer, an unmapped glyph name falling back to
// raw passthrough, edge-of-range/non-Integer/non-Name entries, and an
// empty Differences array.
func TestWSDictEncoderDecode(t *testing.T) {
	t.Parallel()

	data := wsFontDifferencesPDF()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	p := r.Page(1)

	t.Run("consecutive names after leading integer", func(t *testing.T) {
		t.Parallel()

		enc := p.Font("D1").Encoder()
		tests := []struct {
			code string
			want string
		}{
			{"A", "A"},                // byte 0x41 (65): matches /A directly.
			{"B", "B"},                // byte 0x42 (66): index steps past /A to /B.
			{"C", "C"},                // byte 0x43 (67): index steps past /A,/B to /C.
			{string(rune(0x44)), "D"}, // byte 0x44: one past /C, no match, raw passthrough.
		}
		for _, tt := range tests {
			if got := enc.Decode(tt.code); got != tt.want {
				t.Errorf("Decode(%q) = %q, want %q", tt.code, got, tt.want)
			}
		}
	})

	t.Run("second integer resets running index", func(t *testing.T) {
		t.Parallel()

		enc := p.Font("D2").Encoder()

		if got, want := enc.Decode("A"), "A"; got != want {
			t.Errorf("Decode(0x41) = %q, want %q", got, want)
		}
		if got, want := enc.Decode(string([]byte{200})), "0"; got != want {
			t.Errorf("Decode(0xC8) = %q, want %q (rune for /zero, proving the second Integer reset the index to 200)", got, want)
		}
		// If the index had merely continued counting from /A (66) instead
		// of resetting to 200, byte 0x42 would land on /zero. It must not:
		// with no match it falls back to the raw byte itself.
		if got, want := enc.Decode(string(rune(0x42))), "B"; got != want {
			t.Errorf("Decode(0x42) = %q, want %q (raw passthrough)", got, want)
		}
		if got := enc.Decode(string(rune(0x42))); got == "0" {
			t.Errorf("Decode(0x42) = %q, must not match /zero from D2", got)
		}
	})

	t.Run("no leading integer", func(t *testing.T) {
		t.Parallel()

		enc := p.Font("D3").Encoder()

		// The running index starts at -1, which byte 0 (0-255) never
		// matches on the first (/A) entry; the post-increment to 0 then
		// matches on the second (/B) entry instead.
		if got, want := enc.Decode(string(rune(0))), "B"; got != want {
			t.Errorf("Decode(0x00) = %q, want %q", got, want)
		}
		if got := enc.Decode(string(rune(0))); got == "A" {
			t.Errorf("Decode(0x00) = %q, must not map to /A", got)
		}
		// Byte 1 matches neither /A (index -1) nor /B (index 0): raw
		// passthrough, no panic.
		if got, want := enc.Decode(string(rune(1))), string(rune(1)); got != want {
			t.Errorf("Decode(0x01) = %q, want %q", got, want)
		}
	})

	t.Run("unmapped glyph name falls back to raw byte", func(t *testing.T) {
		t.Parallel()

		enc := p.Font("D4").Encoder()

		if got, want := enc.Decode("A"), "A"; got != want {
			t.Errorf("Decode(0x41) = %q, want %q (raw passthrough, /NotARealGlyphName has no mapping)", got, want)
		}
	})

	t.Run("edge integer and non-integer non-name entries do not panic", func(t *testing.T) {
		t.Parallel()

		enc := p.Font("D5").Encoder()

		mustNotCrash(t, func() {
			if got, want := enc.Decode("A"), "A"; got != want {
				t.Errorf("Decode(0x41) = %q, want %q (raw passthrough)", got, want)
			}
		})
	})

	t.Run("empty differences array", func(t *testing.T) {
		t.Parallel()

		enc := p.Font("D6").Encoder()

		if got, want := enc.Decode("hello"), "hello"; got != want {
			t.Errorf("Decode(%q) = %q, want %q unchanged", "hello", got, want)
		}
	})
}

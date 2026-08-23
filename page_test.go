package pdf_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/jh125486/pdf"
)

// TestGetTextByRowStableOrder guards ledongthuc/pdf#16: GetTextByRow and
// GetTextByColumn sorted with sort.Sort, which is not guaranteed to
// preserve the relative order of characters whose sort key compares equal
// (same row, same column bucket). That reordered digits within a run, e.g.
// "176" coming back as "761". sort.Stable keeps characters in the order
// they were drawn when their keys tie.
func TestGetTextByRowStableOrder(t *testing.T) {
	t.Parallel()

	data := validPDF()
	// validPDF's content stream draws "Hello World" at a single Tm; that
	// alone isn't enough to force a tie in the sort key, so this only
	// checks the happy path still returns rows/columns without error and
	// in left-to-right reading order.
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	p := r.Page(1)

	rows, err := p.GetTextByRow()
	if err != nil {
		t.Fatalf("GetTextByRow: %v", err)
	}
	var got strings.Builder
	for _, row := range rows {
		for _, c := range row.Content {
			got.WriteString(c.S)
		}
	}
	if want := "Hello World"; got.String() != want {
		t.Errorf("GetTextByRow text = %q, want %q", got.String(), want)
	}

	cols, err := p.GetTextByColumn()
	if err != nil {
		t.Fatalf("GetTextByColumn: %v", err)
	}
	if len(cols) == 0 {
		t.Error("GetTextByColumn returned no columns")
	}
}

// TestTdUpdatesPosition guards ledongthuc/pdf#18: walkTextBlocks (shared by
// GetTextByRow and GetTextByColumn) only updated currentX/currentY on the
// Tm operator. A page using Td for positioning -- the common case, since Td
// is the relative move used between lines of a paragraph, while Tm is
// normally only used to set the first absolute position -- left every Text
// at (0, 0) and, because every row's Position was then 0, folded all lines
// into a single row.
func TestTdUpdatesPosition(t *testing.T) {
	t.Parallel()

	data := validPDF() // draws "Hello World" via "100 700 Td", no Tm.
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	rows, err := r.Page(1).GetTextByRow()
	if err != nil {
		t.Fatalf("GetTextByRow: %v", err)
	}
	found := false
	for _, row := range rows {
		for _, c := range row.Content {
			if c.S != "Hello World" {
				continue
			}
			found = true
			if c.X != 100 || c.Y != 700 {
				t.Errorf("Text{%q}.X,Y = %v,%v, want 100,700", c.S, c.X, c.Y)
			}
		}
	}
	if !found {
		t.Fatal(`GetTextByRow: "Hello World" not found in any row`)
	}
}

// TestGetPlainTextReadsFormXObjects guards ledongthuc/pdf#67: GetPlainText's
// operator switch had no case for "Do", so text drawn inside a Form
// XObject -- form-field appearance streams are the common real-world case,
// which is what the issue's repro turned out to be -- was silently dropped
// instead of extracted.
func TestGetPlainTextReadsFormXObjects(t *testing.T) {
	t.Parallel()

	// The page's content stream only invokes the Form XObject; all its text
	// lives inside the Form, exercising both the recursion into Do and the
	// Form's own /Resources/Font shadowing the page's.
	pageContent := "q 1 0 0 1 0 0 cm /Xi0 Do Q"
	xobjContent := "q BT /F1 12 Tf 10 10 Td (Hidden Text) Tj ET Q"

	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] " +
			"/Resources << /XObject << /Xi0 6 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(pageContent), pageContent),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 100 20] "+
			"/Resources << /Font << /F1 5 0 R >> >> /Length %d >>\nstream\n%s\nendstream",
			len(xobjContent), xobjContent),
	}
	data := buildPDF(objs)

	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	text, err := r.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	if !strings.Contains(text, "Hidden Text") {
		t.Errorf("GetPlainText = %q, want it to contain %q", text, "Hidden Text")
	}
}

// TestGetPlainTextBoundsFormXObjectRecursion guards against a Form XObject
// whose content invokes itself, directly or through a cycle of other
// Forms: without a depth bound, that recurses through Interpret/Do until
// the goroutine stack is exhausted -- an uncatchable fatal error, not a
// panic GetPlainText's recover could turn into a normal error return.
func TestGetPlainTextBoundsFormXObjectRecursion(t *testing.T) {
	t.Parallel()

	xobjContent := "q 1 0 0 1 0 0 cm /Xi0 Do Q" // invokes itself
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] " +
			"/Resources << /XObject << /Xi0 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(xobjContent), xobjContent),
		fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 100 20] "+
			"/Resources << /XObject << /Xi0 5 0 R >> >> /Length %d >>\nstream\n%s\nendstream",
			len(xobjContent), xobjContent),
	}
	data := buildPDF(objs)

	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	mustNotCrash(t, func() {
		if _, err := r.Page(1).GetPlainText(nil); err == nil {
			t.Error("GetPlainText on a self-referencing Form XObject: got nil error, want one")
		}
	})
}

// fontTestPDF builds a page with one Contents stream and a battery of fonts
// exercising Font.Widths/Width and every branch of Font.getEncoder /
// charmapEncoding.
func fontTestPDF() []byte {
	const content = "BT ET\n"
	differences := "[65 /A /B]"
	toUnicodeCmap := "/CIDInit /ProcSet findresource begin\n" +
		"12 dict begin\nbegincmap\n" +
		"1 begincodespacerange\n<0000> <ffff>\nendcodespacerange\n" +
		"1 beginbfchar\n<0041> <0042>\nendbfchar\n" +
		"endcmap\nend\nend"

	const malformedCmap = "end" // "end" with no matching "begin": readCmap recovers this to nil.

	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] " +
			"/Resources << /Font << /F1 5 0 R /F2 6 0 R /F3 7 0 R /F4 8 0 R " +
			"/F5 9 0 R /F6 10 0 R /F7 11 0 R /F8 13 0 R /F9 14 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /MacRomanEncoding >>",
		"<< /Type /Font /Subtype /Type0 /BaseFont /Identity /Encoding /Identity-H >>",
		fmt.Sprintf("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding << /Differences %s >> >>", differences),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding 12 >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /FooEncoding >>",
		"<< /Type /Font /Subtype /Type0 /BaseFont /Identity /Encoding /Identity-H /ToUnicode 12 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(toUnicodeCmap), toUnicodeCmap),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 65 /LastChar 67 /Widths [500 600 700] >>",
		"<< /Type /Font /Subtype /Type0 /BaseFont /Identity /Encoding /Identity-H /ToUnicode 15 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(malformedCmap), malformedCmap),
	}
	return buildPDF(objs)
}

// TestFontEncoder covers every branch of Font.getEncoder and
// Font.charmapEncoding: named WinAnsi/MacRoman encodings, Identity-H with no
// ToUnicode (falls back to PDFDocEncoding), Identity-H with a ToUnicode CMap,
// a Differences dictionary, an unrecognized encoding name, an unexpected
// /Encoding kind, and no /Encoding entry at all.
func TestFontEncoder(t *testing.T) {
	t.Parallel()

	data := fontTestPDF()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	p := r.Page(1)

	tests := []struct {
		name string
		font string
		code string // raw code point(s) to decode
		want string
	}{
		{"WinAnsiEncoding", "F1", "A", "A"},
		{"MacRomanEncoding", "F2", "A", "A"},
		{"Identity-H no ToUnicode falls back to PDFDoc", "F3", "A", "A"},
		{"Differences dictionary, first entry", "F4", "A", "A"},  // code 65 matches /A directly
		{"Differences dictionary, second entry", "F4", "B", "B"}, // code 66 requires stepping past /A to /B
		{"unexpected encoding kind", "F5", "A", "A"},
		{"unknown encoding name", "F6", "A", "A"},
		{"Identity-H with ToUnicode", "F7", "\x00A", "B"},                  // bfchar maps <0041> -> <0042>
		{"Identity-H with malformed ToUnicode falls back", "F9", "A", "A"}, // readCmap returns nil -> nopEncoder
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			enc := p.Font(tt.font).Encoder()
			if enc == nil {
				t.Fatal("Encoder() returned nil")
			}
			if got := enc.Decode(tt.code); got != tt.want {
				t.Errorf("Font(%s).Encoder().Decode(%q) = %q, want %q", tt.font, tt.code, got, tt.want)
			}
		})
	}
}

// TestFontWidths covers Font.Widths, Font.FirstChar, Font.LastChar, and
// Font.Width's in-range and out-of-range branches.
func TestFontWidths(t *testing.T) {
	t.Parallel()

	data := fontTestPDF()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	f := r.Page(1).Font("F8")

	if got := f.FirstChar(); got != 65 {
		t.Errorf("FirstChar = %d, want 65", got)
	}
	if got := f.LastChar(); got != 67 {
		t.Errorf("LastChar = %d, want 67", got)
	}
	if got := f.Widths(); len(got) != 3 || got[0] != 500 || got[1] != 600 || got[2] != 700 {
		t.Errorf("Widths = %v, want [500 600 700]", got)
	}

	widthTests := []struct {
		name string
		code int
		want float64
	}{
		{"first char", 65, 500},
		{"middle char", 66, 600},
		{"last char", 67, 700},
		{"below range", 64, 0},
		{"above range", 68, 0},
	}
	for _, tt := range widthTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := f.Width(tt.code); got != tt.want {
				t.Errorf("Width(%d) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

// TestPageLookupAcrossPageTree covers Page's descent through a nested
// /Pages tree (a /Kids entry that is itself a /Pages node) and its
// out-of-range and not-found return.
func TestPageLookupAcrossPageTree(t *testing.T) {
	t.Parallel()

	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
		// A nested Pages node, itself holding one page.
		"<< /Type /Pages /Parent 2 0 R /Kids [5 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>",
		"<< /Type /Page /Parent 3 0 R /MediaBox [0 0 200 200] >>",
	}
	data := buildPDF(objs)
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if n := r.NumPage(); n != 2 {
		t.Fatalf("NumPage = %d, want 2", n)
	}

	p1 := r.Page(1)
	if p1.V.IsNull() {
		t.Error("Page(1) is null")
	}
	p2 := r.Page(2)
	if p2.V.IsNull() {
		t.Fatal("Page(2) is null")
	}
	// Page 2 is reached by descending into the nested /Pages node.
	if got := p2.V.Key("Parent").Key("Type").Name(); got != "Pages" {
		t.Errorf("Page(2) parent /Type = %q, want %q", got, "Pages")
	}

	if got := r.Page(3); !got.V.IsNull() {
		t.Error("Page(3): want null Page for out-of-range page number")
	}
}

// TestContentOperators drives Page.Content through the bulk of its content
// stream operator switch: graphics state save/restore, the CTM, fill/stroke
// no-ops, path construction, colorspace no-ops, rectangle accumulation, and
// the text-positioning/showing operators (Td, TD, Tc, Tw, Tz, TL, Tr, Ts,
// Tm, T*, Tj, ', ", TJ with both string and numeric-adjustment elements).
func TestContentOperators(t *testing.T) {
	t.Parallel()

	content := strings.Join([]string{
		"q",
		"1 0 0 1 0 0 cm",
		"/GS0 gs",
		"0 g",
		"10 20 m",
		"30 40 l",
		"/CS0 cs",
		"1 scn",
		"0 0 100 50 re",
		"f",
		"BT",
		"/F1 12 Tf",
		"0 Tc",
		"0 Tw",
		"100 Tz",
		"0 Tr",
		"0 Ts",
		"14 TL",
		"100 700 Td",
		"(Hello) Tj",
		"T*",
		"100 0 TD",
		"(World) '",
		"0.1 0.2 (Bye) \"",
		"[(A) -50 (B)] TJ",
		"1 0 0 1 0 0 Tm",
		"ET",
		"Q",
	}, "\n")

	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] " +
			"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	}
	data := buildPDF(objs)
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	got := r.Page(1).Content()
	if len(got.Rect) != 1 {
		t.Fatalf("Content().Rect = %v, want 1 rectangle", got.Rect)
	}
	if want := (pdf.Rect{Min: pdf.Point{X: 0, Y: 0}, Max: pdf.Point{X: 100, Y: 50}}); got.Rect[0] != want {
		t.Errorf("Content().Rect[0] = %+v, want %+v", got.Rect[0], want)
	}
	if len(got.Text) == 0 {
		t.Fatal("Content().Text is empty")
	}
	var s strings.Builder
	for _, tx := range got.Text {
		s.WriteString(tx.S)
	}
	full := s.String()
	for _, want := range []string{"H", "e", "l", "o", "W", "r", "d", "B", "y", "A", "B"} {
		if !strings.Contains(full, want) {
			t.Errorf("Content().Text = %q, want it to contain %q", full, want)
		}
	}
}

// TestContentUnmatchedQRestoresNothing covers Content()'s "Q" branch when
// there is no saved graphics state to restore: it must be a no-op, not a
// panic or an out-of-bounds slice access.
func TestContentUnmatchedQRestoresNothing(t *testing.T) {
	t.Parallel()

	data := pageWithContentPDF("Q")
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	mustNotCrash(t, func() { r.Page(1).Content() })
}

// pageWithContentPDF builds a minimal single-page PDF whose /Contents is
// exactly content, with no font resources.
func pageWithContentPDF(content string) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
	}
	return buildPDF(objs)
}

// TestTextOrderingRequiresSwap covers TextHorizontal.Swap and
// TextVertical.Swap: sort.Stable only calls Swap when two elements are
// actually out of order, so the fixture below draws its second piece of
// text to the left of (and, for the column case, above) the first, forcing
// a real swap rather than a no-op comparison.
func TestTextOrderingRequiresSwap(t *testing.T) {
	t.Parallel()

	t.Run("row", func(t *testing.T) {
		t.Parallel()

		// Same Y (one row): draw "B" at x=100 first, then "A" at x=20.
		// Sorting by X ascending must swap them into "A", "B" order.
		content := "BT /F1 12 Tf 100 500 Td (B) Tj -80 0 Td (A) Tj ET"
		data := buildPDF([]string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] " +
				"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		})
		r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		rows, err := r.Page(1).GetTextByRow()
		if err != nil {
			t.Fatalf("GetTextByRow: %v", err)
		}
		var got strings.Builder
		for _, row := range rows {
			for _, c := range row.Content {
				got.WriteString(c.S)
			}
		}
		if want := "AB"; got.String() != want {
			t.Errorf("GetTextByRow text = %q, want %q", got.String(), want)
		}
	})

	t.Run("column", func(t *testing.T) {
		t.Parallel()

		// Same X (one column): draw "B" at y=100 first, then "A" at y=200.
		// GetTextByColumn sorts each column top to bottom (descending Y), so
		// "A" (the higher point) must be swapped ahead of "B".
		content := "BT /F1 12 Tf 50 100 Td (B) Tj 0 100 Td (A) Tj ET"
		data := buildPDF([]string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 300] " +
				"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		})
		r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		cols, err := r.Page(1).GetTextByColumn()
		if err != nil {
			t.Fatalf("GetTextByColumn: %v", err)
		}
		if len(cols) != 1 {
			t.Fatalf("GetTextByColumn = %d columns, want 1", len(cols))
		}
		var got strings.Builder
		for _, c := range cols[0].Content {
			got.WriteString(c.S)
		}
		if want := "AB"; got.String() != want {
			t.Errorf("GetTextByColumn text = %q, want %q", got.String(), want)
		}
	})
}

// TestContentBadOperands covers Content()'s per-operator operand-count
// panics. Content has no recover of its own (by design -- callers needing
// an error use GetPlainText/GetTextByRow/GetTextByColumn instead), so each
// case here recovers directly.
func TestContentBadOperands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{"cm too few args", "1 2 3 cm"},
		{"Tc no args", "Tc"},
		{"TD one arg", "1 TD"},
		{"Td one arg", "1 Td"},
		{"Tf one arg", "/F1 Tf"},
		{"quote-quote wrong args", "(x) \""},
		{"quote wrong args", "1 2 (x) '"},
		{"Tj no args", "Tj"},
		{"TL no args", "TL"},
		{"Tm too few args", "1 2 3 4 5 Tm"},
		{"Tr no args", "Tr"},
		{"Ts no args", "Ts"},
		{"Tw no args", "Tw"},
		{"Tz no args", "Tz"},
		{"re too few args", "1 2 3 re"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := pageWithContentPDF(tt.content)
			r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}

			if _, timedOut := run(t, func() { r.Page(1).Content() }); timedOut {
				t.Errorf("did not return within %v", caseTimeout)
			}
		})
	}
}

// TestContentSubsetFontNameStripped covers Content()'s handling of a
// subset font's BaseFont, e.g. "ABCDEF+Helvetica": the subset tag and '+'
// are stripped from Text.Font.
func TestContentSubsetFontNameStripped(t *testing.T) {
	t.Parallel()

	content := "BT /F1 12 Tf (A) Tj ET"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] " +
			"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /ABCDEF+Helvetica /Encoding /WinAnsiEncoding >>",
	}
	data := buildPDF(objs)
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	got := r.Page(1).Content()
	if len(got.Text) == 0 {
		t.Fatal("Content().Text is empty")
	}
	if got := got.Text[0].Font; got != "Helvetica" {
		t.Errorf("Text[0].Font = %q, want %q", got, "Helvetica")
	}
}

// TestWalkTextBlocksOperators drives GetTextByRow (which shares
// walkTextBlocks with GetTextByColumn) through its font lookup and the T*,
// Tf, ", ', Tj, and TJ operators.
func TestWalkTextBlocksOperators(t *testing.T) {
	t.Parallel()

	content := strings.Join([]string{
		"BT",
		"/F1 12 Tf", // matches a font in Resources, so enc is looked up
		"T*",
		"10 20 Td",
		"(A) Tj",
		"1 2 (C) \"",
		"(B) '",
		"[(D)] TJ",
		"ET",
	}, "\n")
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] " +
			"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	}
	data := buildPDF(objs)
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	rows, err := r.Page(1).GetTextByRow()
	if err != nil {
		t.Fatalf("GetTextByRow: %v", err)
	}
	var got strings.Builder
	for _, row := range rows {
		for _, c := range row.Content {
			got.WriteString(c.S)
		}
	}
	for _, want := range []string{"A", "B", "C", "D"} {
		if !strings.Contains(got.String(), want) {
			t.Errorf("GetTextByRow text = %q, want it to contain %q", got.String(), want)
		}
	}
}

// TestWalkTextBlocksEmptyPage covers walkTextBlocks' early return for a page
// with no /Contents.
func TestWalkTextBlocksEmptyPage(t *testing.T) {
	t.Parallel()

	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>",
	}
	data := buildPDF(objs)
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	rows, err := r.Page(1).GetTextByRow()
	if err != nil {
		t.Fatalf("GetTextByRow: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("GetTextByRow on a contentless page = %v, want none", rows)
	}
}

// TestWalkTextBlocksBadOperands covers walkTextBlocks' operand-count panics
// for ', ", Tj, Td, and Tm, surfaced as errors through GetTextByRow.
func TestWalkTextBlocksBadOperands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{"quote wrong args", "1 2 (x) '"},
		{"quote-quote wrong args", "(x) \""},
		{"Tj no args", "Tj"},
		{"Td one arg", "1 Td"},
		{"Tm too few args", "1 2 3 4 5 Tm"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := pageWithContentPDF(tt.content)
			r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}
			if _, err := r.Page(1).GetTextByRow(); err == nil {
				t.Error("GetTextByRow: got nil error, want the recovered panic")
			}
		})
	}
}

// TestGetPlainTextOperators drives GetPlainText's own interpret closure
// (distinct from walkTextBlocks/Content, but structurally similar) through
// T*, TJ with a numeric adjustment element alongside a string, a Do
// invoking a non-Form XObject (a no-op), and a Do with the wrong operand
// count (also a no-op, since GetPlainText's "Do" case returns instead of
// panicking on a bad operand count).
func TestGetPlainTextOperators(t *testing.T) {
	t.Parallel()

	content := strings.Join([]string{
		"BT",
		"/F1 12 Tf",
		"(A) Tj",
		"T*",
		"[(B) -50 (C)] TJ",
		"Do",      // no operand: the "Do" case just returns
		"/Im0 Do", // Im0 is an Image XObject, not a Form: no-op
		"ET",
	}, "\n")
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] " +
			"/Resources << /Font << /F1 5 0 R >> /XObject << /Im0 6 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		"<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /Length 0 >>\nstream\n\nendstream",
	}
	data := buildPDF(objs)
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	text, err := r.Page(1).GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	for _, want := range []string{"A", "B", "C"} {
		if !strings.Contains(text, want) {
			t.Errorf("GetPlainText = %q, want it to contain %q", text, want)
		}
	}
}

// TestGetPlainTextBadTf covers GetPlainText's Tf operand-count panic.
func TestGetPlainTextBadTf(t *testing.T) {
	t.Parallel()

	data := pageWithContentPDF("/F1 Tf")
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if _, err := r.Page(1).GetPlainText(nil); err == nil {
		t.Error("GetPlainText with a one-operand Tf: got nil error, want the panic")
	}
}

// TestPageOutOfRangeAndBadNestedCount covers Page's two "give up" branches
// inside the /Pages descent: a page number past the top node's declared
// /Count, and a nested /Pages kid whose /Count isn't a usable integer.
func TestPageOutOfRangeAndBadNestedCount(t *testing.T) {
	t.Parallel()

	t.Run("page number past declared count", func(t *testing.T) {
		t.Parallel()

		data := buildPDF([]string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>",
		})
		r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		if got := r.Page(5); !got.V.IsNull() {
			t.Error("Page(5) with /Count 1: want null Page")
		}
	})

	t.Run("nested Pages node with non-integer Count", func(t *testing.T) {
		t.Parallel()

		data := buildPDF([]string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 5 >>",
			// /Count is a name, not an integer: int64ToInt fails.
			"<< /Type /Pages /Parent 2 0 R /Kids [] /Count /Bad >>",
		})
		r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		if got := r.Page(1); !got.V.IsNull() {
			t.Error("Page(1) under a nested Pages node with a malformed /Count: want null Page")
		}
	})
}

// TestGetStyledTextsAcrossPages covers Reader.GetStyledTexts' skip of a
// contentless page and its final flush of the in-progress sentence once the
// loop over pages ends.
func TestGetStyledTextsAcrossPages(t *testing.T) {
	t.Parallel()

	content := "BT /F1 12 Tf 100 700 Td (Hello) Tj ET"
	data := buildPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 >>",
		// Page 1 has no /Contents at all: GetStyledTexts must skip it.
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] " +
			"/Resources << /Font << /F1 6 0 R >> >> /Contents 4 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	})
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	sentences, err := r.GetStyledTexts()
	if err != nil {
		t.Fatalf("GetStyledTexts: %v", err)
	}
	if len(sentences) == 0 {
		t.Fatal("GetStyledTexts returned no sentences")
	}
	var got strings.Builder
	for _, s := range sentences {
		got.WriteString(s.S)
	}
	if !strings.Contains(got.String(), "Hello") {
		t.Errorf("GetStyledTexts text = %q, want it to contain %q", got.String(), "Hello")
	}
}

// TestReaderGetPlainTextPerPageError covers Reader.GetPlainText's own
// error-forwarding branch: a per-page Page.GetPlainText error (already
// recovered into an error return by that method) must short-circuit
// Reader.GetPlainText rather than being silently dropped.
func TestReaderGetPlainTextPerPageError(t *testing.T) {
	t.Parallel()

	data := pageWithContentPDF("/F1 Tf") // Tf with one operand always panics
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if _, err := r.GetPlainText(); err == nil {
		t.Error("Reader.GetPlainText: got nil error, want the per-page error forwarded")
	}
}

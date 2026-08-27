// Mutation-coverage workstream (WS8): content-stream matrix math.
//
// This file is kept separate from the rest of the test suite so it can be
// reviewed and merged independently of other mutation-coverage workstreams
// running in parallel. It targets the matrix.mul/identity helpers and the
// Page.Content() graphics-state operators (cm, T*, Td/TD, Tm, TJ kerning,
// Tc, Ts) in page.go, asserting exact numeric coordinates rather than mere
// text presence.
//
// All expected values below are hand-derived from the actual formula in
// page.go's Content():
//
//	Trm := matrix{{Tfs*Th, 0, 0}, {0, Tfs, 0}, {0, Trise, 1}}.mul(Tm).mul(CTM)
//	Text{Font, FontSize: Trm[0][0], X: Trm[2][0], Y: Trm[2][1], W: w0/1000*Trm[0][0]}
//
// where matrix.mul is standard row-vector-on-the-left multiplication
// (z[i][j] = sum_k x[i][k]*y[k][j]), so for a point p, p*(A.mul(B)) ==
// (p*A)*B. "cm" does g.CTM = m.mul(g.CTM) (new cm matrix is the left
// operand, i.e. premultiplied ahead of the accumulated CTM). "Td"/"TD"
// operate on g.Tlm (not g.Tm) and then copy Tlm into Tm; "T*" also updates
// Tlm via x.mul(Tlm) where x is a pure -Tl y-translation. "Tm" replaces
// g.Tm/g.Tlm outright (assignment, not composition).
package pdf_test

import (
	"bytes"
	"fmt"
	"math"
	"testing"

	"github.com/jh125486/pdf"
)

// wsMatrixFontPDF builds a minimal single-page PDF whose /Contents is
// exactly content, wired to one Type1 font resource named /F1 with
// FirstChar 65 (A), LastChar 67 (C), and Widths [500 600 700] -- i.e. A=500,
// B=600, C=700 (in glyph-space thousandths), modeled on fontTestPDF's F8
// font object in page_test.go. Unlike pageWithContentPDF, this gives glyph
// width lookups a non-zero result so inter-glyph advance and kerning are
// exercisable.
func wsMatrixFontPDF(content string) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 600 600] " +
			"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 65 /LastChar 67 /Widths [500 600 700] >>",
	}
	return buildPDF(objs)
}

// wsMatrixContent parses content via wsMatrixFontPDF and returns the page's
// Content(). It fails the test on any reader error.
func wsMatrixContent(t *testing.T, content string) pdf.Content {
	t.Helper()

	data := wsMatrixFontPDF(content)
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return r.Page(1).Content()
}

const wsMatrixTol = 1e-9

func wsMatrixAssertFloat(t *testing.T, name string, got, want float64) {
	t.Helper()

	if math.Abs(got-want) > wsMatrixTol {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func wsMatrixAssertText(t *testing.T, got pdf.Text, wantFontSize, wantX, wantY, wantW float64) {
	t.Helper()

	wsMatrixAssertFloat(t, "FontSize", got.FontSize, wantFontSize)
	wsMatrixAssertFloat(t, "X", got.X, wantX)
	wsMatrixAssertFloat(t, "Y", got.Y, wantY)
	wsMatrixAssertFloat(t, "W", got.W, wantW)
}

// TestContentMatrixSimple covers case 1: a single Td then Tj with no cm,
// verifying the base text-rendering-matrix formula against font widths.
func TestContentMatrixSimple(t *testing.T) {
	t.Parallel()

	content := "BT /F1 10 Tf 100 200 Td (A) Tj ET"
	c := wsMatrixContent(t, content)

	if len(c.Text) != 1 {
		t.Fatalf("len(Text) = %d, want 1", len(c.Text))
	}
	// Tm = translate(100,200); Trm = diag(10,10).mul(Tm).mul(ident).
	// FontSize=Trm[0][0]=10, X=Trm[2][0]=100, Y=Trm[2][1]=200,
	// W = 500/1000*10 = 5 (A's width is 500).
	wsMatrixAssertText(t, c.Text[0], 10, 100, 200, 5)
}

// TestContentMatrixCM covers case 2: a single non-trivial "cm" (scale 2,3 +
// translate 10,20) composed ahead of the same Td/Tj as case 1.
func TestContentMatrixCM(t *testing.T) {
	t.Parallel()

	content := "2 0 0 3 10 20 cm BT /F1 10 Tf 100 200 Td (A) Tj ET"
	c := wsMatrixContent(t, content)

	if len(c.Text) != 1 {
		t.Fatalf("len(Text) = %d, want 1", len(c.Text))
	}
	// CTM = {{2,0,0},{0,3,0},{10,20,1}}.
	// P := diag(10,10).mul(Tm=translate(100,200)) = {{10,0,0},{0,10,0},{100,200,1}}.
	// Trm := P.mul(CTM):
	//   row0 = 10*CTM[0] = {20,0,0}
	//   row1 = 10*CTM[1] = {0,30,0}
	//   row2 = 100*CTM[0] + 200*CTM[1] + CTM[2] = {200+0+10, 0+600+20, 1} = {210,620,1}
	// FontSize=20, X=210, Y=620, W = 500/1000*20 = 10.
	wsMatrixAssertText(t, c.Text[0], 20, 210, 620, 10)
}

// TestContentMatrixCMOrder covers case 3: two successive non-commutative cm
// operators (a non-uniform scale, then a translate), pinning down that the
// source composes them as CTM_new = m_new.mul(CTM_old) -- i.e. each new cm
// is left-multiplied onto the accumulated CTM, so the second cm's
// translation gets scaled by the first cm's scale factor.
func TestContentMatrixCMOrder(t *testing.T) {
	t.Parallel()

	// cm1: scale (2,1). cm2: translate (5,0).
	// CTM1 = cm1.mul(ident) = {{2,0,0},{0,1,0},{0,0,1}}.
	// CTM2 = cm2.mul(CTM1):
	//   row0 = CTM1[0] = {2,0,0}
	//   row1 = CTM1[1] = {0,1,0}
	//   row2 = 5*CTM1[0] + CTM1[2] = {10,0,0}+{0,0,1} = {10,0,1}
	// (If the order were reversed -- cm1.mul(cm2) -- row2 would be {5,0,1}
	// instead of {10,0,1}, since the translate would not be scaled by the
	// earlier cm's factor; that's the discriminator this test pins down.)
	content := "2 0 0 1 0 0 cm 1 0 0 1 5 0 cm BT /F1 10 Tf (A) Tj ET"
	c := wsMatrixContent(t, content)

	if len(c.Text) != 1 {
		t.Fatalf("len(Text) = %d, want 1", len(c.Text))
	}
	// Tm = ident (no Td/Tm issued). P = diag(10,10).mul(ident) = diag(10,10,1).
	// Trm = P.mul(CTM2): row0=10*CTM2[0]={20,0,0}, row1=10*CTM2[1]={0,10,0},
	// row2=CTM2[2]={10,0,1}.
	// FontSize=20, X=10, Y=0, W=500/1000*20=10.
	wsMatrixAssertText(t, c.Text[0], 20, 10, 0, 10)
}

// TestContentMatrixTmReplaces covers case 4: an explicit Tm sets the text
// matrix directly, and a second Tm in the same BT block REPLACES it rather
// than composing with it.
func TestContentMatrixTmReplaces(t *testing.T) {
	t.Parallel()

	content := "BT /F1 10 Tf 1 0 0 1 50 60 Tm (A) Tj 1 0 0 1 200 300 Tm (A) Tj ET"
	c := wsMatrixContent(t, content)

	if len(c.Text) != 2 {
		t.Fatalf("len(Text) = %d, want 2", len(c.Text))
	}
	// First Tm: Tm = {{1,0,0},{0,1,0},{50,60,1}}.
	// Trm = diag(10,10).mul(Tm).mul(ident) = {{10,0,0},{0,10,0},{50,60,1}}.
	wsMatrixAssertText(t, c.Text[0], 10, 50, 60, 5)
	// Second Tm REPLACES g.Tm outright (not m.mul(g.Tm)): Tm = {{1,0,0},{0,1,0},{200,300,1}}.
	// Trm = diag(10,10).mul(Tm) = {{10,0,0},{0,10,0},{200,300,1}}.
	// If Tm instead composed with the prior Tm, X/Y would differ from 200/300.
	wsMatrixAssertText(t, c.Text[1], 10, 200, 300, 5)
}

// TestContentMatrixTdAccumulates covers case 5: two successive Td calls
// accumulate through g.Tlm (each Td is relative to the current line-start
// matrix, not to the position last drawn).
func TestContentMatrixTdAccumulates(t *testing.T) {
	t.Parallel()

	content := "BT /F1 10 Tf 100 200 Td (A) Tj 10 5 Td (A) Tj ET"
	c := wsMatrixContent(t, content)

	if len(c.Text) != 2 {
		t.Fatalf("len(Text) = %d, want 2", len(c.Text))
	}
	wsMatrixAssertText(t, c.Text[0], 10, 100, 200, 5)
	// Second Td(10,5) composes onto Tlm (still {100,200} -- Tlm is untouched
	// by the intra-line glyph advance applied to Tm after Tj), giving
	// Tlm2 = {{1,0,0},{0,1,0},{110,205,1}}. Trm's X/Y therefore accumulate
	// to (110,205) = (100+10, 200+5), not to some value derived from the
	// post-glyph-advance Tm.
	wsMatrixAssertText(t, c.Text[1], 10, 110, 205, 5)
}

// TestContentMatrixTD covers case 6: TD sets the leading (Tl = -ty) in
// addition to moving the text position, verified by a following T* whose
// resulting Y proves both the leading value and its sign.
func TestContentMatrixTD(t *testing.T) {
	t.Parallel()

	content := "BT /F1 10 Tf 0 -15 TD (A) Tj T* (A) Tj ET"
	c := wsMatrixContent(t, content)

	if len(c.Text) != 2 {
		t.Fatalf("len(Text) = %d, want 2", len(c.Text))
	}
	// TD sets g.Tl = -(-15) = 15, then behaves like Td(0,-15): Tlm=Tm={0,-15}.
	wsMatrixAssertText(t, c.Text[0], 10, 0, -15, 5)
	// T*: Tlm2 = translate(0,-Tl).mul(Tlm) = translate(0,-15).mul(Tlm),
	// giving Y = -15 + -15 = -30. This proves Tl was set to +15 (a positive
	// leading) from a negative ty argument.
	wsMatrixAssertText(t, c.Text[1], 10, 0, -30, 5)
}

// TestContentMatrixLeadingAndTStar covers case 7: an explicit TL leading
// value, then Td to a start position, then T* -- asserting the new line's Y
// is exactly start_Y - leading.
func TestContentMatrixLeadingAndTStar(t *testing.T) {
	t.Parallel()

	content := "BT /F1 10 Tf 12 TL 0 100 Td (A) Tj T* (A) Tj ET"
	c := wsMatrixContent(t, content)

	if len(c.Text) != 2 {
		t.Fatalf("len(Text) = %d, want 2", len(c.Text))
	}
	wsMatrixAssertText(t, c.Text[0], 10, 0, 100, 5)
	// T* moves Y by -Tl exactly: 100 - 12 = 88.
	wsMatrixAssertText(t, c.Text[1], 10, 0, 88, 5)
}

// TestContentMatrixTJKerning covers case 8: TJ per-glyph kerning adjustment
// sign convention -- a negative TJ number widens the gap (per PDF spec, the
// operand is subtracted from the horizontal coordinate); a positive TJ
// number narrows it. Both signs are checked to pin down the convention
// exactly, per page.go's `tx := -x.Float64() / 1000 * g.Tfs * g.Th`.
func TestContentMatrixTJKerning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		wantBX  float64
	}{
		{
			name:    "negative operand widens the gap",
			content: "BT /F1 10 Tf 0 0 Td [(A) -1000 (B)] TJ ET",
			// A's own advance: tx = 500/1000*10 + 0 = 5 -> Tm x-offset 5.
			// Then TJ number -1000: tx = -(-1000)/1000*10*1 = 10 -> Tm x-offset += 10 -> 15.
			// B's X = 15 (5 own advance + 10 kerning widening).
			wantBX: 15,
		},
		{
			name:    "positive operand narrows the gap",
			content: "BT /F1 10 Tf 0 0 Td [(A) 1000 (B)] TJ ET",
			// TJ number +1000: tx = -(1000)/1000*10*1 = -10 -> Tm x-offset 5-10 = -5.
			wantBX: -5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := wsMatrixContent(t, tt.content)
			// TJ's showText appends "\n" as a trailing pseudo-glyph after the
			// array, via showText("\n") -- but "\n" isn't a String array
			// element, it's appended unconditionally after the loop, and
			// goes through showText same as any char: it becomes a zero-width
			// Text entry (font has no width entry for '\n', code 10, which is
			// outside FirstChar..LastChar so Width returns 0). So we expect
			// 3 entries: 'A', 'B', '\n'.
			if len(c.Text) != 3 {
				t.Fatalf("len(Text) = %d, want 3", len(c.Text))
			}
			// A: X=0, Y=0, FontSize=10, W=500/1000*10=5.
			wsMatrixAssertText(t, c.Text[0], 10, 0, 0, 5)
			// B: Y=0, FontSize=10, W=600/1000*10=6, X=tt.wantBX.
			wsMatrixAssertText(t, c.Text[1], 10, tt.wantBX, 0, 6)
		})
	}
}

// TestContentMatrixTc covers case 9: Tc (character spacing) shifts a
// subsequent glyph's X by exactly the expected additional amount, on top of
// the previous glyph's own scaled advance.
func TestContentMatrixTc(t *testing.T) {
	t.Parallel()

	content := "BT /F1 10 Tf 2 Tc 0 0 Td (A) Tj (B) Tj ET"
	c := wsMatrixContent(t, content)

	if len(c.Text) != 2 {
		t.Fatalf("len(Text) = %d, want 2", len(c.Text))
	}
	wsMatrixAssertText(t, c.Text[0], 10, 0, 0, 5)
	// tx = w0/1000*Tfs + Tc = 500/1000*10 + 2 = 7 (Th=1). B's X = 7.
	// Without Tc this would be 5, so the +2 isolates Tc's exact contribution.
	wsMatrixAssertText(t, c.Text[1], 10, 7, 0, 6)
}

// TestContentMatrixTs covers case 10: Ts (text rise) shifts Y by exactly the
// rise amount, via the Trise term in the text-rendering matrix's third row.
func TestContentMatrixTs(t *testing.T) {
	t.Parallel()

	content := "BT /F1 10 Tf 3 Ts 0 0 Td (A) Tj ET"
	c := wsMatrixContent(t, content)

	if len(c.Text) != 1 {
		t.Fatalf("len(Text) = %d, want 1", len(c.Text))
	}
	// Trm = {{10,0,0},{0,10,0},{0,3,1}}.mul(ident).mul(ident) -> Y = 3.
	wsMatrixAssertText(t, c.Text[0], 10, 0, 3, 5)
}

// TestContentMatrixIdentityDefaults covers case 11: with no positioning
// operator at all, BT's reset of Tm/Tlm to identity and CTM's identity
// default leave the glyph at the origin.
func TestContentMatrixIdentityDefaults(t *testing.T) {
	t.Parallel()

	content := "BT /F1 10 Tf (A) Tj ET"
	c := wsMatrixContent(t, content)

	if len(c.Text) != 1 {
		t.Fatalf("len(Text) = %d, want 1", len(c.Text))
	}
	wsMatrixAssertText(t, c.Text[0], 10, 0, 0, 5)
}

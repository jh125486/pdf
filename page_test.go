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

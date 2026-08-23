package pdf_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jh125486/pdf"
)

// caseTimeout bounds a single case. The fixed code answers in microseconds; a
// regression that reintroduces an infinite loop shows up as a failure instead
// of a hung CI job.
const caseTimeout = 15 * time.Second

// run calls fn and reports whether it panicked or failed to return in time.
func run(t *testing.T, fn func()) (panicked any, timedOut bool) {
	t.Helper()
	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		fn()
	}()
	select {
	case p := <-done:
		return p, false
	case <-time.After(caseTimeout):
		return nil, true
	}
}

// mustNotCrash fails if fn panics or does not return within caseTimeout.
func mustNotCrash(t *testing.T, fn func()) {
	t.Helper()
	if p, timedOut := run(t, fn); timedOut {
		t.Errorf("did not return within %v", caseTimeout)
	} else if p != nil {
		t.Errorf("panicked: %v", p)
	}
}

// openBytes reads data as a PDF, reporting the error rather than the Reader.
func openBytes(data []byte) error {
	_, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	return err
}

// pad returns a comment line long enough to push a file past the 100-byte
// window NewReader reads from the end.
func pad() string { return "%" + strings.Repeat("p", 90) + "\n" }

// xrefStreamPDF builds a PDF whose startxref points at an xref stream carrying
// the given extra header entries and raw, unfiltered stream data.
func xrefStreamPDF(hdr, data string) []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.5\n")
	b.WriteString(pad())
	off := b.Len()
	b.WriteString("1 0 obj\n")
	fmt.Fprintf(&b, "<< /Type /XRef /Length %d %s >>\n", len(data), hdr)
	b.WriteString("stream\n")
	b.WriteString(data)
	b.WriteString("\nendstream\nendobj\n")
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", off)
	return []byte(b.String())
}

// xrefTablePDF builds a PDF with a classic cross-reference table.
func xrefTablePDF(entries, trailer string) []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	b.WriteString(pad())
	off := b.Len()
	b.WriteString("xref\n")
	b.WriteString(entries)
	b.WriteString("trailer\n")
	b.WriteString(trailer + "\n")
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", off)
	return []byte(b.String())
}

// validPDF builds a small, well-formed PDF with one page of text.
func validPDF() []byte {
	const content = "BT /F1 24 Tf 100 700 Td (Hello World) Tj ET\n"

	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R /MediaBox [0 0 612 792] >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
	}

	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, body := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}

	xrefOff := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n", len(objs)+1)
	b.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\n", len(objs)+1)
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", xrefOff)
	return []byte(b.String())
}

// buildPDF assembles objs (1-indexed bodies, without "N 0 obj"/"endobj")
// into a minimal classic-xref-table PDF.
func buildPDF(objs []string) []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, body := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xrefOff := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xrefOff)
	return []byte(b.String())
}

// objStmPDF builds a well-formed PDF that keeps its catalog, page tree and
// page inside an object stream, indexed by a cross-reference stream. This is
// the layout every /Size, /W, /Index and /N check has to keep working.
func objStmPDF() []byte { return objStmPDFWith("") }

// objStmPDFWith is objStmPDF with the object stream's /N and /First replaced
// by hdr, to exercise the checks on those values. An empty hdr keeps the
// correct ones.
func objStmPDFWith(hdr string) []byte {
	const content = "BT /F1 24 Tf 100 700 Td (Hello Stream) Tj ET\n"

	inner := []struct {
		num  int
		body string
	}{
		{1, "<< /Type /Catalog /Pages 2 0 R >>"},
		{2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>"},
		{3, "<< /Type /Page /Parent 2 0 R /Contents 4 0 R /MediaBox [0 0 612 792] >>"},
	}

	// An object stream holds a table of "number offset" pairs, then the
	// objects themselves starting at /First.
	var pairs, bodies strings.Builder
	for _, o := range inner {
		fmt.Fprintf(&pairs, "%d %d ", o.num, bodies.Len())
		bodies.WriteString(o.body + "\n")
	}
	first := pairs.Len()
	objstm := pairs.String() + bodies.String()

	var b strings.Builder
	b.WriteString("%PDF-1.5\n")
	off := map[int]int{}

	off[4] = b.Len()
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d >>\nstream\n%sendstream\nendobj\n", len(content), content)

	if hdr == "" {
		hdr = fmt.Sprintf("/N %d /First %d", len(inner), first)
	}
	off[5] = b.Len()
	fmt.Fprintf(&b, "5 0 obj\n<< /Type /ObjStm %s /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		hdr, len(objstm), objstm)

	off[6] = b.Len()
	var entries bytes.Buffer
	put := func(kind byte, f2 uint32, f3 uint16) {
		entries.WriteByte(kind)
		_ = binary.Write(&entries, binary.BigEndian, f2)
		_ = binary.Write(&entries, binary.BigEndian, f3)
	}
	put(0, 0, 65535) // object 0, free
	put(2, 5, 0)     // object 1, in stream 5 at index 0
	put(2, 5, 1)     // object 2
	put(2, 5, 2)     // object 3
	// The offsets below come from b.Len() on a test fixture a few hundred
	// bytes long, never anywhere near uint32's range.
	put(1, uint32(off[4]), 0) //nolint:gosec // G115: bounded test fixture length
	put(1, uint32(off[5]), 0) //nolint:gosec // G115: bounded test fixture length
	put(1, uint32(off[6]), 0) //nolint:gosec // G115: bounded test fixture length
	fmt.Fprintf(&b, "6 0 obj\n<< /Type /XRef /Size 7 /W [1 4 2] /Root 1 0 R /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		entries.Len(), entries.String())

	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", off[6])
	return []byte(b.String())
}

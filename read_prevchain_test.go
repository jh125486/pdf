// Mutation-coverage workstream (WS4): this file targets readXrefStream's and
// readXrefTable's /Prev-chain loops and the trailer /Size shrink/truncation
// logic in read.go. It is kept separate from the rest of the suite so it can
// be reviewed and merged independently of the other mutation-coverage
// workstreams running in parallel.
package pdf_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/jh125486/pdf"
)

// wsPrevEntry is a single W [1 1 1] cross-reference stream entry: type 1
// (in use), 1-byte offset 9, 1-byte generation 0. Its content is irrelevant
// to the /Prev-chain checks below, which fail before or independently of
// resolving the offset it encodes.
const wsPrevEntry = "\x01\x09\x00"

// wsPrevChainStreamPDF builds a minimal two-revision xref-stream PDF: a prev
// stream (object 1) written first, and an outer/newest stream (object 2)
// whose header is produced by outerHdrFn once the prev stream's byte offset
// is known. Both headers are caller supplied in full (including /Type,
// /Size and /W), so each case can exercise exactly one check in
// readXrefStream's /Prev loop without needing a fully well-formed document.
func wsPrevChainStreamPDF(prevHdr string, outerHdrFn func(prevOff int) string) []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.5\n")
	b.WriteString(pad())
	prevOff := b.Len()
	fmt.Fprintf(&b, "1 0 obj\n<< %s /Length %d >>\nstream\n%s\nendstream\nendobj\n", prevHdr, len(wsPrevEntry), wsPrevEntry)
	outerOff := b.Len()
	fmt.Fprintf(&b, "2 0 obj\n<< %s /Length %d >>\nstream\n%s\nendstream\nendobj\n", outerHdrFn(prevOff), len(wsPrevEntry), wsPrevEntry)
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", outerOff)
	return []byte(b.String())
}

// wsPrevChainNonStreamPDF is wsPrevChainStreamPDF's counterpart for the case
// where the object /Prev points at is not a stream at all: object 1 is a
// bare dictionary, with no "stream"..."endstream" body.
func wsPrevChainNonStreamPDF(outerHdrFn func(prevOff int) string) []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.5\n")
	b.WriteString(pad())
	prevOff := b.Len()
	b.WriteString("1 0 obj\n<< /Type /XRef /Size 1 >>\nendobj\n")
	outerOff := b.Len()
	fmt.Fprintf(&b, "2 0 obj\n<< %s /Length %d >>\nstream\n%s\nendstream\nendobj\n", outerHdrFn(prevOff), len(wsPrevEntry), wsPrevEntry)
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", outerOff)
	return []byte(b.String())
}

// wsPrevEqualSizeXrefStreamPDF is incrementalXrefStreamPDF's structure
// (objects 1-4 in an original revision, a Gen1 update to object 4, and a
// second xref stream chained back via /Prev), except both the prev stream's
// own /Size and the newest stream's /Size are set to the same value: the
// boundary at which readXrefStream's "psize > size" check must NOT fire.
func wsPrevEqualSizeXrefStreamPDF() []byte {
	gen0 := "BT /F1 12 Tf 10 10 Td (Gen0) Tj ET"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(gen0), gen0),
	}

	var b strings.Builder
	b.WriteString("%PDF-1.5\n")
	offsets := make([]int, len(objs)+1) // 1-indexed; offsets[5] is the xref stream itself
	for i, body := range objs {
		offsets[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}

	entry := func(kind byte, f2 uint32, f3 uint16) string {
		//nolint:gosec // G115: bounded test fixture length, truncation is intentional field packing
		return string([]byte{kind, byte(f2 >> 8), byte(f2)}) + string([]byte{byte(f3 >> 8), byte(f3)})
	}
	var entries strings.Builder
	entries.WriteString(entry(0, 0, 65535))
	for i := 1; i <= len(objs); i++ {
		//nolint:gosec // G115: bounded test fixture length
		entries.WriteString(entry(1, uint32(offsets[i]), 0))
	}
	xref0Off := b.Len()
	// /Size 5 matches the 5 entries written above (objects 0-4).
	fmt.Fprintf(&b, "5 0 obj\n<< /Type /XRef /Size 5 /W [1 2 2] /Root 1 0 R /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		entries.Len(), entries.String())
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", xref0Off)

	gen1 := "BT /F1 12 Tf 10 10 Td (Gen1) Tj ET"
	obj4Off := b.Len()
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(gen1), gen1)

	var entries1 strings.Builder
	//nolint:gosec // G115: bounded test fixture length
	entries1.WriteString(entry(1, uint32(obj4Off), 0))
	xref1Off := b.Len()
	// The newest stream's /Size is 5, exactly matching the prev stream's own
	// /Size above -- the "equal" boundary for the "psize > size" check.
	fmt.Fprintf(&b, "6 0 obj\n<< /Type /XRef /Size 5 /W [1 2 2] /Index [4 1] /Root 1 0 R /Prev %d /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		xref0Off, entries1.Len(), entries1.String())
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", xref1Off)
	return []byte(b.String())
}

// TestPrevChainStreamSizeBoundary covers readXrefStream's comparison of a
// prev stream's own /Size against the newest stream's /Size: equal succeeds,
// one more than the newest stream's /Size fails, and negative fails.
func TestPrevChainStreamSizeBoundary(t *testing.T) {
	t.Parallel()

	t.Run("prev Size equal to newest stream Size opens successfully", func(t *testing.T) {
		t.Parallel()

		data := wsPrevEqualSizeXrefStreamPDF()
		r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		if n := r.NumPage(); n != 1 {
			t.Fatalf("NumPage = %d, want 1 (requires resolving object 2 via Prev)", n)
		}
		text, err := r.Page(1).GetPlainText(nil)
		if err != nil {
			t.Fatalf("GetPlainText: %v", err)
		}
		if !strings.Contains(text, "Gen1") {
			t.Errorf("GetPlainText = %q, want the updated %q content", text, "Gen1")
		}
	})

	t.Run("prev Size one more than newest stream Size errors", func(t *testing.T) {
		t.Parallel()

		data := wsPrevChainStreamPDF(
			"/Type /XRef /Size 2 /W [1 1 1]",
			func(off int) string { return fmt.Sprintf("/Type /XRef /Size 1 /W [1 1 1] /Prev %d", off) },
		)
		err := openBytes(data)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "xref prev stream larger than last stream") {
			t.Errorf("error = %q, want it to contain %q", err.Error(), "xref prev stream larger than last stream")
		}
	})

	t.Run("negative prev Size errors", func(t *testing.T) {
		t.Parallel()

		data := wsPrevChainStreamPDF(
			"/Type /XRef /Size -1 /W [1 1 1]",
			func(off int) string { return fmt.Sprintf("/Type /XRef /Size 1 /W [1 1 1] /Prev %d", off) },
		)
		err := openBytes(data)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "negative xref prev stream Size -1") {
			t.Errorf("error = %q, want it to contain %q", err.Error(), "negative xref prev stream Size -1")
		}
	})
}

// TestPrevChainStreamLinkErrors covers the remaining checks in
// readXrefStream's /Prev loop: a missing /Type /XRef on the prev stream, a
// prev object that is not a stream at all, a /Prev value that is not an
// integer, and a /Prev pointing past EOF.
func TestPrevChainStreamLinkErrors(t *testing.T) {
	t.Parallel()

	t.Run("prev stream missing Type XRef", func(t *testing.T) {
		t.Parallel()

		data := wsPrevChainStreamPDF(
			"/Size 1 /W [1 1 1]",
			func(off int) string { return fmt.Sprintf("/Type /XRef /Size 1 /W [1 1 1] /Prev %d", off) },
		)
		err := openBytes(data)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "xref prev stream does not have type XRef") {
			t.Errorf("error = %q, want it to contain %q", err.Error(), "xref prev stream does not have type XRef")
		}
	})

	t.Run("prev object is not a stream", func(t *testing.T) {
		t.Parallel()

		data := wsPrevChainNonStreamPDF(
			func(off int) string { return fmt.Sprintf("/Type /XRef /Size 1 /W [1 1 1] /Prev %d", off) },
		)
		err := openBytes(data)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "xref prev stream not found") {
			t.Errorf("error = %q, want it to contain %q", err.Error(), "xref prev stream not found")
		}
	})

	t.Run("Prev is not an integer", func(t *testing.T) {
		t.Parallel()

		data := xrefStreamPDF("/Size 1 /W [1 1 1] /Prev /NotANumber", wsPrevEntry)
		err := openBytes(data)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "xref Prev is not integer") {
			t.Errorf("error = %q, want it to contain %q", err.Error(), "xref Prev is not integer")
		}
	})

	t.Run("Prev points past EOF", func(t *testing.T) {
		t.Parallel()

		data := xrefStreamPDF("/Size 1 /W [1 1 1] /Prev 999999999", wsPrevEntry)
		err := openBytes(data)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "malformed PDF: xref Prev:") {
			t.Errorf("error = %q, want it to contain %q", err.Error(), "malformed PDF: xref Prev:")
		}
	})
}

// wsPrevBadTargetTablePDF builds a classic xref-table PDF whose trailer
// /Prev points at a byte offset that holds a bareword token other than
// "xref".
func wsPrevBadTargetTablePDF() []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	b.WriteString(pad())
	notXrefOff := b.Len()
	b.WriteString("notxref\n")
	xrefOff := b.Len()
	b.WriteString("xref\n0 1\n0000000000 65535 f \ntrailer\n")
	fmt.Fprintf(&b, "<< /Size 1 /Root 1 0 R /Prev %d >>\n", notXrefOff)
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", xrefOff)
	return []byte(b.String())
}

// wsPrevMissingTrailerTablePDF builds a classic xref-table PDF whose /Prev
// section is a well-formed "xref" section but is followed by the "trailer"
// keyword and then a bare integer instead of a "<< ... >>" dictionary.
func wsPrevMissingTrailerTablePDF() []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	b.WriteString(pad())
	prevOff := b.Len()
	b.WriteString("xref\n0 1\n0000000000 65535 f \ntrailer\n42\n")
	xrefOff := b.Len()
	b.WriteString("xref\n0 1\n0000000000 65535 f \ntrailer\n")
	fmt.Fprintf(&b, "<< /Size 1 /Root 1 0 R /Prev %d >>\n", prevOff)
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", xrefOff)
	return []byte(b.String())
}

// TestPrevChainTableErrors covers readXrefTable's /Prev-chain checks: a
// /Prev value pointing somewhere other than the "xref" keyword, and a prev
// section not followed by a trailer dictionary.
func TestPrevChainTableErrors(t *testing.T) {
	t.Parallel()

	t.Run("Prev does not point to xref keyword", func(t *testing.T) {
		t.Parallel()

		err := openBytes(wsPrevBadTargetTablePDF())
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "xref Prev does not point to xref") {
			t.Errorf("error = %q, want it to contain %q", err.Error(), "xref Prev does not point to xref")
		}
	})

	t.Run("prev section not followed by trailer dictionary", func(t *testing.T) {
		t.Parallel()

		err := openBytes(wsPrevMissingTrailerTablePDF())
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "xref Prev table not followed by trailer dictionary") {
			t.Errorf("error = %q, want it to contain %q", err.Error(), "xref Prev table not followed by trailer dictionary")
		}
	})
}

// wsPrevSizeShrinkTablePDF builds a classic xref-table PDF with five real
// objects (1-5) plus the mandatory free entry 0, for a table of length 6,
// and a trailer whose /Size is the caller-supplied value rather than the
// table's full length. The trailer also carries /ProbeLow (object 1) and
// /ProbeHigh (object 5, the highest real object number) references, so a
// test can check whether each end of the table survived the shrink.
func wsPrevSizeShrinkTablePDF(size int) []byte {
	objs := []string{
		"<< /Marker (One) >>",
		"<< /Marker (Two) >>",
		"<< /Marker (Three) >>",
		"<< /Marker (Four) >>",
		"<< /Marker (Five) >>",
	}

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
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R /ProbeLow 1 0 R /ProbeHigh %d 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		size, len(objs), xrefOff)
	return []byte(b.String())
}

// TestTrailerSizeShrink covers the trailer /Size shrink/truncation logic in
// readXrefTable: an accumulated table of length 6 (objects 0-5) with a
// trailer /Size of 4 must truncate the table so that object 5 becomes
// unresolvable, while a trailer /Size of 6 (the table's full length, the
// exact boundary) must leave every object resolvable. This is the pair that
// kills a "<" vs "<=" mutant on the shrink comparison: at size == len(table)
// the "<" comparison is false (no truncation) while "<=" would be true
// (wrongly truncating away the very last object).
func TestTrailerSizeShrink(t *testing.T) {
	t.Parallel()

	t.Run("Size below table length truncates the table", func(t *testing.T) {
		t.Parallel()

		data := wsPrevSizeShrinkTablePDF(4)
		r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		low := r.Trailer().Key("ProbeLow")
		if low.IsNull() {
			t.Fatal("ProbeLow (object 1, within the shrunk table): got null, want resolved")
		}
		if got := low.Key("Marker").RawString(); got != "One" {
			t.Errorf("ProbeLow /Marker = %q, want %q", got, "One")
		}
		high := r.Trailer().Key("ProbeHigh")
		if !high.IsNull() {
			t.Errorf("ProbeHigh (object 5, truncated away by /Size 4): got Kind() = %v, want Null", high.Kind())
		}
	})

	t.Run("Size equal to table length keeps every object resolvable", func(t *testing.T) {
		t.Parallel()

		data := wsPrevSizeShrinkTablePDF(6)
		r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		high := r.Trailer().Key("ProbeHigh")
		if high.IsNull() {
			t.Fatal("ProbeHigh (object 5, at the exact /Size boundary): got null, want resolved")
		}
		if got := high.Key("Marker").RawString(); got != "Five" {
			t.Errorf("ProbeHigh /Marker = %q, want %q", got, "Five")
		}
	})
}

// TestTrailerSizeInvalid covers the trailer /Size validity checks: negative
// and missing entirely.
func TestTrailerSizeInvalid(t *testing.T) {
	t.Parallel()

	entries := "0 1\n0000000000 65535 f \n"

	t.Run("negative trailer Size", func(t *testing.T) {
		t.Parallel()

		err := openBytes(xrefTablePDF(entries, "<< /Size -1 >>"))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "negative trailer /Size -1") {
			t.Errorf("error = %q, want it to contain %q", err.Error(), "negative trailer /Size -1")
		}
	})

	t.Run("trailer missing Size entry", func(t *testing.T) {
		t.Parallel()

		err := openBytes(xrefTablePDF(entries, "<< /Root 1 0 R >>"))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "trailer missing /Size entry") {
			t.Errorf("error = %q, want it to contain %q", err.Error(), "trailer missing /Size entry")
		}
	})
}

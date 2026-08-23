package pdf_test

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/jh125486/pdf"
)

// TestNewReaderMaliciousPDF reproduces the exact attack vector from
// MM-63434: a PDF with millions of "0 0 obj" tokens that triggers deep
// recursion during NewReader's xref parsing. With the fix, NewReader
// returns an error (via a recovered panic) rather than crashing the
// process.
func TestNewReaderMaliciousPDF(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.0\n")
	for range 10_000 {
		buf.WriteString("0\n0\nobj\n")
	}
	buf.WriteString("startxref\n0\n%%EOF\n")

	data := buf.Bytes()
	if _, err := pdf.NewReader(bytes.NewReader(data), int64(len(data))); err == nil {
		t.Fatal("expected error from malicious PDF, got nil")
	}
}

// TestMalformedXref covers the cross-reference bounds checks. Every case
// here previously panicked, or allocated tens of gigabytes trying to
// preallocate a table for a file of a few hundred bytes.
func TestMalformedXref(t *testing.T) {
	t.Parallel()

	entry := "\x01\x09\x00" // one W [1 1 1] entry: type 1, offset 9, gen 0

	tests := []struct {
		name string
		data []byte
	}{
		// /Size preallocates the table directly.
		{"size huge", xrefStreamPDF("/Size 4611686018427387904 /W [1 1 1]", entry)},
		{"size negative", xrefStreamPDF("/Size -1 /W [1 1 1]", entry)},
		{"size past file length", xrefStreamPDF("/Size 100000000 /W [1 1 1]", entry)},

		// /Index names the object numbers a subsection describes.
		{"index start huge", xrefStreamPDF("/Size 1 /W [1 1 1] /Index [4000000000 1]", entry)},
		{"index start negative", xrefStreamPDF("/Size 4 /W [1 1 1] /Index [-1 1]", entry)},
		{"index count negative", xrefStreamPDF("/Size 4 /W [1 1 1] /Index [0 -1]", entry)},
		{"index odd length", xrefStreamPDF("/Size 4 /W [1 1 1] /Index [0]", entry)},

		// /W holds the byte width of each field.
		{"w negative", xrefStreamPDF("/Size 4 /W [-1 2 1]", entry)},
		{"w huge", xrefStreamPDF("/Size 4 /W [2000000000 2000000000 2000000000]", entry)},
		{"w too short", xrefStreamPDF("/Size 4 /W [1 1]", entry)},
		{"w missing", xrefStreamPDF("/Size 4", entry)},

		// Classic cross-reference tables take the same values as tokens.
		{"table start huge", xrefTablePDF("4000000000 1\n0000000016 00000 n \n", "<< /Size 5 >>")},
		{"table start negative", xrefTablePDF("-1 1\n0000000016 00000 n \n", "<< /Size 5 >>")},
		{"table count negative", xrefTablePDF("0 -1\n0000000016 00000 n \n", "<< /Size 5 >>")},
		{"trailer size negative", xrefTablePDF("0 1\n0000000000 65535 f \n", "<< /Size -1 >>")},
		{"trailer size missing", xrefTablePDF("0 1\n0000000000 65535 f \n", "<< /Root 1 0 R >>")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var err error
			mustNotCrash(t, func() { err = openBytes(tt.data) })
			if err == nil {
				t.Error("accepted a malformed PDF, want an error")
			}
		})
	}

	// An /Index that reaches past the preallocated table is tolerated, the
	// same way readXrefTableData tolerates a classic subsection past the
	// end: the table grows, now within a bound. Growing capacity does not
	// necessarily grow length past the index, which is what used to panic.
	t.Run("index start past table grows safely", func(t *testing.T) {
		t.Parallel()

		for _, start := range []int{1, 5, 9, 100, 1000} {
			hdr := fmt.Sprintf("/Size 0 /W [1 1 1] /Index [%d 1]", start)
			mustNotCrash(t, func() { _ = openBytes(xrefStreamPDF(hdr, entry)) })
		}
	})
}

// TestMalformedFileStructure covers the checks around the header, the
// trailing %%EOF window and startxref.
func TestMalformedFileStructure(t *testing.T) {
	t.Parallel()

	// A file ending in newlines emptied the end-of-file buffer and then read
	// buf[-1], because && binds tighter than || in the strip loop.
	trailingNewlines := append([]byte("%PDF-1.5\n"), bytes.Repeat([]byte("x"), 50)...)
	trailingNewlines = append(trailingNewlines, bytes.Repeat([]byte("\n"), 100)...)

	tests := []struct {
		name string
		data []byte
	}{
		{"trailing newlines", trailingNewlines},
		{"all newlines after header", append([]byte("%PDF-1.5\n"), bytes.Repeat([]byte("\n"), 120)...)},
		{"empty", nil},
		{"header only", []byte("%PDF-1.5\n")},
		{"truncated", []byte("%PDF-1.5\n" + pad() + "startxref\n9\n%%EOF\n")},
		{"startxref huge", []byte("%PDF-1.5\n" + pad() + "startxref\n999999999999\n%%EOF\n")},
		{"startxref negative", []byte("%PDF-1.5\n" + pad() + "startxref\n-5\n%%EOF\n")},
		{"startxref not a number", []byte("%PDF-1.5\n" + pad() + "startxref\nxyz\n%%EOF\n")},
		{"no eof marker", []byte("%PDF-1.5\n" + pad() + "startxref\n9\n")},
		{"bad version", []byte("%PDF-9.9\n" + pad() + "startxref\n9\n%%EOF\n")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var err error
			mustNotCrash(t, func() { err = openBytes(tt.data) })
			if err == nil {
				t.Error("accepted a malformed PDF, want an error")
			}
		})
	}
}

// TestXrefTableGenerationOutOfRange guards a real truncation bug flagged by
// CodeQL (go/incorrect-integer-conversion): readXrefTableData read a
// classic-table entry's generation number as int64 and narrowed it straight
// to uint16 with no bound check. A crafted generation field larger than
// uint16's range (e.g. "99999999999", still a valid integer token) silently
// wrapped into an unrelated small value instead of being rejected.
func TestXrefTableGenerationOutOfRange(t *testing.T) {
	t.Parallel()

	data := xrefTablePDF(
		"0 2\n0000000000 65535 f \n0000000016 99999999999 n \n",
		"<< /Size 2 /Root 1 0 R >>",
	)
	if err := openBytes(data); err == nil {
		t.Error("accepted an out-of-range xref generation number, want an error")
	}
}

// TestValidPDFStillParses guards against the bounds checks rejecting
// well-formed input.
func TestValidPDFStillParses(t *testing.T) {
	t.Parallel()

	data := validPDF()

	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if n := r.NumPage(); n != 1 {
		t.Errorf("NumPage = %d, want 1", n)
	}

	p := r.Page(1)
	if p.V.IsNull() {
		t.Fatal("Page(1) is null")
	}
	if got := p.V.Key("Type").Name(); got != "Page" {
		t.Errorf("page /Type = %q, want %q", got, "Page")
	}
	// Resources is inherited through /Parent, exercising findInherited.
	if fonts := p.Fonts(); len(fonts) != 1 || fonts[0] != "F1" {
		t.Errorf("Fonts = %v, want [F1]", fonts)
	}
	if got := p.Font("F1").BaseFont(); got != "Helvetica" {
		t.Errorf("BaseFont = %q, want %q", got, "Helvetica")
	}

	text, err := p.GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	if !strings.Contains(text, "Hello World") {
		t.Errorf("GetPlainText = %q, want it to contain %q", text, "Hello World")
	}

	rd, err := r.GetPlainText()
	if err != nil {
		t.Fatalf("Reader.GetPlainText: %v", err)
	}
	all, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("reading text: %v", err)
	}
	if !strings.Contains(string(all), "Hello World") {
		t.Errorf("Reader.GetPlainText = %q, want it to contain %q", all, "Hello World")
	}

	if c := p.Content(); len(c.Text) == 0 {
		t.Error("Content returned no text")
	}
	if _, err := r.GetStyledTexts(); err != nil {
		t.Errorf("GetStyledTexts: %v", err)
	}
	if _, err := p.GetTextByRow(); err != nil {
		t.Errorf("GetTextByRow: %v", err)
	}
	if _, err := p.GetTextByColumn(); err != nil {
		t.Errorf("GetTextByColumn: %v", err)
	}
	r.Outline() // must not panic
}

// TestObjectStreamStillResolves guards the object stream path: the /N loop
// now stops at the first non-integer pair instead of trusting the declared
// count, and offsets are range checked.
func TestObjectStreamStillResolves(t *testing.T) {
	t.Parallel()

	data := objStmPDF()

	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if n := r.NumPage(); n != 1 {
		t.Fatalf("NumPage = %d, want 1", n)
	}

	// Reaching the page at all means objects 1, 2 and 3 were resolved out of
	// the object stream.
	p := r.Page(1)
	if p.V.IsNull() {
		t.Fatal("Page(1) is null")
	}
	if got := p.V.Key("Type").Name(); got != "Page" {
		t.Errorf("page /Type = %q, want %q", got, "Page")
	}
	text, err := p.GetPlainText(nil)
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	if !strings.Contains(text, "Hello Stream") {
		t.Errorf("GetPlainText = %q, want it to contain %q", text, "Hello Stream")
	}
}

// TestMalformedObjectStream covers the object stream header. /N is a
// declared count that the pair loop no longer trusts, and /First and the
// per-object offsets are file supplied positions used to seek.
func TestMalformedObjectStream(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		hdr  string
	}{
		{"n huge", "/N 9223372036854775807 /First 14"},
		{"n negative", "/N -1 /First 14"},
		{"n zero", "/N 0 /First 14"},
		{"first zero", "/N 3 /First 0"},
		{"first negative", "/N 3 /First -1"},
		{"first huge", "/N 3 /First 9223372036854775807"},
		{"first missing", "/N 3"},
		{"n missing", "/First 14"},
		{"both missing", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hdr := tt.hdr
			if hdr == "" {
				hdr = " " // keep objStmPDFWith from filling in the valid header
			}
			data := objStmPDFWith(hdr)
			// Opening may succeed, since the object stream is only read when
			// an object inside it is resolved. Either way nothing may crash
			// or hang, and any failure has to arrive as an error.
			mustNotCrash(t, func() {
				r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
				if err != nil {
					t.Logf("NewReader reported: %v", err)
					return
				}
				if _, err := r.GetPlainText(); err != nil {
					t.Logf("GetPlainText reported: %v", err)
				}
			})
		})
	}
}

// TestConcurrentReads pins the property that made resolve carry its
// recursion depth as a parameter instead of on the Reader: an opened Reader
// is immutable, so several goroutines may read from it at once. Run under
// -race to be meaningful.
func TestConcurrentReads(t *testing.T) {
	t.Parallel()

	data := objStmPDF()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	const goroutines = 8
	errs := make(chan error, goroutines)
	for range goroutines {
		go func() {
			for range 20 {
				text, err := r.Page(1).GetPlainText(nil)
				if err != nil {
					errs <- err
					return
				}
				if !strings.Contains(text, "Hello Stream") {
					errs <- fmt.Errorf("got %q", text)
					return
				}
			}
			errs <- nil
		}()
	}
	for range goroutines {
		if err := <-errs; err != nil {
			t.Errorf("concurrent read: %v", err)
		}
	}
}

// TestHeaderTrailingSpace guards ledongthuc/pdf#22: libtiff/tiff2pdf emits a
// header line like "%PDF-1.1 \n" (a space before the newline), which real
// readers accept but this package used to reject as an invalid header.
func TestHeaderTrailingSpace(t *testing.T) {
	t.Parallel()

	data := validPDF()
	data = bytes.Replace(data, []byte("%PDF-1.4\n"), []byte("%PDF-1.4 \n"), 1)

	if _, err := pdf.NewReader(bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("NewReader with space-padded header: %v", err)
	}
}

// TestStreamNotPresentEndsCleanly guards ledongthuc/pdf#24 and #35: a page
// whose /Contents resolves to a non-stream value (here, a free/missing
// object) used to make errorReadCloser hand buffer.reload a fresh
// fmt.Errorf("stream not present") on every call. Because that error had no
// identity to compare against, reload treated it as an unrecoverable read
// error and panicked with "malformed PDF: reading at offset 0: stream not
// present" instead of simply ending the (empty) content stream.
func TestStreamNotPresentEndsCleanly(t *testing.T) {
	t.Parallel()

	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		// /Contents 9 0 R points at an object number with no xref entry, so
		// it resolves to a null Value rather than a stream.
		"<< /Type /Page /Parent 2 0 R /Contents 9 0 R /MediaBox [0 0 612 792] >>",
	}
	data := buildPDF(objs)

	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	mustNotCrash(t, func() {
		p := r.Page(1)
		if text, err := p.GetPlainText(nil); err != nil {
			t.Errorf("GetPlainText: %v", err)
		} else if text != "" {
			t.Errorf("GetPlainText = %q, want empty", text)
		}
	})
}

// TestReaderRecoversFromMalformedFilter guards ledongthuc/pdf#57: setting up
// a stream's filter chain (unknown filter name, corrupt zlib header, an
// invalid PNG predictor) panics from inside Value.Reader, which has no
// error return. GetPlainText and friends recover from panics in the
// content-stream path, but a caller reading an image or other embedded
// stream directly -- the normal way to get at one -- had nothing between it
// and the panic. A single malformed image in an otherwise valid PDF crashed
// the whole process instead of surfacing as an error from Read.
func TestReaderRecoversFromMalformedFilter(t *testing.T) {
	t.Parallel()

	imgData := "garbage-not-flate-data"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] " +
			"/Resources << /XObject << /Im0 4 0 R >> >> /Contents 5 0 R >>",
		fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 1 /Height 1 "+
			"/ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode "+
			"/DecodeParms << /Predictor 99 /Columns 1 >> /Length %d >>\nstream\n%s\nendstream",
			len(imgData), imgData),
		"<< /Length 0 >>\nstream\n\nendstream",
	}
	data := buildPDF(objs)

	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	var rc io.ReadCloser
	mustNotCrash(t, func() {
		rc = r.Page(1).Resources().Key("XObject").Key("Im0").Reader()
	})
	if rc == nil {
		t.Fatal("Reader() returned nil after recovering")
	}
	defer rc.Close()
	if _, err := io.ReadAll(rc); err == nil {
		t.Error("reading a malformed image filter chain: got nil error, want one")
	}
}

// aesEncryptMetadataFalsePDFBase64 is the minimal repro from
// ledongthuc/pdf#82: a one-page "hello world" PDF encrypted with V=4, R=4,
// 128-bit AES (AESV2), an empty user and owner password, and
// /EncryptMetadata false. Built with `qpdf --encrypt "" "" 128 --use-aes=y
// --force-V4 --cleartext-metadata -- in.pdf repro.pdf` and confirmed to open
// with an empty password in qpdf, pdfcpu, and Adobe.
const aesEncryptMetadataFalsePDFBase64 = `` +
	`JVBERi0xLjYKJb/3ov4KMSAwIG9iago8PCAvUGFnZXMgMiAwIFIgL1R5cGUgL0NhdGFsb2cgPj4K
ZW5kb2JqCjIgMCBvYmoKPDwgL0NvdW50IDEgL0tpZHMgWyAzIDAgUiBdIC9UeXBlIC9QYWdlcyA+
PgplbmRvYmoKMyAwIG9iago8PCAvQ29udGVudHMgNCAwIFIgL01lZGlhQm94IFsgMCAwIDIwMCAx
MDAgXSAvUGFyZW50IDIgMCBSIC9SZXNvdXJjZXMgPDwgL0ZvbnQgPDwgL0YxIDUgMCBSID4+ID4+
IC9UeXBlIC9QYWdlID4+CmVuZG9iago0IDAgb2JqCjw8IC9MZW5ndGggODAgL0ZpbHRlciAvRmxh
dGVEZWNvZGUgPj4Kc3RyZWFtCnEOVaJCEl2iYC6i4jlIx50rXT2ZRhamhIIQWZROnsqigqG2ql57
ToeOpRCn7FdtNPAcxxdfWjM7djpeNEsEdS/+9dAZLbRQ+siWE0qVeDOZZW5kc3RyZWFtCmVuZG9i
ago1IDAgb2JqCjw8IC9CYXNlRm9udCAvSGVsdmV0aWNhIC9TdWJ0eXBlIC9UeXBlMSAvVHlwZSAv
Rm9udCA+PgplbmRvYmoKNiAwIG9iago8PCAvQ0YgPDwgL1N0ZENGIDw8IC9BdXRoRXZlbnQgL0Rv
Y09wZW4gL0NGTSAvQUVTVjIgL0xlbmd0aCAxNiA+PiA+PiAvRW5jcnlwdE1ldGFkYXRhIGZhbHNl
IC9GaWx0ZXIgL1N0YW5kYXJkIC9MZW5ndGggMTI4IC9PIDwzNjQ1MWJkMzlkNzUzYjdjMWQxMDky
MmMyOGU2NjY1YWE0ZjMzNTNmYjAzNDhiNTM2ODkzZTNiMWRiNWM1NzliPiAvT0UgPD4gL1AgLTQg
L1IgNCAvU3RtRiAvU3RkQ0YgL1N0ckYgL1N0ZENGIC9VIDw3ODczNTU0ZjI5ZTMwNzZkZWI3NmU3
YmUwYWQ0NmU1YjAwMjE0NDY5OTBiOWU0MTE0MDcxYTRkOTEwNDk4NGMxPiAvVUUgPD4gL1YgNCA+
PgplbmRvYmoKeHJlZgowIDcKMDAwMDAwMDAwMCA2NTUzNSBmIAowMDAwMDAwMDE1IDAwMDAwIG4g
CjAwMDAwMDAwNjQgMDAwMDAgbiAKMDAwMDAwMDEyMyAwMDAwMCBuIAowMDAwMDAwMjUxIDAwMDAw
IG4gCjAwMDAwMDA0MDEgMDAwMDAgbiAKMDAwMDAwMDQ3MSAwMDAwMCBuIAp0cmFpbGVyIDw8IC9S
b290IDEgMCBSIC9TaXplIDcgL0lEIFs8MjAyNDlkNjNkOGM0ODhmYTcxNjc1NDAzMjkzYmUzZjE+
PGI2NzRhMzY5MmE1NzllMzA3MGI1ZDBhYmRkMDE4ZjRkPl0gL0VuY3J5cHQgNiAwIFIgPj4Kc3Rh
cnR4cmVmCjgwNwolJUVPRgo=`

// TestAESEncryptMetadataFalse guards ledongthuc/pdf#82: initEncrypt never
// implemented PDF 32000-1:2008 Algorithm 2 step (f) -- hashing in four 0xFF
// bytes when /EncryptMetadata is false, for R>=4. Every key derived for
// such a file was wrong, so the U check always failed and a correctly empty
// password came back as ErrInvalidPassword. This encryption combination
// (content encrypted, metadata left in cleartext for indexing) is common
// from e-signature/export tools, so this rejected otherwise normal files
// outright.
func TestAESEncryptMetadataFalse(t *testing.T) {
	t.Parallel()

	data, err := base64.StdEncoding.DecodeString(
		strings.ReplaceAll(aesEncryptMetadataFalsePDFBase64, "\n", ""))
	if err != nil {
		t.Fatalf("decoding embedded test PDF: %v", err)
	}

	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	rd, err := r.GetPlainText()
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	text, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("reading text: %v", err)
	}
	if !strings.Contains(string(text), "hello world") {
		t.Errorf("GetPlainText = %q, want it to contain %q", text, "hello world")
	}
}

package pdf_test

import (
	"bytes"
	"compress/zlib"
	"encoding/ascii85"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
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

// TestValueAccessors covers Value's basic accessors across every Kind:
// Bool, Kind() for Bool and Real, Float64, RawString/Text (both the plain
// and UTF-16 branches), TextFromUTF16, and Len for a non-Array value. The
// values live directly on the Catalog dict for convenience; nothing reads
// them but the test.
func TestValueAccessors(t *testing.T) {
	t.Parallel()

	// <FEFF0041> is a UTF-16BE string (BOM + 'A') written as a hex string.
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R /BoolKey true /RealKey 12.5 " +
			"/Utf16Key <FEFF0041> /PlainKey (hello) >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>",
	}
	data := buildPDF(objs)
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	root := r.Trailer().Key("Root")

	if got := root.Key("BoolKey").Kind(); got != pdf.Bool {
		t.Errorf("BoolKey.Kind() = %v, want Bool", got)
	}
	if got := root.Key("BoolKey").Bool(); got != true {
		t.Errorf("BoolKey.Bool() = %v, want true", got)
	}
	if got := root.Key("PlainKey").Bool(); got != false {
		t.Errorf("non-bool.Bool() = %v, want false", got)
	}

	if got := root.Key("RealKey").Kind(); got != pdf.Real {
		t.Errorf("RealKey.Kind() = %v, want Real", got)
	}
	if got := root.Key("RealKey").Float64(); got != 12.5 {
		t.Errorf("RealKey.Float64() = %v, want 12.5", got)
	}

	if got := root.Key("Utf16Key").Text(); got != "A" {
		t.Errorf("Utf16Key.Text() = %q, want %q", got, "A")
	}
	if got := root.Key("Utf16Key").TextFromUTF16(); got != "\uFEFFA" {
		t.Errorf("Utf16Key.TextFromUTF16() = %q, want %q", got, "\uFEFFA")
	}
	if got := root.Key("PlainKey").Text(); got != "hello" {
		t.Errorf("PlainKey.Text() = %q, want %q", got, "hello")
	}
	// TextFromUTF16 on a Value that isn't Kind() == String returns "".
	if got := root.Key("BoolKey").TextFromUTF16(); got != "" {
		t.Errorf("non-string.TextFromUTF16() = %q, want empty", got)
	}
	// TextFromUTF16 on an odd-length string returns "".
	if got := root.Key("PlainKey").TextFromUTF16(); got != "" {
		t.Errorf("odd-length TextFromUTF16() = %q, want empty", got)
	}

	// Len on a non-Array value returns 0.
	if got := root.Key("BoolKey").Len(); got != 0 {
		t.Errorf("non-array Len() = %d, want 0", got)
	}
}

// TestValueStringFormatting covers Value.String (and the objfmt helper it
// wraps) across dict, array, stream, name, plain string, and unresolved
// objptr formatting. The objptr case is reached because objfmt formats an
// array's raw elements directly, without resolving indirect references the
// way Index does.
func TestValueStringFormatting(t *testing.T) {
	t.Parallel()

	content := "BT ET\n"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R /Refs [4 0 R] /Nested << /A (x) >> >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
	}
	data := buildPDF(objs)
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	root := r.Trailer().Key("Root")

	if got := root.Key("Nested").String(); got != `<</A "x">>` {
		t.Errorf("dict String() = %q, want %q", got, `<</A "x">>`)
	}
	if got := root.Key("Refs").String(); got != "[4 0 R]" {
		t.Errorf("array of unresolved ref String() = %q, want %q", got, "[4 0 R]")
	}
	if got := r.Page(1).V.Key("Contents").String(); !strings.HasPrefix(got, "<</Length 6>>@") {
		t.Errorf("stream String() = %q, want it to start with %q", got, "<</Length 6>>@")
	}
}

// TestOpen covers Open's success and not-found paths against a real file on
// disk (NewReaderEncrypted is exercised in-memory everywhere else in this
// package; Open is the only entry point that also does its own file I/O).
func TestOpen(t *testing.T) {
	t.Parallel()

	t.Run("existing file", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := dir + "/test.pdf"
		if err := os.WriteFile(path, validPDF(), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		f, r, err := pdf.Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer f.Close()
		if n := r.NumPage(); n != 1 {
			t.Errorf("NumPage = %d, want 1", n)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		t.Parallel()

		_, _, err := pdf.Open("/nonexistent/path/does-not-exist.pdf")
		if err == nil {
			t.Error("Open on a missing file: got nil error, want one")
		}
	})
}

// asciiEncode returns s ASCII85-encoded, for building /ASCII85Decode stream
// fixtures.
func asciiEncode(s string) string {
	var buf bytes.Buffer
	w := ascii85.NewEncoder(&buf)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return buf.String()
}

// TestValueReaderFilters covers Value.Reader/applyFilter's less-common
// paths: ASCII85Decode (success and unexpected DecodeParms), an unknown
// Filter name, and a FlateDecode /Predictor value other than 12 or absent.
// Each malformed case panics inside applyFilter; Reader recovers and
// reports it through the returned ReadCloser's Read, which is also what
// covers errorReadCloser.Read (also reached directly by the non-stream
// case).
func TestValueReaderFilters(t *testing.T) {
	t.Parallel()

	t.Run("non-stream value", func(t *testing.T) {
		t.Parallel()

		data := buildPDF([]string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [] /Count 0 >>"})
		r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		rc := r.Trailer().Key("Root").Key("NoSuchKey").Reader()
		defer rc.Close()
		buf := make([]byte, 8)
		if _, err := rc.Read(buf); err == nil {
			t.Error("Read on a non-stream value's Reader(): got nil error, want one")
		}
	})

	t.Run("ASCII85Decode success", func(t *testing.T) {
		t.Parallel()

		encoded := asciiEncode("Hello, ASCII85!")
		data := buildPDF([]string{
			"<< /Type /Catalog /Pages 2 0 R /Blob 3 0 R >>",
			"<< /Type /Pages /Kids [] /Count 0 >>",
			fmt.Sprintf("<< /Filter /ASCII85Decode /Length %d >>\nstream\n%s\nendstream", len(encoded), encoded),
		})
		r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		rc := r.Trailer().Key("Root").Key("Blob").Reader()
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("reading ASCII85Decode stream: %v", err)
		}
		if string(got) != "Hello, ASCII85!" {
			t.Errorf("decoded = %q, want %q", got, "Hello, ASCII85!")
		}
	})

	t.Run("ASCII85Decode with unexpected DecodeParms", func(t *testing.T) {
		t.Parallel()

		encoded := asciiEncode("x")
		data := buildPDF([]string{
			"<< /Type /Catalog /Pages 2 0 R /Blob 3 0 R >>",
			"<< /Type /Pages /Kids [] /Count 0 >>",
			fmt.Sprintf("<< /Filter /ASCII85Decode /DecodeParms << /Unexpected 1 >> /Length %d >>\nstream\n%s\nendstream", len(encoded), encoded),
		})
		r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		rc := r.Trailer().Key("Root").Key("Blob").Reader()
		defer rc.Close()
		if _, err := io.ReadAll(rc); err == nil {
			t.Error("ASCII85Decode with unexpected DecodeParms: got nil error, want one")
		}
	})

	t.Run("unknown filter name", func(t *testing.T) {
		t.Parallel()

		data := buildPDF([]string{
			"<< /Type /Catalog /Pages 2 0 R /Blob 3 0 R >>",
			"<< /Type /Pages /Kids [] /Count 0 >>",
			"<< /Filter /NotAFilter /Length 3 >>\nstream\nabc\nendstream",
		})
		r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		rc := r.Trailer().Key("Root").Key("Blob").Reader()
		defer rc.Close()
		if _, err := io.ReadAll(rc); err == nil {
			t.Error("unknown filter name: got nil error, want one")
		}
	})

	t.Run("FlateDecode unknown predictor", func(t *testing.T) {
		t.Parallel()

		var zbuf bytes.Buffer
		zw := zlib.NewWriter(&zbuf)
		_, _ = zw.Write([]byte("hello"))
		_ = zw.Close()

		data := buildPDF([]string{
			"<< /Type /Catalog /Pages 2 0 R /Blob 3 0 R >>",
			"<< /Type /Pages /Kids [] /Count 0 >>",
			fmt.Sprintf("<< /Filter /FlateDecode /DecodeParms << /Predictor 2 /Columns 1 >> /Length %d >>\nstream\n%s\nendstream",
				zbuf.Len(), zbuf.String()),
		})
		r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		rc := r.Trailer().Key("Root").Key("Blob").Reader()
		defer rc.Close()
		if _, err := io.ReadAll(rc); err == nil {
			t.Error("FlateDecode with /Predictor 2: got nil error, want one")
		}
	})
}

// incrementalTablePDF builds a PDF with an incremental update using classic
// cross-reference tables: an original revision (objects 1-4, page content
// "Gen0") followed by an update that replaces object 4's content with
// "Gen1" and points its trailer's /Prev back at the original xref table.
// This is the standard incremental-update shape a PDF editor produces, and
// is what exercises readXrefTable's own /Prev chase.
func incrementalTablePDF() []byte {
	gen0 := "BT /F1 12 Tf 10 10 Td (Gen0) Tj ET"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(gen0), gen0),
	}

	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, body := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xref0Off := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref0Off)

	// Incremental update: a new body for object 4 only.
	gen1 := "BT /F1 12 Tf 10 10 Td (Gen1) Tj ET"
	obj4Off := b.Len()
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(gen1), gen1)
	xref1Off := b.Len()
	fmt.Fprintf(&b, "xref\n4 1\n%010d 00000 n \n", obj4Off)
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R /Prev %d >>\nstartxref\n%d\n%%%%EOF\n",
		len(objs)+1, xref0Off, xref1Off)
	return []byte(b.String())
}

// TestIncrementalUpdateTableChain covers readXrefTable's /Prev chase: the
// final revision's table only describes object 4, so objects 1-3 must be
// found by following /Prev back to the original table, and object 4 itself
// must resolve to the newer of the two entries (the update's table is read
// before Prev is followed, and an already-set table slot is left alone).
func TestIncrementalUpdateTableChain(t *testing.T) {
	t.Parallel()

	data := incrementalTablePDF()
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
	if strings.Contains(text, "Gen0") {
		t.Errorf("GetPlainText = %q, want it not to contain the superseded %q content", text, "Gen0")
	}
}

// incrementalXrefStreamPDF is incrementalTablePDF's xref-stream counterpart:
// an original revision using a cross-reference stream, followed by an
// update (new object 4 content) whose own xref stream only describes
// object 4 and chains back via /Prev.
func incrementalXrefStreamPDF() []byte {
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
	// /Size 5 matches the 5 entries written above (objects 0-4); the xref
	// stream object itself (5) need not be self-describing.
	fmt.Fprintf(&b, "5 0 obj\n<< /Type /XRef /Size 5 /W [1 2 2] /Root 1 0 R /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		entries.Len(), entries.String())
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", xref0Off)

	// Incremental update: a new body for object 4, plus a new xref stream
	// that only describes object 4 and chains back to the original via /Prev.
	gen1 := "BT /F1 12 Tf 10 10 Td (Gen1) Tj ET"
	obj4Off := b.Len()
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(gen1), gen1)

	var entries1 strings.Builder
	//nolint:gosec // G115: bounded test fixture length
	entries1.WriteString(entry(1, uint32(obj4Off), 0))
	xref1Off := b.Len()
	fmt.Fprintf(&b, "6 0 obj\n<< /Type /XRef /Size 6 /W [1 2 2] /Index [4 1] /Root 1 0 R /Prev %d /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		xref0Off, entries1.Len(), entries1.String())
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", xref1Off)
	return []byte(b.String())
}

// TestIncrementalUpdateXrefStreamChain covers readXrefStream's /Prev chase,
// the cross-reference-stream counterpart of
// TestIncrementalUpdateTableChain.
func TestIncrementalUpdateXrefStreamChain(t *testing.T) {
	t.Parallel()

	data := incrementalXrefStreamPDF()
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
}

// encryptedPDF builds a minimal PDF whose trailer carries /Encrypt pointing
// at a caller-supplied Encrypt dictionary body, and (unless omitOD is true)
// a well-formed /ID array. It has no valid encryption key material, so
// opening it always fails one way or another; what varies across tests is
// which check fails, and each assertion is against that.
func encryptedPDF(encryptBody string, omitID bool) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>",
		encryptBody,
	}
	var b strings.Builder
	b.WriteString("%PDF-1.6\n")
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
	idClause := " /ID [(0123456789ABCDEF) (0123456789ABCDEF)]"
	if omitID {
		idClause = ""
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R /Encrypt 4 0 R%s >>\nstartxref\n%d\n%%%%EOF\n",
		len(objs)+1, idClause, xrefOff)
	return []byte(b.String())
}

// TestInitEncryptV4Checks covers okayV4's individual checks (each reached
// through NewReader, which calls initEncrypt as soon as it sees /Encrypt in
// the trailer) and initEncrypt's own surrounding checks: an unsupported
// /Filter, an out-of-range key length, an unsupported /V, and a missing
// /ID. Every case here is expected to fail opening the file -- there is no
// valid key material anywhere in this fixture -- what's under test is which
// specific error each configuration produces.
func TestInitEncryptV4Checks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		encrypt    string
		omitID     bool
		wantErrSub string
	}{
		{"unsupported filter", "<< /Filter /NotStandard >>", false, "encryption filter"},
		{"key length not multiple of 8", "<< /Filter /Standard /Length 41 >>", false, "bit encryption key"},
		{"key length too small", "<< /Filter /Standard /Length 8 >>", false, "bit encryption key"},
		{"key length too large", "<< /Filter /Standard /Length 256 >>", false, "bit encryption key"},
		{"unsupported V", "<< /Filter /Standard /V 3 >>", false, "encryption version"},
		{"V4 missing CF", "<< /Filter /Standard /V 4 >>", false, "encryption version"},
		{"V4 missing StmF", "<< /Filter /Standard /V 4 /CF << /StdCF << /CFM /AESV2 >> >> >>", false, "encryption version"},
		{"V4 StmF and StrF differ", "<< /Filter /Standard /V 4 /CF << /StdCF << /CFM /AESV2 >> >> " +
			"/StmF /StdCF /StrF /Identity >>", false, "encryption version"},
		{"V4 bad AuthEvent", "<< /Filter /Standard /V 4 /CF << /StdCF << /CFM /AESV2 /AuthEvent /EFOpen >> >> " +
			"/StmF /StdCF /StrF /StdCF >>", false, "encryption version"},
		{"V4 bad CF Length", "<< /Filter /Standard /V 4 /CF << /StdCF << /CFM /AESV2 /Length 5 >> >> " +
			"/StmF /StdCF /StrF /StdCF >>", false, "encryption version"},
		{"V4 bad CFM", "<< /Filter /Standard /V 4 /CF << /StdCF << /CFM /V2 >> >> " +
			"/StmF /StdCF /StrF /StdCF >>", false, "encryption version"},
		{"V4 valid CF, missing ID", "<< /Filter /Standard /V 4 /CF << /StdCF << /CFM /AESV2 >> >> " +
			"/StmF /StdCF /StrF /StdCF >>", true, "missing ID"},
		{"V4 valid CF, missing O/U", "<< /Filter /Standard /V 4 /R 4 /CF << /StdCF << /CFM /AESV2 >> >> " +
			"/StmF /StdCF /StrF /StdCF >>", false, "O= or U="},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := encryptedPDF(tt.encrypt, tt.omitID)
			_, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
			if err == nil {
				t.Fatal("NewReader on an encrypted PDF with no usable key material: got nil error, want one")
			}
			if !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Errorf("NewReader error = %q, want it to contain %q", err, tt.wantErrSub)
			}
		})
	}
}

// TestNewReaderEncryptedPasswordCallback covers NewReaderEncrypted's
// password-retry loop: a wrong password is retried, and an empty string
// from pw stops the loop and returns the last error rather than looping
// forever.
func TestNewReaderEncryptedPasswordCallback(t *testing.T) {
	t.Parallel()

	data, err := base64.StdEncoding.DecodeString(
		strings.ReplaceAll(aesEncryptMetadataFalsePDFBase64, "\n", ""))
	if err != nil {
		t.Fatalf("decoding embedded test PDF: %v", err)
	}

	t.Run("wrong then correct password", func(t *testing.T) {
		t.Parallel()

		tries := []string{"wrong1", "wrong2", ""} // correct password is empty
		i := 0
		r, err := pdf.NewReaderEncrypted(bytes.NewReader(data), int64(len(data)), func() string {
			pw := tries[i]
			i++
			return pw
		})
		if err != nil {
			t.Fatalf("NewReaderEncrypted: %v", err)
		}
		if r == nil {
			t.Fatal("NewReaderEncrypted returned a nil Reader with a nil error")
		}
	})

	t.Run("all wrong, empty string stops the loop", func(t *testing.T) {
		t.Parallel()

		// Unlike the fixture above, this one's /O and /U are 32 bytes of
		// garbage rather than a real hash: no password, empty or otherwise,
		// will ever satisfy the U check, so initEncrypt("") itself fails
		// and the retry loop actually runs pw().
		garbage := strings.Repeat("X", 32)
		unbreakable := encryptedPDF(fmt.Sprintf(
			"<< /Filter /Standard /V 4 /R 4 /CF << /StdCF << /CFM /AESV2 >> >> "+
				"/StmF /StdCF /StrF /StdCF /O (%s) /U (%s) /P -4 >>", garbage, garbage), false)

		calls := 0
		_, err := pdf.NewReaderEncrypted(bytes.NewReader(unbreakable), int64(len(unbreakable)), func() string {
			calls++
			if calls > 3 {
				return ""
			}
			return "still-wrong"
		})
		if err == nil {
			t.Fatal("NewReaderEncrypted with no correct password ever supplied: got nil error, want one")
		}
		if !errors.Is(err, pdf.ErrInvalidPassword) {
			t.Errorf("error = %v, want it to be ErrInvalidPassword", err)
		}
		if calls != 4 {
			t.Errorf("pw callback called %d times, want exactly 4 (3 wrong + empty stop)", calls)
		}
	})
}

// TestValueReaderPNGUpPredictor covers Value.Reader's FlateDecode
// /Predictor 12 (PNG "Up") path end to end: pngUpReader.Read undoes the
// per-row delta encoding, row by row. Row 1's delta bytes are its raw pixel
// values, since with an all-zero history the "Up" delta is the identity.
// Row 2's delta bytes are (row2 - row1) per column, since the Up filter's
// decode step adds the previous row's decoded value back in; encoding the
// second row's raw bytes directly (as if history stayed zero) would be
// wrong once the first row has actually been accumulated into hist.
func TestValueReaderPNGUpPredictor(t *testing.T) {
	t.Parallel()

	want1, want2 := "Hello", "World"
	row1 := append([]byte{2}, []byte(want1)...)
	delta2 := make([]byte, len(want2))
	for i := range delta2 {
		delta2[i] = want2[i] - want1[i]
	}
	row2 := append([]byte{2}, delta2...)
	raw := append(append([]byte{}, row1...), row2...)

	var zbuf bytes.Buffer
	zw := zlib.NewWriter(&zbuf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatalf("zlib.Write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zlib.Close: %v", err)
	}

	data := buildPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R /Blob 3 0 R >>",
		"<< /Type /Pages /Kids [] /Count 0 >>",
		fmt.Sprintf("<< /Filter /FlateDecode /DecodeParms << /Predictor 12 /Columns 5 >> /Length %d >>\nstream\n%s\nendstream",
			zbuf.Len(), zbuf.String()),
	})
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	rc := r.Trailer().Key("Root").Key("Blob").Reader()
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading PNG-Up predictor stream: %v", err)
	}
	if string(got) != "HelloWorld" {
		t.Errorf("decoded = %q, want %q", got, "HelloWorld")
	}
}

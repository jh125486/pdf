// This file deviates from the repo's usual one-test-file-per-source-file
// convention: it is a standalone mutation-coverage workstream (WS2, covering
// Reader.resolveAt's object-stream guards in read.go) kept in its own file
// so it can be reviewed and merged independently of read_test.go.
package pdf_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jh125486/pdf"
)

// wsObjStmMaxObjectNumber mirrors read.go's maxObjectNumber (1<<23), the
// upper bound resolveAt enforces on an object stream's /N.
const wsObjStmMaxObjectNumber = 1 << 23

// wsObjStmMaxExtends mirrors read.go's maxObjStmExtends (32), the cap on how
// many streams resolveAt will follow through /Extends before giving up.
const wsObjStmMaxExtends = 32

// wsObjStmOpen opens data as a PDF, failing the test immediately if it does
// not parse. Only call this from the test's own goroutine -- never from
// inside the fn passed to run(), since testing.T is not safe to fail from a
// spawned goroutine.
func wsObjStmOpen(t *testing.T, data []byte) *pdf.Reader {
	t.Helper()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return r
}

// wsObjStmDefaults extracts the /N, /First and /Length baked into
// objStmPDF()'s object stream, so cases can build correct-except-one-field
// variants without duplicating objStmPDFWith's internal arithmetic.
func wsObjStmDefaults(t *testing.T) (n, first, length int) {
	t.Helper()
	data := objStmPDF()
	re := regexp.MustCompile(`/Type /ObjStm /N (-?\d+) /First (-?\d+) /Length (\d+)`)
	m := re.FindSubmatch(data)
	if m == nil {
		t.Fatalf("could not find ObjStm header in objStmPDF() fixture")
	}
	var err error
	if n, err = strconv.Atoi(string(m[1])); err != nil {
		t.Fatalf("parse N: %v", err)
	}
	if first, err = strconv.Atoi(string(m[2])); err != nil {
		t.Fatalf("parse First: %v", err)
	}
	if length, err = strconv.Atoi(string(m[3])); err != nil {
		t.Fatalf("parse Length: %v", err)
	}
	return n, first, length
}

// wsObjStmXrefPut appends one (kind, f2, f3) row -- matching the W [1 4 2]
// widths every builder below uses -- to an xref stream's raw entry bytes.
func wsObjStmXrefPut(buf *bytes.Buffer, kind byte, f2 uint32, f3 uint16) {
	buf.WriteByte(kind)
	_ = binary.Write(buf, binary.BigEndian, f2)
	_ = binary.Write(buf, binary.BigEndian, f3)
}

// wsObjStmExtendsPDF builds a PDF whose object 1 is declared in-stream,
// pointing at a chain of n object streams (numbered from 5) linked by
// /Extends, all with /N 0 so resolving object 1 always falls through the
// whole chain. Only the last stream omits /Extends. Root is object 1, so
// r.Trailer().Key("Root") drives resolveAt straight into the chain.
func wsObjStmExtendsPDF(n int) []byte {
	const base = 5
	var b strings.Builder
	b.WriteString("%PDF-1.5\n")

	off := make(map[int]int, n+1)
	for j := range n {
		num := base + j
		off[num] = b.Len()
		hdr := "/Type /ObjStm /N 0 /First 1"
		if j < n-1 {
			fmt.Fprintf(&b, "%d 0 obj\n<< %s /Extends %d 0 R /Length 0 >>\nstream\n\nendstream\nendobj\n", num, hdr, num+1)
		} else {
			fmt.Fprintf(&b, "%d 0 obj\n<< %s /Length 0 >>\nstream\n\nendstream\nendobj\n", num, hdr)
		}
	}

	xrefNum := base + n
	off[xrefNum] = b.Len()

	var entries bytes.Buffer
	wsObjStmXrefPut(&entries, 0, 0, 65535) // 0: free
	wsObjStmXrefPut(&entries, 2, base, 0)  // 1: in stream `base`
	wsObjStmXrefPut(&entries, 0, 0, 0)     // 2: free (unused)
	wsObjStmXrefPut(&entries, 0, 0, 0)     // 3: free (unused)
	wsObjStmXrefPut(&entries, 0, 0, 0)     // 4: free (unused)
	for j := range n {
		streamOff := off[base+j]
		wsObjStmXrefPut(&entries, 1, uint32(streamOff), 0) //nolint:gosec // G115: bounded test fixture length
	}
	wsObjStmXrefPut(&entries, 1, uint32(off[xrefNum]), 0) //nolint:gosec // G115: bounded test fixture length

	size := xrefNum + 1
	fmt.Fprintf(&b, "%d 0 obj\n<< /Type /XRef /Size %d /W [1 4 2] /Root 1 0 R /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		xrefNum, size, entries.Len(), entries.String())
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", off[xrefNum])
	return []byte(b.String())
}

// wsObjStmRawPDF builds a PDF whose object 1 is in-stream inside a single
// object stream (object 5) carrying the given /N, /First and raw stream
// data verbatim, letting a case control exactly what byte offset resolveAt
// lands on when it resolves object 1.
func wsObjStmRawPDF(n, first int, data string) []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.5\n")

	off := map[int]int{}
	off[5] = b.Len()
	fmt.Fprintf(&b, "5 0 obj\n<< /Type /ObjStm /N %d /First %d /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		n, first, len(data), data)

	off[6] = b.Len()
	var entries bytes.Buffer
	wsObjStmXrefPut(&entries, 0, 0, 65535)
	wsObjStmXrefPut(&entries, 2, 5, 0)
	wsObjStmXrefPut(&entries, 0, 0, 0)
	wsObjStmXrefPut(&entries, 0, 0, 0)
	wsObjStmXrefPut(&entries, 0, 0, 0)
	wsObjStmXrefPut(&entries, 1, uint32(off[5]), 0) //nolint:gosec // G115: bounded test fixture length
	wsObjStmXrefPut(&entries, 1, uint32(off[6]), 0) //nolint:gosec // G115: bounded test fixture length

	fmt.Fprintf(&b, "6 0 obj\n<< /Type /XRef /Size 7 /W [1 4 2] /Root 1 0 R /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		entries.Len(), entries.String())
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", off[6])
	return []byte(b.String())
}

// wsObjStmWrongTypePDF builds a PDF whose object 1 is in-stream inside a
// real stream (object 5) that is missing /Type /ObjStm, to hit resolveAt's
// "not an object stream" guard rather than any /Extends- or /N-related one.
func wsObjStmWrongTypePDF() []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.5\n")

	off := map[int]int{}
	off[5] = b.Len()
	b.WriteString("5 0 obj\n<< /Type /Foo /N 0 /First 1 /Length 0 >>\nstream\n\nendstream\nendobj\n")

	off[6] = b.Len()
	var entries bytes.Buffer
	wsObjStmXrefPut(&entries, 0, 0, 65535)
	wsObjStmXrefPut(&entries, 2, 5, 0)
	wsObjStmXrefPut(&entries, 0, 0, 0)
	wsObjStmXrefPut(&entries, 0, 0, 0)
	wsObjStmXrefPut(&entries, 0, 0, 0)
	wsObjStmXrefPut(&entries, 1, uint32(off[5]), 0) //nolint:gosec // G115: bounded test fixture length
	wsObjStmXrefPut(&entries, 1, uint32(off[6]), 0) //nolint:gosec // G115: bounded test fixture length

	fmt.Fprintf(&b, "6 0 obj\n<< /Type /XRef /Size 7 /W [1 4 2] /Root 1 0 R /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		entries.Len(), entries.String())
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", off[6])
	return []byte(b.String())
}

// wsObjStmExtendsNonStreamPDF builds a PDF whose object stream (object 5,
// /N 0) has an /Extends pointing at object 7, a plain dictionary rather than
// a stream -- exercising the "cannot find object in stream" panic via the
// ext.Kind() != Stream path (as opposed to a missing /Extends key).
func wsObjStmExtendsNonStreamPDF() []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.5\n")

	off := map[int]int{}
	off[5] = b.Len()
	b.WriteString("5 0 obj\n<< /Type /ObjStm /N 0 /First 1 /Extends 7 0 R /Length 0 >>\nstream\n\nendstream\nendobj\n")

	off[7] = b.Len()
	b.WriteString("7 0 obj\n<< /Foo 1 >>\nendobj\n")

	off[6] = b.Len()
	var entries bytes.Buffer
	wsObjStmXrefPut(&entries, 0, 0, 65535)
	wsObjStmXrefPut(&entries, 2, 5, 0)
	wsObjStmXrefPut(&entries, 0, 0, 0)
	wsObjStmXrefPut(&entries, 0, 0, 0)
	wsObjStmXrefPut(&entries, 0, 0, 0)
	wsObjStmXrefPut(&entries, 1, uint32(off[5]), 0) //nolint:gosec // G115: bounded test fixture length
	wsObjStmXrefPut(&entries, 1, uint32(off[6]), 0) //nolint:gosec // G115: bounded test fixture length
	wsObjStmXrefPut(&entries, 1, uint32(off[7]), 0) //nolint:gosec // G115: bounded test fixture length

	fmt.Fprintf(&b, "6 0 obj\n<< /Type /XRef /Size 8 /W [1 4 2] /Root 1 0 R /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		entries.Len(), entries.String())
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", off[6])
	return []byte(b.String())
}

// wsObjStmDepthPDF builds a PDF where object 1's container is itself
// declared in-stream, chained 33 deep (objects 1..33, each pointing at the
// next), to trip resolveAt's maxResolveDepth guard. None of the 33 need
// real stream bytes on disk: the depth check fires before any of their
// content is ever read.
func wsObjStmDepthPDF() []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.5\n")

	off := map[int]int{}
	off[34] = b.Len()

	var entries bytes.Buffer
	wsObjStmXrefPut(&entries, 0, 0, 65535) // 0: free
	for id := 1; id <= 32; id++ {
		wsObjStmXrefPut(&entries, 2, uint32(id+1), 0) // id: in-stream, container is id+1
	}
	wsObjStmXrefPut(&entries, 2, 33, 0)              // 33: in-stream, container is itself (never read)
	wsObjStmXrefPut(&entries, 1, uint32(off[34]), 0) //nolint:gosec // G115: bounded test fixture length

	fmt.Fprintf(&b, "34 0 obj\n<< /Type /XRef /Size 35 /W [1 4 2] /Root 1 0 R /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		entries.Len(), entries.String())
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", off[34])
	return []byte(b.String())
}

// wsObjStmBadOffsetPDF builds a classic-xref-table PDF whose entry for
// object 1 points at object 2's "N 0 obj" header instead of its own, to hit
// resolveAt's def.ptr != ptr guard.
func wsObjStmBadOffsetPDF() []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")

	b.WriteString("1 0 obj\n<< /Foo (one) >>\nendobj\n")
	off2 := b.Len()
	b.WriteString("2 0 obj\n<< /Foo (two) >>\nendobj\n")

	xrefOff := b.Len()
	b.WriteString("xref\n0 3\n")
	b.WriteString("0000000000 65535 f \n")
	fmt.Fprintf(&b, "%010d 00000 n \n", off2) // object 1's entry: wrong, points at object 2
	fmt.Fprintf(&b, "%010d 00000 n \n", off2) // object 2's entry: correct
	b.WriteString("trailer\n<< /Size 3 /Root 1 0 R >>\n")
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", xrefOff)
	return []byte(b.String())
}

func TestWSObjStmBaseline(t *testing.T) {
	t.Parallel()

	data := objStmPDF()
	r := wsObjStmOpen(t, data)

	if n := r.NumPage(); n != 1 {
		t.Fatalf("NumPage = %d, want 1", n)
	}
	text, err := r.GetPlainText()
	if err != nil {
		t.Fatalf("GetPlainText: %v", err)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(text)
	if !strings.Contains(buf.String(), "Hello Stream") {
		t.Fatalf("GetPlainText = %q, want it to contain %q", buf.String(), "Hello Stream")
	}
}

func TestWSObjStmNZero(t *testing.T) {
	t.Parallel()

	_, first, _ := wsObjStmDefaults(t)
	hdr := fmt.Sprintf("/N 0 /First %d", first)
	data := objStmPDFWith(hdr)
	r := wsObjStmOpen(t, data)

	panicked, timedOut := run(t, func() { r.Trailer().Key("Root") })
	if timedOut {
		t.Fatal("did not return in time")
	}
	if panicked == nil {
		t.Fatal("expected a panic, got none")
	}
	if msg := fmt.Sprint(panicked); !strings.Contains(msg, "cannot find object in stream") {
		t.Errorf("panic = %q, want it to contain %q", msg, "cannot find object in stream")
	}
}

func TestWSObjStmNAcceptedAtBound(t *testing.T) {
	t.Parallel()

	// /N at the maxObjectNumber bound must not trip the range guard. Object
	// 1 is still the first pair table entry, so resolution succeeds outright.
	_, first, _ := wsObjStmDefaults(t)
	hdr := fmt.Sprintf("/N %d /First %d", wsObjStmMaxObjectNumber, first)
	data := objStmPDFWith(hdr)
	r := wsObjStmOpen(t, data)

	panicked, timedOut := run(t, func() { r.Trailer().Key("Root") })
	if timedOut {
		t.Fatal("did not return in time")
	}
	if panicked != nil {
		if msg := fmt.Sprint(panicked); strings.Contains(msg, "/N out of range") {
			t.Errorf("panic = %q, must not report /N out of range at the accepted bound", msg)
		}
	}
}

func TestWSObjStmNOutOfRange(t *testing.T) {
	t.Parallel()

	_, first, _ := wsObjStmDefaults(t)
	tests := []struct {
		name string
		n    int
	}{
		{"one over max", wsObjStmMaxObjectNumber + 1},
		{"negative", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hdr := fmt.Sprintf("/N %d /First %d", tt.n, first)
			data := objStmPDFWith(hdr)
			r := wsObjStmOpen(t, data)

			panicked, timedOut := run(t, func() { r.Trailer().Key("Root") })
			if timedOut {
				t.Fatal("did not return in time")
			}
			if panicked == nil {
				t.Fatal("expected a panic, got none")
			}
			if msg := fmt.Sprint(panicked); !strings.Contains(msg, "object stream /N out of range") {
				t.Errorf("panic = %q, want it to contain %q", msg, "object stream /N out of range")
			}
		})
	}
}

func TestWSObjStmFirstMissing(t *testing.T) {
	t.Parallel()

	n, _, _ := wsObjStmDefaults(t)
	tests := []struct {
		name  string
		first int
	}{
		{"zero", 0},
		{"negative", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hdr := fmt.Sprintf("/N %d /First %d", n, tt.first)
			data := objStmPDFWith(hdr)
			r := wsObjStmOpen(t, data)

			panicked, timedOut := run(t, func() { r.Trailer().Key("Root") })
			if timedOut {
				t.Fatal("did not return in time")
			}
			if panicked == nil {
				t.Fatal("expected a panic, got none")
			}
			if msg := fmt.Sprint(panicked); !strings.Contains(msg, "missing First") {
				t.Errorf("panic = %q, want it to contain %q", msg, "missing First")
			}
		})
	}
}

func TestWSObjStmFirstWrongButPositive(t *testing.T) {
	t.Parallel()

	n, _, _ := wsObjStmDefaults(t)
	hdr := fmt.Sprintf("/N %d /First 1", n) // small, positive, but not the real table length
	data := objStmPDFWith(hdr)
	r := wsObjStmOpen(t, data)

	panicked, timedOut := run(t, func() { r.Trailer().Key("Root") })
	if timedOut {
		t.Fatal("did not return in time")
	}
	if panicked != nil {
		if msg := fmt.Sprint(panicked); strings.Contains(msg, "missing First") {
			t.Errorf("panic = %q, must not report missing First for a wrong-but-positive value", msg)
		}
	}
}

func TestWSObjStmExtendsChain(t *testing.T) {
	t.Parallel()

	t.Run("exactly at bound", func(t *testing.T) {
		t.Parallel()

		data := wsObjStmExtendsPDF(wsObjStmMaxExtends)
		r := wsObjStmOpen(t, data)

		panicked, timedOut := run(t, func() { r.Trailer().Key("Root") })
		if timedOut {
			t.Fatal("did not return in time")
		}
		if panicked == nil {
			t.Fatal("expected a panic, got none")
		}
		msg := fmt.Sprint(panicked)
		if strings.Contains(msg, "chain too long") {
			t.Errorf("panic = %q, must not report chain too long exactly at the bound", msg)
		}
		if !strings.Contains(msg, "cannot find object in stream") {
			t.Errorf("panic = %q, want it to contain %q", msg, "cannot find object in stream")
		}
	})

	t.Run("one over bound", func(t *testing.T) {
		t.Parallel()

		data := wsObjStmExtendsPDF(wsObjStmMaxExtends + 1)
		r := wsObjStmOpen(t, data)

		panicked, timedOut := run(t, func() { r.Trailer().Key("Root") })
		if timedOut {
			t.Fatal("did not return in time")
		}
		if panicked == nil {
			t.Fatal("expected a panic, got none")
		}
		if msg := fmt.Sprint(panicked); !strings.Contains(msg, "object stream /Extends chain too long") {
			t.Errorf("panic = %q, want it to contain %q", msg, "object stream /Extends chain too long")
		}
	})
}

func TestWSObjStmOffsetPastEOF(t *testing.T) {
	t.Parallel()

	// A one-entry pair table "1 0 " (4 bytes) followed by a single-byte body
	// "5" (an integer literal): total data is 5 bytes, indices 0..4. Object
	// 1's offset is always 0 (the first pair table entry), so /First alone
	// controls where seekForward lands.
	const data = "1 0 5"

	t.Run("last byte", func(t *testing.T) {
		t.Parallel()

		pdfBytes := wsObjStmRawPDF(1, len(data)-1, data)
		r := wsObjStmOpen(t, pdfBytes)

		panicked, timedOut := run(t, func() { r.Trailer().Key("Root") })
		if timedOut {
			t.Fatal("did not return in time")
		}
		if panicked != nil {
			if msg := fmt.Sprint(panicked); strings.Contains(msg, "past end of stream") {
				t.Errorf("panic = %q, must not report past-end-of-stream landing on the last byte", msg)
			}
		}
	})

	t.Run("one byte past", func(t *testing.T) {
		t.Parallel()

		pdfBytes := wsObjStmRawPDF(1, len(data), data)
		r := wsObjStmOpen(t, pdfBytes)

		panicked, timedOut := run(t, func() { r.Trailer().Key("Root") })
		if timedOut {
			t.Fatal("did not return in time")
		}
		if panicked == nil {
			t.Fatal("expected a panic, got none")
		}
		if msg := fmt.Sprint(panicked); !strings.Contains(msg, "past end of stream") {
			t.Errorf("panic = %q, want it to contain %q", msg, "past end of stream")
		}
	})
}

func TestWSObjStmNegativeOffset(t *testing.T) {
	t.Parallel()

	// Pair table entry "1 -5 ": object 1 at offset -5.
	const data = "1 -5 "
	pdfBytes := wsObjStmRawPDF(1, len(data), data)
	r := wsObjStmOpen(t, pdfBytes)

	panicked, timedOut := run(t, func() { r.Trailer().Key("Root") })
	if timedOut {
		t.Fatal("did not return in time")
	}
	if panicked == nil {
		t.Fatal("expected a panic, got none")
	}
	if msg := fmt.Sprint(panicked); !strings.Contains(msg, "negative object stream offset") {
		t.Errorf("panic = %q, want it to contain %q", msg, "negative object stream offset")
	}
}

func TestWSObjStmDepthCap(t *testing.T) {
	t.Parallel()

	data := wsObjStmDepthPDF()
	r := wsObjStmOpen(t, data)

	panicked, timedOut := run(t, func() { r.Trailer().Key("Root") })
	if timedOut {
		t.Fatal("did not return in time")
	}
	if panicked == nil {
		t.Fatal("expected a panic, got none")
	}
	if msg := fmt.Sprint(panicked); !strings.Contains(msg, "PDF object stream nesting too deep") {
		t.Errorf("panic = %q, want it to contain %q", msg, "PDF object stream nesting too deep")
	}
}

func TestWSObjStmNotAnObjectStream(t *testing.T) {
	t.Parallel()

	data := wsObjStmWrongTypePDF()
	r := wsObjStmOpen(t, data)

	panicked, timedOut := run(t, func() { r.Trailer().Key("Root") })
	if timedOut {
		t.Fatal("did not return in time")
	}
	if panicked == nil {
		t.Fatal("expected a panic, got none")
	}
	if msg := fmt.Sprint(panicked); !strings.Contains(msg, "not an object stream") {
		t.Errorf("panic = %q, want it to contain %q", msg, "not an object stream")
	}
}

func TestWSObjStmExtendsNonStream(t *testing.T) {
	t.Parallel()

	data := wsObjStmExtendsNonStreamPDF()
	r := wsObjStmOpen(t, data)

	panicked, timedOut := run(t, func() { r.Trailer().Key("Root") })
	if timedOut {
		t.Fatal("did not return in time")
	}
	if panicked == nil {
		t.Fatal("expected a panic, got none")
	}
	if msg := fmt.Sprint(panicked); !strings.Contains(msg, "cannot find object in stream") {
		t.Errorf("panic = %q, want it to contain %q", msg, "cannot find object in stream")
	}
}

func TestWSObjStmMismatchedPtr(t *testing.T) {
	t.Parallel()

	data := wsObjStmBadOffsetPDF()
	r := wsObjStmOpen(t, data)

	panicked, timedOut := run(t, func() { r.Trailer().Key("Root") })
	if timedOut {
		t.Fatal("did not return in time")
	}
	if panicked == nil {
		t.Fatal("expected a panic, got none")
	}
	if msg := fmt.Sprint(panicked); !strings.Contains(msg, "found") {
		t.Errorf("panic = %q, want it to report the mismatched object found at the offset", msg)
	}
}

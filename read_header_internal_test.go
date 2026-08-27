// Mutation-coverage workstream (WS7): header, trailer scan, findLastLine,
// and Reader.sectionReader in read.go. Kept in its own file so this
// workstream can be reviewed and merged independently of the others.
//
// Whitebox: findLastLine and Reader.sectionReader are unexported, and the
// sectionReader cases build a *Reader by poking its unexported f/end fields
// directly, none of which package_test (blackbox) can reach.

package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// TestFindLastLine drives findLastLine directly with hand-built byte slices
// so each boundary in its backward scan can be pinned independently of
// NewReader's own use of it.
func TestFindLastLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		buf  []byte
		s    string
		want int
	}{
		{
			name: "keyword on its own line",
			// "x\nstartxref\n1\n": keyword starts at index 2, preceded by
			// '\n' at 1 and followed by '\n' at 11.
			buf:  []byte("x\nstartxref\n1\n"),
			s:    "startxref",
			want: 2,
		},
		{
			name: "keyword at index 0 is rejected",
			// LastIndex finds the keyword at i == 0; the i <= 0 guard
			// rejects it outright, with no earlier occurrence to fall
			// back to.
			buf:  []byte("startxref\nX"),
			s:    "startxref",
			want: -1,
		},
		{
			name: "trailing newline would sit exactly at buffer end",
			// "\nstartxref" (10 bytes): the keyword's last byte is the
			// buffer's last byte, so i+len(s) == len(buf) and there is no
			// byte left to check as a line terminator.
			buf:  []byte("\nstartxref"),
			s:    "startxref",
			want: -1,
		},
		{
			name: "keyword as substring of a longer token is skipped",
			// "\nstartxref\nXstartxref\n1\n": the later occurrence (index
			// 12) is preceded by 'X', not a line terminator, so it is
			// rejected and the scan continues backward to the earlier,
			// validly-terminated occurrence at index 1.
			buf:  []byte("\nstartxref\nXstartxref\n1\n"),
			s:    "startxref",
			want: 1,
		},
		{
			name: "carriage return accepted as line terminator",
			buf:  []byte("\rstartxref\r"),
			s:    "startxref",
			want: 1,
		},
		{
			name: "keyword absent entirely",
			buf:  []byte("no keyword in here at all"),
			s:    "startxref",
			want: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := findLastLine(tt.buf, tt.s); got != tt.want {
				t.Errorf("findLastLine(%q, %q) = %d, want %d", tt.buf, tt.s, got, tt.want)
			}
		})
	}
}

// wsHeaderWithVersion returns validPDF() with its 8-byte "%PDF-x.y" marker
// replaced by version, leaving the rest of the file (offsets, xref table,
// startxref, %%EOF) untouched.
func wsHeaderWithVersion(version string) []byte {
	if len(version) != 8 {
		panic("wsHeaderWithVersion: version must be exactly 8 bytes")
	}
	data := validPDF()
	copy(data[:8], version)
	return data
}

// TestHeaderMagicBytes covers NewReader's two distinct guards on the "%PDF-"
// magic: a prefix check against "%PDF-1." and a separate digit-range check
// on the byte that follows it (buf[7] within ['0','7']). "%PDF-0.9" and
// "%PDF-2.0" fail the prefix check; "%PDF-1.8"/"%PDF-1.9" pass the prefix
// check but fail the digit-range check. All four report the same message,
// but through different arms of the guard's || chain.
func TestHeaderMagicBytes(t *testing.T) {
	t.Parallel()

	t.Run("accepted versions", func(t *testing.T) {
		t.Parallel()

		for _, version := range []string{"%PDF-1.0", "%PDF-1.7"} {
			t.Run(version, func(t *testing.T) {
				t.Parallel()

				data := wsHeaderWithVersion(version)
				if _, err := NewReader(bytes.NewReader(data), int64(len(data))); err != nil {
					t.Fatalf("NewReader(%s) = %v, want success", version, err)
				}
			})
		}
	})

	t.Run("digit-range guard rejects out-of-range 1.x", func(t *testing.T) {
		t.Parallel()

		for _, version := range []string{"%PDF-1.8", "%PDF-1.9"} {
			t.Run(version, func(t *testing.T) {
				t.Parallel()

				data := wsHeaderWithVersion(version)
				_, err := NewReader(bytes.NewReader(data), int64(len(data)))
				if err == nil {
					t.Fatalf("NewReader(%s) = nil error, want invalid header", version)
				}
				const want = "not a PDF file: invalid header"
				if err.Error() != want {
					t.Errorf("NewReader(%s) error = %q, want %q", version, err.Error(), want)
				}
			})
		}
	})

	t.Run("prefix guard rejects non-1.x major/minor", func(t *testing.T) {
		t.Parallel()

		for _, version := range []string{"%PDF-0.9", "%PDF-2.0"} {
			t.Run(version, func(t *testing.T) {
				t.Parallel()

				data := wsHeaderWithVersion(version)
				_, err := NewReader(bytes.NewReader(data), int64(len(data)))
				if err == nil {
					t.Fatalf("NewReader(%s) = nil error, want invalid header", version)
				}
				const want = "not a PDF file: invalid header"
				if err.Error() != want {
					t.Errorf("NewReader(%s) error = %q, want %q", version, err.Error(), want)
				}
			})
		}
	})
}

// wsHeaderTerminator builds a minimal file whose header terminator byte(s)
// (the byte(s) right after "%PDF-1.0") are exactly term, padded out past
// NewReader's minimum-size guard. The rest of the file is deliberately not a
// valid PDF -- these cases only need to reach (and stop at, or pass) the
// terminator check.
func wsHeaderTerminator(term string) []byte {
	data := "%PDF-1.0" + term
	for len(data) < len("%PDF-1.0\n%%EOF")+4 {
		data += "z"
	}
	return []byte(data)
}

// TestHeaderTerminatorByte covers the byte(s) NewReader requires right after
// the "%PDF-1.N" version digit. TestHeaderTrailingSpace already covers the
// space-then-newline case; this fills in the other terminator combinations.
func TestHeaderTerminatorByte(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		term          string
		wantInvalid   bool
		wantErrSubstr string
	}{
		{name: "newline", term: "\n"},
		{name: "carriage return", term: "\r"},
		{name: "space then carriage return", term: " \r"},
		{
			name:          "arbitrary non-whitespace byte rejected",
			term:          "X",
			wantInvalid:   true,
			wantErrSubstr: "not a PDF file: invalid header",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := wsHeaderTerminator(tt.term)
			_, err := NewReader(bytes.NewReader(data), int64(len(data)))
			if tt.wantInvalid {
				if err == nil || err.Error() != tt.wantErrSubstr {
					t.Fatalf("NewReader() error = %v, want %q", err, tt.wantErrSubstr)
				}
				return
			}
			// The file is otherwise malformed, so some later error is
			// expected -- just not one from the header/terminator check.
			if err != nil && strings.Contains(err.Error(), "invalid header") {
				t.Errorf("NewReader() error = %v, want no invalid-header complaint", err)
			}
		})
	}
}

// TestMinimumFileSizeGuard covers the len(f) < len("%PDF-1.0\n%%EOF") guard:
// one byte under is rejected on size alone; exactly at the minimum passes
// the size check and fails later (on the missing startxref), a distinctly
// different error.
func TestMinimumFileSizeGuard(t *testing.T) {
	t.Parallel()

	t.Run("one byte under minimum", func(t *testing.T) {
		t.Parallel()

		data := []byte("%PDF-1.0\n%%EO") // 13 bytes; minimum is 14
		_, err := NewReader(bytes.NewReader(data), int64(len(data)))
		const want = "not a PDF file: too short"
		if err == nil || err.Error() != want {
			t.Fatalf("NewReader() error = %v, want %q", err, want)
		}
	})

	t.Run("exactly at minimum", func(t *testing.T) {
		t.Parallel()

		data := []byte("%PDF-1.0\n%%EOF") // exactly 14 bytes
		_, err := NewReader(bytes.NewReader(data), int64(len(data)))
		if err == nil {
			t.Fatal("NewReader() = nil error, want an error (no startxref in this fixture)")
		}
		if strings.Contains(err.Error(), "too short") {
			t.Errorf("NewReader() error = %v, want a non-size error at exactly the minimum length", err)
		}
		const want = "malformed PDF file: missing final startxref"
		if err.Error() != want {
			t.Errorf("NewReader() error = %q, want %q", err.Error(), want)
		}
	})
}

// TestEOFTrailingBytes covers the %%EOF trim/suffix check: trailing
// whitespace/control bytes after a real %%EOF are tolerated, an unexpected
// non-whitespace byte after %%EOF is rejected, and a tail of nothing but
// newlines (no %%EOF at all) is rejected without panicking.
func TestEOFTrailingBytes(t *testing.T) {
	t.Parallel()

	t.Run("trailing whitespace after %%EOF still opens", func(t *testing.T) {
		t.Parallel()

		data := append(validPDF(), []byte("\n\r \t\n")...)
		if _, err := NewReader(bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatalf("NewReader() with whitespace after %%%%EOF: %v", err)
		}
	})

	t.Run("unexpected byte after %%EOF is rejected", func(t *testing.T) {
		t.Parallel()

		data := append(validPDF(), 'X')
		_, err := NewReader(bytes.NewReader(data), int64(len(data)))
		const want = "not a PDF file: missing %%EOF"
		if err == nil || err.Error() != want {
			t.Fatalf("NewReader() error = %v, want %q", err, want)
		}
	})

	t.Run("tail of only newlines does not panic", func(t *testing.T) {
		t.Parallel()

		data := []byte("%PDF-1.0\n" + strings.Repeat("\n", 100))
		mustNotCrash(t, func() {
			_, err := NewReader(bytes.NewReader(data), int64(len(data)))
			const want = "not a PDF file: missing %%EOF"
			if err == nil || err.Error() != want {
				t.Errorf("NewReader() error = %v, want %q", err, want)
			}
		})
	})
}

// TestMissingStartxrefKeyword covers a file that ends properly in %%EOF but
// never carries a startxref keyword within NewReader's trailing scan window,
// which is its own distinct error from a missing-%%EOF or too-short file.
func TestMissingStartxrefKeyword(t *testing.T) {
	t.Parallel()

	data := []byte("%PDF-1.0\n" + strings.Repeat("junk ", 20) + "\n%%EOF")
	_, err := NewReader(bytes.NewReader(data), int64(len(data)))
	const want = "malformed PDF file: missing final startxref"
	if err == nil || err.Error() != want {
		t.Fatalf("NewReader() error = %v, want %q", err, want)
	}
}

// TestSectionReaderBounds covers Reader.sectionReader's offset bound check
// at both edges: end-1 must succeed and end itself must fail, which is what
// pins the boundary operator against a mutant that swaps >= for >.
func TestSectionReaderBounds(t *testing.T) {
	t.Parallel()

	const size = 5
	r := &Reader{f: bytes.NewReader([]byte("abcde")), end: size}

	tests := []struct {
		name    string
		off     int64
		wantErr bool
	}{
		{name: "offset 0 succeeds", off: 0, wantErr: false},
		{name: "offset end-1 succeeds", off: size - 1, wantErr: false},
		{name: "offset end fails", off: size, wantErr: true},
		{name: "negative offset fails", off: -1, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sr, err := r.sectionReader(tt.off)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("sectionReader(%d) = nil error, want out-of-range error", tt.off)
				}
				want := fmt.Sprintf("offset %d out of range [0, %d)", tt.off, int64(size))
				if err.Error() != want {
					t.Errorf("sectionReader(%d) error = %q, want %q", tt.off, err.Error(), want)
				}
				return
			}
			if err != nil {
				t.Fatalf("sectionReader(%d): %v", tt.off, err)
			}
			if sr == nil {
				t.Fatalf("sectionReader(%d) = nil reader, want non-nil", tt.off)
			}
		})
	}
}

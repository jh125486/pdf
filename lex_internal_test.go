// Whitebox: exercises buffer, token, and object parsing internals of lex.go
// that have no exported surface (buffer, token, keyword, name are all
// unexported).

package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// buildUnterminatedArrayPDF constructs a minimal single-page PDF whose
// content stream ends inside an array that is never closed. Real-world
// truncated or malformed PDFs exhibit the same shape: the tokenizer hits
// end of input while readArray is still collecting elements.
func buildUnterminatedArrayPDF() []byte {
	var buf bytes.Buffer
	offsets := make([]int, 5)

	buf.WriteString("%PDF-1.4\n")

	// The content stream ends inside "[ ... " with no closing "]".
	content := "BT /F1 12 Tf [ (hello) 1 2"
	objs := []string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> /Contents 4 0 R >>\nendobj\n",
		fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(content), content),
	}
	for i, obj := range objs {
		offsets[i+1] = buf.Len()
		buf.WriteString(obj)
	}

	xrefOffset := buf.Len()
	buf.WriteString("xref\n0 5\n")
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= 4; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	buf.WriteString("trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n")
	fmt.Fprintf(&buf, "%d\n%%%%EOF\n", xrefOffset)
	return buf.Bytes()
}

// TestUnterminatedArrayTerminates verifies that text extraction terminates
// on a PDF whose content stream is truncated inside an unterminated array.
// Before readArray handled io.EOF, readToken returned io.EOF as a token
// value, which matched neither nil nor keyword("]"), so readArray appended
// io.EOF objects forever, allocating memory without bound.
func TestUnterminatedArrayTerminates(t *testing.T) {
	t.Parallel()

	data := buildUnterminatedArrayPDF()
	r, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// The extracted text is irrelevant; the call just has to return.
		_, _ = r.GetPlainText()
	}()

	select {
	case <-done:
		// ok: extraction terminated
	case <-time.After(5 * time.Second):
		t.Fatal("GetPlainText did not return within 5s: readArray is looping on io.EOF at end of a truncated content stream")
	}
}

// TestSeekForwardOutOfRange pins the buffer position check. A negative or
// backwards seek left buf.pos negative, which indexed buf out of bounds on
// the next read.
func TestSeekForwardOutOfRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		off  int64
	}{
		{"negative", -1},
		{"far negative", -1 << 40},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBuffer(bytes.NewReader([]byte("hello world")), 0)
			b.allowEOF = true
			func() {
				defer func() {
					if recover() == nil {
						t.Errorf("seekForward(%d): no error reported", tt.off)
					}
				}()
				b.seekForward(tt.off)
				b.readByte()
			}()
		})
	}
}

// TestMalformedLexer covers tokenizer termination for hand-crafted content
// streams that previously hung or panicked in unexpected ways.
func TestMalformedLexer(t *testing.T) {
	t.Parallel()

	// readByte reports end of input as '\n' once allowEOF is set, so a hex
	// string with no closing '>' used to skip whitespace forever.
	unterminatedHex := []string{"<AB", "<AB ", "<", "<A", "< ", "<AB\n\n\n"}
	for _, content := range unterminatedHex {
		t.Run("unterminated hex string "+content, func(t *testing.T) {
			t.Parallel()

			mustNotCrash(t, func() {
				Interpret(rawStream(content), func(stk *Stack, op string) {})
			})
		})
	}

	// These are rejected by design. What matters is that they terminate: the
	// panic is the library's own error signal, reported to callers by the
	// public APIs that recover.
	rejected := []string{"(unterminated", "(nested (deeper", "/name#", "/name#z", "<AB<CD", "<AG>"}
	for _, content := range rejected {
		t.Run("rejected "+content, func(t *testing.T) {
			t.Parallel()

			if _, timedOut := run(t, func() {
				Interpret(rawStream(content), func(stk *Stack, op string) {})
			}); timedOut {
				t.Errorf("did not return within %v", caseTimeout)
			}
		})
	}
}

// TestBufferSeek pins buffer.seek's reset behavior: it repositions to the
// given offset and drops any buffered data, position, and unread tokens.
// seek has no caller in the current codebase (only seekForward is used by
// object-stream resolution), but it is part of buffer's contract -- a clean
// reset, not a relative adjustment -- which is worth pinning directly.
func TestBufferSeek(t *testing.T) {
	t.Parallel()

	b := newBuffer(strings.NewReader("hello world"), 0)
	b.allowEOF = true
	_ = b.readByte() // populate buf/pos from the initial offset
	b.unreadToken(keyword("stale"))

	b.seek(3)

	if b.offset != 3 {
		t.Errorf("offset = %d, want 3", b.offset)
	}
	if len(b.buf) != 0 {
		t.Errorf("buf len = %d, want 0", len(b.buf))
	}
	if b.pos != 0 {
		t.Errorf("pos = %d, want 0", b.pos)
	}
	if len(b.unread) != 0 {
		t.Errorf("unread len = %d, want 0 (stale token should be dropped)", len(b.unread))
	}
}

// errReader always returns a non-EOF error, to reach reload's malformed-PDF
// panic branch: a read failure that is neither io.EOF nor
// errStreamNotPresent is always fatal, regardless of allowEOF.
type errReader struct{ err error }

func (r *errReader) Read([]byte) (int, error) { return 0, r.err }

// TestBufferReloadNonEOFError covers reload's error path for a read failure
// that is not io.EOF or errStreamNotPresent. This must panic (via errorf)
// whether or not allowEOF is set, since allowEOF only tolerates a clean end
// of input, not an arbitrary I/O error.
func TestBufferReloadNonEOFError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		allowEOF bool
	}{
		{"allowEOF false", false},
		{"allowEOF true", true},
	}

	wantErr := errors.New("boom")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBuffer(&errReader{wantErr}, 0)
			b.allowEOF = tt.allowEOF

			func() {
				defer func() {
					r := recover()
					if r == nil {
						t.Fatal("reload: no panic, want malformed PDF error")
					}
					if err, ok := r.(error); !ok || !strings.Contains(err.Error(), "boom") {
						t.Errorf("panic = %v, want it to wrap %q", r, wantErr)
					}
				}()
				b.reload()
			}()
		})
	}
}

// TestSeekForwardBeyondBufferWindow covers seekForward's post-loop bounds
// check for a target behind the currently loaded window: after several
// reloads, buf only holds the most recent chunk, so seeking back to an
// offset earlier than (b.offset - len(buf)) computes a negative pos.
func TestSeekForwardBeyondBufferWindow(t *testing.T) {
	t.Parallel()

	// cap(b.buf) is fixed at 4096 by newBuffer. Reading past one buffer's
	// worth of data forces a second reload, after which buf only holds the
	// most recent chunk and no longer covers offset 0.
	data := bytes.Repeat([]byte("x"), 9000)
	b := newBuffer(bytes.NewReader(data), 0)
	b.allowEOF = true

	for b.offset <= 4096 {
		b.readByte()
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("seekForward(0): no panic, want out-of-buffer error")
		}
	}()
	b.seekForward(0)
}

// TestReadKeywordLiterals covers readKeyword's boolean, integer, real, and
// bare-keyword branches, plus the malformed-integer error path for a value
// that fits the digit-only grammar but overflows int64.
func TestReadKeywordLiterals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want token
	}{
		{"true", "true", true},
		{"false", "false", false},
		{"integer", "42", int64(42)},
		{"negative integer", "-7", int64(-7)},
		{"real", "12.5", 12.5},
		{"negative real", "-0.5", -0.5},
		{"bare keyword", "obj", keyword("obj")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBuffer(strings.NewReader(tt.in), 0)
			b.allowEOF = true
			if got := b.readKeyword(); got != tt.want {
				t.Errorf("readKeyword(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}

	t.Run("integer overflow panics", func(t *testing.T) {
		t.Parallel()

		// All-digit, so isInteger accepts it, but it does not fit an int64.
		b := newBuffer(strings.NewReader("99999999999999999999"), 0)
		b.allowEOF = true

		defer func() {
			if recover() == nil {
				t.Error("readKeyword: no panic on integer overflow, want one")
			}
		}()
		b.readKeyword()
	})
}

// TestIsInteger covers isInteger's sign handling and digit scan directly.
func TestIsInteger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"digits", "123", true},
		{"leading plus", "+123", true},
		{"leading minus", "-123", true},
		{"sign only", "-", false},
		{"non digit", "12a", false},
		{"decimal point", "1.2", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := isInteger(tt.in); got != tt.want {
				t.Errorf("isInteger(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestIsReal covers isReal's sign handling, dot counting, and digit scan.
func TestIsReal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"integer, no dot", "123", false},
		{"one dot", "1.2", true},
		{"leading plus", "+1.2", true},
		{"leading minus", "-1.2", true},
		{"sign only", "-", false},
		{"two dots", "1.2.3", false},
		{"non digit", "1.2a", false},
		{"dot only", ".", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := isReal(tt.in); got != tt.want {
				t.Errorf("isReal(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestReadDict covers readDict's branches directly: an io.EOF token ending
// the key/value loop, a non-name key (reported and skipped), the
// allowStream=false early return, and the "stream" keyword's newline
// handling (bare \n, \r\n, lone \r, and a missing newline entirely).
func TestReadDict(t *testing.T) {
	t.Parallel()

	t.Run("eof ends dict", func(t *testing.T) {
		t.Parallel()

		// No closing >>, so the loop only stops via readToken returning
		// io.EOF once allowEOF observes end of input.
		b := newBuffer(strings.NewReader("/A 1"), 0)
		b.allowEOF = true
		got, ok := b.readDict().(dict)
		if !ok {
			t.Fatal("readDict did not return a dict")
		}
		if got[name("A")] != int64(1) {
			t.Errorf("dict[A] = %v, want 1", got[name("A")])
		}
	})

	t.Run("non-name key panics", func(t *testing.T) {
		t.Parallel()

		b := newBuffer(strings.NewReader("1 2 >>"), 0)
		b.allowEOF = true
		defer func() {
			if recover() == nil {
				t.Error("readDict: no panic on non-name key, want one")
			}
		}()
		b.readDict()
	})

	t.Run("allowStream false skips stream body", func(t *testing.T) {
		t.Parallel()

		b := newBuffer(strings.NewReader("/Length 1 >>\nstream\nX\nendstream"), 0)
		b.allowEOF = true
		b.allowStream = false
		got, ok := b.readDict().(dict)
		if !ok {
			t.Fatal("readDict did not return a dict")
		}
		if got[name("Length")] != int64(1) {
			t.Errorf("dict[Length] = %v, want 1", got[name("Length")])
		}
	})

	streamNewline := []struct {
		name string
		body string
	}{
		{"lf", "/L 1 >>\nstream\nX"},
		{"crlf", "/L 1 >>\nstream\r\nX"},
		{"lone cr", "/L 1 >>\nstream\rX"},
	}
	for _, tt := range streamNewline {
		t.Run("stream newline "+tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBuffer(strings.NewReader(tt.body), 0)
			b.allowEOF = true
			strm, ok := b.readDict().(stream)
			if !ok {
				t.Fatal("readDict did not return a stream")
			}
			if strm.hdr[name("L")] != int64(1) {
				t.Errorf("stream.hdr[L] = %v, want 1", strm.hdr[name("L")])
			}
		})
	}

	t.Run("stream keyword without newline panics", func(t *testing.T) {
		t.Parallel()

		b := newBuffer(strings.NewReader("/L 1 >>\nstream(X"), 0)
		b.allowEOF = true
		defer func() {
			if recover() == nil {
				t.Error("readDict: no panic when stream is not followed by a newline, want one")
			}
		}()
		b.readDict()
	})
}

// TestReadObjectIndirectReferenceForms covers readObject's lookahead for the
// "N G R" and "N G obj ... endobj" forms: two integers not followed by
// either keyword must be unread and returned as a bare token, and a missing
// endobj after a non-stream object body must panic.
func TestReadObjectIndirectReferenceForms(t *testing.T) {
	t.Parallel()

	t.Run("two integers with no R or obj", func(t *testing.T) {
		t.Parallel()

		b := newBuffer(strings.NewReader("1 2 3"), 0)
		b.allowEOF = true
		got := b.readObject()
		if got != int64(1) {
			t.Fatalf("readObject() = %#v, want int64(1)", got)
		}
		// The unread "2" and "3" must still be available as separate
		// tokens, not swallowed by the lookahead.
		if got := b.readToken(); got != int64(2) {
			t.Errorf("next token = %#v, want int64(2)", got)
		}
		if got := b.readToken(); got != int64(3) {
			t.Errorf("next token = %#v, want int64(3)", got)
		}
	})

	t.Run("missing endobj panics", func(t *testing.T) {
		t.Parallel()

		b := newBuffer(strings.NewReader("1 0 obj (hi) notendobj"), 0)
		b.allowEOF = true
		defer func() {
			if recover() == nil {
				t.Error("readObject: no panic on missing endobj, want one")
			}
		}()
		b.readObject()
	})

	t.Run("indirect reference", func(t *testing.T) {
		t.Parallel()

		b := newBuffer(strings.NewReader("5 0 R"), 0)
		b.allowEOF = true
		got, ok := b.readObject().(objptr)
		if !ok || got != (objptr{5, 0}) {
			t.Errorf("readObject() = %#v, want objptr{5,0}", got)
		}
	})
}

// TestReadLiteralString covers readLiteralString's escape handling: named
// escapes (\n \r \b \t \f), literal escapes of the three special characters,
// nested balanced parens, a backslash-newline line continuation (both bare
// \n and \r\n), octal character escapes, an out-of-range octal escape, and
// an unrecognized escape character. Called directly because the leading '('
// is consumed by readToken before dispatching here.
func TestReadLiteralString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string // without the leading '(' that readToken would consume
		want string
	}{
		{"empty", ")", ""},
		{"plain text", "hello)", "hello"},
		{"nested parens", "a(b)c)", "a(b)c"},
		{"named escapes", `\n\r\b\t\f)`, "\n\r\b\t\f"},
		{"literal escapes", `\(\)\\)`, `()\`},
		{"line continuation lf", "a\\\nb)", "ab"},
		{"line continuation crlf", "a\\\r\nb)", "ab"},
		{"line continuation cr only", "a\\\rb)", "ab"},
		{"octal escape", `\101)`, "A"}, // \101 octal = 0x41 = 'A'
		{"short octal escape", `\7)`, "\a"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBuffer(strings.NewReader(tt.in), 0)
			b.allowEOF = true
			if got := b.readLiteralString(); got != token(tt.want) {
				t.Errorf("readLiteralString(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	t.Run("octal escape overflow panics", func(t *testing.T) {
		t.Parallel()

		b := newBuffer(strings.NewReader(`\777)`), 0)
		b.allowEOF = true
		defer func() {
			if recover() == nil {
				t.Error("readLiteralString: no panic on octal escape > 255, want one")
			}
		}()
		b.readLiteralString()
	})

	t.Run("invalid escape panics", func(t *testing.T) {
		t.Parallel()

		b := newBuffer(strings.NewReader(`\z)`), 0)
		b.allowEOF = true
		defer func() {
			if recover() == nil {
				t.Error("readLiteralString: no panic on invalid escape, want one")
			}
		}()
		b.readLiteralString()
	})
}

// Whitebox: exercises buffer, token, and object parsing internals of lex.go
// that have no exported surface (buffer, token, keyword, name are all
// unexported).

package pdf

import (
	"bytes"
	"fmt"
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

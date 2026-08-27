// Whitebox: exercises readObject's numeric-token-to-object-pointer/object-
// definition promotion bounds and readArray's EOF-termination guards, all in
// lex.go. buffer, newBuffer, readObject, readArray, objptr, and objdef are
// all unexported, so this coverage is only reachable from inside package pdf.
//
// This is a mutation-coverage workstream (WS12) kept separate from other
// lex.go test additions so it can be reviewed and merged independently.

package pdf

import (
	"strings"
	"testing"
)

// TestWSLexReadObjectReferenceBounds covers readObject's lookahead that
// promotes two consecutive integers followed by "R" into an objptr: the
// first integer is bounded by math.MaxUint32, the second by math.MaxUint16,
// both floored at zero. Values outside those bounds fall through to the
// bare first-token return described in the "obj/objdef bounds" comment
// in lex.go, without consuming the lookahead tokens.
func TestWSLexReadObjectReferenceBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want objptr
	}{
		{"first at MaxUint32", "4294967295 0 R", objptr{4294967295, 0}},
		{"second at MaxUint16", "1 65535 R", objptr{1, 65535}},
		{"both at low boundary zero", "0 0 R", objptr{0, 0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBuffer(strings.NewReader(tt.in), 0)
			b.allowEOF = true
			got, ok := b.readObject().(objptr)
			if !ok || got != tt.want {
				t.Errorf("readObject(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

// TestWSLexReadObjectReferenceRejected covers the same lookahead one step
// past each bound (and below zero): the promotion must not fire, and the
// concrete non-reference value it falls back to must be exact. In every
// case here, readObject returns the first integer token bare (as int64),
// because the bound check on t1/t2 fails before any lookahead token is
// consumed and unread -- so the next token(s) on the buffer are whatever
// followed the first number in the raw input, untouched.
func TestWSLexReadObjectReferenceRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		in        string
		want      int64
		wantNext  token
		wantNext2 token
	}{
		{"first one over MaxUint32", "4294967296 0 R", 4294967296, int64(0), keyword("R")},
		{"second one over MaxUint16", "1 65536 R", 1, int64(65536), keyword("R")},
		{"first negative", "-1 0 R", -1, int64(0), keyword("R")},
		{"second negative", "1 -1 R", 1, int64(-1), keyword("R")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBuffer(strings.NewReader(tt.in), 0)
			b.allowEOF = true
			got := b.readObject()
			if got != token(tt.want) {
				t.Fatalf("readObject(%q) = %#v, want int64(%d)", tt.in, got, tt.want)
			}
			if _, ok := got.(objptr); ok {
				t.Fatalf("readObject(%q) returned an objptr, want a bare int64", tt.in)
			}
			if n := b.readToken(); n != tt.wantNext {
				t.Errorf("next token = %#v, want %#v", n, tt.wantNext)
			}
			if n := b.readToken(); n != tt.wantNext2 {
				t.Errorf("second next token = %#v, want %#v", n, tt.wantNext2)
			}
		})
	}
}

// TestWSLexReadObjectDefinitionBounds covers the "obj ... endobj" form of
// the same lookahead: in bounds, it produces an objdef carrying the parsed
// body; the boundary-plus-one case falls through to the bare first token,
// identical in shape to the reference-form rejection above, without
// consuming "obj null endobj" at all.
func TestWSLexReadObjectDefinitionBounds(t *testing.T) {
	t.Parallel()

	t.Run("both at high boundary", func(t *testing.T) {
		t.Parallel()

		b := newBuffer(strings.NewReader("4294967295 65535 obj null endobj"), 0)
		b.allowEOF = true
		want := objdef{objptr{4294967295, 65535}, nil}
		got, ok := b.readObject().(objdef)
		if !ok || got != want {
			t.Errorf("readObject() = %#v, want %#v", got, want)
		}
	})

	t.Run("first one over MaxUint32 rejected", func(t *testing.T) {
		t.Parallel()

		b := newBuffer(strings.NewReader("4294967296 0 obj null endobj"), 0)
		b.allowEOF = true
		got := b.readObject()
		if got != token(int64(4294967296)) {
			t.Fatalf("readObject() = %#v, want int64(4294967296)", got)
		}
		if _, ok := got.(objdef); ok {
			t.Fatal("readObject() returned an objdef, want a bare int64")
		}
		// "0 obj null endobj" was never touched by the failed lookahead.
		if n := b.readToken(); n != token(int64(0)) {
			t.Errorf("next token = %#v, want int64(0)", n)
		}
		if n := b.readToken(); n != token(keyword("obj")) {
			t.Errorf("next token = %#v, want keyword(obj)", n)
		}
	})
}

// TestWSLexReadObjectNonReferenceUnread covers the case where two integers
// are followed by neither "R" nor "obj": the switch on the third token
// matches nothing, and readObject must push both the third and second
// tokens back onto the unread queue rather than swallowing them, so the
// caller still sees the second number as an ordinary subsequent token.
func TestWSLexReadObjectNonReferenceUnread(t *testing.T) {
	t.Parallel()

	b := newBuffer(strings.NewReader("1 0 X"), 0)
	b.allowEOF = true

	got := b.readObject()
	if got != token(int64(1)) {
		t.Fatalf("readObject() = %#v, want int64(1)", got)
	}
	if n := b.readToken(); n != token(int64(0)) {
		t.Errorf("next token = %#v, want int64(0) (the unread second number)", n)
	}
	if n := b.readToken(); n != token(keyword("X")) {
		t.Errorf("next token = %#v, want keyword(X)", n)
	}
}

// TestWSLexReadArrayTerminationExact covers readArray's normal, EOF, empty,
// and nested termination paths with exact element-by-element assertions.
func TestWSLexReadArrayTerminationExact(t *testing.T) {
	t.Parallel()

	t.Run("closed by bracket", func(t *testing.T) {
		t.Parallel()

		b := newBuffer(strings.NewReader("[1 2 3]"), 0)
		b.allowEOF = true
		got, ok := b.readObject().(array)
		want := array{int64(1), int64(2), int64(3)}
		if !ok || len(got) != len(want) {
			t.Fatalf("readObject() = %#v, want %#v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("element %d = %#v, want %#v", i, got[i], want[i])
			}
		}
	})

	// readArray hits the bare io.EOF sentinel (readToken's line-162 return)
	// once allowEOF is set and the underlying reader is exhausted mid-array.
	// This is the only reachable EOF-termination path in readArray: the
	// second guard in the source (tok.(error) with err == io.EOF, a few
	// lines below the direct "tok == io.EOF" comparison) is unreachable
	// dead code. For any given tok, "tok == io.EOF" and "tok, ok :=
	// tok.(error); ok && tok == io.EOF" are the same boolean by Go's
	// interface-equality semantics (== on an interface value already
	// compares dynamic type and value, which is exactly what the type
	// assertion plus comparison re-derives) -- so whichever is true, the
	// first, textually-earlier check in the loop always fires first.
	// readToken also only ever manufactures the bare io.EOF sentinel
	// (never a distinct wrapped error value) as a token, so there is no
	// input that reaches the second check at all. Confirmed by construction
	// against the current source; not exercised as a second test case
	// because no such input exists.
	t.Run("unterminated hits EOF", func(t *testing.T) {
		t.Parallel()

		mustNotCrash(t, func() {
			b := newBuffer(strings.NewReader("[1 2 3"), 0)
			b.allowEOF = true
			got, ok := b.readObject().(array)
			want := array{int64(1), int64(2), int64(3)}
			if !ok || len(got) != len(want) {
				t.Fatalf("readObject() = %#v, want %#v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("element %d = %#v, want %#v", i, got[i], want[i])
				}
			}
		})
	})

	t.Run("empty array", func(t *testing.T) {
		t.Parallel()

		b := newBuffer(strings.NewReader("[]"), 0)
		b.allowEOF = true
		got, ok := b.readObject().(array)
		if !ok {
			t.Fatal("readObject() did not return an array")
		}
		// readArray's underlying `var x array` is never appended to when the
		// first token is "]", so the result is a nil slice with len 0 -- not
		// a non-nil empty slice. Assert the actual, verified shape.
		if len(got) != 0 {
			t.Errorf("len(got) = %d, want 0", len(got))
		}
	})

	t.Run("nested array", func(t *testing.T) {
		t.Parallel()

		b := newBuffer(strings.NewReader("[[1] 2]"), 0)
		b.allowEOF = true
		got, ok := b.readObject().(array)
		if !ok || len(got) != 2 {
			t.Fatalf("readObject() = %#v, want a 2-element array", got)
		}
		inner, ok := got[0].(array)
		if !ok || len(inner) != 1 || inner[0] != int64(1) {
			t.Errorf("element 0 = %#v, want array{1}", got[0])
		}
		if got[1] != int64(2) {
			t.Errorf("element 1 = %#v, want int64(2)", got[1])
		}
	})
}

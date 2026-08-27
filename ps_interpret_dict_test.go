// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file is a mutation-testing coverage workstream (WS11), kept separate
// from the rest of the suite so it can be reviewed and merged independently.
// It covers Interpret's guard/panic paths around the "currentdict", "begin",
// "end", and "def" PostScript operators in ps.go (plus the adjacent "dict"
// and "pop" keywords handled by the same switch), which TestInterpretDictOperators
// in ps_test.go does not exercise: bare end/currentdict/def with no open
// dict, begin on a non-dict value, nesting-depth tracking across repeated
// begin/end, currentdict observability, and def with a non-name key.

package pdf_test

import (
	"testing"

	"github.com/jh125486/pdf"
)

// TestInterpretPanicsWithoutOpenDict covers the three operators that require
// at least one open dict on Interpret's internal dict stack -- "end",
// "currentdict", and "def" -- and panic with an exact message when none is
// open.
func TestInterpretPanicsWithoutOpenDict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "bare end",
			content: "end",
			want:    "mismatched begin/end",
		},
		{
			name:    "bare currentdict",
			content: "currentdict",
			want:    "no current dictionary",
		},
		{
			name:    "def with no open dict",
			content: "/Key (val) def",
			want:    "def without open dict",
		},
		{
			name:    "begin once then end twice panics on the second end",
			content: "1 dict begin end end",
			want:    "mismatched begin/end",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			panicked, timedOut := run(t, func() {
				pdf.Interpret(rawStreamPS(tt.content), func(*pdf.Stack, string) {})
			})
			if timedOut {
				t.Fatalf("did not return within %v", caseTimeout)
			}
			msg, ok := panicked.(string)
			if !ok {
				t.Fatalf("panic value = %#v, want string %q", panicked, tt.want)
			}
			if msg != tt.want {
				t.Errorf("panic = %q, want %q", msg, tt.want)
			}
		})
	}
}

// TestInterpretBeginNonDictPanics covers "begin" popping a value that is not
// a dict, for each of several non-dict kinds. The source panics with the
// same message regardless of what kind the offending value is.
func TestInterpretBeginNonDictPanics(t *testing.T) {
	t.Parallel()

	const want = "cannot begin non-dict"

	tests := []struct {
		name    string
		content string
	}{
		{name: "integer", content: "1 begin"},
		{name: "string", content: "(str) begin"},
		{name: "name", content: "/Foo begin"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			panicked, timedOut := run(t, func() {
				pdf.Interpret(rawStreamPS(tt.content), func(*pdf.Stack, string) {})
			})
			if timedOut {
				t.Fatalf("did not return within %v", caseTimeout)
			}
			msg, ok := panicked.(string)
			if !ok {
				t.Fatalf("panic value = %#v, want string %q", panicked, want)
			}
			if msg != want {
				t.Errorf("panic = %q, want %q", msg, want)
			}
		})
	}
}

// TestInterpretNestedBeginEndBalances covers that Interpret's dict stack
// correctly tracks nesting depth: two begins each need their own end, and
// no panic occurs as long as begin/end stay balanced.
func TestInterpretNestedBeginEndBalances(t *testing.T) {
	t.Parallel()

	mustNotCrash(t, func() {
		pdf.Interpret(rawStreamPS("1 dict begin 1 dict begin end end"), func(*pdf.Stack, string) {})
	})
}

// TestInterpretCurrentDictIsInnermost covers that "currentdict" exposes the
// innermost open dict, and that a "def" made inside it is observable through
// the dict Value that currentdict pushes -- captured here via the do
// callback and Value.Key, the same observation mechanism
// TestInterpretNameLookupResolvesValue uses.
func TestInterpretCurrentDictIsInnermost(t *testing.T) {
	t.Parallel()

	var got pdf.Value
	pdf.Interpret(rawStreamPS("1 dict begin /Greeting (hi) def currentdict show"), func(stk *pdf.Stack, op string) {
		if op != "show" {
			return
		}
		got = stk.Pop()
	})

	if got.Kind() != pdf.Dict {
		t.Fatalf("currentdict pushed Kind() = %v, want Dict", got.Kind())
	}
	if greeting := got.Key("Greeting").RawString(); greeting != "hi" {
		t.Errorf("currentdict Key(%q) = %q, want %q", "Greeting", greeting, "hi")
	}
}

// TestInterpretDefNonNameKeySkipped covers "def" whose key is not a Name:
// per the current source it does not panic, it silently discards the
// key/value pair (both are still popped off the stack) rather than storing
// anything in the open dict.
func TestInterpretDefNonNameKeySkipped(t *testing.T) {
	t.Parallel()

	var got pdf.Value
	mustNotCrash(t, func() {
		pdf.Interpret(rawStreamPS("1 dict begin (notaname) (val) def currentdict show"), func(stk *pdf.Stack, op string) {
			if op != "show" {
				return
			}
			got = stk.Pop()
		})
	})

	if got.Kind() != pdf.Dict {
		t.Fatalf("currentdict pushed Kind() = %v, want Dict", got.Kind())
	}
	if keys := got.Keys(); len(keys) != 0 {
		t.Errorf("dict Keys() = %v, want none (def with non-name key must not store anything)", keys)
	}
}

// TestInterpretDisallowsObjptrAndStreamTokens covers that Interpret's
// buffer is configured with allowObjptr=false and allowStream=false (ps.go),
// so token forms that would normally be recognized as an indirect reference
// or a stream body are instead surfaced as plain keywords/values.
func TestInterpretDisallowsObjptrAndStreamTokens(t *testing.T) {
	t.Parallel()

	t.Run("indirect-reference-looking tokens are not merged into an objptr", func(t *testing.T) {
		t.Parallel()

		var ops []string
		var stackLenAtR int
		pdf.Interpret(rawStreamPS("1 0 R"), func(stk *pdf.Stack, op string) {
			ops = append(ops, op)
			if op == "R" {
				stackLenAtR = stk.Len()
			}
		})

		if want := []string{"R"}; len(ops) != len(want) || ops[0] != want[0] {
			t.Fatalf("ops = %v, want %v (allowObjptr=false must leave \"R\" as a bare keyword)", ops, want)
		}
		if stackLenAtR != 2 {
			t.Errorf("stack length when %q reached do = %d, want 2 (the un-merged 1 and 0)", "R", stackLenAtR)
		}
	})

	t.Run("stream keyword is not consumed as a stream body marker", func(t *testing.T) {
		t.Parallel()

		var ops []string
		mustNotCrash(t, func() {
			pdf.Interpret(rawStreamPS("<< >> stream"), func(_ *pdf.Stack, op string) {
				ops = append(ops, op)
			})
		})

		if want := []string{"stream"}; len(ops) != len(want) || ops[0] != want[0] {
			t.Fatalf("ops = %v, want %v (allowStream=false must leave \"stream\" as a bare keyword)", ops, want)
		}
	})
}

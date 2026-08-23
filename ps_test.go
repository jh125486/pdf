package pdf_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/jh125486/pdf"
)

// splitTextArrayPDF builds a page whose /Contents is an array of two
// separate content streams: the first opens a "[ (Hello)" TJ array and the
// second closes it with "( world)] TJ". Interpret concatenates the streams
// with io.MultiReader, so the array token has to survive the boundary
// between them.
func splitTextArrayPDF() []byte {
	var pdfBuf bytes.Buffer
	pdfBuf.WriteString("%PDF-1.4\n")
	offsets := make([]int, 7)
	writeObject := func(number int, body string) {
		offsets[number] = pdfBuf.Len()
		fmt.Fprintf(&pdfBuf, "%d 0 obj\n%s\nendobj\n", number, body)
	}
	writeStream := func(number int, content string) {
		writeObject(number, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content)+1, content))
	}

	writeObject(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObject(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObject(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << /Font << /F1 6 0 R >> >> /Contents [4 0 R 5 0 R] >>")
	writeStream(4, "BT /F1 12 Tf 20 100 Td [(Hello)")
	writeStream(5, "( world)] TJ ET")
	writeObject(6, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	xrefOffset := pdfBuf.Len()
	pdfBuf.WriteString("xref\n0 7\n0000000000 65535 f \n")
	for number := 1; number <= 6; number++ {
		fmt.Fprintf(&pdfBuf, "%010d 00000 n \n", offsets[number])
	}
	fmt.Fprintf(&pdfBuf, "trailer\n<< /Size 7 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOffset)
	return pdfBuf.Bytes()
}

// TestInterpretContinuesTokensAcrossContentStreams calls the exported
// Interpret directly against a /Contents value that resolves to an array of
// two streams (Value.Kind() == Array), verifying the TJ array token split
// across the stream boundary is reassembled correctly rather than treated
// as two separate, truncated tokens.
func TestInterpretContinuesTokensAcrossContentStreams(t *testing.T) {
	t.Parallel()

	data := splitTextArrayPDF()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}

	contents := r.Page(1).V.Key("Contents")
	if contents.Kind() != pdf.Array {
		t.Fatalf("Contents kind = %v, want Array", contents.Kind())
	}

	var got strings.Builder
	pdf.Interpret(contents, func(stk *pdf.Stack, op string) {
		if op != "TJ" {
			return
		}
		arr := stk.Pop()
		for i := 0; i < arr.Len(); i++ {
			if el := arr.Index(i); el.Kind() == pdf.String {
				got.WriteString(el.RawString())
			}
		}
	})

	if want := "Hello world"; got.String() != want {
		t.Fatalf("text = %q, want %q", got.String(), want)
	}
}

// TestStackPopEmpty covers Pop's empty-stack branch, which returns the zero
// Value rather than panicking or indexing out of bounds.
func TestStackPopEmpty(t *testing.T) {
	t.Parallel()

	var stk pdf.Stack
	got := stk.Pop()
	if got.Kind() != pdf.Null {
		t.Errorf("Pop on empty stack: Kind() = %v, want Null", got.Kind())
	}
}

// TestInterpretDictOperators covers Interpret's built-in PostScript-dict
// operators (dict, begin, currentdict, def, pop, end) and the name-lookup
// path that resolves a bare keyword against the open dict stack before
// handing it to the caller's do function. These operators back the limited
// PostScript found in ToUnicode CMaps, but are exercised here directly
// rather than through a full cmap, since most real cmaps never use "def".
func TestInterpretDictOperators(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		wantOps []string // operator names seen by do, in order
	}{
		{
			name:    "dict def then resolved by name lookup",
			content: "1 dict begin /Greeting (hi) def Greeting end",
			// "Greeting" resolves against the open dict instead of
			// reaching do as an operator, so only the dict push/pop
			// machinery runs; do sees nothing.
			wantOps: nil,
		},
		{
			name:    "currentdict pushes the open dict",
			content: "1 dict begin currentdict end",
			wantOps: nil,
		},
		{
			name:    "pop discards the top of stack",
			content: "1 2 pop show",
			wantOps: []string{"show"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotOps []string
			mustNotCrash(t, func() {
				pdf.Interpret(rawStreamPS(tt.content), func(stk *pdf.Stack, op string) {
					gotOps = append(gotOps, op)
				})
			})
			if len(gotOps) != len(tt.wantOps) {
				t.Fatalf("ops = %v, want %v", gotOps, tt.wantOps)
			}
			for i := range gotOps {
				if gotOps[i] != tt.wantOps[i] {
					t.Errorf("ops[%d] = %q, want %q", i, gotOps[i], tt.wantOps[i])
				}
			}
		})
	}
}

// TestInterpretNameLookupResolvesValue covers the branch where a bare
// keyword resolves to a value bound by "def" in an open dict: the resolved
// value is pushed onto the stack rather than the keyword reaching do as an
// operator.
func TestInterpretNameLookupResolvesValue(t *testing.T) {
	t.Parallel()

	var got string
	pdf.Interpret(rawStreamPS("1 dict begin /Greeting (hi) def Greeting show"), func(stk *pdf.Stack, op string) {
		if op != "show" {
			return
		}
		got = stk.Pop().RawString()
	})
	if got != "hi" {
		t.Errorf("resolved Greeting = %q, want %q", got, "hi")
	}
}

// rawStreamPS returns a Value of Kind Stream holding content, with no
// filter, for exercising Interpret directly. It mirrors helpers_test.go's
// pattern for building fixtures from a real PDF, but Interpret only needs a
// Reader, not a full document.
func rawStreamPS(content string) pdf.Value {
	data := buildPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
	})
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		panic(err)
	}
	return r.Page(1).V.Key("Contents")
}

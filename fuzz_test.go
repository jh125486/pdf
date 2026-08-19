package pdf

import (
	"bytes"
	"testing"
)

// FuzzNewReader exercises the whole parse-and-extract path against
// arbitrary byte input. NewReader and GetPlainText both already recover
// from panics raised by malformed input (see read.go, page.go), so this
// isn't expected to find new crashes so much as guard against a future
// change reintroducing one -- exactly the class of regression the bounds
// checks throughout this fork exist to prevent in the first place.
//
// Run with `go test -fuzz=FuzzNewReader` to actually fuzz beyond the seed
// corpus; a plain `go test` run replays only the seeds below.
func FuzzNewReader(f *testing.F) {
	f.Add(validPDF())
	f.Add(objStmPDF())
	f.Add(xrefStreamPDF("/Size 4 /W [1 1 1]", "\x01\x09\x00"))

	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return
		}
		_, _ = r.GetPlainText()
	})
}

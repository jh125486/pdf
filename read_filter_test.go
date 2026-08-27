// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file is a mutation-coverage workstream (WS5: Value.Reader filter
// chain and PNG-Up predictor bounds) kept separate so it can be reviewed
// and merged independently of other coverage work in this package. It adds
// only test code; no production source is touched.
package pdf_test

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"testing"

	"github.com/jh125486/pdf"
)

// wsFilterPNGUpRows returns the raw (pre-zlib) bytes for a PNG "Up"
// predictor stream built from rows, each of the same length (the
// /Columns value). Every row is prefixed with the PNG filter-type byte 2
// (Up), and its payload is the delta from the previous row -- or from an
// all-zero row for the first one, since pngUpReader starts its history at
// zero.
func wsFilterPNGUpRows(rows [][]byte) []byte {
	var raw []byte
	prev := make([]byte, len(rows[0]))
	for _, row := range rows {
		raw = append(raw, 2)
		for i, b := range row {
			raw = append(raw, b-prev[i])
		}
		prev = row
	}
	return raw
}

// wsFilterZlib zlib-compresses raw, failing the test on any writer error.
func wsFilterZlib(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatalf("zlib.Write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zlib.Close: %v", err)
	}
	return buf.Bytes()
}

// wsFilterBlobPDF builds a minimal PDF whose Catalog has a /Blob entry
// pointing at a stream object with the given header fields (everything
// between "<<" and ">>", not including /Length, which is computed from
// data) and raw stream data.
func wsFilterBlobPDF(hdrFields, data string) []byte {
	return buildPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R /Blob 3 0 R >>",
		"<< /Type /Pages /Kids [] /Count 0 >>",
		fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", hdrFields, len(data), data),
	})
}

// wsFilterBlobReader opens data as a PDF and returns the Reader for the
// Catalog's /Blob entry, failing the test if the PDF itself doesn't open.
func wsFilterBlobReader(t *testing.T, data []byte) io.ReadCloser {
	t.Helper()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return r.Trailer().Key("Root").Key("Blob").Reader()
}

// TestValueReaderColumnsBounds covers applyFilter's /Columns bound guard
// for FlateDecode /Predictor 12: accepted exactly at maxPredictorColumns,
// rejected one above it, and rejected for a negative value. This targets
// the boundary of `columns < 0 || columns > maxPredictorColumns` in
// read.go specifically -- TestValueReaderPNGUpPredictor (read_test.go)
// exercises the PNG-Up decode itself at a small, unbounded /Columns and
// doesn't touch this guard.
func TestValueReaderColumnsBounds(t *testing.T) {
	t.Parallel()

	const maxColumns = 1 << 20 // maxPredictorColumns in read.go

	t.Run("accepted at the limit", func(t *testing.T) {
		t.Parallel()

		row := bytes.Repeat([]byte{'A'}, maxColumns)
		raw := wsFilterPNGUpRows([][]byte{row})
		compressed := wsFilterZlib(t, raw)
		data := wsFilterBlobPDF(
			fmt.Sprintf("/Filter /FlateDecode /DecodeParms << /Predictor 12 /Columns %d >>", maxColumns),
			string(compressed))
		rc := wsFilterBlobReader(t, data)
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("reading at /Columns %d: %v", maxColumns, err)
		}
		if !bytes.Equal(got, row) {
			t.Errorf("decoded %d bytes, want %d bytes matching the input row", len(got), len(row))
		}
	})

	t.Run("one above the limit", func(t *testing.T) {
		t.Parallel()

		// The /Columns check runs before any stream data is consumed, so
		// the compressed payload just needs to be a valid zlib stream.
		compressed := wsFilterZlib(t, []byte("x"))
		data := wsFilterBlobPDF(
			fmt.Sprintf("/Filter /FlateDecode /DecodeParms << /Predictor 12 /Columns %d >>", maxColumns+1),
			string(compressed))
		rc := wsFilterBlobReader(t, data)
		defer rc.Close()
		_, err := io.ReadAll(rc)
		if err == nil {
			t.Fatal("got nil error, want one")
		}
		want := fmt.Sprintf("invalid FlateDecode /Columns %d", maxColumns+1)
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
	})

	t.Run("negative", func(t *testing.T) {
		t.Parallel()

		compressed := wsFilterZlib(t, []byte("x"))
		data := wsFilterBlobPDF(
			"/Filter /FlateDecode /DecodeParms << /Predictor 12 /Columns -1 >>",
			string(compressed))
		rc := wsFilterBlobReader(t, data)
		defer rc.Close()
		_, err := io.ReadAll(rc)
		if err == nil {
			t.Fatal("got nil error, want one")
		}
		const want = "invalid FlateDecode /Columns -1"
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
	})

	t.Run("Predictor 12 at Columns 1 minimal boundary", func(t *testing.T) {
		t.Parallel()

		rows := [][]byte{{10}, {20}, {15}}
		raw := wsFilterPNGUpRows(rows)
		compressed := wsFilterZlib(t, raw)
		data := wsFilterBlobPDF(
			"/Filter /FlateDecode /DecodeParms << /Predictor 12 /Columns 1 >>",
			string(compressed))
		rc := wsFilterBlobReader(t, data)
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("reading at /Columns 1: %v", err)
		}
		want := []byte{10, 20, 15}
		if !bytes.Equal(got, want) {
			t.Errorf("decoded = %v, want %v", got, want)
		}
	})
}

// TestValueReaderPredictorDispatch covers the /Predictor value switch in
// applyFilter: absent (Null, raw zlib), and an unsupported value (15,
// distinct from the /Predictor 2 case already covered by
// TestValueReaderFilters in read_test.go, which only checks that an error
// occurs and not its exact text).
func TestValueReaderPredictorDispatch(t *testing.T) {
	t.Parallel()

	t.Run("Predictor absent returns raw zlib reader", func(t *testing.T) {
		t.Parallel()

		want := "the quick brown fox jumps over the lazy dog, repeated for zlib to have something to compress"
		compressed := wsFilterZlib(t, []byte(want))
		data := wsFilterBlobPDF("/Filter /FlateDecode", string(compressed))
		rc := wsFilterBlobReader(t, data)
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("reading FlateDecode without /Predictor: %v", err)
		}
		if string(got) != want {
			t.Errorf("decoded = %q, want %q", got, want)
		}
	})

	t.Run("unsupported Predictor value", func(t *testing.T) {
		t.Parallel()

		compressed := wsFilterZlib(t, []byte("hello"))
		data := wsFilterBlobPDF(
			"/Filter /FlateDecode /DecodeParms << /Predictor 15 /Columns 1 >>",
			string(compressed))
		rc := wsFilterBlobReader(t, data)
		defer rc.Close()
		_, err := io.ReadAll(rc)
		if err == nil {
			t.Fatal("got nil error, want one")
		}
		const want = "pred"
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
	})
}

// TestValueReaderFilterArrayChain covers Value.Reader's Array-kind Filter
// dispatch: filters applied in declared order, an array of length 1
// distinguished from one of length >= 2, and /DecodeParms supplied as
// either a matching array (of nulls) or, tolerated by the source since
// Value.Index on a non-array Kind simply returns a null Value, a plain
// dictionary.
func TestValueReaderFilterArrayChain(t *testing.T) {
	t.Parallel()

	const want = "round trip through ASCII85 then Flate, applied in filter-array order"

	encodeChain := func(t *testing.T) string {
		t.Helper()
		compressed := wsFilterZlib(t, []byte(want))
		return asciiEncode(string(compressed))
	}

	t.Run("two filters, DecodeParms array of matching length", func(t *testing.T) {
		t.Parallel()

		encoded := encodeChain(t)
		data := wsFilterBlobPDF(
			"/Filter [/ASCII85Decode /FlateDecode] /DecodeParms [null null]",
			encoded)
		rc := wsFilterBlobReader(t, data)
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("reading 2-filter array chain: %v", err)
		}
		if string(got) != want {
			t.Errorf("decoded = %q, want %q", got, want)
		}
	})

	t.Run("two filters, DecodeParms as a plain dictionary", func(t *testing.T) {
		t.Parallel()

		// Value.Index requires Kind() == Array; on a Dict it returns a null
		// Value regardless of index, which is exactly what an absent
		// DecodeParms entry would also produce -- so a dict here is
		// tolerated rather than rejected.
		encoded := encodeChain(t)
		data := wsFilterBlobPDF(
			"/Filter [/ASCII85Decode /FlateDecode] /DecodeParms << /Unrelated 1 >>",
			encoded)
		rc := wsFilterBlobReader(t, data)
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("reading with dict DecodeParms: %v", err)
		}
		if string(got) != want {
			t.Errorf("decoded = %q, want %q", got, want)
		}
	})

	t.Run("single-element filter array", func(t *testing.T) {
		t.Parallel()

		single := "just one filter in the array"
		compressed := wsFilterZlib(t, []byte(single))
		data := wsFilterBlobPDF("/Filter [/FlateDecode]", string(compressed))
		rc := wsFilterBlobReader(t, data)
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("reading 1-filter array: %v", err)
		}
		if string(got) != single {
			t.Errorf("decoded = %q, want %q", got, single)
		}
	})
}

// TestValueReaderFilterKindErrors covers the Kind-dispatch switch in
// Value.Reader (a /Filter that is neither Null, Name, nor Array) and the
// default case of the filter-name switch in applyFilter (a Name that
// looks like a real filter but isn't implemented).
func TestValueReaderFilterKindErrors(t *testing.T) {
	t.Parallel()

	t.Run("Filter is an integer, not a name or array", func(t *testing.T) {
		t.Parallel()

		data := wsFilterBlobPDF("/Filter 5", "abc")
		rc := wsFilterBlobReader(t, data)
		defer rc.Close()
		_, err := io.ReadAll(rc)
		if err == nil {
			t.Fatal("got nil error, want one")
		}
		const want = "unsupported filter 5"
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
	})

	t.Run("Filter names a valid-looking but unimplemented filter", func(t *testing.T) {
		t.Parallel()

		data := wsFilterBlobPDF("/Filter /LZWDecode", "abc")
		rc := wsFilterBlobReader(t, data)
		defer rc.Close()
		_, err := io.ReadAll(rc)
		if err == nil {
			t.Fatal("got nil error, want one")
		}
		const want = "unknown filter LZWDecode"
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
	})
}

// TestValueReaderEmptyFlateStream covers Value.Reader's /Length 0
// short-circuit for a FlateDecode stream specifically: it must return
// immediately with an empty reader and no error rather than handing an
// empty section to zlib.NewReader, which would fail with an unexpected-EOF
// error.
func TestValueReaderEmptyFlateStream(t *testing.T) {
	t.Parallel()

	data := wsFilterBlobPDF("/Filter /FlateDecode", "")
	rc := wsFilterBlobReader(t, data)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading 0-length FlateDecode stream: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("decoded %d bytes, want 0", len(got))
	}
}

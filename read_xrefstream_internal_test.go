// Whitebox: verifies the decoded contents of the unexported r.xref table
// built by readXrefStreamData, which requires constructing raw /W-encoded
// xref stream bytes and reading the unexported xref/objptr fields directly.
// Blackbox tests in read_test.go already cover the error paths (malformed
// /W, /Index, /Size); this file covers the success path -- that valid /W
// field widths and /Index subsections decode to the exact table entries the
// PDF spec describes.

package pdf

import (
	"bytes"
	"fmt"
	"testing"
)

// encodeXrefField appends v to buf as a big-endian value width bytes wide.
// width may be 0, per the PDF spec's allowance for the first /W field, in
// which case nothing is written and the field decodes to 0 on read.
func encodeXrefField(buf *bytes.Buffer, width int, v int64) {
	b := make([]byte, width)
	x := v
	for i := width - 1; i >= 0; i-- {
		b[i] = byte(x) //nolint:gosec // G115: truncation to the low byte is the intended encoding
		x >>= 8
	}
	buf.Write(b)
}

// encodeXrefEntries packs rows of (type, field2, field3) triples using the
// given /W field widths, matching the byte layout readXrefStreamData reads.
func encodeXrefEntries(w [3]int, rows [][3]int64) string {
	var buf bytes.Buffer
	for _, row := range rows {
		encodeXrefField(&buf, w[0], row[0])
		encodeXrefField(&buf, w[1], row[1])
		encodeXrefField(&buf, w[2], row[2])
	}
	return buf.String()
}

// TestReadXrefStreamDataDecoding checks that readXrefStreamData, reached
// through NewReader, decodes /W field widths and /Index subsections into
// the exact cross-reference entries the PDF spec describes: free (type 0),
// normal (type 1), and compressed (type 2) entries at any field width,
// multiple /Index subsections (including gaps and an omitted /Index
// defaulting to the full /Size range), and "first entry wins" when an
// object number is described more than once.
func TestReadXrefStreamDataDecoding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		w     [3]int
		index string // extra /Index array contents; omitted when empty
		size  int64
		rows  [][3]int64
		want  map[int]xref // exact expected entries, by object number
	}{
		{
			name: "single-byte widths cover all three entry types",
			w:    [3]int{1, 1, 1},
			size: 3,
			rows: [][3]int64{
				{0, 0, 65535}, // free
				{1, 9, 0},     // normal: offset 9, generation 0
				{2, 5, 3},     // compressed: in object stream 5 at index 3
			},
			want: map[int]xref{
				0: {ptr: objptr{0, 65535}},
				1: {ptr: objptr{1, 0}, offset: 9},
				2: {ptr: objptr{2, 0}, inStream: true, stream: objptr{5, 0}, offset: 3},
			},
		},
		{
			name: "wide multi-byte fields decode big-endian",
			w:    [3]int{1, 4, 2},
			size: 1,
			rows: [][3]int64{
				{1, 16909320, 300}, // offset needs all 4 bytes; generation needs 2
			},
			want: map[int]xref{
				0: {ptr: objptr{0, 300}, offset: 16909320},
			},
		},
		{
			name: "zero-width first field forces type 1 and decodes to zero",
			w:    [3]int{0, 4, 0},
			size: 1,
			rows: [][3]int64{
				{9, 1234, 9}, // type and third field are unwritten (width 0)
			},
			want: map[int]xref{
				0: {ptr: objptr{0, 0}, offset: 1234},
			},
		},
		{
			name:  "two Index subsections leave the gap between them unset",
			w:     [3]int{1, 1, 1},
			index: "0 2 10 2",
			size:  12,
			rows: [][3]int64{
				{1, 1, 0}, // object 0
				{0, 0, 65535},
				{1, 2, 0}, // object 10
				{0, 0, 65535},
			},
			want: map[int]xref{
				0:  {ptr: objptr{0, 0}, offset: 1},
				1:  {ptr: objptr{0, 65535}},
				5:  {}, // in the gap: never described, stays zero-value
				10: {ptr: objptr{10, 0}, offset: 2},
				11: {ptr: objptr{0, 65535}},
			},
		},
		{
			name:  "an object number repeated in a later subsection is ignored",
			w:     [3]int{1, 1, 1},
			index: "1 1 1 1",
			size:  2,
			rows: [][3]int64{
				{1, 9, 0},  // first description of object 1: offset 9
				{1, 99, 0}, // repeated: must not overwrite the first
			},
			want: map[int]xref{
				1: {ptr: objptr{1, 0}, offset: 9},
			},
		},
		{
			name: "an omitted Index defaults to the full Size range",
			w:    [3]int{1, 1, 1},
			size: 2,
			rows: [][3]int64{
				{1, 5, 0},
				{1, 6, 0},
			},
			want: map[int]xref{
				0: {ptr: objptr{0, 0}, offset: 5},
				1: {ptr: objptr{1, 0}, offset: 6},
			},
		},
		{
			name: "an unrecognized entry type leaves the slot unset",
			w:    [3]int{1, 1, 1},
			size: 1,
			rows: [][3]int64{
				{9, 1, 1}, // type 9 is not 0, 1, or 2
			},
			want: map[int]xref{
				0: {},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			wArr := fmt.Sprintf("[%d %d %d]", tt.w[0], tt.w[1], tt.w[2])
			hdr := fmt.Sprintf("/Size %d /W %s", tt.size, wArr)
			if tt.index != "" {
				hdr += fmt.Sprintf(" /Index [%s]", tt.index)
			}
			data := xrefStreamPDF(hdr, encodeXrefEntries(tt.w, tt.rows))

			r, err := NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}

			for id, want := range tt.want {
				if id >= len(r.xref) {
					t.Errorf("object %d: table has only %d entries", id, len(r.xref))
					continue
				}
				if got := r.xref[id]; got != want {
					t.Errorf("object %d: xref = %+v, want %+v", id, got, want)
				}
			}
		})
	}
}

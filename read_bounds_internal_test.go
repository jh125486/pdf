// Mutation-coverage workstream (WS1): exercises the numeric bound
// primitives checkObjectNumber, int64ToInt, and preallocXref, plus their
// xref-stream and classic-xref-table call sites, directly. Kept as its own
// file (rather than folded into read_internal_test.go) so this workstream
// merges independently of the other mutation-coverage passes running in
// parallel against this package.
//
// Whitebox: checkObjectNumber, int64ToInt, preallocXref, readXrefTableData,
// and the maxObjectNumber/maxXrefPrealloc constants are all unexported, so
// package pdf_test cannot reach them directly.

package pdf

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"
)

// wsBoundsTablePDF builds a minimal classic-xref-table PDF whose xref
// section is exactly the given subsection header line, followed immediately
// by "trailer" (no entries). xrefTablePDF (helpers_test.go) is blackbox-only
// and unavailable to this whitebox file, hence this local equivalent.
func wsBoundsTablePDF(subsection string) []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	off := b.Len()
	b.WriteString("xref\n")
	b.WriteString(subsection)
	b.WriteString("\n")
	b.WriteString("trailer\n<< /Size 1 /Root 1 0 R >>\n")
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", off)
	return []byte(b.String())
}

func TestCheckObjectNumberBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		x          int64
		wantErr    bool
		wantSubstr string
	}{
		{name: "zero", x: 0, wantErr: false},
		{name: "at max", x: maxObjectNumber, wantErr: false},
		{
			name:       "one past max",
			x:          maxObjectNumber + 1,
			wantErr:    true,
			wantSubstr: "object number 8388609 out of range [0, 8388608]",
		},
		{name: "negative one", x: -1, wantErr: true},
		{name: "math.MinInt64", x: math.MinInt64, wantErr: true},
		{name: "math.MaxInt64", x: math.MaxInt64, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkObjectNumber(tt.x)
			if tt.wantErr && err == nil {
				t.Fatalf("checkObjectNumber(%d) = nil, want error", tt.x)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("checkObjectNumber(%d) = %v, want nil", tt.x, err)
			}
			if tt.wantSubstr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantSubstr)) {
				t.Fatalf("checkObjectNumber(%d) error = %v, want substring %q", tt.x, err, tt.wantSubstr)
			}
		})
	}
}

func TestPreallocXrefBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		size    int64
		wantLen int
	}{
		{name: "zero", size: 0, wantLen: 0},
		{name: "at max prealloc", size: maxXrefPrealloc, wantLen: 65536},
		// One past maxXrefPrealloc is clamped, not rejected: read.go's
		// preallocXref caps size at maxXrefPrealloc rather than erroring.
		{name: "one past max prealloc, clamped", size: maxXrefPrealloc + 1, wantLen: 65536},
		{name: "max object number, clamped", size: maxObjectNumber, wantLen: 65536},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := preallocXref(tt.size)
			if len(got) != tt.wantLen {
				t.Fatalf("len(preallocXref(%d)) = %d, want %d", tt.size, len(got), tt.wantLen)
			}
		})
	}
}

// TestInt64ToIntBounds checks the round-trip at the representable extremes.
// On amd64/arm64 (64-bit int), math.MinInt == math.MinInt64 and
// math.MaxInt == math.MaxInt64, so there is no representable int64 value
// "one past" either bound to probe the x < math.MinInt / x > math.MaxInt
// comparison at read.go:163 with. The exact-limit values below are the only
// values that can reach that comparison at all; a statement-deletion mutant
// on that line is behaviorally equivalent on these platforms and may need a
// //nomutant suppression upstream if it still survives after this test (out
// of scope for this test-only change, so not added here).
func TestInt64ToIntBounds(t *testing.T) {
	t.Parallel()

	tests := []int64{0, math.MaxInt64, math.MinInt64, -1}

	for _, x := range tests {
		t.Run(fmt.Sprintf("x=%d", x), func(t *testing.T) {
			t.Parallel()

			got, ok := int64ToInt(x)
			if !ok {
				t.Fatalf("int64ToInt(%d) ok = false, want true", x)
			}
			if int64(got) != x {
				t.Fatalf("int64ToInt(%d) = %d, want %d", x, got, x)
			}
		})
	}
}

func TestReadXrefStreamSizeBound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		hdr          string
		wantSizeErr  bool
		forbidSubstr string
	}{
		{
			name:         "Size at max object number",
			hdr:          "/Size 8388608 /W [1 2 2] /Root 1 0 R",
			wantSizeErr:  false,
			forbidSubstr: "xref stream Size",
		},
		{
			name:        "Size one past max object number",
			hdr:         "/Size 8388609 /W [1 2 2] /Root 1 0 R",
			wantSizeErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := xrefStreamPDF(tt.hdr, "")
			_, err := NewReader(bytes.NewReader(data), int64(len(data)))
			if tt.wantSizeErr {
				if err == nil || !strings.Contains(err.Error(), "xref stream Size") {
					t.Fatalf("NewReader() error = %v, want substring %q", err, "xref stream Size")
				}
				if !strings.Contains(err.Error(), "object number 8388609 out of range [0, 8388608]") {
					t.Fatalf("NewReader() error = %v, want it to also name the exact offender", err)
				}
				return
			}
			// The declared Size passed the bound check; the file still fails
			// later (no entry data follows), but must not fail on Size.
			if err == nil {
				t.Fatalf("NewReader() = nil error, want a later (non-Size) failure since no entry data follows")
			}
			if strings.Contains(err.Error(), tt.forbidSubstr) {
				t.Fatalf("NewReader() error = %v, must not contain %q", err, tt.forbidSubstr)
			}
		})
	}
}

func TestReadXrefTableDataSubsectionBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		subsection   string
		wantSubstr   string
		forbidSubstr string
	}{
		{
			// start=0, n=maxObjectNumber: both checkObjectNumber(start) and
			// checkObjectNumber(start+n) accept this (start+n == maxObjectNumber
			// exactly). The subsection is accepted; the file only fails later
			// because no entries follow "trailer".
			name:         "subsection count at max object number is accepted",
			subsection:   "0 8388608",
			forbidSubstr: "out of range",
		},
		{
			// start=maxObjectNumber, n=1: start+n = maxObjectNumber+1, which
			// checkObjectNumber(start+n) rejects.
			name:       "start+n one past max object number",
			subsection: "8388608 1",
			wantSubstr: "object number 8388609 out of range [0, 8388608]",
		},
		{
			name:       "negative start",
			subsection: "-1 1",
			wantSubstr: "invalid subsection -1 1",
		},
		{
			// Ticket expectation was that this hits the dedicated "subsection
			// count %d out of range" message at read.go:590-591. Verified
			// against the actual source: with start=0, start+n == n, so
			// checkObjectNumber(start+n) (read.go:585, checked before the
			// n > maxObjectNumber guard) already rejects n=8388609 first,
			// via the same "object number ... out of range" message as the
			// case above. Because start is always >= 0 at that point (the
			// start < 0 case returns earlier), start+n >= n always holds, so
			// the "subsection count" message at read.go:590-591 can never be
			// reached by any valid input -- it is equivalent/dead code given
			// the preceding checkObjectNumber(start+n) guard. Documented
			// here rather than asserted as unreachable.
			name:       "subsection count over max also hits object-number check first",
			subsection: "0 8388609",
			wantSubstr: "object number 8388609 out of range [0, 8388608]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := wsBoundsTablePDF(tt.subsection)
			_, err := NewReader(bytes.NewReader(data), int64(len(data)))
			if err == nil {
				t.Fatalf("NewReader() = nil error, want an error for subsection %q", tt.subsection)
			}
			if tt.wantSubstr != "" && !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("NewReader() error = %v, want substring %q", err, tt.wantSubstr)
			}
			if tt.forbidSubstr != "" && strings.Contains(err.Error(), tt.forbidSubstr) {
				t.Fatalf("NewReader() error = %v, must not contain %q", err, tt.forbidSubstr)
			}
		})
	}
}

// Whitebox: utf16Decode, isPDFDocEncoded and pdfDocDecode are unexported and
// have no exported wrapper (Value.Text exercises them together, but not
// every branch individually).

package pdf

import "testing"

// TestIsPDFDocEncoded pins the two ways a string is rejected as
// PDFDocEncoded: being UTF-16 (BOM prefix) or containing a byte with no
// PDFDocEncoding mapping (noRune).
func TestIsPDFDocEncoded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", true},
		{"plain ascii", "Hello", true},
		{"utf16 bom", "\xfe\xff\x00A", false},
		{"unmapped byte", "\x7f", false},
		{"high mapped byte", "\xa9", true}, // 0xa9 maps to 0x00a9 in pdfDocEncoding
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := isPDFDocEncoded(tt.in); got != tt.want {
				t.Errorf("isPDFDocEncoded(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestPdfDocDecode covers both the fast path (every byte already maps to
// itself, so the input is returned unchanged) and the decode path (some byte
// maps to a different rune, or is >= 0x80).
func TestPdfDocDecode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"ascii passthrough", "Hello", "Hello"},
		{"bullet decodes", "\x80", "•"},
		{"high byte passthrough value", "\xa9", "©"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := pdfDocDecode(tt.in); got != tt.want {
				t.Errorf("pdfDocDecode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestUTF16DecodeOddLength pins the direct fix: an odd trailing byte cannot
// form a code unit and is dropped rather than read past.
func TestUTF16DecodeOddLength(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"single code unit", "\x00A", "A"},
		{"lone byte dropped", "A", ""},
		{"two code units", "\x00A\x00B", "AB"},
		{"trailing byte dropped", "\x00A\x00B\x00", "AB"},
		{"three code units", "\x00A\x00B\x00C\x00", "ABC"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := utf16Decode(tt.in); got != tt.want {
				t.Errorf("utf16Decode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

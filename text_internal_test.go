// Whitebox: utf16Decode is unexported and has no exported wrapper.

package pdf

import "testing"

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

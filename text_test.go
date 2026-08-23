package pdf_test

import (
	"testing"

	"github.com/jh125486/pdf"
)

func TestIsSameSentence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		last, current pdf.Text
		want          bool
	}{
		{
			name:    "same font, size, close Y, non-empty last text",
			last:    pdf.Text{Font: "Helvetica", FontSize: 12, Y: 100, S: "Hello"},
			current: pdf.Text{Font: "Helvetica", FontSize: 12, Y: 100.2, S: "world"},
			want:    true,
		},
		{
			name:    "different font",
			last:    pdf.Text{Font: "Helvetica", FontSize: 12, Y: 100, S: "Hello"},
			current: pdf.Text{Font: "Times", FontSize: 12, Y: 100, S: "world"},
			want:    false,
		},
		{
			name:    "font size differs by more than 0.1",
			last:    pdf.Text{Font: "Helvetica", FontSize: 12, Y: 100, S: "Hello"},
			current: pdf.Text{Font: "Helvetica", FontSize: 12.2, Y: 100, S: "world"},
			want:    false,
		},
		{
			name:    "Y differs by more than 5",
			last:    pdf.Text{Font: "Helvetica", FontSize: 12, Y: 100, S: "Hello"},
			current: pdf.Text{Font: "Helvetica", FontSize: 12, Y: 106, S: "world"},
			want:    false,
		},
		{
			name:    "empty last text starts a new sentence",
			last:    pdf.Text{Font: "Helvetica", FontSize: 12, Y: 100, S: ""},
			current: pdf.Text{Font: "Helvetica", FontSize: 12, Y: 100, S: "world"},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := pdf.IsSameSentence(tt.last, tt.current); got != tt.want {
				t.Errorf("IsSameSentence(%+v, %+v) = %v, want %v", tt.last, tt.current, got, tt.want)
			}
		})
	}
}

package pdf_test

import (
	"math"
	"testing"

	"github.com/jh125486/pdf"
)

func TestIsSameSentence(t *testing.T) {
	t.Parallel()

	// Anchor last.FontSize/Y at 0 so the delta fed into IsSameSentence is
	// bit-for-bit exact (subtracting 0 introduces no rounding), and derive
	// the just-inside/just-outside values with math.Nextafter so the
	// threshold comparisons below are boundary-exact rather than relying on
	// decimal literals that may round unpredictably in float64.
	const (
		fontSizeThreshold = 0.1
		yThreshold        = 5.0
	)
	fontSizeAtThreshold := fontSizeThreshold
	fontSizeJustIn := math.Nextafter(fontSizeThreshold, 0)
	fontSizeJustOut := math.Nextafter(fontSizeThreshold, 1)
	yAtThreshold := yThreshold
	yJustIn := math.Nextafter(yThreshold, 0)
	yJustOut := math.Nextafter(yThreshold, math.Inf(1))

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
		{
			name:    "font size delta just inside 0.1 threshold",
			last:    pdf.Text{Font: "Helvetica", FontSize: 0, Y: 100, S: "Hello"},
			current: pdf.Text{Font: "Helvetica", FontSize: fontSizeJustIn, Y: 100, S: "world"},
			want:    true,
		},
		{
			name:    "font size delta exactly at 0.1 threshold is not same sentence",
			last:    pdf.Text{Font: "Helvetica", FontSize: 0, Y: 100, S: "Hello"},
			current: pdf.Text{Font: "Helvetica", FontSize: fontSizeAtThreshold, Y: 100, S: "world"},
			want:    false,
		},
		{
			name:    "font size delta just outside 0.1 threshold",
			last:    pdf.Text{Font: "Helvetica", FontSize: 0, Y: 100, S: "Hello"},
			current: pdf.Text{Font: "Helvetica", FontSize: fontSizeJustOut, Y: 100, S: "world"},
			want:    false,
		},
		{
			name:    "negative font size delta just inside 0.1 threshold",
			last:    pdf.Text{Font: "Helvetica", FontSize: 0, Y: 100, S: "Hello"},
			current: pdf.Text{Font: "Helvetica", FontSize: -fontSizeJustIn, Y: 100, S: "world"},
			want:    true,
		},
		{
			name:    "negative font size delta just outside 0.1 threshold",
			last:    pdf.Text{Font: "Helvetica", FontSize: 0, Y: 100, S: "Hello"},
			current: pdf.Text{Font: "Helvetica", FontSize: -fontSizeJustOut, Y: 100, S: "world"},
			want:    false,
		},
		{
			name:    "Y delta just inside 5 threshold",
			last:    pdf.Text{Font: "Helvetica", FontSize: 12, Y: 0, S: "Hello"},
			current: pdf.Text{Font: "Helvetica", FontSize: 12, Y: yJustIn, S: "world"},
			want:    true,
		},
		{
			name:    "Y delta exactly at 5 threshold is not same sentence",
			last:    pdf.Text{Font: "Helvetica", FontSize: 12, Y: 0, S: "Hello"},
			current: pdf.Text{Font: "Helvetica", FontSize: 12, Y: yAtThreshold, S: "world"},
			want:    false,
		},
		{
			name:    "Y delta just outside 5 threshold",
			last:    pdf.Text{Font: "Helvetica", FontSize: 12, Y: 0, S: "Hello"},
			current: pdf.Text{Font: "Helvetica", FontSize: 12, Y: yJustOut, S: "world"},
			want:    false,
		},
		{
			name:    "negative Y delta just inside 5 threshold",
			last:    pdf.Text{Font: "Helvetica", FontSize: 12, Y: 0, S: "Hello"},
			current: pdf.Text{Font: "Helvetica", FontSize: 12, Y: -yJustIn, S: "world"},
			want:    true,
		},
		{
			name:    "negative Y delta just outside 5 threshold",
			last:    pdf.Text{Font: "Helvetica", FontSize: 12, Y: 0, S: "Hello"},
			current: pdf.Text{Font: "Helvetica", FontSize: 12, Y: -yJustOut, S: "world"},
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

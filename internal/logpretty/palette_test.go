package logpretty

import (
	"fmt"
	"math"
	"testing"
)

// Node hues must not resemble the red, orange, and yellow that mark failures,
// retries, and approvals. Each palette entry is decoded from the xterm-256
// color cube and rejected when its hue lands in the warm arc.
func TestNodePaletteAvoidsWarningHues(t *testing.T) {
	for _, code := range nodePalette {
		var idx int
		if _, err := fmt.Sscanf(code, "\x1b[38;5;%dm", &idx); err != nil {
			t.Fatalf("palette entry %q is not a 256-color foreground", code)
		}
		if idx < 16 || idx > 231 {
			t.Fatalf("palette entry %d is outside the 6x6x6 color cube", idx)
		}
		hue, sat := cubeHueSat(idx)
		if sat > 0.2 && (hue < 75 || hue > 340) {
			t.Errorf("palette entry %d has warm hue %.0f°; it would read as an error or warning", idx, hue)
		}
	}
	if len(nodePalette) < 8 {
		t.Fatalf("palette has %d entries; runs with many nodes need more contrast", len(nodePalette))
	}
}

func cubeHueSat(idx int) (hue, sat float64) {
	levels := []float64{0, 95, 135, 175, 215, 255}
	i := idx - 16
	r, g, b := levels[i/36]/255, levels[(i/6)%6]/255, levels[i%6]/255
	max := math.Max(r, math.Max(g, b))
	min := math.Min(r, math.Min(g, b))
	if max == 0 {
		return 0, 0
	}
	sat = (max - min) / max
	d := max - min
	if d == 0 {
		return 0, sat
	}
	switch max {
	case r:
		hue = 60 * math.Mod((g-b)/d, 6)
	case g:
		hue = 60 * ((b-r)/d + 2)
	default:
		hue = 60 * ((r-g)/d + 4)
	}
	if hue < 0 {
		hue += 360
	}
	return hue, sat
}

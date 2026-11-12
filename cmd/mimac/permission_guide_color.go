package main

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// Indicator green remains the capsule tint. Text uses a separate foreground
// derived toward the theme label color, since a small label needs more contrast.
func permissionGuideGrantedTextColor(green, background, label ui.Color) ui.Color {
	for i := 0; i < 20 && permissionGuideColorContrast(green, background) < 4.5; i++ {
		green = green.Mix(label, .1)
	}
	return green
}

func permissionGuideColorContrast(a, b ui.Color) float64 {
	luminance := func(c ui.Color) float64 {
		linear := func(value uint8) float64 {
			v := float64(value) / 255
			if v <= .04045 {
				return v / 12.92
			}
			return math.Pow((v+.055)/1.055, 2.4)
		}
		return .2126*linear(c.R) + .7152*linear(c.G) + .0722*linear(c.B)
	}
	x, y := luminance(a), luminance(b)
	return (max(x, y) + .05) / (min(x, y) + .05)
}

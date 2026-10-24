//go:build darwin

// Package typography exposes the platform's standard font size to the host.
package typography

import (
	"math"

	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
)

// SystemSize reads AppKit's current standard size after the MyGo loop starts.
// The host applies its three text-size choices to this shared baseline.
func SystemSize() float32 {
	size := float32(13)
	mygo.RunOnMain(func() {
		if font := objc.GetClass("NSFont"); font != 0 {
			value := objc.Send[float64](objc.ID(font), objc.RegisterName("systemFontSize"))
			if !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0 {
				size = float32(value)
			}
		}
	})
	return size
}

// Package readyindicator attaches a small native Core Animation indicator to
// an existing MyGo window.
package readyindicator

import "math"

// Rect is a MyGo window-content rectangle in DIPs.
type Rect struct {
	X, Y, W, H float64
}

func validRect(r Rect) bool {
	return finite(r.X) && finite(r.Y) && finite(r.W) && finite(r.H) && r.W > 0 && r.H > 0
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

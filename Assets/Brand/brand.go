// Package brand embeds the user's original mark and its menu bar rendering.
package brand

import _ "embed"

//go:embed mark.svg
var SVG string

//go:embed tray.png
var TrayPNG []byte

// AboutIconPNG is exported at 2x for the 64pt settings logo. The full icon
// stays a bundle asset instead of a large embedded runtime image.
//
//go:embed about-icon.png
var AboutIconPNG []byte

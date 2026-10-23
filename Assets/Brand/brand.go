// Package brand embeds the user's original mark and its menu bar rendering.
package brand

import _ "embed"

//go:embed mark.svg
var SVG string

//go:embed tray.png
var TrayPNG []byte

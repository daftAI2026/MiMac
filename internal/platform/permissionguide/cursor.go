package permissionguide

import "image"

// CursorGraphic keeps the native logical size separate from bitmap pixels.
type CursorGraphic struct {
	Pixels        image.Image
	Width, Height float32
}

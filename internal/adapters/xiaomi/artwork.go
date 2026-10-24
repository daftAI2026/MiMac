package xiaomi

import (
	"bytes"
	_ "embed"
	"image"
	"image/png"
)

//go:embed assets/rc003.png
var rc003PNG []byte

// Artwork returns the user-supplied Xiaomi 2 Pro PNG in the same pixel
// coordinates as DeviceProfile. Decode once per UI host, never per frame.
func Artwork() (image.Image, error) {
	return png.Decode(bytes.NewReader(rc003PNG))
}

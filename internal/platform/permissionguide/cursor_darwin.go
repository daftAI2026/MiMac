//go:build darwin

package permissionguide

import (
	"bytes"
	"errors"
	"fmt"
	"unsafe"

	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
	"golang.org/x/image/tiff"
)

type cursorSize struct{ Width, Height float64 }

// ClosedHandCursorImage reads the system artwork without changing the cursor.
// The caller must have the MyGo event loop running.
func ClosedHandCursorImage() (CursorGraphic, error) {
	var encoded []byte
	var size cursorSize
	err := errors.New("permissionguide: native event loop unavailable")
	mygo.RunOnMain(func() {
		if err = loadAppKitFramework(); err != nil {
			return
		}
		poolClass, cursorClass := objc.GetClass("NSAutoreleasePool"), objc.GetClass("NSCursor")
		if poolClass == 0 || cursorClass == 0 {
			err = errors.New("permissionguide: cursor classes unavailable")
			return
		}
		pool := objc.ID(poolClass).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("init"))
		if pool == 0 {
			err = errors.New("permissionguide: cursor autorelease pool unavailable")
			return
		}
		defer pool.Send(objc.RegisterName("drain"))
		cursor := objc.ID(cursorClass).Send(objc.RegisterName("closedHandCursor"))
		if cursor == 0 {
			err = errors.New("permissionguide: closed-hand cursor unavailable")
			return
		}
		img := cursor.Send(objc.RegisterName("image"))
		if img == 0 {
			err = errors.New("permissionguide: cursor image unavailable")
			return
		}
		size = objc.Send[cursorSize](img, objc.RegisterName("size"))
		if size.Width <= 0 || size.Height <= 0 {
			err = errors.New("permissionguide: cursor logical size unavailable")
			return
		}
		data := img.Send(objc.RegisterName("TIFFRepresentation"))
		if data == 0 {
			err = errors.New("permissionguide: cursor bitmap unavailable")
			return
		}
		length := objc.Send[uintptr](data, objc.RegisterName("length"))
		ptr := objc.Send[*byte](data, objc.RegisterName("bytes"))
		if length == 0 || length > 8*1024*1024 || ptr == nil {
			err = errors.New("permissionguide: invalid cursor bitmap")
			return
		}
		encoded = append([]byte(nil), unsafe.Slice(ptr, int(length))...)
		err = nil
	})
	if err != nil {
		return CursorGraphic{}, err
	}
	pixels, err := tiff.Decode(bytes.NewReader(encoded))
	if err != nil {
		return CursorGraphic{}, fmt.Errorf("permissionguide: decode cursor bitmap: %w", err)
	}
	return CursorGraphic{Pixels: pixels, Width: float32(size.Width), Height: float32(size.Height)}, nil
}

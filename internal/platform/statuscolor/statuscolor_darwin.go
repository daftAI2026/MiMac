//go:build darwin

package statuscolor

import (
	"errors"
	"math"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

var greenCache struct {
	sync.RWMutex
	colors [2]ui.Color
	valid  [2]bool
}

// SystemGreen resolves AppKit's semantic green for the supplied effective
// appearance and caches one value for light and one for dark mode.
func SystemGreen(dark bool) (ui.Color, error) {
	key := 0
	if dark {
		key = 1
	}
	greenCache.RLock()
	if greenCache.valid[key] {
		color := greenCache.colors[key]
		greenCache.RUnlock()
		return color, nil
	}
	greenCache.RUnlock()

	color := ui.Color{}
	err := errors.New("statuscolor: MyGo main loop is unavailable")
	mygo.RunOnMain(func() {
		color, err = readSystemGreen(dark)
	})
	if err != nil {
		return ui.Color{}, err
	}

	greenCache.Lock()
	greenCache.colors[key], greenCache.valid[key] = color, true
	greenCache.Unlock()
	return color, nil
}

func readSystemGreen(dark bool) (ui.Color, error) {
	poolClass := objc.GetClass("NSAutoreleasePool")
	if poolClass == 0 {
		return ui.Color{}, errors.New("statuscolor: AppKit autorelease pool unavailable")
	}
	pool := objc.ID(poolClass).Send(selector("alloc")).Send(selector("init"))
	if pool == 0 {
		return ui.Color{}, errors.New("statuscolor: could not create autorelease pool")
	}
	defer pool.Send(selector("drain"))

	colorClass := objc.GetClass("NSColor")
	appearanceClass := objc.GetClass("NSAppearance")
	colorSpaceClass := objc.GetClass("NSColorSpace")
	stringClass := objc.GetClass("NSString")
	if colorClass == 0 || appearanceClass == 0 || colorSpaceClass == 0 || stringClass == 0 {
		return ui.Color{}, errors.New("statuscolor: AppKit color classes unavailable")
	}
	if !responds(objc.ID(colorClass), "systemGreenColor") ||
		!responds(objc.ID(appearanceClass), "appearanceNamed:") ||
		!responds(objc.ID(appearanceClass), "currentAppearance") ||
		!responds(objc.ID(appearanceClass), "setCurrentAppearance:") ||
		!responds(objc.ID(colorSpaceClass), "sRGBColorSpace") {
		return ui.Color{}, errors.New("statuscolor: semantic system green unavailable")
	}

	appearanceName := "NSAppearanceNameAqua"
	if dark {
		appearanceName = "NSAppearanceNameDarkAqua"
	}
	appearance := objc.ID(appearanceClass).Send(selector("appearanceNamed:"), nsString(stringClass, appearanceName))
	if appearance == 0 {
		return ui.Color{}, errors.New("statuscolor: current appearance unavailable")
	}
	previousAppearance := objc.ID(appearanceClass).Send(selector("currentAppearance"))
	objc.ID(appearanceClass).Send(selector("setCurrentAppearance:"), appearance)
	defer objc.ID(appearanceClass).Send(selector("setCurrentAppearance:"), previousAppearance)

	green := objc.ID(colorClass).Send(selector("systemGreenColor"))
	if green == 0 {
		return ui.Color{}, errors.New("statuscolor: system green unavailable")
	}
	space := objc.ID(colorSpaceClass).Send(selector("sRGBColorSpace"))
	if space == 0 {
		return ui.Color{}, errors.New("statuscolor: sRGB color space unavailable")
	}
	converted := green.Send(selector("colorUsingColorSpace:"), space)
	if converted == 0 {
		return ui.Color{}, errors.New("statuscolor: system green is not convertible to sRGB")
	}

	var r, g, b, a float64
	converted.Send(selector("getRed:green:blue:alpha:"),
		uintptr(unsafe.Pointer(&r)), uintptr(unsafe.Pointer(&g)),
		uintptr(unsafe.Pointer(&b)), uintptr(unsafe.Pointer(&a)))
	return ui.RGBA(colorByte(r), colorByte(g), colorByte(b), float32(a)), nil
}

func colorByte(value float64) uint8 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return uint8(max(0, min(value, 1))*255 + 0.5)
}

func responds(id objc.ID, name string) bool {
	return objc.Send[bool](id, selector("respondsToSelector:"), objc.RegisterName(name))
}

func nsString(stringClass objc.Class, value string) objc.ID {
	return objc.ID(stringClass).Send(selector("stringWithUTF8String:"), value+"\x00")
}

func selector(name string) objc.SEL { return objc.RegisterName(name) }

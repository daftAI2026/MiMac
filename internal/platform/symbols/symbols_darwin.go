//go:build darwin

package symbols

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"unsafe"

	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
	"golang.org/x/image/tiff"
)

// Image renders an AppKit SF Symbol into an alpha-preserving bitmap. It must
// be called while MyGo's event loop is active; native work runs on its main
// thread. dark selects a light foreground for a dark sidebar.
func Image(name string, dark bool) (image.Image, error) {
	if name == "" {
		return nil, errors.New("symbols: empty SF Symbol name")
	}
	var result image.Image
	err := errors.New("symbols: MyGo main loop is unavailable")
	mygo.RunOnMain(func() {
		result, err = imageOnMain(name, dark)
	})
	return result, err
}

func imageOnMain(name string, dark bool) (image.Image, error) {
	poolClass := objc.GetClass("NSAutoreleasePool")
	if poolClass == 0 {
		return nil, errors.New("symbols: AppKit autorelease pool unavailable")
	}
	pool := objc.ID(poolClass).Send(selector("alloc")).Send(selector("init"))
	if pool == 0 {
		return nil, errors.New("symbols: could not create autorelease pool")
	}
	defer pool.Send(selector("drain"))

	imageClass := objc.GetClass("NSImage")
	configClass := objc.GetClass("NSImageSymbolConfiguration")
	colorClass := objc.GetClass("NSColor")
	if imageClass == 0 || configClass == 0 || colorClass == 0 {
		return nil, errors.New("symbols: SF Symbols require macOS 11 or later")
	}
	if !responds(objc.ID(imageClass), "imageWithSystemSymbolName:accessibilityDescription:") ||
		!responds(objc.ID(configClass), "configurationWithPointSize:weight:") ||
		!responds(objc.ID(configClass), "configurationWithHierarchicalColor:") {
		return nil, errors.New("symbols: required SF Symbol configuration is unavailable")
	}

	label := nsString(name)
	base := objc.ID(imageClass).Send(selector("imageWithSystemSymbolName:accessibilityDescription:"), nsString(name), label)
	if base == 0 {
		return nil, fmt.Errorf("symbols: unknown SF Symbol %q", name)
	}
	base.Send(selector("retain"))
	defer base.Send(selector("release"))

	// Rasterize at 2x so the 20 DIP settings icon stays sharp on Retina displays.
	pointConfig := objc.ID(configClass).Send(selector("configurationWithPointSize:weight:"), 40.0, 0.0)
	if pointConfig == 0 {
		return nil, errors.New("symbols: could not configure SF Symbol size")
	}
	pointConfig.Send(selector("retain"))
	defer pointConfig.Send(selector("release"))
	if !responds(pointConfig, "configurationByApplyingConfiguration:") {
		return nil, errors.New("symbols: SF Symbol color configuration is unavailable")
	}

	red, green, blue := 24.0/255, 24.0/255, 27.0/255
	if dark {
		red, green, blue = 244.0/255, 244.0/255, 245.0/255
	}
	color := objc.ID(colorClass).Send(selector("colorWithSRGBRed:green:blue:alpha:"), red, green, blue, 1.0)
	if color == 0 {
		return nil, errors.New("symbols: could not create SF Symbol color")
	}
	color.Send(selector("retain"))
	defer color.Send(selector("release"))

	colorConfig := objc.ID(configClass).Send(selector("configurationWithHierarchicalColor:"), color)
	if colorConfig == 0 {
		return nil, errors.New("symbols: could not configure SF Symbol color")
	}
	colorConfig.Send(selector("retain"))
	defer colorConfig.Send(selector("release"))

	combined := pointConfig.Send(selector("configurationByApplyingConfiguration:"), colorConfig)
	if combined == 0 {
		return nil, errors.New("symbols: could not combine SF Symbol configuration")
	}
	combined.Send(selector("retain"))
	defer combined.Send(selector("release"))
	if !responds(base, "imageWithSymbolConfiguration:") {
		return nil, errors.New("symbols: SF Symbol rendering configuration is unavailable")
	}

	rendered := base.Send(selector("imageWithSymbolConfiguration:"), combined)
	if rendered == 0 {
		return nil, errors.New("symbols: AppKit could not render SF Symbol")
	}
	rendered.Send(selector("retain"))
	defer rendered.Send(selector("release"))

	data := rendered.Send(selector("TIFFRepresentation"))
	if data == 0 {
		return nil, errors.New("symbols: AppKit returned no TIFF data")
	}
	data.Send(selector("retain"))
	defer data.Send(selector("release"))

	length := objc.Send[uintptr](data, selector("length"))
	if length == 0 || length > 8*1024*1024 {
		return nil, errors.New("symbols: invalid TIFF data length")
	}
	pixels := objc.Send[*byte](data, selector("bytes"))
	if pixels == nil {
		return nil, errors.New("symbols: TIFF data has no bytes")
	}
	encoded := append([]byte(nil), unsafe.Slice(pixels, int(length))...)
	decoded, err := tiff.Decode(bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("symbols: decode AppKit TIFF: %w", err)
	}
	return decoded, nil
}

func responds(id objc.ID, name string) bool {
	return objc.Send[bool](id, selector("respondsToSelector:"), objc.RegisterName(name))
}

func nsString(value string) objc.ID {
	return objc.ID(objc.GetClass("NSString")).Send(selector("stringWithUTF8String:"), value+"\x00")
}

func selector(name string) objc.SEL { return objc.RegisterName(name) }

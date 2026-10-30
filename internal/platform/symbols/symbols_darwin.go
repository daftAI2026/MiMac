//go:build darwin

package symbols

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"math"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
	"golang.org/x/image/tiff"
)

type nsPoint struct{ X, Y float64 }
type nsSize struct{ Width, Height float64 }
type nsRect struct {
	Origin nsPoint
	Size   nsSize
}

var (
	objcLibrary        uintptr
	bitmapMessagesOnce sync.Once
	bitmapMessagesErr  error
	msgInitBitmap      func(obj objc.ID, sel objc.SEL, planes uintptr, width, height, bitsPerSample, samplesPerPixel int, alpha, planar bool, colorSpace objc.ID, bytesPerRow, bitsPerPixel int) objc.ID
	msgSetSize         func(obj objc.ID, sel objc.SEL, size nsSize)
	msgDrawImage       func(obj objc.ID, sel objc.SEL, dst, src nsRect, operation uint, fraction float64)
)

// Image rasterizes one AppKit SF Symbol into a square 2× bitmap for a logical
// pointSize × pointSize slot using weight 0. The returned image's bounds are
// pixels; its TIFF representation also carries the logical point size. It
// must be called while MyGo's event loop is active; native work runs on its
// main thread. dark selects a light foreground for a dark sidebar.
func Image(name string, dark bool, pointSize float64) (image.Image, error) {
	if name == "" {
		return nil, errors.New("symbols: empty SF Symbol name")
	}
	images, err := Images([]string{name}, dark, pointSize, 0)
	if err != nil {
		return nil, err
	}
	return images[0], nil
}

// Images rasterizes SF Symbols as one visual group. All symbols share a point
// size, weight, palette, and fit scale, and are returned in input order. It
// must be called while MyGo's event loop is active; AppKit work runs in one
// main-thread callback and one autorelease pool.
func Images(names []string, dark bool, pointSize, weight float64) ([]image.Image, error) {
	for i, name := range names {
		if name == "" {
			return nil, fmt.Errorf("symbols: empty SF Symbol name at index %d", i)
		}
	}
	if math.IsNaN(pointSize) || math.IsInf(pointSize, 0) || pointSize <= 0 {
		return nil, fmt.Errorf("symbols: invalid point size %v", pointSize)
	}
	if math.Ceil(pointSize*2) > maxRasterPixels {
		return nil, fmt.Errorf("symbols: point size %v exceeds raster limit", pointSize)
	}
	if math.IsNaN(weight) || math.IsInf(weight, 0) {
		return nil, fmt.Errorf("symbols: invalid symbol weight %v", weight)
	}
	if len(names) == 0 {
		return []image.Image{}, nil
	}
	if err := loadBitmapMessages(); err != nil {
		return nil, err
	}
	// Copy the caller's slice before handing the batch to the main thread.
	symbolNames := append([]string(nil), names...)
	var result []image.Image
	err := errors.New("symbols: MyGo main loop is unavailable")
	mygo.RunOnMain(func() {
		result, err = imagesOnMain(symbolNames, dark, pointSize, weight)
	})
	return result, err
}

const maxRasterPixels = 2048

func loadBitmapMessages() error {
	bitmapMessagesOnce.Do(func() {
		var err error
		objcLibrary, err = purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_GLOBAL|purego.RTLD_NOW)
		if err != nil {
			bitmapMessagesErr = fmt.Errorf("symbols: load Objective-C runtime: %w", err)
			return
		}
		msgSend, err := purego.Dlsym(objcLibrary, "objc_msgSend")
		if err != nil {
			bitmapMessagesErr = fmt.Errorf("symbols: find objc_msgSend: %w", err)
			return
		}
		purego.RegisterFunc(&msgInitBitmap, msgSend)
		purego.RegisterFunc(&msgSetSize, msgSend)
		purego.RegisterFunc(&msgDrawImage, msgSend)
	})
	return bitmapMessagesErr
}

type configuredSymbol struct {
	name    string
	image   objc.ID
	natural nsSize
}

func imagesOnMain(names []string, dark bool, pointSize, weight float64) ([]image.Image, error) {
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
	bitmapClass := objc.GetClass("NSBitmapImageRep")
	graphicsClass := objc.GetClass("NSGraphicsContext")
	colorSpaceClass := objc.GetClass("NSColorSpace")
	if imageClass == 0 || configClass == 0 || colorClass == 0 || bitmapClass == 0 || graphicsClass == 0 || colorSpaceClass == 0 {
		return nil, errors.New("symbols: AppKit image classes are unavailable")
	}
	if !responds(objc.ID(imageClass), "imageWithSystemSymbolName:accessibilityDescription:") ||
		!responds(objc.ID(configClass), "configurationWithPointSize:weight:") ||
		!responds(objc.ID(configClass), "configurationWithPaletteColors:") {
		return nil, errors.New("symbols: required SF Symbol configuration is unavailable")
	}

	pointConfig := objc.ID(configClass).Send(selector("configurationWithPointSize:weight:"), pointSize, weight)
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

	// Give every symbol layer the same foreground. Hierarchical color makes
	// outlines such as info.circle's ring look lighter than sibling icons.
	colors := [...]objc.ID{color, color, color}
	palette := objc.ID(objc.GetClass("NSArray")).Send(selector("arrayWithObjects:count:"), unsafe.Pointer(&colors[0]), uintptr(len(colors)))
	if palette == 0 {
		return nil, errors.New("symbols: could not create SF Symbol palette")
	}
	colorConfig := objc.ID(configClass).Send(selector("configurationWithPaletteColors:"), palette)
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
	configured := make([]configuredSymbol, 0, len(names))
	defer func() {
		for _, symbol := range configured {
			symbol.image.Send(selector("release"))
		}
	}()
	for _, name := range names {
		base := objc.ID(imageClass).Send(selector("imageWithSystemSymbolName:accessibilityDescription:"), nsString(name), nsString(name))
		if base == 0 {
			return nil, fmt.Errorf("symbols: unknown SF Symbol %q", name)
		}
		base.Send(selector("retain"))
		if !responds(base, "imageWithSymbolConfiguration:") {
			base.Send(selector("release"))
			return nil, errors.New("symbols: SF Symbol rendering configuration is unavailable")
		}
		image := base.Send(selector("imageWithSymbolConfiguration:"), combined)
		if image == 0 {
			base.Send(selector("release"))
			return nil, fmt.Errorf("symbols: AppKit could not render SF Symbol %q", name)
		}
		image.Send(selector("retain"))
		base.Send(selector("release"))
		configured = append(configured, configuredSymbol{name: name, image: image})
	}

	// Configure the full set before measuring any natural size. One fit scale
	// then preserves relative scale across the entire sidebar column.
	var maxWidth, maxHeight float64
	for i := range configured {
		natural := objc.Send[nsSize](configured[i].image, selector("size"))
		if natural.Width <= 0 || natural.Height <= 0 || math.IsNaN(natural.Width) || math.IsNaN(natural.Height) || math.IsInf(natural.Width, 0) || math.IsInf(natural.Height, 0) {
			return nil, fmt.Errorf("symbols: invalid SF Symbol %q size %gx%g", configured[i].name, natural.Width, natural.Height)
		}
		configured[i].natural = natural
		maxWidth = math.Max(maxWidth, natural.Width)
		maxHeight = math.Max(maxHeight, natural.Height)
	}
	fitScale := math.Min(pointSize/maxWidth, pointSize/maxHeight)
	if fitScale <= 0 || math.IsNaN(fitScale) || math.IsInf(fitScale, 0) {
		return nil, fmt.Errorf("symbols: invalid common fit scale %v", fitScale)
	}
	pixels := int(math.Ceil(pointSize * 2))
	result := make([]image.Image, len(configured))
	for i, symbol := range configured {
		raster, err := rasterizeConfiguredSymbol(symbol, pointSize, fitScale, pixels,
			objc.ID(bitmapClass), objc.ID(graphicsClass), objc.ID(colorSpaceClass))
		if err != nil {
			return nil, fmt.Errorf("symbols: rasterize %q: %w", symbol.name, err)
		}
		result[i] = raster
	}
	return result, nil
}

func rasterizeConfiguredSymbol(symbol configuredSymbol, pointSize, fitScale float64, pixels int, bitmapClass, graphicsClass, colorSpaceClass objc.ID) (image.Image, error) {
	drawSize := nsSize{Width: symbol.natural.Width * fitScale, Height: symbol.natural.Height * fitScale}
	destination := nsRect{
		Origin: nsPoint{X: (pointSize - drawSize.Width) / 2, Y: (pointSize - drawSize.Height) / 2},
		Size:   drawSize,
	}

	allocated := objc.ID(bitmapClass).Send(selector("alloc"))
	if allocated == 0 {
		return nil, errors.New("could not allocate SF Symbol bitmap")
	}
	bitmap := msgInitBitmap(allocated, selector("initWithBitmapDataPlanes:pixelsWide:pixelsHigh:bitsPerSample:samplesPerPixel:hasAlpha:isPlanar:colorSpaceName:bytesPerRow:bitsPerPixel:"),
		0, pixels, pixels, 8, 4, true, false, nsString("NSCalibratedRGBColorSpace"), 0, 0)
	if bitmap == 0 {
		return nil, errors.New("could not initialize SF Symbol bitmap")
	}
	bitmap.Send(selector("autorelease"))

	sRGBSpace := objc.ID(colorSpaceClass).Send(selector("sRGBColorSpace"))
	if sRGBSpace == 0 {
		return nil, errors.New("could not get sRGB color space")
	}
	bitmap = bitmap.Send(selector("bitmapImageRepByRetaggingWithColorSpace:"), uintptr(sRGBSpace))
	if bitmap == 0 {
		return nil, errors.New("could not retag SF Symbol bitmap as sRGB")
	}
	msgSetSize(bitmap, selector("setSize:"), nsSize{Width: pointSize, Height: pointSize})

	context := objc.ID(graphicsClass).Send(selector("graphicsContextWithBitmapImageRep:"), uintptr(bitmap))
	if context == 0 {
		return nil, errors.New("could not create SF Symbol bitmap context")
	}
	objc.ID(graphicsClass).Send(selector("saveGraphicsState"))
	defer objc.ID(graphicsClass).Send(selector("restoreGraphicsState"))
	objc.ID(graphicsClass).Send(selector("setCurrentContext:"), uintptr(context))
	context.Send(selector("setImageInterpolation:"), 3)                                                            // NSImageInterpolationHigh
	msgDrawImage(symbol.image, selector("drawInRect:fromRect:operation:fraction:"), destination, nsRect{}, 2, 1.0) // SourceOver
	context.Send(selector("flushGraphics"))

	data := bitmap.Send(selector("TIFFRepresentation"))
	if data == 0 {
		return nil, errors.New("AppKit returned no TIFF data")
	}
	data.Send(selector("retain"))
	defer data.Send(selector("release"))

	length := objc.Send[uintptr](data, selector("length"))
	if length == 0 || length > 8*1024*1024 {
		return nil, errors.New("invalid TIFF data length")
	}
	pixelsPtr := objc.Send[*byte](data, selector("bytes"))
	if pixelsPtr == nil {
		return nil, errors.New("TIFF data has no bytes")
	}
	encoded := append([]byte(nil), unsafe.Slice(pixelsPtr, int(length))...)
	decoded, err := tiff.Decode(bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("decode AppKit TIFF: %w", err)
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

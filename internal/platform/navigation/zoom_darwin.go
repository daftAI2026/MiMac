//go:build darwin

package navigation

import (
	"errors"
	"fmt"
	"structs"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

const (
	fullScreenPrimary   = uintptr(1 << 7)
	fullScreenAuxiliary = uintptr(1 << 8)
	fullScreenNone      = uintptr(1 << 9)
	fullScreenMask      = fullScreenPrimary | fullScreenAuxiliary | fullScreenNone
)

// zoomPoint, zoomSize, and zoomRect have CoreGraphics' NSRect layout.
// HostLayout makes the value structs use the platform ABI for Objective-C
// callbacks; purego supports struct callback arguments and returns on Darwin
// amd64 and arm64.
type zoomPoint struct {
	structs.HostLayout
	X, Y float64
}

type zoomSize struct {
	structs.HostLayout
	Width, Height float64
}

type zoomRect struct {
	structs.HostLayout
	Origin zoomPoint
	Size   zoomSize
}

type verticalZoom struct {
	window, delegate  objc.ID
	originalClass     objc.Class
	installedClass    objc.Class
	originalBehavior  uintptr
	installedBehavior uintptr
	closed            bool
}

var verticalZoomState = struct {
	sync.Mutex
	classByBase    map[objc.Class]objc.Class
	baseByClass    map[objc.Class]objc.Class
	byWindow       map[objc.ID]*verticalZoom
	byDelegate     map[objc.ID]*verticalZoom
	objectSetClass func(objc.ID, objc.Class) objc.Class
	objcHandle     uintptr
	loadErr        error
	loadOnce       sync.Once
}{
	classByBase: make(map[objc.Class]objc.Class),
	baseByClass: make(map[objc.Class]objc.Class),
	byWindow:    make(map[objc.ID]*verticalZoom),
	byDelegate:  make(map[objc.ID]*verticalZoom),
}

// attachVerticalZoom adds AppKit's zoom-frame callback to this window's
// existing MyGo delegate object. Call it on MyGo's main thread. It never
// replaces the delegate object, so MyGo's pointer-indexed lifecycle callbacks
// continue to use the same instance.
func attachVerticalZoom(window objc.ID) (*verticalZoom, error) {
	if window == 0 {
		return nil, errors.New("navigation: vertical zoom needs a window")
	}
	if err := loadObjectSetClass(); err != nil {
		return nil, err
	}
	delegate := objc.Send[objc.ID](window, sel("delegate"))
	if delegate == 0 {
		return nil, errors.New("navigation: window has no delegate")
	}

	verticalZoomState.Lock()
	if verticalZoomState.byWindow[window] != nil || verticalZoomState.byDelegate[delegate] != nil {
		verticalZoomState.Unlock()
		return nil, errors.New("navigation: vertical zoom is already attached")
	}
	originalClass := delegate.Class()
	if _, installed := verticalZoomState.baseByClass[originalClass]; installed {
		verticalZoomState.Unlock()
		return nil, errors.New("navigation: delegate class was left in vertical zoom state")
	}
	if objc.Send[bool](delegate, sel("respondsToSelector:"), sel("windowWillUseStandardFrame:defaultFrame:")) {
		verticalZoomState.Unlock()
		return nil, errors.New("navigation: delegate already defines a zoom-frame policy")
	}
	installedClass, err := verticalZoomDelegateClassLocked(originalClass)
	verticalZoomState.Unlock()
	if err != nil {
		return nil, err
	}

	z := &verticalZoom{
		window:         window,
		delegate:       delegate,
		originalClass:  originalClass,
		installedClass: installedClass,
	}
	window.Send(sel("retain"))
	delegate.Send(sel("retain"))
	z.originalBehavior = objc.Send[uintptr](window, sel("collectionBehavior"))
	z.installedBehavior = (z.originalBehavior &^ (fullScreenPrimary | fullScreenAuxiliary)) | fullScreenNone

	if oldClass := verticalZoomState.objectSetClass(delegate, installedClass); oldClass != originalClass {
		verticalZoomState.objectSetClass(delegate, oldClass)
		delegate.Send(sel("release"))
		window.Send(sel("release"))
		return nil, errors.New("navigation: window delegate class changed while attaching vertical zoom")
	}
	if delegate.Class() != installedClass {
		verticalZoomState.objectSetClass(delegate, originalClass)
		delegate.Send(sel("release"))
		window.Send(sel("release"))
		return nil, errors.New("navigation: could not install vertical zoom delegate subclass")
	}

	verticalZoomState.Lock()
	verticalZoomState.byWindow[window] = z
	verticalZoomState.byDelegate[delegate] = z
	verticalZoomState.Unlock()
	window.Send(sel("setCollectionBehavior:"), z.installedBehavior)
	return z, nil
}

// close restores the delegate object's original class and the window's
// full-screen collection flags. Call it on MyGo's main thread before releasing
// the navigation control.
func (z *verticalZoom) close() {
	if z == nil || z.closed {
		return
	}
	z.closed = true

	verticalZoomState.Lock()
	if verticalZoomState.byWindow[z.window] == z {
		delete(verticalZoomState.byWindow, z.window)
	}
	if verticalZoomState.byDelegate[z.delegate] == z {
		delete(verticalZoomState.byDelegate, z.delegate)
	}
	verticalZoomState.Unlock()

	if z.delegate != 0 && z.delegate.Class() == z.installedClass {
		verticalZoomState.objectSetClass(z.delegate, z.originalClass)
	}
	if z.window != 0 {
		behavior := objc.Send[uintptr](z.window, sel("collectionBehavior"))
		if behavior&fullScreenMask == z.installedBehavior&fullScreenMask {
			behavior = (behavior &^ fullScreenMask) | (z.originalBehavior & fullScreenMask)
			z.window.Send(sel("setCollectionBehavior:"), behavior)
		}
	}
	if z.delegate != 0 {
		z.delegate.Send(sel("release"))
	}
	if z.window != 0 {
		z.window.Send(sel("release"))
	}
	z.window, z.delegate = 0, 0
}

func verticalZoomDelegateClassLocked(base objc.Class) (objc.Class, error) {
	if class := verticalZoomState.classByBase[base]; class != 0 {
		return class, nil
	}
	name := fmt.Sprintf("MiMacVerticalZoomDelegate_%x", uintptr(base))
	if existing := objc.GetClass(name); existing != 0 {
		if existing.SuperClass() != base {
			return 0, fmt.Errorf("navigation: Objective-C class %s has an unexpected superclass", name)
		}
		verticalZoomState.classByBase[base] = existing
		verticalZoomState.baseByClass[existing] = base
		return existing, nil
	}
	class, err := objc.RegisterClass(name, base, nil, nil, []objc.MethodDef{{
		Cmd: sel("windowWillUseStandardFrame:defaultFrame:"),
		Fn:  verticalZoomStandardFrame,
	}})
	if err != nil {
		return 0, fmt.Errorf("navigation: register vertical zoom delegate subclass: %w", err)
	}
	verticalZoomState.classByBase[base] = class
	verticalZoomState.baseByClass[class] = base
	return class, nil
}

func verticalZoomStandardFrame(_ objc.ID, _ objc.SEL, window objc.ID, defaultFrame zoomRect) zoomRect {
	verticalZoomState.Lock()
	z := verticalZoomState.byWindow[window]
	active := z != nil && !z.closed
	verticalZoomState.Unlock()
	if !active {
		return defaultFrame
	}
	current := objc.Send[zoomRect](window, sel("frame"))
	current.Origin.Y = defaultFrame.Origin.Y
	current.Size.Height = defaultFrame.Size.Height
	return current
}

func loadObjectSetClass() error {
	verticalZoomState.loadOnce.Do(func() {
		handle, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			verticalZoomState.loadErr = fmt.Errorf("navigation: open Objective-C runtime: %w", err)
			return
		}
		address, err := purego.Dlsym(handle, "object_setClass")
		if err != nil {
			purego.Dlclose(handle)
			verticalZoomState.loadErr = fmt.Errorf("navigation: find object_setClass: %w", err)
			return
		}
		purego.RegisterFunc(&verticalZoomState.objectSetClass, address)
		verticalZoomState.objcHandle = handle
	})
	return verticalZoomState.loadErr
}

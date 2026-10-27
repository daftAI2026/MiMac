//go:build darwin

package readyindicator

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/egoist/mygo"
)

const nativeLibraryName = "libMiMacReadyIndicator.dylib"

type nativeAPI struct {
	library uintptr // Kept loaded for the process lifetime.
	version func() uint32
	install func(uintptr) uintptr
	update  func(uintptr, float64, float64, float64, float64, uint32, int32) int32
	close   func(uintptr)
}

var (
	nativeOnce sync.Once
	native     *nativeAPI
	nativeErr  error
)

// Control owns one overlay attached to a MyGo NSWindow. Its mutable fields are
// confined to MyGo's main thread.
type Control struct {
	api    *nativeAPI
	handle uintptr
	closed bool
}

// New attaches the native view. The process-wide dylib loads only at first
// true-ready use and remains resident until process exit.
func New(window uintptr) (*Control, error) {
	if window == 0 {
		return nil, errors.New("readyindicator: invalid native window")
	}
	var ran bool
	var control *Control
	var resultErr error
	mygo.RunOnMain(func() {
		ran = true
		api, err := loadNativeAPI()
		if err != nil {
			resultErr = err
			return
		}
		handle := api.install(window)
		if handle == 0 {
			resultErr = errors.New("readyindicator: native overlay creation failed")
			return
		}
		control = &Control{api: api, handle: handle}
	})
	if !ran {
		return nil, errors.New("readyindicator: MyGo main loop is unavailable")
	}
	return control, resultErr
}

// Update moves and colors the overlay. animate selects the repeating native
// pulse; false leaves only the fixed center visible.
func (c *Control) Update(rect Rect, rgba uint32, animate bool) error {
	if c == nil {
		return errors.New("readyindicator: nil control")
	}
	if !validRect(rect) {
		return errors.New("readyindicator: invalid layout rectangle")
	}
	var ran bool
	var resultErr error
	mygo.RunOnMain(func() {
		ran = true
		if c.closed || c.handle == 0 || c.api == nil {
			resultErr = errors.New("readyindicator: native overlay is closed")
			return
		}
		mode := int32(0)
		if animate {
			mode = 1
		}
		if c.api.update(c.handle, rect.X, rect.Y, rect.W, rect.H, rgba, mode) != 1 {
			resultErr = errors.New("readyindicator: native overlay rejected update")
		}
	})
	if !ran {
		return errors.New("readyindicator: MyGo main loop is unavailable")
	}
	return resultErr
}

// Close detaches the native overlay once. The shared dylib remains loaded.
func (c *Control) Close() {
	if c == nil {
		return
	}
	mygo.RunOnMain(func() {
		if c.closed {
			return
		}
		c.closed = true
		if c.handle != 0 && c.api != nil {
			c.api.close(c.handle)
		}
		c.handle = 0
	})
}

func loadNativeAPI() (*nativeAPI, error) {
	nativeOnce.Do(func() {
		resourceDir, err := mygo.App.Path(mygo.PathResources)
		if err != nil {
			nativeErr = fmt.Errorf("readyindicator: locate MyGo resources: %w", err)
			return
		}
		libraryPath := filepath.Join(resourceDir, nativeLibraryName)
		library, err := purego.Dlopen(libraryPath, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			nativeErr = fmt.Errorf("readyindicator: load %s: %w", libraryPath, err)
			return
		}
		api := &nativeAPI{library: library}
		version, err := purego.Dlsym(library, "MiMacReadyIndicatorABIVersion")
		if err != nil {
			nativeErr = fmt.Errorf("readyindicator: resolve MiMacReadyIndicatorABIVersion: %w", err)
			return
		}
		install, err := purego.Dlsym(library, "MiMacReadyIndicatorInstall")
		if err != nil {
			nativeErr = fmt.Errorf("readyindicator: resolve MiMacReadyIndicatorInstall: %w", err)
			return
		}
		update, err := purego.Dlsym(library, "MiMacReadyIndicatorUpdate")
		if err != nil {
			nativeErr = fmt.Errorf("readyindicator: resolve MiMacReadyIndicatorUpdate: %w", err)
			return
		}
		closeIndicator, err := purego.Dlsym(library, "MiMacReadyIndicatorClose")
		if err != nil {
			nativeErr = fmt.Errorf("readyindicator: resolve MiMacReadyIndicatorClose: %w", err)
			return
		}
		purego.RegisterFunc(&api.version, version)
		purego.RegisterFunc(&api.install, install)
		purego.RegisterFunc(&api.update, update)
		purego.RegisterFunc(&api.close, closeIndicator)
		if api.version() != 1 {
			nativeErr = fmt.Errorf("readyindicator: unsupported native ABI version %d", api.version())
			return
		}
		native = api
	})
	if nativeErr != nil {
		return nil, nativeErr
	}
	if native == nil {
		return nil, errors.New("readyindicator: native library is unavailable")
	}
	return native, nil
}

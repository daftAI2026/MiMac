//go:build darwin

package preferences

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/ebitengine/purego"
	"github.com/egoist/mygo"
)

const nativeLibraryName = "libMiMacSettings.dylib"

type nativeAPI struct {
	library  uintptr // Intentionally kept loaded for the process lifetime.
	create   func(uintptr, float64, float64, float64, float64, uint64, uintptr) uintptr
	update   func(uintptr, string) int32
	close    func(uintptr)
	callback uintptr
}

var (
	nativeOnce sync.Once
	native     *nativeAPI
	nativeErr  error

	nextToken atomic.Uint64

	controlsMu sync.Mutex
	controls   = make(map[uint64]*Control)
)

// Control owns the SwiftUI preferences view attached to a MyGo window. All
// native calls and mutable fields are confined to MyGo's main thread.
type Control struct {
	api       *nativeAPI
	handle    uintptr
	token     uint64
	callbacks Callbacks
	updating  bool
	closed    bool
}

// New attaches the native SwiftUI form to the existing NSWindow. The
// process-wide dylib and callback trampoline are loaded only on first use.
func New(window uintptr, layout Layout, state State, callbacks Callbacks) (*Control, error) {
	if window == 0 || !validLayout(layout) {
		return nil, errors.New("preferences: invalid window or layout")
	}
	var control *Control
	var ran bool
	var err error
	mygo.RunOnMain(func() {
		ran = true
		api, loadErr := loadNativeAPI()
		if loadErr != nil {
			err = loadErr
			return
		}
		token := nextToken.Add(1)
		if token == 0 {
			err = errors.New("preferences: callback token exhausted")
			return
		}
		control = &Control{api: api, token: token, callbacks: callbacks}
		controlsMu.Lock()
		controls[token] = control
		controlsMu.Unlock()

		control.handle = api.create(window, layout.Left, layout.Top, layout.Right, layout.Bottom, token, api.callback)
		if control.handle == 0 {
			control.removeCallback()
			control = nil
			err = errors.New("preferences: native SwiftUI form creation failed")
			return
		}
		if err = control.updateOnMain(state); err != nil {
			control.closeOnMain()
			control = nil
		}
	})
	if !ran {
		return nil, errors.New("preferences: MyGo main loop is unavailable")
	}
	return control, err
}

func validLayout(layout Layout) bool {
	return finite(layout.Left) && finite(layout.Top) && finite(layout.Right) && finite(layout.Bottom) &&
		layout.Left >= 0 && layout.Top >= 0 && layout.Right >= 0 && layout.Bottom >= 0
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// Update replaces the form's displayed state. The native update status is
// checked so decoding or closed-handle failures reach the caller.
func (c *Control) Update(state State) error {
	if c == nil {
		return nil
	}
	var ran bool
	var err error
	mygo.RunOnMain(func() {
		ran = true
		err = c.updateOnMain(state)
	})
	if !ran {
		return errors.New("preferences: MyGo main loop is unavailable")
	}
	return err
}

func (c *Control) updateOnMain(state State) error {
	if c.closed || c.handle == 0 || c.api == nil {
		return errors.New("preferences: native SwiftUI form is closed")
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("preferences: encode state: %w", err)
	}
	c.updating = true
	defer func() { c.updating = false }()
	switch status := c.api.update(c.handle, string(encoded)); status {
	case 0:
		return nil
	case 1:
		return errors.New("preferences: native SwiftUI form rejected state JSON")
	case 2:
		return errors.New("preferences: native SwiftUI form is closed")
	default:
		return fmt.Errorf("preferences: native SwiftUI update returned status %d", status)
	}
}

// Close detaches the native view and drops its callback registration. The
// dylib remains loaded because Swift runtime code can outlive an individual
// form and is not safe to unload.
func (c *Control) Close() {
	if c == nil {
		return
	}
	c.removeCallback()
	mygo.RunOnMain(c.closeOnMain)
}

func (c *Control) closeOnMain() {
	if c == nil || c.closed {
		return
	}
	c.removeCallback()
	c.closed = true
	if c.handle != 0 && c.api != nil {
		c.api.close(c.handle)
	}
	c.handle = 0
	c.callbacks = Callbacks{}
}

func (c *Control) removeCallback() {
	if c == nil || c.token == 0 {
		return
	}
	controlsMu.Lock()
	delete(controls, c.token)
	controlsMu.Unlock()
}

func loadNativeAPI() (*nativeAPI, error) {
	nativeOnce.Do(func() {
		resourceDir, err := mygo.App.Path(mygo.PathResources)
		if err != nil {
			nativeErr = fmt.Errorf("preferences: locate MyGo resources: %w", err)
			return
		}
		libraryPath := filepath.Join(resourceDir, nativeLibraryName)
		library, err := purego.Dlopen(libraryPath, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			nativeErr = fmt.Errorf("preferences: load native settings library %s: %w", libraryPath, err)
			return
		}
		api := &nativeAPI{library: library}
		create, err := purego.Dlsym(library, "MiMacPreferencesCreate")
		if err != nil {
			nativeErr = fmt.Errorf("preferences: resolve MiMacPreferencesCreate: %w", err)
			return
		}
		update, err := purego.Dlsym(library, "MiMacPreferencesUpdate")
		if err != nil {
			nativeErr = fmt.Errorf("preferences: resolve MiMacPreferencesUpdate: %w", err)
			return
		}
		closeForm, err := purego.Dlsym(library, "MiMacPreferencesClose")
		if err != nil {
			nativeErr = fmt.Errorf("preferences: resolve MiMacPreferencesClose: %w", err)
			return
		}
		callback, err := makeCallback()
		if err != nil {
			nativeErr = err
			return
		}
		purego.RegisterFunc(&api.create, create)
		purego.RegisterFunc(&api.update, update)
		purego.RegisterFunc(&api.close, closeForm)
		api.callback = callback
		native = api
	})
	if nativeErr != nil {
		return nil, nativeErr
	}
	if native == nil {
		return nil, errors.New("preferences: native settings library is unavailable")
	}
	return native, nil
}

var (
	callbackOnce sync.Once
	callbackPtr  uintptr
	callbackErr  error
)

func makeCallback() (uintptr, error) {
	callbackOnce.Do(func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				callbackErr = fmt.Errorf("preferences: register native callback: %v", recovered)
			}
		}()
		callbackPtr = purego.NewCallback(nativePreferenceChanged)
		if callbackPtr == 0 {
			callbackErr = errors.New("preferences: native callback registration returned null")
		}
	})
	return callbackPtr, callbackErr
}

func nativePreferenceChanged(token uint64, kind int32, value int32) {
	// SwiftUI actions are main-thread callbacks. Marshal defensively so any
	// callback from a native executor still enters host code on MyGo's thread.
	mygo.RunOnMain(func() {
		controlsMu.Lock()
		c := controls[token]
		controlsMu.Unlock()
		if c == nil || c.closed || c.updating {
			return
		}
		switch kind {
		case 1:
			if callback, ok := boolCallback(c.callbacks.Login, value); ok {
				callback()
			}
		case 2:
			if callback, ok := boolCallback(c.callbacks.Dock, value); ok {
				callback()
			}
		case 3:
			if value >= 0 && value <= 2 && c.callbacks.Appearance != nil {
				c.callbacks.Appearance([]string{"system", "light", "dark"}[value])
			}
		case 4:
			if value >= -1 && value <= 1 && c.callbacks.TextSize != nil {
				c.callbacks.TextSize(int(value))
			}
		}
	})
}

func boolCallback(callback func(bool), value int32) (func(), bool) {
	if callback == nil || (value != 0 && value != 1) {
		return nil, false
	}
	enabled := value == 1
	return func() { callback(enabled) }, true
}

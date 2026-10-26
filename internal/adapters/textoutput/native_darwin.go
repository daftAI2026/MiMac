//go:build darwin

package textoutput

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

const (
	cfStringEncodingUTF8    = uint32(0x08000100)
	axErrorSuccess          = int32(0)
	axErrorAttrUnsupported  = int32(-25205)
	axErrorNotImplemented   = int32(-25208)
	axErrorAPIDisabled      = int32(-25211)
	axErrorNoValue          = int32(-25212)
	axErrorParamUnsupported = int32(-25213)
	axValueCFRange          = uint32(4)
)

type nativeAPI struct {
	createApplication           func(int32) uintptr
	copyAttributeValue          func(uintptr, uintptr, *uintptr) int32
	copyParameterizedNames      func(uintptr, *uintptr) int32
	copyParameterizedValue      func(uintptr, uintptr, uintptr, *uintptr) int32
	getPID                      func(uintptr, *int32) int32
	isAttributeSettable         func(uintptr, uintptr, *uint8) int32
	setAttributeValue           func(uintptr, uintptr, uintptr) int32
	isProcessTrusted            func() uint8
	isProcessTrustedWithOptions func(uintptr) uint8
	cfDictionaryCreateMutable   func(uintptr, int64, uintptr, uintptr) uintptr
	cfDictionarySetValue        func(uintptr, uintptr, uintptr)
	cfStringCreateCString       func(uintptr, *byte, uint32) uintptr
	cfStringCreateBytes         func(uintptr, *byte, int64, uint32, uint8) uintptr
	cfEqual                     func(uintptr, uintptr) uint8
	cfRelease                   func(uintptr)
	cfGetTypeID                 func(uintptr) uintptr
	cfBooleanGetTypeID          func() uintptr
	cfBooleanGetValue           func(uintptr) uint8
	cfArrayCreateMutable        func(uintptr, int64, uintptr) uintptr
	cfArrayAppendValue          func(uintptr, uintptr)
	cfArrayGetCount             func(uintptr) int64
	cfArrayGetValueAtIndex      func(uintptr, int64) uintptr
	cfArrayGetTypeID            func() uintptr
	axValueGetTypeID            func() uintptr
	axValueGetType              func(uintptr) uint32
	axValueGetValue             func(uintptr, uint32, unsafe.Pointer) uint8
	axValueCreate               func(uint32, unsafe.Pointer) uintptr
	isSecureEventInputOn        func() uint8
	copyNativePointer           func(*uintptr, uintptr, uintptr) uintptr
	trustedCheckPromptKey       uintptr
	cfBooleanTrue               uintptr
	cfBooleanFalse              uintptr
	arrayCallbacks              uintptr
	dictionaryKeyCallbacks      uintptr
	dictionaryValueCallbacks    uintptr

	// Framework handles must outlive every registered function pointer.
	frameworks []uintptr
}

var nativeRuntime struct {
	once sync.Once
	api  *nativeAPI
	err  error
}

type systemNative struct{}

func defaultNative() native { return systemNative{} }

func hasAccessibilityAccess() bool {
	api, err := loadNativeAPI()
	return err == nil && api.isProcessTrusted() != 0
}

func requestAccessibilityAccess() bool {
	api, err := loadNativeAPI()
	if err != nil {
		return false
	}
	if api.isProcessTrusted() != 0 {
		return true
	}
	options := api.cfDictionaryCreateMutable(0, 1, api.dictionaryKeyCallbacks, api.dictionaryValueCallbacks)
	if options == 0 {
		return false
	}
	defer api.cfRelease(options)
	api.cfDictionarySetValue(options, api.trustedCheckPromptKey, api.cfBooleanTrue)
	return api.isProcessTrustedWithOptions(options) != 0
}

func (systemNative) capture() (snapshot, error) {
	api, err := loadNativeAPI()
	if err != nil {
		return snapshot{}, err
	}
	if api.isProcessTrusted() == 0 {
		return snapshot{}, ErrPermissionDenied
	}
	if secureEventInputEnabled(api) {
		return snapshot{}, ErrSecureInput
	}

	frontPID, err := frontmostProcessID()
	if err != nil {
		return snapshot{}, err
	}
	element, elementPID, err := focusedElement(api, frontPID)
	if err != nil {
		return snapshot{}, err
	}
	state := snapshot{processID: elementPID, element: element}
	releaseElement := true
	defer func() {
		if releaseElement {
			api.cfRelease(element)
		}
	}()
	if frontPID != elementPID {
		return snapshot{}, ErrTargetChanged
	}
	if err := validateEditableElement(api, element); err != nil {
		return snapshot{}, err
	}
	selection, err := selectedRange(api, element)
	if err != nil {
		return snapshot{}, err
	}
	state.selection = selection
	releaseSelection := true
	defer func() {
		if releaseSelection {
			api.cfRelease(selection)
		}
	}()
	state.marker, err = operationMarker(api, element)
	if err != nil {
		return snapshot{}, err
	}
	releaseMarker := true
	defer func() {
		if releaseMarker && state.marker != 0 {
			api.cfRelease(state.marker)
		}
	}()

	// Fail closed if the foreground process, focused element, or selection moved
	// while the initial snapshot was being assembled.
	if err := confirmSnapshot(api, state); err != nil {
		return snapshot{}, err
	}
	releaseElement = false
	releaseSelection = false
	releaseMarker = false
	return state, nil
}

func (systemNative) insert(state snapshot, text string) error {
	api, err := loadNativeAPI()
	if err != nil {
		return err
	}
	if api.isProcessTrusted() == 0 {
		return ErrPermissionDenied
	}
	if state.marker != 0 {
		return insertTextOperation(api, state, text)
	}
	// The only output operation: set AXSelectedText on the captured element.
	// There is no clipboard, key event, Return, or submit action fallback.
	attribute, err := api.makeString("AXSelectedText")
	if err != nil {
		return err
	}
	defer api.cfRelease(attribute)
	value, err := api.makeUTF8String(text)
	if err != nil {
		return err
	}
	defer api.cfRelease(value)
	// Do all allocation before the final foreground/focus/selection check. Once
	// it passes, the next native operation is the single set request.
	if err := confirmSnapshot(api, state); err != nil {
		return err
	}
	if code := api.setAttributeValue(state.element, attribute, value); code != axErrorSuccess {
		return nativeAXError("set AXSelectedText", code)
	}
	return nil
}

func (systemNative) close(state snapshot) error {
	if api, err := loadNativeAPI(); err == nil {
		if state.marker != 0 {
			api.cfRelease(state.marker)
		}
		if state.selection != 0 {
			api.cfRelease(state.selection)
		}
		if state.element != 0 {
			api.cfRelease(state.element)
		}
		return nil
	} else {
		// A snapshot cannot exist until the native API was loaded. Keep an
		// explicit error if a future implementation changes that invariant.
		return err
	}
}

func loadNativeAPI() (*nativeAPI, error) {
	nativeRuntime.once.Do(func() {
		frameworkPaths := []string{
			"/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation",
			"/System/Library/Frameworks/ApplicationServices.framework/Frameworks/HIServices.framework/HIServices",
			"/System/Library/Frameworks/AppKit.framework/AppKit",
			"/System/Library/Frameworks/Carbon.framework/Frameworks/HIToolbox.framework/HIToolbox",
			"/usr/lib/libSystem.B.dylib",
		}
		handles := make([]uintptr, 0, len(frameworkPaths))
		for _, path := range frameworkPaths {
			handle, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
			if err != nil {
				for _, loaded := range handles {
					_ = purego.Dlclose(loaded)
				}
				nativeRuntime.err = fmt.Errorf("textoutput: load framework %s: %w", path, err)
				return
			}
			handles = append(handles, handle)
		}

		api := &nativeAPI{frameworks: handles}
		bindings := []struct {
			library int
			dst     any
			symbol  string
		}{
			{1, &api.createApplication, "AXUIElementCreateApplication"},
			{1, &api.copyAttributeValue, "AXUIElementCopyAttributeValue"},
			{1, &api.copyParameterizedNames, "AXUIElementCopyParameterizedAttributeNames"},
			{1, &api.copyParameterizedValue, "AXUIElementCopyParameterizedAttributeValue"},
			{1, &api.getPID, "AXUIElementGetPid"},
			{1, &api.isAttributeSettable, "AXUIElementIsAttributeSettable"},
			{1, &api.setAttributeValue, "AXUIElementSetAttributeValue"},
			{1, &api.isProcessTrusted, "AXIsProcessTrusted"},
			{1, &api.isProcessTrustedWithOptions, "AXIsProcessTrustedWithOptions"},
			{0, &api.cfDictionaryCreateMutable, "CFDictionaryCreateMutable"},
			{0, &api.cfDictionarySetValue, "CFDictionarySetValue"},
			{0, &api.cfStringCreateCString, "CFStringCreateWithCString"},
			{0, &api.cfStringCreateBytes, "CFStringCreateWithBytes"},
			{0, &api.cfEqual, "CFEqual"},
			{0, &api.cfRelease, "CFRelease"},
			{0, &api.cfGetTypeID, "CFGetTypeID"},
			{0, &api.cfBooleanGetTypeID, "CFBooleanGetTypeID"},
			{0, &api.cfBooleanGetValue, "CFBooleanGetValue"},
			{0, &api.cfArrayCreateMutable, "CFArrayCreateMutable"},
			{0, &api.cfArrayAppendValue, "CFArrayAppendValue"},
			{0, &api.cfArrayGetCount, "CFArrayGetCount"},
			{0, &api.cfArrayGetValueAtIndex, "CFArrayGetValueAtIndex"},
			{0, &api.cfArrayGetTypeID, "CFArrayGetTypeID"},
			{1, &api.axValueGetTypeID, "AXValueGetTypeID"},
			{1, &api.axValueGetType, "AXValueGetType"},
			{1, &api.axValueGetValue, "AXValueGetValue"},
			{1, &api.axValueCreate, "AXValueCreate"},
			{3, &api.isSecureEventInputOn, "IsSecureEventInputEnabled"},
			{4, &api.copyNativePointer, "memcpy"},
		}
		for _, binding := range bindings {
			address, err := purego.Dlsym(handles[binding.library], binding.symbol)
			if err != nil {
				for _, loaded := range handles {
					_ = purego.Dlclose(loaded)
				}
				nativeRuntime.err = fmt.Errorf("textoutput: resolve %s: %w", binding.symbol, err)
				return
			}
			purego.RegisterFunc(binding.dst, address)
		}
		for _, binding := range []struct {
			library     int
			symbol      string
			destination *uintptr
			dereference bool
		}{
			{1, "kAXTrustedCheckOptionPrompt", &api.trustedCheckPromptKey, true},
			{0, "kCFBooleanTrue", &api.cfBooleanTrue, true},
			{0, "kCFBooleanFalse", &api.cfBooleanFalse, true},
			{0, "kCFTypeArrayCallBacks", &api.arrayCallbacks, false},
			{0, "kCFTypeDictionaryKeyCallBacks", &api.dictionaryKeyCallbacks, false},
			{0, "kCFTypeDictionaryValueCallBacks", &api.dictionaryValueCallbacks, false},
		} {
			address, err := purego.Dlsym(handles[binding.library], binding.symbol)
			if err != nil {
				for _, loaded := range handles {
					_ = purego.Dlclose(loaded)
				}
				nativeRuntime.err = fmt.Errorf("textoutput: resolve %s: %w", binding.symbol, err)
				return
			}
			if address == 0 {
				for _, loaded := range handles {
					_ = purego.Dlclose(loaded)
				}
				nativeRuntime.err = fmt.Errorf("textoutput: resolve %s: null symbol address", binding.symbol)
				return
			}
			if binding.dereference {
				api.copyNativePointer(binding.destination, address, unsafe.Sizeof(*binding.destination))
			} else {
				*binding.destination = address
			}
		}
		if api.trustedCheckPromptKey == 0 || api.cfBooleanTrue == 0 || api.cfBooleanFalse == 0 || api.arrayCallbacks == 0 ||
			api.dictionaryKeyCallbacks == 0 || api.dictionaryValueCallbacks == 0 {
			for _, loaded := range handles {
				_ = purego.Dlclose(loaded)
			}
			nativeRuntime.err = errors.New("textoutput: Accessibility prompt constants are unavailable")
			return
		}
		nativeRuntime.api = api
	})
	if nativeRuntime.err != nil {
		return nil, nativeRuntime.err
	}
	return nativeRuntime.api, nil
}

func (api *nativeAPI) makeString(value string) (uintptr, error) {
	if value == "" || strings.IndexByte(value, 0) >= 0 {
		return 0, errors.New("textoutput: invalid native attribute name")
	}
	bytes := append([]byte(value), 0)
	result := api.cfStringCreateCString(0, &bytes[0], cfStringEncodingUTF8)
	runtime.KeepAlive(bytes)
	if result == 0 {
		return 0, errors.New("textoutput: create native attribute name failed")
	}
	return result, nil
}

func (api *nativeAPI) makeUTF8String(value string) (uintptr, error) {
	bytes := []byte(value)
	if len(bytes) == 0 || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 || len(bytes) > MaxTextBytes {
		return 0, errors.New("textoutput: invalid native text")
	}
	result := api.cfStringCreateBytes(0, &bytes[0], int64(len(bytes)), cfStringEncodingUTF8, 0)
	runtime.KeepAlive(bytes)
	if result == 0 {
		return 0, errors.New("textoutput: create native text failed")
	}
	return result, nil
}

func (api *nativeAPI) optionalAttribute(element uintptr, name string) (uintptr, error) {
	attribute, err := api.makeString(name)
	if err != nil {
		return 0, err
	}
	defer api.cfRelease(attribute)
	var value uintptr
	code := api.copyAttributeValue(element, attribute, &value)
	if code == axErrorAttrUnsupported || code == axErrorNoValue {
		return 0, nil
	}
	if code != axErrorSuccess {
		return 0, nativeAXError("copy "+name, code)
	}
	if value == 0 {
		return 0, nil
	}
	return value, nil
}

func (api *nativeAPI) requiredAttribute(element uintptr, name string) (uintptr, error) {
	value, err := api.optionalAttribute(element, name)
	if err != nil {
		return 0, err
	}
	if value == 0 {
		return 0, ErrNoFocusedElement
	}
	return value, nil
}

func (api *nativeAPI) secureState(element uintptr) error {
	if secureEventInputEnabled(api) {
		return ErrSecureInput
	}
	subrole, err := api.optionalAttribute(element, "AXSubrole")
	if err != nil {
		return err
	}
	if subrole != 0 {
		secureSubrole, createErr := api.makeString("AXSecureTextField")
		if createErr != nil {
			api.cfRelease(subrole)
			return createErr
		}
		isSecure := api.cfEqual(subrole, secureSubrole) != 0
		api.cfRelease(secureSubrole)
		api.cfRelease(subrole)
		if isSecure {
			return ErrSecureInput
		}
	}
	protected, err := api.optionalAttribute(element, "AXContainsProtectedContent")
	if err != nil {
		return err
	}
	if protected != 0 {
		defer api.cfRelease(protected)
		if api.cfGetTypeID(protected) != api.cfBooleanGetTypeID() || api.cfBooleanGetValue(protected) != 0 {
			return ErrSecureInput
		}
	}
	return nil
}

func validateEditableElement(api *nativeAPI, element uintptr) error {
	if element == 0 {
		return ErrInvalidTarget
	}
	if err := api.secureState(element); err != nil {
		return err
	}
	if editable, err := api.optionalAttribute(element, "AXIsEditable"); err != nil {
		return err
	} else if editable != 0 {
		defer api.cfRelease(editable)
		if api.cfGetTypeID(editable) != api.cfBooleanGetTypeID() || api.cfBooleanGetValue(editable) == 0 {
			return ErrNotEditable
		}
	}
	attribute, err := api.makeString("AXSelectedText")
	if err != nil {
		return err
	}
	defer api.cfRelease(attribute)
	var settable uint8
	if code := api.isAttributeSettable(element, attribute, &settable); code != axErrorSuccess {
		if code == axErrorAttrUnsupported || code == axErrorNoValue {
			return ErrNotEditable
		}
		return nativeAXError("check AXSelectedText writability", code)
	}
	if settable == 0 {
		return ErrNotEditable
	}
	return nil
}

func selectedRange(api *nativeAPI, element uintptr) (uintptr, error) {
	value, err := api.requiredAttribute(element, "AXSelectedTextRange")
	if err != nil {
		if errors.Is(err, ErrNoFocusedElement) {
			return 0, ErrNotEditable
		}
		return 0, err
	}
	if api.cfGetTypeID(value) != api.axValueGetTypeID() || api.axValueGetType(value) != axValueCFRange {
		api.cfRelease(value)
		return 0, ErrNotEditable
	}
	return value, nil
}

func focusedElement(api *nativeAPI, pid int32) (uintptr, int32, error) {
	root := api.createApplication(pid)
	if root == 0 {
		return 0, 0, ErrPermissionDenied
	}
	defer api.cfRelease(root)
	element, err := api.requiredAttribute(root, "AXFocusedUIElement")
	if err != nil {
		return 0, 0, err
	}
	var elementPID int32
	if code := api.getPID(element, &elementPID); code != axErrorSuccess || elementPID <= 0 {
		api.cfRelease(element)
		if code != axErrorSuccess {
			return 0, 0, nativeAXError("get focused element PID", code)
		}
		return 0, 0, ErrNoFocusedElement
	}
	return element, elementPID, nil
}

func confirmSnapshot(api *nativeAPI, expected snapshot) error {
	if err := validateEditableElement(api, expected.element); err != nil {
		return err
	}
	if api.isProcessTrusted() == 0 {
		return ErrPermissionDenied
	}
	if secureEventInputEnabled(api) {
		return ErrSecureInput
	}
	frontPID, err := frontmostProcessID()
	if err != nil {
		return err
	}
	if frontPID != expected.processID {
		return ErrTargetChanged
	}
	element, pid, err := focusedElement(api, frontPID)
	if err != nil {
		return err
	}
	defer api.cfRelease(element)
	if pid != expected.processID || api.cfEqual(element, expected.element) == 0 {
		return ErrTargetChanged
	}
	selection, err := selectedRange(api, element)
	if err != nil {
		return err
	}
	sameSelection := api.cfEqual(selection, expected.selection) != 0
	api.cfRelease(selection)
	if !sameSelection {
		return ErrTargetChanged
	}
	if expected.marker != 0 {
		marker, err := api.requiredAttribute(element, "AXSelectedTextMarkerRange")
		if err != nil {
			return err
		}
		sameMarker := api.cfEqual(marker, expected.marker) != 0
		api.cfRelease(marker)
		if !sameMarker {
			return ErrTargetChanged
		}
	}
	// Process identity is checked again after the final AX element and selection
	// reads, immediately before the caller asks this same captured element to
	// accept the requested text or selection change.
	finalPID, err := frontmostProcessID()
	if err != nil {
		return err
	}
	if finalPID != expected.processID {
		return ErrTargetChanged
	}
	return nil
}

func frontmostProcessID() (int32, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	poolClass := objc.GetClass("NSAutoreleasePool")
	workspaceClass := objc.GetClass("NSWorkspace")
	if poolClass == 0 || workspaceClass == 0 {
		return 0, errors.New("textoutput: AppKit frontmost-process API unavailable")
	}
	pool := objc.ID(poolClass).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("init"))
	if pool == 0 {
		return 0, errors.New("textoutput: create AppKit autorelease pool failed")
	}
	defer pool.Send(objc.RegisterName("drain"))
	workspace := objc.ID(workspaceClass).Send(objc.RegisterName("sharedWorkspace"))
	if workspace == 0 {
		return 0, errors.New("textoutput: read NSWorkspace failed")
	}
	application := objc.ID(workspace).Send(objc.RegisterName("frontmostApplication"))
	if application == 0 {
		return 0, ErrNoFrontmostApp
	}
	pid := objc.Send[int32](application, objc.RegisterName("processIdentifier"))
	if pid <= 0 {
		return 0, ErrNoFrontmostApp
	}
	return pid, nil
}

func nativeAXError(operation string, code int32) error {
	if code == axErrorAPIDisabled {
		return fmt.Errorf("%w: Accessibility API disabled during %s", ErrPermissionDenied, operation)
	}
	return fmt.Errorf("textoutput: AX %s failed with error %d", operation, code)
}

var secureEventInputMu sync.Mutex

func secureEventInputEnabled(api *nativeAPI) bool {
	secureEventInputMu.Lock()
	defer secureEventInputMu.Unlock()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return api.isSecureEventInputOn() != 0
}

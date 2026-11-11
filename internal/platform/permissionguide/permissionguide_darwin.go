//go:build darwin

package permissionguide

import (
	"runtime"
	"sync"
	"unicode/utf8"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
)

const systemPreferencesBundleID = "com.apple.systempreferences"

var foundationFramework struct {
	once   sync.Once
	handle uintptr
	err    error
}

func loadFoundationFramework() error {
	foundationFramework.once.Do(func() {
		handle, err := purego.Dlopen("/System/Library/Frameworks/Foundation.framework/Foundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			foundationFramework.err = err
			return
		}
		foundationFramework.handle = handle
	})
	return foundationFramework.err
}

func MainBundleIdentifier() (string, bool) {
	if loadFoundationFramework() != nil {
		return "", false
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	poolClass, bundleClass := objc.GetClass("NSAutoreleasePool"), objc.GetClass("NSBundle")
	if poolClass == 0 || bundleClass == 0 {
		return "", false
	}
	pool := objc.ID(poolClass).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("init"))
	if pool == 0 {
		return "", false
	}
	defer pool.Send(objc.RegisterName("drain"))
	mainBundleSelector := objc.RegisterName("mainBundle")
	identifierSelector := objc.RegisterName("bundleIdentifier")
	respondsSelector := objc.RegisterName("respondsToSelector:")
	bundle := objc.Send[objc.ID](objc.ID(bundleClass), mainBundleSelector)
	if bundle == 0 || !objc.Send[bool](bundle, respondsSelector, identifierSelector) {
		return "", false
	}
	identifier := objc.Send[objc.ID](bundle, identifierSelector)
	utf8Selector := objc.RegisterName("UTF8String")
	lengthSelector := objc.RegisterName("lengthOfBytesUsingEncoding:")
	if identifier == 0 || !objc.Send[bool](identifier, respondsSelector, utf8Selector) || !objc.Send[bool](identifier, respondsSelector, lengthSelector) {
		return "", false
	}
	nativeString := objc.Send[*byte](identifier, utf8Selector)
	length := objc.Send[uintptr](identifier, lengthSelector, uintptr(4)) // NSUTF8StringEncoding.
	if nativeString == nil || length == 0 || length > 1024 {
		return "", false
	}
	bytes := unsafe.Slice(nativeString, int(length))
	bundleID := string(bytes)
	return bundleID, utf8.ValidString(bundleID)
}

var bluetoothFramework struct {
	once   sync.Once
	handle uintptr
	err    error
}

func loadBluetoothFramework() error {
	bluetoothFramework.once.Do(func() {
		handle, err := purego.Dlopen("/System/Library/Frameworks/CoreBluetooth.framework/CoreBluetooth", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			bluetoothFramework.err = err
			return
		}
		bluetoothFramework.handle = handle
	})
	return bluetoothFramework.err
}

func ReadBluetoothAccess() BluetoothAccess {
	return readBluetoothAccess()
}

func readBluetoothAccess() BluetoothAccess {
	if loadBluetoothFramework() != nil {
		return BluetoothAccessUnknown
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	class := objc.GetClass("CBManager")
	if class == 0 {
		return BluetoothAccessUnknown
	}
	selector := objc.RegisterName("authorization")
	if !objc.Send[bool](objc.ID(class), objc.RegisterName("respondsToSelector:"), selector) {
		return BluetoothAccessUnknown
	}
	return bluetoothAccessFromNative(objc.Send[int64](objc.ID(class), selector))
}

func bluetoothAccessFromNative(value int64) BluetoothAccess {
	switch value {
	case 0:
		return BluetoothAccessNotDetermined
	case 1:
		return BluetoothAccessRestricted
	case 2:
		return BluetoothAccessDenied
	case 3:
		return BluetoothAccessAuthorized
	default:
		return BluetoothAccessUnknown
	}
}

var pendingBluetoothRequest struct {
	sync.Mutex
	manager objc.ID
}

// RequestBluetoothAccess creates one passive CBCentralManager only after an
// explicit user action. It never scans, connects or reads a device. The
// manager is held until a read observes a decision or the panel closes.
func RequestBluetoothAccess() BluetoothAccess {
	state := readBluetoothAccess()
	if state != BluetoothAccessNotDetermined {
		return state
	}
	if loadBluetoothFramework() != nil {
		return BluetoothAccessUnknown
	}
	if hasPendingBluetoothRequest() {
		return state
	}
	runtime.LockOSThread()
	class := objc.GetClass("CBCentralManager")
	if class == 0 {
		runtime.UnlockOSThread()
		return BluetoothAccessUnknown
	}
	managerClass := objc.ID(class)
	alloc := objc.RegisterName("alloc")
	init := objc.RegisterName("initWithDelegate:queue:options:")
	if !objc.Send[bool](managerClass, objc.RegisterName("respondsToSelector:"), alloc) {
		runtime.UnlockOSThread()
		return BluetoothAccessUnknown
	}
	allocated := managerClass.Send(alloc)
	if allocated == 0 || !objc.Send[bool](allocated, objc.RegisterName("respondsToSelector:"), init) {
		if allocated != 0 {
			allocated.Send(objc.RegisterName("release"))
		}
		runtime.UnlockOSThread()
		return BluetoothAccessUnknown
	}
	manager := objc.Send[objc.ID](allocated, init, objc.ID(0), objc.ID(0), objc.ID(0))
	runtime.UnlockOSThread()
	if manager == 0 {
		return readBluetoothAccess()
	}

	pendingBluetoothRequest.Lock()
	if pendingBluetoothRequest.manager != 0 {
		pendingBluetoothRequest.Unlock()
		releaseBluetoothManager(manager)
		return readBluetoothAccess()
	}
	pendingBluetoothRequest.manager = manager
	pendingBluetoothRequest.Unlock()
	return readBluetoothAccess()
}

func hasPendingBluetoothRequest() bool {
	pendingBluetoothRequest.Lock()
	pending := pendingBluetoothRequest.manager != 0
	pendingBluetoothRequest.Unlock()
	return pending
}

func CancelBluetoothRequest() {
	mygo.RunOnMain(func() {
		pendingBluetoothRequest.Lock()
		manager := pendingBluetoothRequest.manager
		pendingBluetoothRequest.manager = 0
		pendingBluetoothRequest.Unlock()
		if manager != 0 {
			releaseBluetoothManager(manager)
		}
	})
}

func releaseBluetoothManager(manager objc.ID) {
	runtime.LockOSThread()
	manager.Send(objc.RegisterName("release"))
	runtime.UnlockOSThread()
}

var appKitFramework struct {
	once   sync.Once
	handle uintptr
	err    error
}

func loadAppKitFramework() error {
	appKitFramework.once.Do(func() {
		handle, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			appKitFramework.err = err
			return
		}
		appKitFramework.handle = handle
	})
	return appKitFramework.err
}

func systemSettingsPID() (int32, bool) {
	if loadAppKitFramework() != nil {
		return 0, false
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	poolClass, appClass, stringClass := objc.GetClass("NSAutoreleasePool"), objc.GetClass("NSRunningApplication"), objc.GetClass("NSString")
	if poolClass == 0 || appClass == 0 || stringClass == 0 {
		return 0, false
	}
	pool := objc.ID(poolClass).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("init"))
	if pool == 0 {
		return 0, false
	}
	defer pool.Send(objc.RegisterName("drain"))
	identifier := objc.Send[objc.ID](objc.ID(stringClass), objc.RegisterName("stringWithUTF8String:"), systemPreferencesBundleID+"\x00")
	class := objc.ID(appClass)
	selector := objc.RegisterName("runningApplicationsWithBundleIdentifier:")
	if identifier == 0 || !objc.Send[bool](class, objc.RegisterName("respondsToSelector:"), selector) {
		return 0, false
	}
	applications := objc.Send[objc.ID](class, selector, identifier)
	if applications == 0 || !objc.Send[bool](applications, objc.RegisterName("respondsToSelector:"), objc.RegisterName("count")) ||
		!objc.Send[bool](applications, objc.RegisterName("respondsToSelector:"), objc.RegisterName("objectAtIndex:")) {
		return 0, false
	}
	count := objc.Send[uintptr](applications, objc.RegisterName("count"))
	if count != 1 {
		return 0, false
	}
	application := objc.Send[objc.ID](applications, objc.RegisterName("objectAtIndex:"), uintptr(0))
	if application == 0 || !objc.Send[bool](application, objc.RegisterName("respondsToSelector:"), objc.RegisterName("processIdentifier")) ||
		!objc.Send[bool](application, objc.RegisterName("respondsToSelector:"), objc.RegisterName("isTerminated")) ||
		objc.Send[bool](application, objc.RegisterName("isTerminated")) {
		return 0, false
	}
	pid := objc.Send[int32](application, objc.RegisterName("processIdentifier"))
	return pid, pid > 0
}

type cgRect struct{ X, Y, Width, Height float64 }

var windowListAPI struct {
	once          sync.Once
	windowList    func(uint32, uint32) uintptr
	buttonState   func(int32, uint32) bool
	arrayCount    func(uintptr) int64
	arrayValue    func(uintptr, int64) uintptr
	dictionaryGet func(uintptr, uintptr) uintptr
	numberGet     func(uintptr, int64, unsafe.Pointer) bool
	rectFromDict  func(uintptr, *cgRect) bool
	release       func(uintptr)
	ownerPIDKey   uintptr
	layerKey      uintptr
	boundsKey     uintptr
	numberKey     uintptr
	err           error
}

func loadWindowListAPI() error {
	windowListAPI.once.Do(func() {
		graphics, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			windowListAPI.err = err
			return
		}
		coreFoundation, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			windowListAPI.err = err
			return
		}
		bindings := []struct {
			library uintptr
			name    string
			target  any
		}{
			{graphics, "CGWindowListCopyWindowInfo", &windowListAPI.windowList},
			{graphics, "CGEventSourceButtonState", &windowListAPI.buttonState},
			{coreFoundation, "CFArrayGetCount", &windowListAPI.arrayCount},
			{coreFoundation, "CFArrayGetValueAtIndex", &windowListAPI.arrayValue},
			{coreFoundation, "CFDictionaryGetValue", &windowListAPI.dictionaryGet},
			{coreFoundation, "CFNumberGetValue", &windowListAPI.numberGet},
			{graphics, "CGRectMakeWithDictionaryRepresentation", &windowListAPI.rectFromDict},
			{coreFoundation, "CFRelease", &windowListAPI.release},
		}
		for _, binding := range bindings {
			address, err := purego.Dlsym(binding.library, binding.name)
			if err != nil {
				windowListAPI.err = err
				return
			}
			purego.RegisterFunc(binding.target, address)
		}
		libSystem, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			windowListAPI.err = err
			return
		}
		copyAddress, err := purego.Dlsym(libSystem, "memcpy")
		if err != nil {
			windowListAPI.err = err
			return
		}
		// These symbols contain native CFStringRef values, not Go pointers.
		var copyNative func(*uintptr, uintptr, uintptr) uintptr
		purego.RegisterFunc(&copyNative, copyAddress)
		for name, target := range map[string]*uintptr{
			"kCGWindowOwnerPID": &windowListAPI.ownerPIDKey,
			"kCGWindowLayer":    &windowListAPI.layerKey,
			"kCGWindowBounds":   &windowListAPI.boundsKey,
			"kCGWindowNumber":   &windowListAPI.numberKey,
		} {
			address, err := purego.Dlsym(graphics, name)
			if err != nil {
				windowListAPI.err = err
				return
			}
			copyNative(target, address, unsafe.Sizeof(*target))
			if *target == 0 {
				windowListAPI.err = errWindowQuery
				return
			}
		}
	})
	return windowListAPI.err
}

func SystemSettingsWindowBounds() (Rect, bool) {
	snapshot, found := SystemSettingsWindowSnapshot()
	return snapshot.Frame, found
}

func SystemSettingsWindowSnapshot() (WindowSnapshot, bool) {
	pid, ok := systemSettingsPID()
	if !ok || loadWindowListAPI() != nil {
		return WindowSnapshot{}, false
	}
	list := windowListAPI.windowList(1, 0) // kCGWindowListOptionOnScreenOnly, kCGNullWindowID.
	if list == 0 {
		return WindowSnapshot{}, false
	}
	defer windowListAPI.release(list)
	count := windowListAPI.arrayCount(list)
	if count < 0 || count > 8192 {
		return WindowSnapshot{}, false
	}
	var match WindowSnapshot
	matches := 0
	for index := int64(0); index < count; index++ {
		entry := windowListAPI.arrayValue(list, index)
		if entry == 0 {
			continue
		}
		pidValue := windowListAPI.dictionaryGet(entry, windowListAPI.ownerPIDKey)
		layerValue := windowListAPI.dictionaryGet(entry, windowListAPI.layerKey)
		boundsValue := windowListAPI.dictionaryGet(entry, windowListAPI.boundsKey)
		if pidValue == 0 || layerValue == 0 || boundsValue == 0 {
			continue
		}
		var ownerPID, layer int32
		if !windowListAPI.numberGet(pidValue, 3, unsafe.Pointer(&ownerPID)) ||
			!windowListAPI.numberGet(layerValue, 3, unsafe.Pointer(&layer)) || ownerPID != pid || layer != 0 {
			continue
		}
		var bounds cgRect
		if !windowListAPI.rectFromDict(boundsValue, &bounds) || bounds.Width < 200 || bounds.Height < 150 {
			continue
		}
		numberValue := windowListAPI.dictionaryGet(entry, windowListAPI.numberKey)
		var number int64
		// kCFNumberSInt64Type preserves the complete uint32 CGWindowID range.
		if numberValue == 0 || !windowListAPI.numberGet(numberValue, 4, unsafe.Pointer(&number)) || number <= 0 || number > 1<<32-1 {
			continue
		}
		match = WindowSnapshot{ID: uint32(number), Frame: Rect(bounds)}
		matches++
	}
	if matches != 1 {
		return WindowSnapshot{}, false
	}
	return match, true
}

var errWindowQuery = &windowQueryError{}

type windowQueryError struct{}

func (*windowQueryError) Error() string {
	return "permissionguide: invalid CoreGraphics window metadata"
}

// PrimaryMouseButtonDown reads the HID state table. It installs no event tap
// and neither requests Input Monitoring nor records pointer events.
func PrimaryMouseButtonDown() bool {
	if loadWindowListAPI() != nil {
		return false
	}
	const hidSystemState int32 = 1
	const leftMouseButton uint32 = 0
	return windowListAPI.buttonState(hidSystemState, leftMouseButton)
}

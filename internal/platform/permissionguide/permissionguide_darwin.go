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

var pendingBluetoothRequest bluetoothRequestObserver

var bluetoothDelegateRegistration struct {
	once  sync.Once
	class objc.Class
	err   error
}

// RequestBluetoothAccess creates one passive CBCentralManager only after an
// explicit user action. It never scans, connects or reads a device. The
// manager is held until a read observes a decision or the panel closes.
func RequestBluetoothAccess() BluetoothAccess {
	return RequestBluetoothAccessWithCallback(nil)
}

// RequestBluetoothAccessWithCallback preserves the no-argument API while
// notifying the caller when this manager reports a CoreBluetooth state change.
func RequestBluetoothAccessWithCallback(onStateUpdate func()) BluetoothAccess {
	var state BluetoothAccess
	mygo.RunOnMain(func() { state = requestBluetoothAccessOnMain(onStateUpdate) })
	return state
}

func requestBluetoothAccessOnMain(onStateUpdate func()) BluetoothAccess {
	state := readBluetoothAccess()
	if pendingBluetoothRequest.hasManager() {
		pendingBluetoothRequest.updateHandler(onStateUpdate)
		return state
	}
	if state != BluetoothAccessNotDetermined || loadBluetoothFramework() != nil || ensureBluetoothDelegate() != nil {
		if state == BluetoothAccessNotDetermined {
			return BluetoothAccessUnknown
		}
		return state
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	class := objc.GetClass("CBCentralManager")
	if class == 0 {
		return BluetoothAccessUnknown
	}
	delegate := objc.ID(bluetoothDelegateRegistration.class).Send(objc.RegisterName("new"))
	if delegate == 0 {
		return BluetoothAccessUnknown
	}
	managerClass := objc.ID(class)
	alloc := objc.RegisterName("alloc")
	init := objc.RegisterName("initWithDelegate:queue:options:")
	if !objc.Send[bool](managerClass, objc.RegisterName("respondsToSelector:"), alloc) {
		delegate.Send(objc.RegisterName("release"))
		return BluetoothAccessUnknown
	}
	allocated := managerClass.Send(alloc)
	if allocated == 0 || !objc.Send[bool](allocated, objc.RegisterName("respondsToSelector:"), init) {
		if allocated != 0 {
			allocated.Send(objc.RegisterName("release"))
		}
		delegate.Send(objc.RegisterName("release"))
		return BluetoothAccessUnknown
	}
	manager := objc.Send[objc.ID](allocated, init, delegate, objc.ID(0), objc.ID(0))
	if manager == 0 {
		delegate.Send(objc.RegisterName("release"))
		return readBluetoothAccess()
	}
	if !pendingBluetoothRequest.install(uintptr(manager), uintptr(delegate), onStateUpdate) {
		releaseBluetoothRequestObject(manager)
		delegate.Send(objc.RegisterName("release"))
		return readBluetoothAccess()
	}
	return readBluetoothAccess()
}

func ensureBluetoothDelegate() error {
	bluetoothDelegateRegistration.once.Do(func() {
		bluetoothDelegateRegistration.class, bluetoothDelegateRegistration.err = objc.RegisterClass(
			"MiMacPermissionGuideBluetoothDelegate", objc.GetClass("NSObject"), nil, nil,
			[]objc.MethodDef{{Cmd: objc.RegisterName("centralManagerDidUpdateState:"), Fn: bluetoothCentralManagerDidUpdateState}},
		)
	})
	return bluetoothDelegateRegistration.err
}

func bluetoothCentralManagerDidUpdateState(delegate objc.ID, _ objc.SEL, manager objc.ID) {
	onStateUpdate := pendingBluetoothRequest.handlerFor(uintptr(manager), uintptr(delegate))
	if onStateUpdate != nil {
		// CoreBluetooth uses the main queue here. Queue the UI work from another
		// goroutine so terminal refresh can release the manager after this callback.
		go mygo.RunOnMain(onStateUpdate)
	}
}

func CancelBluetoothRequest() {
	mygo.RunOnMain(func() {
		managerID, delegateID := pendingBluetoothRequest.take()
		if managerID != 0 {
			manager := objc.ID(managerID)
			if objc.Send[bool](manager, objc.RegisterName("respondsToSelector:"), objc.RegisterName("setDelegate:")) {
				manager.Send(objc.RegisterName("setDelegate:"), objc.ID(0))
			}
			releaseBluetoothRequestObject(manager)
		}
		if delegateID != 0 {
			releaseBluetoothRequestObject(objc.ID(delegateID))
		}
	})
}

func releaseBluetoothRequestObject(object objc.ID) {
	runtime.LockOSThread()
	object.Send(objc.RegisterName("release"))
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

// SystemSettingsRunning only queries the existing application; it never launches it.
func SystemSettingsRunning() bool {
	_, running := systemSettingsPID()
	return running
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
	alphaKey      uintptr
	nameKey       uintptr
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
			"kCGWindowAlpha":    &windowListAPI.alphaKey,
			"kCGWindowName":     &windowListAPI.nameKey,
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
	return systemSettingsWindow(0, false)
}

// VisibleSystemSettingsWindow uses the front-to-back list to exclude covered targets.
func VisibleSystemSettingsWindow(id uint32) (WindowSnapshot, bool) {
	return systemSettingsWindow(id, true)
}

// SettingsReceivesReleasedDrop checks the live receiver at file delivery. A
// cached overlay hole can outlive its destination during native drag tracking.
func SettingsReceivesReleasedDrop(id uint32, x, y int, input, routed bool) bool {
	if id == 0 || loadWindowListAPI() != nil || PrimaryMouseButtonDown() {
		return false
	}
	pid, ok := systemSettingsPID()
	if !ok {
		return false
	}
	workspace := objc.Send[objc.ID](objc.ID(objc.GetClass("NSWorkspace")), objc.RegisterName("sharedWorkspace"))
	front := objc.Send[objc.ID](workspace, objc.RegisterName("frontmostApplication"))
	if front == 0 || objc.Send[int32](front, objc.RegisterName("processIdentifier")) != pid {
		return false
	}
	records, ok := readWindowRecords(0x11, 0)
	if !ok || settingsHasAttachedDialog(records, pid) || !settingsReceivesPoint(records, id, pid, float64(x), float64(y)) {
		return false
	}
	return registrationPageMatches(id, input, routed)
}

// The gesture still originates over the guide. Validate the selected system
// pane itself before exposing the file, without testing the pointer as a drop.
func SettingsReadyForControlDrag(routed bool) bool {
	pid, ok := systemSettingsPID()
	if !ok || loadWindowListAPI() != nil {
		return false
	}
	records, ok := readWindowRecords(0x11, 0)
	if !ok || settingsHasAttachedDialog(records, pid) {
		return false
	}
	target, found := selectSettingsWindow(records, pid, 0)
	if !found || !visibleSettingsTarget(records, target) {
		return false
	}
	return registrationPageMatches(target.ID, false, routed)
}

func registrationPageMatches(id uint32, input, routed bool) bool {
	// This field may be unavailable. Never request screen access to read it.
	const includingWindow = 1 << 3
	window := windowListAPI.windowList(includingWindow, id)
	title := ""
	if window != 0 {
		defer windowListAPI.release(window)
		if windowListAPI.arrayCount(window) == 1 {
			entry := windowListAPI.arrayValue(window, 0)
			value := windowListAPI.dictionaryGet(entry, windowListAPI.nameKey)
			if value != 0 {
				title = objc.Send[string](objc.ID(value), objc.RegisterName("UTF8String"))
			}
		}
	}
	return settingsDropMatchesPage(title, input, routed)
}

func systemSettingsWindow(id uint32, checkOcclusion bool) (WindowSnapshot, bool) {
	pid, ok := systemSettingsPID()
	if !ok || loadWindowListAPI() != nil {
		return WindowSnapshot{}, false
	}
	records, ok := readWindowRecords(0x11, 0)
	if !ok {
		return WindowSnapshot{}, false
	}
	selected, ok := selectSettingsWindow(records, pid, id)
	if !ok {
		return WindowSnapshot{}, false
	}
	if checkOcclusion && !visibleSettingsTarget(records, selected) {
		return WindowSnapshot{}, false
	}
	return selected, true
}

func readWindowRecords(options, relative uint32) ([]windowRecord, bool) {
	list := windowListAPI.windowList(options, relative)
	if list == 0 {
		return nil, false
	}
	defer windowListAPI.release(list)
	count := windowListAPI.arrayCount(list)
	if count < 0 || count > 8192 {
		return nil, false
	}
	records := make([]windowRecord, 0, int(count))
	for i := int64(0); i < count; i++ {
		entry := windowListAPI.arrayValue(list, i)
		if entry == 0 {
			continue
		}
		pidValue := windowListAPI.dictionaryGet(entry, windowListAPI.ownerPIDKey)
		numberValue := windowListAPI.dictionaryGet(entry, windowListAPI.numberKey)
		boundsValue := windowListAPI.dictionaryGet(entry, windowListAPI.boundsKey)
		var pid, number, layer int32
		var bounds cgRect
		if pidValue == 0 || numberValue == 0 || boundsValue == 0 ||
			!windowListAPI.numberGet(pidValue, 3, unsafe.Pointer(&pid)) ||
			!windowListAPI.numberGet(numberValue, 3, unsafe.Pointer(&number)) || number == 0 ||
			!windowListAPI.rectFromDict(boundsValue, &bounds) {
			continue
		}
		if value := windowListAPI.dictionaryGet(entry, windowListAPI.layerKey); value != 0 && !windowListAPI.numberGet(value, 3, unsafe.Pointer(&layer)) {
			continue
		}
		alpha := float64(1)
		if value := windowListAPI.dictionaryGet(entry, windowListAPI.alphaKey); value != 0 && !windowListAPI.numberGet(value, 6, unsafe.Pointer(&alpha)) {
			continue
		}
		records = append(records, windowRecord{id: uint32(number), owner: pid, layer: layer, alpha: alpha, frame: Rect(bounds)})
	}
	return records, true
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

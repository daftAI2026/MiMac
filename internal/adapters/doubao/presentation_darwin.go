//go:build darwin

package doubao

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"howett.net/plist"
	"mimac.local/mimac/internal/platform/processidentity"
)

const (
	presentationCFStringUTF8    = uint32(0x08000100)
	presentationAXPointType     = uint32(1)
	presentationAXSizeType      = uint32(2)
	presentationCFNumberIntType = int64(3)
	presentationOnScreenWindows = uint32(1)
	presentationIncludeWindow   = uint32(1 << 3)
	presentationMaxWindowCount  = int64(8192)
	presentationMaxAXWindows    = int64(2048)
)

type presentationNativeAPI struct {
	windowList      func(uint32, uint32) uintptr
	cfArrayCount    func(uintptr) int64
	cfArrayValue    func(uintptr, int64) uintptr
	cfDictionaryGet func(uintptr, uintptr) uintptr
	cfNumberGet     func(uintptr, int64, unsafe.Pointer) uint8
	cfBooleanValue  func(uintptr) uint8
	cfGetTypeID     func(uintptr) uintptr
	cfStringTypeID  func() uintptr
	cfNumberTypeID  func() uintptr
	cfBooleanTypeID func() uintptr
	cfArrayTypeID   func() uintptr
	cfCreateString  func(uintptr, *byte, uint32) uintptr
	cfStringLength  func(uintptr) int64
	cfStringCString func(uintptr, *byte, int64, uint32) uint8
	cfRetain        func(uintptr) uintptr
	cfRelease       func(uintptr)
	copyNative      func(*uintptr, uintptr, uintptr) uintptr
	rectFromDict    func(uintptr, *presentationRect) uint8

	axCreateApplication func(int32) uintptr
	axCopyAttribute     func(uintptr, uintptr, *uintptr) int32
	axGetPID            func(uintptr, *int32) int32
	axIsSettable        func(uintptr, uintptr, *uint8) int32
	axSetAttribute      func(uintptr, uintptr, uintptr) int32
	axValueGetType      func(uintptr) uint32
	axValueGetValue     func(uintptr, uint32, unsafe.Pointer) uint8
	axValueCreate       func(uint32, unsafe.Pointer) uintptr
	axSetTimeout        func(uintptr, float32)

	keyWindowNumber   uintptr
	keyWindowOwnerPID uintptr
	keyWindowLayer    uintptr
	keyWindowBounds   uintptr
	keyWindowOnscreen uintptr
	attributes        map[string]uintptr
	libraries         []uintptr
}

var presentationNativeRuntime struct {
	once sync.Once
	api  *presentationNativeAPI
	err  error
}

type darwinPresentationProbe struct{ api *presentationNativeAPI }

func captureRecordingBarBaselineNative(pid int32, expectedExecutable string) *RecordingBarBaseline {
	if pid <= 0 || !isVerifiedDoubaoBundleExecutable(expectedExecutable) {
		return nil
	}
	identity, err := processidentity.Capture(pid, expectedExecutable)
	if err != nil {
		return nil
	}
	probe, err := loadDarwinPresentationProbe()
	if err != nil {
		return nil
	}
	baseline, err := captureRecordingBarBaseline(pid, identity.Check, probe)
	if err != nil {
		return nil
	}
	return baseline
}

// Verify bundle identity from the selected executable's enclosing app bundle,
// without restricting the installation directory or release version.
func isVerifiedDoubaoBundleExecutable(executable string) bool {
	if !filepath.IsAbs(executable) || strings.ContainsRune(executable, 0) {
		return false
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(executable))
	if err != nil {
		return false
	}
	macOSDir := filepath.Dir(canonical)
	contentsDir := filepath.Dir(macOSDir)
	appBundle := filepath.Dir(contentsDir)
	if filepath.Base(macOSDir) != "MacOS" || filepath.Base(contentsDir) != "Contents" ||
		filepath.Ext(appBundle) != ".app" {
		return false
	}
	info, err := os.ReadFile(filepath.Join(contentsDir, "Info.plist"))
	if err != nil || len(info) == 0 || len(info) > 1024*1024 {
		return false
	}
	var fields map[string]any
	if _, err := plist.Unmarshal(info, &fields); err != nil {
		return false
	}
	return fields["CFBundleIdentifier"] == bundleIdentifier &&
		fields["CFBundleExecutable"] == filepath.Base(canonical)
}

func loadDarwinPresentationProbe() (presentationProbe, error) {
	api, err := loadPresentationNativeAPI()
	if err != nil {
		return nil, err
	}
	return darwinPresentationProbe{api: api}, nil
}

func loadPresentationNativeAPI() (*presentationNativeAPI, error) {
	presentationNativeRuntime.once.Do(func() {
		paths := []string{
			"/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation",
			"/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics",
			"/System/Library/Frameworks/ApplicationServices.framework/Frameworks/HIServices.framework/HIServices",
			"/usr/lib/libSystem.B.dylib",
		}
		libs := make([]uintptr, 0, len(paths))
		for _, path := range paths {
			handle, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
			if err != nil {
				closePresentationLibraries(libs)
				presentationNativeRuntime.err = fmt.Errorf("doubao presentation: load %s: %w", filepath.Base(path), err)
				return
			}
			libs = append(libs, handle)
		}
		api := &presentationNativeAPI{libraries: libs, attributes: make(map[string]uintptr)}
		bindings := []struct {
			library int
			name    string
			target  any
		}{
			{1, "CGWindowListCopyWindowInfo", &api.windowList},
			{0, "CFArrayGetCount", &api.cfArrayCount},
			{0, "CFArrayGetValueAtIndex", &api.cfArrayValue},
			{0, "CFDictionaryGetValue", &api.cfDictionaryGet},
			{0, "CFNumberGetValue", &api.cfNumberGet},
			{0, "CFBooleanGetValue", &api.cfBooleanValue},
			{0, "CFGetTypeID", &api.cfGetTypeID},
			{0, "CFStringGetTypeID", &api.cfStringTypeID},
			{0, "CFNumberGetTypeID", &api.cfNumberTypeID},
			{0, "CFBooleanGetTypeID", &api.cfBooleanTypeID},
			{0, "CFArrayGetTypeID", &api.cfArrayTypeID},
			{0, "CFStringCreateWithCString", &api.cfCreateString},
			{0, "CFStringGetLength", &api.cfStringLength},
			{0, "CFStringGetCString", &api.cfStringCString},
			{0, "CFRetain", &api.cfRetain},
			{0, "CFRelease", &api.cfRelease},
			{3, "memcpy", &api.copyNative},
			{1, "CGRectMakeWithDictionaryRepresentation", &api.rectFromDict},
			{2, "AXUIElementCreateApplication", &api.axCreateApplication},
			{2, "AXUIElementCopyAttributeValue", &api.axCopyAttribute},
			{2, "AXUIElementGetPid", &api.axGetPID},
			{2, "AXUIElementIsAttributeSettable", &api.axIsSettable},
			{2, "AXUIElementSetAttributeValue", &api.axSetAttribute},
			{2, "AXValueGetType", &api.axValueGetType},
			{2, "AXValueGetValue", &api.axValueGetValue},
			{2, "AXValueCreate", &api.axValueCreate},
			{2, "AXUIElementSetMessagingTimeout", &api.axSetTimeout},
		}
		for _, binding := range bindings {
			address, err := purego.Dlsym(libs[binding.library], binding.name)
			if err != nil {
				closePresentationLibraries(libs)
				presentationNativeRuntime.err = fmt.Errorf("doubao presentation: resolve %s: %w", binding.name, err)
				return
			}
			purego.RegisterFunc(binding.target, address)
		}
		for name, destination := range map[string]*uintptr{
			"kCGWindowNumber":     &api.keyWindowNumber,
			"kCGWindowOwnerPID":   &api.keyWindowOwnerPID,
			"kCGWindowLayer":      &api.keyWindowLayer,
			"kCGWindowBounds":     &api.keyWindowBounds,
			"kCGWindowIsOnscreen": &api.keyWindowOnscreen,
		} {
			address, err := purego.Dlsym(libs[1], name)
			if err != nil {
				closePresentationLibraries(libs)
				presentationNativeRuntime.err = fmt.Errorf("doubao presentation: resolve %s: %w", name, err)
				return
			}
			api.copyNative(destination, address, unsafe.Sizeof(*destination))
			if *destination == 0 {
				closePresentationLibraries(libs)
				presentationNativeRuntime.err = fmt.Errorf("doubao presentation: null %s", name)
				return
			}
		}
		for _, name := range []string{"AXWindows", "AXRole", "AXSubrole", "AXPosition", "AXSize", "AXChildren"} {
			ref, err := api.newString(name)
			if err != nil {
				closePresentationLibraries(libs)
				presentationNativeRuntime.err = err
				return
			}
			api.attributes[name] = ref
		}
		presentationNativeRuntime.api = api
	})
	return presentationNativeRuntime.api, presentationNativeRuntime.err
}

func closePresentationLibraries(libraries []uintptr) {
	for _, library := range libraries {
		_ = purego.Dlclose(library)
	}
}

func (api *presentationNativeAPI) newString(value string) (uintptr, error) {
	if value == "" || strings.ContainsRune(value, 0) {
		return 0, ErrPresentationUnavailable
	}
	bytes := append([]byte(value), 0)
	ref := api.cfCreateString(0, &bytes[0], presentationCFStringUTF8)
	runtime.KeepAlive(bytes)
	if ref == 0 {
		return 0, ErrPresentationUnavailable
	}
	return ref, nil
}

func (api *presentationNativeAPI) windowRecords(options, relative uint32) ([]presentationWindow, error) {
	list := api.windowList(options, relative)
	if list == 0 {
		return nil, ErrPresentationUnavailable
	}
	defer api.cfRelease(list)
	count := api.cfArrayCount(list)
	if count < 0 || count > presentationMaxWindowCount {
		return nil, ErrPresentationUnavailable
	}
	records := make([]presentationWindow, 0, int(count))
	for index := int64(0); index < count; index++ {
		entry := api.cfArrayValue(list, index)
		if entry == 0 {
			continue
		}
		number := api.numberValue(api.cfDictionaryGet(entry, api.keyWindowNumber))
		owner := api.numberValue(api.cfDictionaryGet(entry, api.keyWindowOwnerPID))
		layer := api.numberValue(api.cfDictionaryGet(entry, api.keyWindowLayer))
		bounds := api.cfDictionaryGet(entry, api.keyWindowBounds)
		if number <= 0 || number > int64(^uint32(0)) || owner <= 0 || owner > int64(^uint32(0)) || bounds == 0 {
			continue
		}
		var frame presentationRect
		if api.rectFromDict(bounds, &frame) == 0 || !finitePoint(frame.origin) ||
			!finiteSize(frame.size) || frame.size.width <= 0 || frame.size.height <= 0 {
			continue
		}
		onScreen := false
		onScreenValue := api.cfDictionaryGet(entry, api.keyWindowOnscreen)
		if onScreenValue != 0 && api.cfGetTypeID(onScreenValue) == api.cfBooleanTypeID() {
			onScreen = api.cfBooleanValue(onScreenValue) != 0
		}
		records = append(records, presentationWindow{
			id: uint32(number), ownerPID: int32(owner), layer: int32(layer),
			frame: frame, onScreen: onScreen,
		})
	}
	return records, nil
}

func (api *presentationNativeAPI) numberValue(value uintptr) int64 {
	if value == 0 || api.cfGetTypeID(value) != api.cfNumberTypeID() {
		return 0
	}
	var result int32
	if api.cfNumberGet(value, presentationCFNumberIntType, unsafe.Pointer(&result)) == 0 {
		return 0
	}
	return int64(result)
}

func (probe darwinPresentationProbe) windowNumbers(pid int32) (map[uint32]struct{}, error) {
	records, err := probe.api.windowRecords(presentationOnScreenWindows, 0)
	if err != nil {
		return nil, err
	}
	result := make(map[uint32]struct{})
	for _, record := range records {
		if record.ownerPID == pid {
			result[record.id] = struct{}{}
		}
	}
	return result, nil
}

func (probe darwinPresentationProbe) windows(pid int32, newerThan map[uint32]struct{}) ([]presentationWindow, error) {
	records, err := probe.api.windowRecords(presentationOnScreenWindows, 0)
	if err != nil {
		return nil, err
	}
	newRecords := make([]presentationWindow, 0, 2)
	for _, record := range records {
		if record.ownerPID != pid {
			continue
		}
		if _, old := newerThan[record.id]; old {
			continue
		}
		newRecords = append(newRecords, record)
	}
	if len(newRecords) != 1 {
		return newRecords, nil
	}
	matched, err := probe.api.matchAXWindow(pid, newRecords[0].frame)
	if err != nil {
		return nil, err
	}
	newRecords[0].axMatchCount = matched.count
	if matched.count == 1 {
		newRecords[0].role = matched.role
		newRecords[0].subrole = matched.subrole
		newRecords[0].element = matched.element
		newRecords[0].position = matched.position
		newRecords[0].size = matched.size
		newRecords[0].positionSettable = matched.positionSettable
		newRecords[0].recordingBarChildren = matched.recordingBarChildren
	}
	return newRecords, nil
}

type axWindowMatch struct {
	count                int
	role                 string
	subrole              string
	element              uintptr
	position             presentationPoint
	size                 presentationSize
	positionSettable     bool
	recordingBarChildren bool
}

func (api *presentationNativeAPI) matchAXWindow(pid int32, frame presentationRect) (axWindowMatch, error) {
	var result axWindowMatch
	app := api.axCreateApplication(pid)
	if app == 0 {
		return result, ErrPresentationUnavailable
	}
	defer api.cfRelease(app)
	api.axSetTimeout(app, 0.15)
	windows, code := api.copyAttribute(app, "AXWindows")
	if code != 0 || windows == 0 || api.cfGetTypeID(windows) != api.cfArrayTypeID() {
		if windows != 0 {
			api.cfRelease(windows)
		}
		return result, fmt.Errorf("%w: AXWindows query failed (%d)", ErrPresentationUnavailable, code)
	}
	defer api.cfRelease(windows)
	count := api.cfArrayCount(windows)
	if count < 0 || count > presentationMaxAXWindows {
		return result, ErrPresentationUnavailable
	}
	for index := int64(0); index < count; index++ {
		element := api.cfArrayValue(windows, index)
		if element == 0 {
			continue
		}
		var elementPID int32
		if api.axGetPID(element, &elementPID) != 0 || elementPID != pid {
			continue
		}
		role := api.stringAttribute(element, "AXRole")
		position, positionOK := api.pointAttribute(element, "AXPosition")
		size, sizeOK := api.sizeAttribute(element, "AXSize")
		if !positionOK || !sizeOK || !sameRect(presentationRect{origin: position, size: size}, frame) {
			continue
		}
		result.count++
		if result.count != 1 {
			if result.element != 0 {
				api.cfRelease(result.element)
				result.element = 0
			}
			continue
		}
		result.role = role
		result.subrole = api.stringAttribute(element, "AXSubrole")
		result.position = position
		result.size = size
		result.recordingBarChildren = api.hasRecordingBarChildren(element)
		var settable uint8
		if api.axIsSettable(element, api.attributes["AXPosition"], &settable) == 0 {
			result.positionSettable = settable != 0
		}
		result.element = api.cfRetain(element)
	}
	if result.count != 1 && result.element != 0 {
		api.cfRelease(result.element)
		result.element = 0
	}
	return result, nil
}

func (api *presentationNativeAPI) copyAttribute(element uintptr, name string) (uintptr, int32) {
	attr := api.attributes[name]
	if element == 0 || attr == 0 {
		return 0, -25205
	}
	var value uintptr
	code := api.axCopyAttribute(element, attr, &value)
	return value, code
}

func (api *presentationNativeAPI) stringAttribute(element uintptr, name string) string {
	value, code := api.copyAttribute(element, name)
	if code != 0 || value == 0 {
		if value != 0 {
			api.cfRelease(value)
		}
		return ""
	}
	defer api.cfRelease(value)
	return api.stringValue(value)
}

func (api *presentationNativeAPI) stringValue(value uintptr) string {
	if value == 0 || api.cfGetTypeID(value) != api.cfStringTypeID() {
		return ""
	}
	length := api.cfStringLength(value)
	if length < 0 || length > 4096 {
		return ""
	}
	buffer := make([]byte, int(length)+1)
	if api.cfStringCString(value, &buffer[0], int64(len(buffer)), presentationCFStringUTF8) == 0 {
		return ""
	}
	for index, value := range buffer {
		if value == 0 {
			return string(buffer[:index])
		}
	}
	return ""
}

func (api *presentationNativeAPI) pointAttribute(element uintptr, name string) (presentationPoint, bool) {
	value, code := api.copyAttribute(element, name)
	if code != 0 || value == 0 {
		if value != 0 {
			api.cfRelease(value)
		}
		return presentationPoint{}, false
	}
	defer api.cfRelease(value)
	if api.axValueGetType(value) != presentationAXPointType {
		return presentationPoint{}, false
	}
	var point presentationPoint
	if api.axValueGetValue(value, presentationAXPointType, unsafe.Pointer(&point)) == 0 || !finitePoint(point) {
		return presentationPoint{}, false
	}
	return point, true
}

func (api *presentationNativeAPI) sizeAttribute(element uintptr, name string) (presentationSize, bool) {
	value, code := api.copyAttribute(element, name)
	if code != 0 || value == 0 {
		if value != 0 {
			api.cfRelease(value)
		}
		return presentationSize{}, false
	}
	defer api.cfRelease(value)
	if api.axValueGetType(value) != presentationAXSizeType {
		return presentationSize{}, false
	}
	var size presentationSize
	if api.axValueGetValue(value, presentationAXSizeType, unsafe.Pointer(&size)) == 0 || !finiteSize(size) {
		return presentationSize{}, false
	}
	return size, true
}

// Candidate lists contain a group. The compact listening panel exposes two
// direct static-text elements, even when their contents are empty.
func (api *presentationNativeAPI) hasRecordingBarChildren(window uintptr) bool {
	children, code := api.copyAttribute(window, "AXChildren")
	if children == 0 {
		return false
	}
	defer api.cfRelease(children)
	if code != 0 || api.cfGetTypeID(children) != api.cfArrayTypeID() || api.cfArrayCount(children) != 2 {
		return false
	}
	for i := int64(0); i < 2; i++ {
		child := api.cfArrayValue(children, i)
		if child == 0 || api.stringAttribute(child, "AXRole") != "AXStaticText" {
			return false
		}
	}
	return true
}

func (probe darwinPresentationProbe) windowByID(pid int32, id uint32) (presentationWindow, bool, error) {
	records, err := probe.api.windowRecords(presentationIncludeWindow, id)
	if err != nil {
		return presentationWindow{}, false, err
	}
	var found presentationWindow
	count := 0
	for _, record := range records {
		if record.id == id && record.ownerPID == pid {
			found = record
			count++
		}
	}
	if count != 1 {
		return presentationWindow{}, false, nil
	}
	return found, true, nil
}

func (probe darwinPresentationProbe) setPosition(element uintptr, point presentationPoint) error {
	if element == 0 || !finitePoint(point) {
		return ErrPresentationUnavailable
	}
	value := probe.api.axValueCreate(presentationAXPointType, unsafe.Pointer(&point))
	if value == 0 {
		return ErrPresentationUnavailable
	}
	defer probe.api.cfRelease(value)
	if code := probe.api.axSetAttribute(element, probe.api.attributes["AXPosition"], value); code != 0 {
		return fmt.Errorf("AXUIElementSetAttributeValue returned %d", code)
	}
	return nil
}

func (probe darwinPresentationProbe) position(element uintptr) (presentationPoint, error) {
	value, ok := probe.api.pointAttribute(element, "AXPosition")
	if !ok {
		return presentationPoint{}, ErrPresentationUnavailable
	}
	return value, nil
}

func (probe darwinPresentationProbe) elementPID(element uintptr) (int32, error) {
	var pid int32
	if element == 0 || probe.api.axGetPID(element, &pid) != 0 || pid <= 0 {
		return 0, ErrPresentationUnavailable
	}
	return pid, nil
}

func (probe darwinPresentationProbe) retain(element uintptr) uintptr {
	if element == 0 {
		return 0
	}
	return probe.api.cfRetain(element)
}

func (probe darwinPresentationProbe) release(element uintptr) {
	if element != 0 {
		probe.api.cfRelease(element)
	}
}

func finiteSize(size presentationSize) bool {
	return !math.IsNaN(size.width) && !math.IsNaN(size.height) &&
		!math.IsInf(size.width, 0) && !math.IsInf(size.height, 0)
}

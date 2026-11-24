//go:build darwin

package windowlease

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
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
	rectFromDict    func(uintptr, *Rect) uint8

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

func LoadDarwinProbe() (Probe, error) {
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
				presentationNativeRuntime.err = fmt.Errorf("recording window: load %s: %w", filepath.Base(path), err)
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
				presentationNativeRuntime.err = fmt.Errorf("recording window: resolve %s: %w", binding.name, err)
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
				presentationNativeRuntime.err = fmt.Errorf("recording window: resolve %s: %w", name, err)
				return
			}
			api.copyNative(destination, address, unsafe.Sizeof(*destination))
			if *destination == 0 {
				closePresentationLibraries(libs)
				presentationNativeRuntime.err = fmt.Errorf("recording window: null %s", name)
				return
			}
		}
		for _, name := range []string{"AXWindows", "AXRole", "AXSubrole", "AXPosition", "AXSize", "AXChildren", "AXIdentifier", "AXDescription"} {
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
		return 0, ErrUnavailable
	}
	bytes := append([]byte(value), 0)
	ref := api.cfCreateString(0, &bytes[0], presentationCFStringUTF8)
	runtime.KeepAlive(bytes)
	if ref == 0 {
		return 0, ErrUnavailable
	}
	return ref, nil
}

func (api *presentationNativeAPI) windowRecords(options, relative uint32) ([]Window, error) {
	list := api.windowList(options, relative)
	if list == 0 {
		return nil, ErrUnavailable
	}
	defer api.cfRelease(list)
	count := api.cfArrayCount(list)
	if count < 0 || count > presentationMaxWindowCount {
		return nil, ErrUnavailable
	}
	records := make([]Window, 0, int(count))
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
		var frame Rect
		if api.rectFromDict(bounds, &frame) == 0 || !finitePoint(frame.Origin) ||
			!finiteSize(frame.Size) || frame.Size.Width <= 0 || frame.Size.Height <= 0 {
			continue
		}
		onScreen := false
		onScreenValue := api.cfDictionaryGet(entry, api.keyWindowOnscreen)
		if onScreenValue != 0 && api.cfGetTypeID(onScreenValue) == api.cfBooleanTypeID() {
			onScreen = api.cfBooleanValue(onScreenValue) != 0
		}
		records = append(records, Window{
			ID: uint32(number), OwnerPID: int32(owner), Layer: int32(layer),
			Frame: frame, OnScreen: onScreen,
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

func (probe darwinPresentationProbe) WindowNumbers(pid int32) (map[uint32]struct{}, error) {
	records, err := probe.api.windowRecords(presentationOnScreenWindows, 0)
	if err != nil {
		return nil, err
	}
	result := make(map[uint32]struct{})
	for _, record := range records {
		if record.OwnerPID == pid {
			result[record.ID] = struct{}{}
		}
	}
	return result, nil
}

func (probe darwinPresentationProbe) Windows(pid int32, newerThan map[uint32]struct{}) ([]Window, error) {
	records, err := probe.api.windowRecords(presentationOnScreenWindows, 0)
	if err != nil {
		return nil, err
	}
	newRecords := make([]Window, 0, 2)
	for _, record := range records {
		if record.OwnerPID != pid {
			continue
		}
		if _, old := newerThan[record.ID]; old {
			continue
		}
		newRecords = append(newRecords, record)
	}
	if len(newRecords) != 1 {
		return newRecords, nil
	}
	matched, err := probe.api.matchAXWindow(pid, newRecords[0].Frame)
	if err != nil {
		return nil, err
	}
	newRecords[0].AXMatchCount = matched.Count
	if matched.Count == 1 {
		newRecords[0].Role = matched.Role
		newRecords[0].Subrole = matched.Subrole
		newRecords[0].Element = matched.Element
		newRecords[0].Position = matched.Position
		newRecords[0].Size = matched.Size
		newRecords[0].PositionSettable = matched.PositionSettable
		newRecords[0].IdentifierEmpty = matched.IdentifierEmpty
		newRecords[0].IdentifierKnown = matched.IdentifierKnown
		newRecords[0].DescriptionEmpty = matched.DescriptionEmpty
		newRecords[0].DescriptionKnown = matched.DescriptionKnown
		newRecords[0].ChildrenKnown = matched.ChildrenKnown
		newRecords[0].ChildRoles = matched.ChildRoles
	}
	return newRecords, nil
}

type axWindowMatch struct {
	Count            int
	Role             string
	Subrole          string
	Element          uintptr
	Position         Point
	Size             Size
	PositionSettable bool
	IdentifierEmpty  bool
	IdentifierKnown  bool
	DescriptionEmpty bool
	DescriptionKnown bool
	ChildrenKnown    bool
	ChildRoles       []string
}

func (api *presentationNativeAPI) matchAXWindow(pid int32, frame Rect) (axWindowMatch, error) {
	var result axWindowMatch
	app := api.axCreateApplication(pid)
	if app == 0 {
		return result, ErrUnavailable
	}
	defer api.cfRelease(app)
	api.axSetTimeout(app, 0.15)
	windows, code := api.copyAttribute(app, "AXWindows")
	if code != 0 || windows == 0 || api.cfGetTypeID(windows) != api.cfArrayTypeID() {
		if windows != 0 {
			api.cfRelease(windows)
		}
		return result, fmt.Errorf("%w: AXWindows query failed (%d)", ErrUnavailable, code)
	}
	defer api.cfRelease(windows)
	count := api.cfArrayCount(windows)
	if count < 0 || count > presentationMaxAXWindows {
		return result, ErrUnavailable
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
		if !positionOK || !sizeOK || !sameRect(Rect{Origin: position, Size: size}, frame) {
			continue
		}
		result.Count++
		if result.Count != 1 {
			if result.Element != 0 {
				api.cfRelease(result.Element)
				result.Element = 0
			}
			continue
		}
		result.Role = role
		result.Subrole = api.stringAttribute(element, "AXSubrole")
		result.Position = position
		result.Size = size
		result.IdentifierEmpty, result.IdentifierKnown = api.stringAttributeEmpty(element, "AXIdentifier")
		result.DescriptionEmpty, result.DescriptionKnown = api.stringAttributeEmpty(element, "AXDescription")
		result.ChildRoles, result.ChildrenKnown = api.childRoles(element)
		var settable uint8
		if api.axIsSettable(element, api.attributes["AXPosition"], &settable) == 0 {
			result.PositionSettable = settable != 0
		}
		result.Element = api.cfRetain(element)
	}
	if result.Count != 1 && result.Element != 0 {
		api.cfRelease(result.Element)
		result.Element = 0
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

// stringAttributeEmpty checks only whether an AX string attribute is absent or
// has length zero. It never copies AXDescription content into Go memory.
func (api *presentationNativeAPI) stringAttributeEmpty(element uintptr, name string) (bool, bool) {
	value, code := api.copyAttribute(element, name)
	if code == -25205 || code == -25212 { // attribute unsupported or no value
		if value != 0 {
			api.cfRelease(value)
		}
		return true, true
	}
	if code != 0 || value == 0 {
		if value != 0 {
			api.cfRelease(value)
		}
		return false, false
	}
	defer api.cfRelease(value)
	if api.cfGetTypeID(value) != api.cfStringTypeID() {
		return false, false
	}
	length := api.cfStringLength(value)
	if length < 0 || length > 4096 {
		return false, false
	}
	return length == 0, true
}

func (api *presentationNativeAPI) pointAttribute(element uintptr, name string) (Point, bool) {
	value, code := api.copyAttribute(element, name)
	if code != 0 || value == 0 {
		if value != 0 {
			api.cfRelease(value)
		}
		return Point{}, false
	}
	defer api.cfRelease(value)
	if api.axValueGetType(value) != presentationAXPointType {
		return Point{}, false
	}
	var point Point
	if api.axValueGetValue(value, presentationAXPointType, unsafe.Pointer(&point)) == 0 || !finitePoint(point) {
		return Point{}, false
	}
	return point, true
}

func (api *presentationNativeAPI) sizeAttribute(element uintptr, name string) (Size, bool) {
	value, code := api.copyAttribute(element, name)
	if code != 0 || value == 0 {
		if value != 0 {
			api.cfRelease(value)
		}
		return Size{}, false
	}
	defer api.cfRelease(value)
	if api.axValueGetType(value) != presentationAXSizeType {
		return Size{}, false
	}
	var size Size
	if api.axValueGetValue(value, presentationAXSizeType, unsafe.Pointer(&size)) == 0 || !finiteSize(size) {
		return Size{}, false
	}
	return size, true
}

// childRoles distinguishes an accessible empty child list from an unavailable
// AXChildren query without reading child text values.
func (api *presentationNativeAPI) childRoles(window uintptr) ([]string, bool) {
	children, code := api.copyAttribute(window, "AXChildren")
	if children == 0 {
		return nil, false
	}
	defer api.cfRelease(children)
	if code != 0 || api.cfGetTypeID(children) != api.cfArrayTypeID() {
		return nil, false
	}
	count := api.cfArrayCount(children)
	if count < 0 || count > presentationMaxAXWindows {
		return nil, false
	}
	roles := make([]string, 0, int(count))
	for i := int64(0); i < count; i++ {
		child := api.cfArrayValue(children, i)
		if child == 0 {
			return nil, false
		}
		roles = append(roles, api.stringAttribute(child, "AXRole"))
	}
	return roles, true
}

func (probe darwinPresentationProbe) WindowByID(pid int32, id uint32) (Window, bool, error) {
	records, err := probe.api.windowRecords(presentationIncludeWindow, id)
	if err != nil {
		return Window{}, false, err
	}
	var found Window
	count := 0
	for _, record := range records {
		if record.ID == id && record.OwnerPID == pid {
			found = record
			count++
		}
	}
	if count != 1 {
		return Window{}, false, nil
	}
	return found, true, nil
}

func (probe darwinPresentationProbe) SetPosition(element uintptr, point Point) error {
	if element == 0 || !finitePoint(point) {
		return ErrUnavailable
	}
	value := probe.api.axValueCreate(presentationAXPointType, unsafe.Pointer(&point))
	if value == 0 {
		return ErrUnavailable
	}
	defer probe.api.cfRelease(value)
	if code := probe.api.axSetAttribute(element, probe.api.attributes["AXPosition"], value); code != 0 {
		return fmt.Errorf("AXUIElementSetAttributeValue returned %d", code)
	}
	return nil
}

func (probe darwinPresentationProbe) Position(element uintptr) (Point, error) {
	value, ok := probe.api.pointAttribute(element, "AXPosition")
	if !ok {
		return Point{}, ErrUnavailable
	}
	return value, nil
}

func (probe darwinPresentationProbe) ElementPID(element uintptr) (int32, error) {
	var pid int32
	if element == 0 || probe.api.axGetPID(element, &pid) != 0 || pid <= 0 {
		return 0, ErrUnavailable
	}
	return pid, nil
}

func (probe darwinPresentationProbe) Retain(element uintptr) uintptr {
	if element == 0 {
		return 0
	}
	return probe.api.cfRetain(element)
}

func (probe darwinPresentationProbe) Release(element uintptr) {
	if element != 0 {
		probe.api.cfRelease(element)
	}
}

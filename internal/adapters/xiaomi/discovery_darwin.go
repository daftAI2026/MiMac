package xiaomi

import (
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// This is the descriptor observed on the first physical 2717:32b8 device.
// Exact matching prevents applying its 16-bit report parser to other layouts.
const observedDescriptor = "05010906a10105070906a101850195037510150025fe190029fe8100c00600ff0900a101850675089578150025ff190029ff8100850775089578150025ff190029ff8100850875089578150025ff190029ff8100c0c0"

type registryAPI struct {
	matching      func(string) uintptr
	services      func(uint32, uintptr, *uint32) int32
	next          func(uint32) uint32
	releaseObject func(uint32) int32
	entryID       func(uint32, *uint64) int32
	property      func(uint32, uintptr, uintptr, uint32) uintptr
	release       func(uintptr)
	typeID        func(uintptr) uint64
	stringType    func() uint64
	numberType    func() uint64
	dataType      func() uint64
	makeString    func(uintptr, string, uint32) uintptr
	stringBytes   func(uintptr, *byte, int64, uint32) bool
	number        func(uintptr, int64, *int64) bool
	dataLength    func(uintptr) int64
	dataBytes     func(uintptr) *byte
}

var registry struct {
	once sync.Once
	api  registryAPI
	err  error
}

func registryFunctions() (*registryAPI, error) {
	registry.once.Do(func() {
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			registry.err = err
			return
		}
		io, err := purego.Dlopen("/System/Library/Frameworks/IOKit.framework/IOKit", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			registry.err = err
			return
		}
		a := &registry.api
		// Framework handles stay loaded for the lifetime of these function pointers.
		bindings := []struct {
			library     uintptr
			destination any
			symbol      string
		}{
			{io, &a.matching, "IOServiceMatching"}, {io, &a.services, "IOServiceGetMatchingServices"},
			{io, &a.next, "IOIteratorNext"}, {io, &a.releaseObject, "IOObjectRelease"},
			{io, &a.entryID, "IORegistryEntryGetRegistryEntryID"}, {io, &a.property, "IORegistryEntryCreateCFProperty"},
			{cf, &a.release, "CFRelease"}, {cf, &a.typeID, "CFGetTypeID"},
			{cf, &a.stringType, "CFStringGetTypeID"}, {cf, &a.numberType, "CFNumberGetTypeID"},
			{cf, &a.dataType, "CFDataGetTypeID"}, {cf, &a.makeString, "CFStringCreateWithCString"},
			{cf, &a.stringBytes, "CFStringGetCString"}, {cf, &a.number, "CFNumberGetValue"},
			{cf, &a.dataLength, "CFDataGetLength"}, {cf, &a.dataBytes, "CFDataGetBytePtr"},
		}
		for _, b := range bindings {
			address, err := purego.Dlsym(b.library, b.symbol)
			if err != nil {
				registry.err = err
				return
			}
			purego.RegisterFunc(b.destination, address)
		}
	})
	return &registry.api, registry.err
}

// DiscoverHID reads registry metadata without opening IOHIDManager, registering
// input callbacks, requesting Input Monitoring, or changing system mappings.
// Call only on demand: a returned device may disconnect immediately afterwards.
func DiscoverHID() ([]HIDDevice, error) {
	a, err := registryFunctions()
	if err != nil {
		return nil, fmt.Errorf("xiaomi: registry unavailable: %w", err)
	}
	match := a.matching("IOHIDDevice")
	if match == 0 {
		return nil, fmt.Errorf("xiaomi: cannot create HID registry query")
	}
	var iterator uint32
	// IOServiceGetMatchingServices consumes match even on failure.
	if status := a.services(0, match, &iterator); status != 0 {
		return nil, fmt.Errorf("xiaomi: registry query status %d", status)
	}
	defer a.releaseObject(iterator)
	var devices []HIDDevice
	for entry := a.next(iterator); entry != 0; entry = a.next(iterator) {
		device, ok := a.device(entry)
		a.releaseObject(entry)
		if ok {
			devices = append(devices, device)
		}
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].RegistryID < devices[j].RegistryID })
	return devices, nil
}

func (a *registryAPI) get(entry uint32, name string) uintptr {
	key := a.makeString(0, name, 0x08000100) // kCFStringEncodingUTF8
	if key == 0 {
		return 0
	}
	defer a.release(key)
	return a.property(entry, key, 0, 0)
}

func (a *registryAPI) integer(entry uint32, name string) (int64, bool) {
	value := a.get(entry, name)
	if value == 0 {
		return 0, false
	}
	defer a.release(value)
	var result int64
	if a.typeID(value) != a.numberType() || !a.number(value, 4, &result) {
		return 0, false
	}
	return result, true
}

func (a *registryAPI) text(entry uint32, name string) string {
	value := a.get(entry, name)
	if value == 0 {
		return ""
	}
	defer a.release(value)
	if a.typeID(value) != a.stringType() {
		return ""
	}
	var buffer [1024]byte
	if !a.stringBytes(value, &buffer[0], int64(len(buffer)), 0x08000100) {
		return ""
	}
	for n, b := range buffer {
		if b == 0 {
			return string(buffer[:n])
		}
	}
	return ""
}

func (a *registryAPI) knownLayout(entry uint32) bool {
	value := a.get(entry, "ReportDescriptor")
	if value == 0 {
		return false
	}
	defer a.release(value)
	if a.typeID(value) != a.dataType() {
		return false
	}
	length := a.dataLength(value)
	if length != int64(len(observedDescriptor)/2) {
		return false
	}
	bytes := a.dataBytes(value)
	return bytes != nil && hex.EncodeToString(unsafe.Slice(bytes, int(length))) == observedDescriptor
}

func (a *registryAPI) device(entry uint32) (HIDDevice, bool) {
	vendor, ok := a.integer(entry, "VendorID")
	if !ok || vendor != 0x2717 {
		return HIDDevice{}, false
	}
	product, ok := a.integer(entry, "ProductID")
	if !ok || product != 0x32b8 {
		return HIDDevice{}, false
	}
	transport := a.text(entry, "Transport")
	if transport != "Bluetooth Low Energy" {
		return HIDDevice{}, false
	}
	var id uint64
	if a.entryID(entry, &id) != 0 || id == 0 {
		return HIDDevice{}, false
	}
	return HIDDevice{RegistryID: id, Name: a.text(entry, "Product"), Manufacturer: a.text(entry, "Manufacturer"),
		Transport: transport, VendorID: uint16(vendor), ProductID: uint16(product), KnownReportLayout: a.knownLayout(entry)}, true
}

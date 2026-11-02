//go:build darwin

package xiaomi

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"howett.net/plist"
)

const (
	voiceMappingProperty           = "UserKeyMapping"
	voiceMappingPrimaryUsagePage   = 1
	voiceMappingPrimaryUsage       = 6
	voiceMappingCFNumberSInt64     = 4
	voiceMappingCFStringUTF8       = uint32(0x08000100)
	voiceMappingCFPropertyBinary   = int64(200) // kCFPropertyListBinaryFormat_v1_0 (CFIndex)
	voiceMappingMaxParentDepth     = 32
	voiceMappingMaxPropertyListLen = 1 << 20
	voiceMappingMainPortDefault    = uint32(0)
	voiceMappingRegistryIOService  = "IOService"
)

var (
	errVoiceMappingPortClosed        = errors.New("xiaomi HID: voice mapping port is closed")
	errVoiceMappingNativeUnavailable = errors.New("xiaomi HID: voice mapping native API is unavailable")
	errVoiceMappingServiceNotUnique  = errors.New("xiaomi HID: matching keyboard event service is not unique for the active device")
	errVoiceMappingServiceMissing    = ErrVoiceMappingServiceNotReady
	errVoiceMappingServiceChanged    = errors.New("xiaomi HID: keyboard event service no longer belongs to the active device")
	errVoiceMappingRegistryEntryGone = errors.New("xiaomi HID: registry entry ID is no longer present")
)

// voiceMappingNative is the narrow seam used by tests. open returns owned CF
// references for owner and service even when it returns an error; the caller
// releases any partial result. verify checks live registry identity and the
// parent chain before every property read or write.
type voiceMappingNative interface {
	open(rawRegistryID uint64) (owner, service uintptr, serviceRegistryID uint64, err error)
	verify(rawRegistryID, serviceRegistryID uint64) error
	serviceGone(serviceRegistryID uint64) (bool, error)
	readMappings(service uintptr) ([]voiceMappingEntry, error)
	writeMappings(service uintptr, entries []voiceMappingEntry) error
	releaseCF(object uintptr)
}

type darwinVoiceMappingPort struct {
	mu                sync.Mutex
	native            voiceMappingNative
	rawRegistryID     uint64
	serviceRegistryID uint64
	owner             uintptr
	service           uintptr
	closed            bool
}

var (
	voiceMappingNativeOnce sync.Once
	voiceMappingSystem     voiceMappingNative
	voiceMappingSystemErr  error
)

func openVoiceMappingPort(rawRegistryID uint64) (*darwinVoiceMappingPort, error) {
	voiceMappingNativeOnce.Do(func() {
		voiceMappingSystem, voiceMappingSystemErr = newDarwinVoiceMappingSystem()
	})
	if voiceMappingSystemErr != nil {
		return nil, voiceMappingSystemErr
	}
	return openVoiceMappingPortWithNative(rawRegistryID, voiceMappingSystem)
}

func openVoiceMappingPortWithNative(rawRegistryID uint64, native voiceMappingNative) (*darwinVoiceMappingPort, error) {
	if native == nil || rawRegistryID == 0 {
		return nil, errVoiceMappingNativeUnavailable
	}
	owner, service, serviceRegistryID, err := native.open(rawRegistryID)
	if err != nil {
		releaseVoiceMappingReferences(native, service, owner)
		return nil, err
	}
	if owner == 0 || service == 0 || serviceRegistryID == 0 {
		releaseVoiceMappingReferences(native, service, owner)
		return nil, errors.New("xiaomi HID: native resolver returned an incomplete service")
	}
	port := &darwinVoiceMappingPort{
		native:            native,
		rawRegistryID:     rawRegistryID,
		serviceRegistryID: serviceRegistryID,
		owner:             owner,
		service:           service,
	}
	if err := native.verify(rawRegistryID, serviceRegistryID); err != nil {
		_ = port.Close()
		return nil, fmt.Errorf("verify voice mapping service after open: %w", err)
	}
	return port, nil
}

func releaseVoiceMappingReferences(native voiceMappingNative, service, owner uintptr) {
	if service != 0 {
		native.releaseCF(service)
	}
	if owner != 0 {
		native.releaseCF(owner)
	}
}

func (p *darwinVoiceMappingPort) RegistryID() uint64 {
	if p == nil {
		return 0
	}
	return p.serviceRegistryID
}

func (p *darwinVoiceMappingPort) Read() ([]voiceMappingEntry, error) {
	if p == nil {
		return nil, errVoiceMappingPortClosed
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ready(); err != nil {
		return nil, err
	}
	if err := p.native.verify(p.rawRegistryID, p.serviceRegistryID); err != nil {
		return nil, fmt.Errorf("verify voice mapping service before read: %w", err)
	}
	entries, err := p.native.readMappings(p.service)
	if err != nil {
		return nil, fmt.Errorf("read UserKeyMapping: %w", err)
	}
	return entries, nil
}

func (p *darwinVoiceMappingPort) Write(entries []voiceMappingEntry) error {
	if p == nil {
		return errVoiceMappingPortClosed
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ready(); err != nil {
		return err
	}
	if err := p.native.verify(p.rawRegistryID, p.serviceRegistryID); err != nil {
		return fmt.Errorf("verify voice mapping service before write: %w", err)
	}
	if err := p.native.writeMappings(p.service, entries); err != nil {
		return fmt.Errorf("write UserKeyMapping: %w", err)
	}
	return nil
}

// voiceMappingServiceGone checks only the concrete event-service ID captured
// when the port opened. A false result or an error leaves recovery state intact.
// The port and its reservation cannot survive a reboot, so a positive exact-ID
// absence check is necessarily scoped to the current boot.
func (p *darwinVoiceMappingPort) voiceMappingServiceGone() (bool, error) {
	if p == nil {
		return false, errVoiceMappingPortClosed
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ready(); err != nil {
		return false, err
	}
	return p.native.serviceGone(p.serviceRegistryID)
}

func (p *darwinVoiceMappingPort) ready() error {
	if p == nil || p.native == nil || p.closed || p.owner == 0 || p.service == 0 {
		return errVoiceMappingPortClosed
	}
	return nil
}

// Close releases the retained event-system client and keyboard-service
// references. It never changes UserKeyMapping; the lease owner must release
// its mapping before closing this port.
func (p *darwinVoiceMappingPort) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	releaseVoiceMappingReferences(p.native, p.service, p.owner)
	p.service = 0
	p.owner = 0
	return nil
}

type darwinVoiceMappingSystem struct {
	api      *voiceMappingNativeAPI
	registry *registryAPI
}

func newDarwinVoiceMappingSystem() (voiceMappingNative, error) {
	api, err := voiceMappingNativeFunctions()
	if err != nil {
		return nil, err
	}
	registry, err := registryFunctions()
	if err != nil {
		return nil, err
	}
	return &darwinVoiceMappingSystem{api: api, registry: registry}, nil
}

func (s *darwinVoiceMappingSystem) open(rawRegistryID uint64) (uintptr, uintptr, uint64, error) {
	if rawRegistryID == 0 {
		return 0, 0, 0, errors.New("xiaomi HID: raw device registry ID is zero")
	}
	root, err := s.resolveEntry(rawRegistryID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("resolve raw HID device: %w", err)
	}
	if err := s.validateRawDevice(rawRegistryID, root); err != nil {
		s.api.releaseIO(root)
		return 0, 0, 0, err
	}
	s.api.releaseIO(root)

	client := s.api.eventSystemClientCreate(0)
	if client == 0 {
		return 0, 0, 0, errors.New("xiaomi HID: IOHIDEventSystemClientCreateSimpleClient failed")
	}
	services := s.api.eventSystemClientCopyServices(client)
	if services == 0 {
		return client, 0, 0, errors.New("xiaomi HID: IOHIDEventSystemClientCopyServices failed")
	}
	defer s.api.cfRelease(services)

	count := s.api.cfArrayGetCount(services)
	var selectedService uintptr
	var selectedID uint64
	selectedCount := 0
	for index := int64(0); index < count; index++ {
		service := s.api.cfArrayGetValueAtIndex(services, index)
		if service == 0 {
			continue
		}
		matches, err := s.isTargetKeyboardService(service)
		if err != nil {
			return client, 0, 0, err
		}
		if !matches {
			continue
		}
		serviceID, ok := s.serviceRegistryID(service)
		if !ok || serviceID == 0 {
			return client, 0, 0, errors.New("xiaomi HID: target keyboard service has no registry ID")
		}
		linked, err := s.eventServiceBelongsToDevice(rawRegistryID, serviceID)
		if err != nil {
			return client, 0, 0, fmt.Errorf("verify target event-service ancestry: %w", err)
		}
		if !linked {
			continue
		}
		selectedCount++
		if selectedCount == 1 {
			selectedService = service // borrowed from `services`; retained after uniqueness is known
			selectedID = serviceID
		}
	}
	if selectedCount == 0 {
		return client, 0, 0, errVoiceMappingServiceMissing
	}
	if selectedCount != 1 {
		return client, 0, 0, errVoiceMappingServiceNotUnique
	}
	retained := s.api.cfRetain(selectedService)
	if retained == 0 {
		return client, 0, 0, errors.New("xiaomi HID: CFRetain target keyboard service failed")
	}
	return client, retained, selectedID, nil
}

func (s *darwinVoiceMappingSystem) verify(rawRegistryID, serviceRegistryID uint64) error {
	if rawRegistryID == 0 || serviceRegistryID == 0 {
		return errVoiceMappingServiceChanged
	}
	root, err := s.resolveEntry(rawRegistryID)
	if err != nil {
		return errVoiceMappingServiceChanged
	}
	rootErr := s.validateRawDevice(rawRegistryID, root)
	s.api.releaseIO(root)
	if rootErr != nil {
		return rootErr
	}
	eventService, err := s.resolveEntry(serviceRegistryID)
	if err != nil {
		return errVoiceMappingServiceChanged
	}
	defer s.api.releaseIO(eventService)
	if err := s.validateEventService(serviceRegistryID, eventService); err != nil {
		return err
	}
	linked, err := s.eventServiceEntryBelongsToDevice(rawRegistryID, eventService)
	if err != nil {
		return err
	}
	if !linked {
		return errVoiceMappingServiceChanged
	}
	return nil
}

func (s *darwinVoiceMappingSystem) serviceGone(serviceRegistryID uint64) (bool, error) {
	if serviceRegistryID == 0 {
		return false, errVoiceMappingServiceChanged
	}
	entry, err := s.resolveEntry(serviceRegistryID)
	if errors.Is(err, errVoiceMappingRegistryEntryGone) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer s.api.releaseIO(entry)
	var actualID uint64
	if s.registry.entryID(entry, &actualID) != 0 || actualID != serviceRegistryID {
		return false, errVoiceMappingServiceChanged
	}
	return false, nil
}

func (s *darwinVoiceMappingSystem) readMappings(service uintptr) ([]voiceMappingEntry, error) {
	value, err := s.copyServiceProperty(service, voiceMappingProperty)
	if err != nil {
		return nil, err
	}
	if value == 0 {
		return []voiceMappingEntry{}, nil
	}
	defer s.api.cfRelease(value)
	if s.api.cfGetTypeID(value) != s.api.cfArrayGetTypeID() {
		return nil, errors.New("UserKeyMapping property is not an array")
	}
	var cfError uintptr
	data := s.api.cfPropertyListCreateData(0, value, voiceMappingCFPropertyBinary, 0, &cfError)
	if cfError != 0 {
		defer s.api.cfRelease(cfError)
	}
	if data == 0 {
		return nil, errors.New("serialize UserKeyMapping property list failed")
	}
	defer s.api.cfRelease(data)
	length := s.api.cfDataGetLength(data)
	if length <= 0 {
		return nil, errors.New("serialized UserKeyMapping property list is empty")
	}
	if length > voiceMappingMaxPropertyListLen {
		return nil, fmt.Errorf("serialized UserKeyMapping property list exceeds %d bytes", voiceMappingMaxPropertyListLen)
	}
	bytes := s.api.cfDataGetBytePtr(data)
	if bytes == nil {
		return nil, errors.New("serialized UserKeyMapping property list has no data")
	}
	encoded := append([]byte(nil), unsafe.Slice(bytes, int(length))...)
	runtime.KeepAlive(data)
	entries, err := decodeVoiceMappingEntries(encoded)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *darwinVoiceMappingSystem) writeMappings(service uintptr, entries []voiceMappingEntry) error {
	encoded, err := encodeVoiceMappingEntries(entries)
	if err != nil {
		return err
	}
	if len(encoded) == 0 {
		return errors.New("encoded UserKeyMapping property list is empty")
	}
	if len(encoded) > voiceMappingMaxPropertyListLen {
		return fmt.Errorf("encoded UserKeyMapping property list exceeds %d bytes", voiceMappingMaxPropertyListLen)
	}
	data := s.api.cfDataCreate(0, &encoded[0], int64(len(encoded)))
	runtime.KeepAlive(encoded)
	if data == 0 {
		return errors.New("create UserKeyMapping CFData failed")
	}
	defer s.api.cfRelease(data)
	var format int64
	var cfError uintptr
	value := s.api.cfPropertyListCreateWithData(0, data, 0, &format, &cfError)
	if cfError != 0 {
		defer s.api.cfRelease(cfError)
	}
	if value == 0 {
		return errors.New("decode UserKeyMapping CF property list failed")
	}
	defer s.api.cfRelease(value)
	if s.api.cfGetTypeID(value) != s.api.cfArrayGetTypeID() || format != voiceMappingCFPropertyBinary {
		return errors.New("encoded UserKeyMapping property list is not a binary array")
	}
	key, err := s.createCFString(voiceMappingProperty)
	if err != nil {
		return err
	}
	defer s.api.cfRelease(key)
	if !s.api.serviceSetProperty(service, key, value) {
		return errors.New("IOHIDServiceClientSetProperty rejected UserKeyMapping")
	}
	return nil
}

func encodeVoiceMappingEntries(entries []voiceMappingEntry) ([]byte, error) {
	if entries == nil {
		entries = []voiceMappingEntry{}
	}
	for index, entry := range entries {
		if entry == nil {
			return nil, fmt.Errorf("mapping entry %d is not a dictionary", index)
		}
	}
	data, err := plist.Marshal(entries, plist.BinaryFormat)
	if err != nil {
		return nil, fmt.Errorf("encode UserKeyMapping entries: %w", err)
	}
	return data, nil
}

func decodeVoiceMappingEntries(data []byte) ([]voiceMappingEntry, error) {
	var entries []voiceMappingEntry
	if _, err := plist.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("decode UserKeyMapping entries: %w", err)
	}
	if entries == nil {
		return nil, errors.New("UserKeyMapping property list is not an array")
	}
	for index, entry := range entries {
		if entry == nil {
			return nil, fmt.Errorf("UserKeyMapping entry %d is not a dictionary", index)
		}
	}
	return entries, nil
}

func (s *darwinVoiceMappingSystem) validateRawDevice(rawRegistryID uint64, entry uint32) error {
	var actualID uint64
	if s.registry.entryID(entry, &actualID) != 0 || actualID != rawRegistryID {
		return errVoiceMappingServiceChanged
	}
	vendor, vendorOK := s.registry.integer(entry, "VendorID")
	product, productOK := s.registry.integer(entry, "ProductID")
	usagePage, usagePageOK := s.registry.integer(entry, "PrimaryUsagePage")
	usage, usageOK := s.registry.integer(entry, "PrimaryUsage")
	if !vendorOK || vendor != 0x2717 || !productOK || product != 0x32b8 ||
		!usagePageOK || usagePage != voiceMappingPrimaryUsagePage || !usageOK || usage != voiceMappingPrimaryUsage ||
		s.registry.text(entry, "Transport") != "Bluetooth Low Energy" || !s.registry.knownLayout(entry) {
		return errVoiceMappingServiceChanged
	}
	return nil
}

func (s *darwinVoiceMappingSystem) validateEventService(serviceRegistryID uint64, entry uint32) error {
	var actualID uint64
	if s.registry.entryID(entry, &actualID) != 0 || actualID != serviceRegistryID {
		return errVoiceMappingServiceChanged
	}
	vendor, vendorOK := s.registry.integer(entry, "VendorID")
	product, productOK := s.registry.integer(entry, "ProductID")
	usagePage, usagePageOK := s.registry.integer(entry, "PrimaryUsagePage")
	usage, usageOK := s.registry.integer(entry, "PrimaryUsage")
	if !vendorOK || vendor != 0x2717 || !productOK || product != 0x32b8 ||
		!usagePageOK || usagePage != voiceMappingPrimaryUsagePage || !usageOK || usage != voiceMappingPrimaryUsage ||
		s.registry.text(entry, "Transport") != "Bluetooth Low Energy" {
		return errVoiceMappingServiceChanged
	}
	return nil
}

func (s *darwinVoiceMappingSystem) isTargetKeyboardService(service uintptr) (bool, error) {
	vendor, vendorOK, err := s.serviceInteger(service, "VendorID")
	if err != nil {
		return false, err
	}
	product, productOK, err := s.serviceInteger(service, "ProductID")
	if err != nil {
		return false, err
	}
	usagePage, usagePageOK, err := s.serviceInteger(service, "PrimaryUsagePage")
	if err != nil {
		return false, err
	}
	usage, usageOK, err := s.serviceInteger(service, "PrimaryUsage")
	if err != nil {
		return false, err
	}
	transport, transportOK, err := s.serviceString(service, "Transport")
	if err != nil {
		return false, err
	}
	return vendorOK && vendor == 0x2717 && productOK && product == 0x32b8 &&
		usagePageOK && usagePage == voiceMappingPrimaryUsagePage && usageOK && usage == voiceMappingPrimaryUsage &&
		transportOK && transport == "Bluetooth Low Energy", nil
}

func (s *darwinVoiceMappingSystem) serviceRegistryID(service uintptr) (uint64, bool) {
	value := s.api.serviceGetRegistryID(service) // Get API: borrowed CFNumber owned by the service client.
	if value == 0 || s.api.cfGetTypeID(value) != s.api.cfNumberGetTypeID() {
		return 0, false
	}
	var id int64
	if !s.api.cfNumberGetValue(value, voiceMappingCFNumberSInt64, &id) || id <= 0 {
		return 0, false
	}
	return uint64(id), true
}

func (s *darwinVoiceMappingSystem) serviceInteger(service uintptr, name string) (int64, bool, error) {
	value, err := s.copyServiceProperty(service, name)
	if err != nil || value == 0 {
		return 0, false, err
	}
	defer s.api.cfRelease(value)
	if s.api.cfGetTypeID(value) != s.api.cfNumberGetTypeID() {
		return 0, false, nil
	}
	var result int64
	if !s.api.cfNumberGetValue(value, voiceMappingCFNumberSInt64, &result) {
		return 0, false, nil
	}
	return result, true, nil
}

func (s *darwinVoiceMappingSystem) serviceString(service uintptr, name string) (string, bool, error) {
	value, err := s.copyServiceProperty(service, name)
	if err != nil || value == 0 {
		return "", false, err
	}
	defer s.api.cfRelease(value)
	if s.api.cfGetTypeID(value) != s.api.cfStringGetTypeID() {
		return "", false, nil
	}
	var buffer [1024]byte
	if !s.api.cfStringGetCString(value, &buffer[0], int64(len(buffer)), voiceMappingCFStringUTF8) {
		return "", false, nil
	}
	for index, b := range buffer {
		if b == 0 {
			return string(buffer[:index]), true, nil
		}
	}
	return "", false, nil
}

func (s *darwinVoiceMappingSystem) copyServiceProperty(service uintptr, name string) (uintptr, error) {
	key, err := s.createCFString(name)
	if err != nil {
		return 0, err
	}
	defer s.api.cfRelease(key)
	return s.api.serviceCopyProperty(service, key), nil
}

func (s *darwinVoiceMappingSystem) createCFString(value string) (uintptr, error) {
	encoded := append([]byte(value), 0)
	result := s.api.cfStringCreateWithCString(0, &encoded[0], voiceMappingCFStringUTF8)
	runtime.KeepAlive(encoded)
	if result == 0 {
		return 0, fmt.Errorf("create CoreFoundation key %q failed", value)
	}
	return result, nil
}

func (s *darwinVoiceMappingSystem) eventServiceBelongsToDevice(rawRegistryID, serviceRegistryID uint64) (bool, error) {
	entry, err := s.resolveEntry(serviceRegistryID)
	if err != nil {
		return false, err
	}
	defer s.api.releaseIO(entry)
	if err := s.validateEventService(serviceRegistryID, entry); err != nil {
		return false, err
	}
	return s.eventServiceEntryBelongsToDevice(rawRegistryID, entry)
}

func (s *darwinVoiceMappingSystem) eventServiceEntryBelongsToDevice(rawRegistryID uint64, eventEntry uint32) (bool, error) {
	current := eventEntry
	currentOwned := false
	for depth := 0; depth <= voiceMappingMaxParentDepth; depth++ {
		var entryID uint64
		if s.registry.entryID(current, &entryID) != 0 || entryID == 0 {
			if currentOwned {
				s.api.releaseIO(current)
			}
			return false, errors.New("read IORegistry parent-chain entry ID failed")
		}
		if entryID == rawRegistryID {
			if currentOwned {
				s.api.releaseIO(current)
			}
			return true, nil
		}
		if depth == voiceMappingMaxParentDepth {
			if currentOwned {
				s.api.releaseIO(current)
			}
			return false, fmt.Errorf("IORegistry parent chain exceeds %d ancestors", voiceMappingMaxParentDepth)
		}
		var parent uint32
		status := s.api.registryGetParentEntry(current, &s.api.ioServicePlane[0], &parent)
		if currentOwned {
			s.api.releaseIO(current)
		}
		if status != 0 || parent == 0 {
			if parent != 0 {
				s.api.releaseIO(parent)
			}
			return false, nil
		}
		current = parent
		currentOwned = true
	}
	return false, errors.New("IORegistry parent chain traversal ended unexpectedly")
}

func (s *darwinVoiceMappingSystem) resolveEntry(registryID uint64) (uint32, error) {
	matching := s.api.registryEntryIDMatching(registryID)
	if matching == 0 {
		return 0, errors.New("IORegistryEntryIDMatching failed")
	}
	entry := s.api.serviceGetMatchingService(voiceMappingMainPortDefault, matching) // consumes matching
	if entry == 0 {
		return 0, fmt.Errorf("IOServiceGetMatchingService: %w", errVoiceMappingRegistryEntryGone)
	}
	return entry, nil
}

type voiceMappingNativeAPI struct {
	eventSystemClientCreate       func(uintptr) uintptr
	eventSystemClientCopyServices func(uintptr) uintptr
	serviceGetRegistryID          func(uintptr) uintptr
	serviceCopyProperty           func(uintptr, uintptr) uintptr
	serviceSetProperty            func(uintptr, uintptr, uintptr) bool
	cfArrayGetCount               func(uintptr) int64
	cfArrayGetValueAtIndex        func(uintptr, int64) uintptr
	cfRetain                      func(uintptr) uintptr
	cfRelease                     func(uintptr)
	cfGetTypeID                   func(uintptr) uint64
	cfArrayGetTypeID              func() uint64
	cfNumberGetTypeID             func() uint64
	cfStringGetTypeID             func() uint64
	cfNumberGetValue              func(uintptr, int64, *int64) bool
	cfStringCreateWithCString     func(uintptr, *byte, uint32) uintptr
	cfStringGetCString            func(uintptr, *byte, int64, uint32) bool
	cfPropertyListCreateData      func(uintptr, uintptr, int64, uint64, *uintptr) uintptr
	cfPropertyListCreateWithData  func(uintptr, uintptr, uint64, *int64, *uintptr) uintptr
	cfDataCreate                  func(uintptr, *byte, int64) uintptr
	cfDataGetLength               func(uintptr) int64
	cfDataGetBytePtr              func(uintptr) *byte
	registryEntryIDMatching       func(uint64) uintptr
	serviceGetMatchingService     func(uint32, uintptr) uint32
	registryGetParentEntry        func(uint32, *byte, *uint32) int32
	releaseIO                     func(uint32) int32
	registry                      *registryAPI
	ioServicePlane                []byte
	ioLibrary                     uintptr
	cfLibrary                     uintptr
}

var (
	voiceMappingAPIOOnce sync.Once
	voiceMappingAPIValue *voiceMappingNativeAPI
	voiceMappingAPIError error
)

func voiceMappingNativeFunctions() (*voiceMappingNativeAPI, error) {
	voiceMappingAPIOOnce.Do(func() {
		io, err := purego.Dlopen("/System/Library/Frameworks/IOKit.framework/IOKit", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			voiceMappingAPIError = fmt.Errorf("xiaomi HID: load IOKit for voice mapping: %w", err)
			return
		}
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			voiceMappingAPIError = fmt.Errorf("xiaomi HID: load CoreFoundation for voice mapping: %w", err)
			return
		}
		api := &voiceMappingNativeAPI{ioLibrary: io, cfLibrary: cf, ioServicePlane: []byte(voiceMappingRegistryIOService + "\x00")}
		bindings := []struct {
			library     uintptr
			destination any
			symbol      string
		}{
			{io, &api.eventSystemClientCreate, "IOHIDEventSystemClientCreateSimpleClient"},
			{io, &api.eventSystemClientCopyServices, "IOHIDEventSystemClientCopyServices"},
			{io, &api.serviceGetRegistryID, "IOHIDServiceClientGetRegistryID"},
			{io, &api.serviceCopyProperty, "IOHIDServiceClientCopyProperty"},
			{io, &api.serviceSetProperty, "IOHIDServiceClientSetProperty"},
			{io, &api.registryEntryIDMatching, "IORegistryEntryIDMatching"},
			{io, &api.serviceGetMatchingService, "IOServiceGetMatchingService"},
			{io, &api.registryGetParentEntry, "IORegistryEntryGetParentEntry"},
			{io, &api.releaseIO, "IOObjectRelease"},
			{cf, &api.cfArrayGetCount, "CFArrayGetCount"},
			{cf, &api.cfArrayGetValueAtIndex, "CFArrayGetValueAtIndex"},
			{cf, &api.cfRetain, "CFRetain"},
			{cf, &api.cfRelease, "CFRelease"},
			{cf, &api.cfGetTypeID, "CFGetTypeID"},
			{cf, &api.cfArrayGetTypeID, "CFArrayGetTypeID"},
			{cf, &api.cfNumberGetTypeID, "CFNumberGetTypeID"},
			{cf, &api.cfStringGetTypeID, "CFStringGetTypeID"},
			{cf, &api.cfNumberGetValue, "CFNumberGetValue"},
			{cf, &api.cfStringCreateWithCString, "CFStringCreateWithCString"},
			{cf, &api.cfStringGetCString, "CFStringGetCString"},
			{cf, &api.cfPropertyListCreateData, "CFPropertyListCreateData"},
			{cf, &api.cfPropertyListCreateWithData, "CFPropertyListCreateWithData"},
			{cf, &api.cfDataCreate, "CFDataCreate"},
			{cf, &api.cfDataGetLength, "CFDataGetLength"},
			{cf, &api.cfDataGetBytePtr, "CFDataGetBytePtr"},
		}
		for _, binding := range bindings {
			address, err := purego.Dlsym(binding.library, binding.symbol)
			if err != nil {
				voiceMappingAPIError = fmt.Errorf("xiaomi HID: resolve %s: %w", binding.symbol, err)
				return
			}
			purego.RegisterFunc(binding.destination, address)
		}
		registry, err := registryFunctions()
		if err != nil {
			voiceMappingAPIError = err
			return
		}
		api.registry = registry
		voiceMappingAPIValue = api
	})
	if voiceMappingAPIValue == nil && voiceMappingAPIError == nil {
		voiceMappingAPIError = errVoiceMappingNativeUnavailable
	}
	return voiceMappingAPIValue, voiceMappingAPIError
}

func (s *darwinVoiceMappingSystem) releaseCF(object uintptr) {
	if object != 0 {
		s.api.cfRelease(object)
	}
}

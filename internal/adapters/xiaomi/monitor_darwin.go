//go:build darwin

package xiaomi

import (
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"mimac.local/mimac/internal/bridge"
)

const (
	listenEventRequestType int32  = 1 // kIOHIDRequestTypeListenEvent
	accessGranted          int32  = 0 // kIOHIDAccessTypeGranted
	accessDenied           int32  = 1 // kIOHIDAccessTypeDenied
	accessUnknown          int32  = 2 // kIOHIDAccessTypeUnknown
	inputReportType        int32  = 0 // kIOHIDReportTypeInput
	cfStringEncodingUTF8   uint32 = 0x08000100
	cfNumberSInt32Type     int64  = 3 // kCFNumberSInt32Type
	knownReportBufferSize         = opaquePayloadLen + 1
)

type monitorAPI struct {
	checkAccess               func(int32) int32
	requestAccess             func(int32) bool
	managerCreate             func(uintptr, uint32) uintptr
	managerSetDeviceMatching  func(uintptr, uintptr)
	managerRegisterMatching   func(uintptr, uintptr, uintptr)
	managerRegisterRemoval    func(uintptr, uintptr, uintptr)
	managerSchedule           func(uintptr, uintptr, uintptr)
	managerUnschedule         func(uintptr, uintptr, uintptr)
	managerOpen               func(uintptr, uint32) int32
	managerClose              func(uintptr, uint32) int32
	managerCopyDevices        func(uintptr) uintptr
	deviceGetService          func(uintptr) uint32
	deviceRegisterInputReport func(uintptr, uintptr, int64, uintptr, uintptr)
	createMutableDictionary   func(uintptr, int64, uintptr, uintptr) uintptr
	dictionarySetValue        func(uintptr, uintptr, uintptr)
	createNumber              func(uintptr, int64, *int32) uintptr
	createData                func(uintptr, *byte, int64) uintptr
	setGetCount               func(uintptr) int64
	setGetValues              func(uintptr, uintptr)
	retain                    func(uintptr) uintptr
	release                   func(uintptr)
	runLoopGetMain            func() uintptr
	malloc                    func(uintptr) uintptr
	free                      func(uintptr)
	copyFromNative            func(*uintptr, uintptr, uintptr) uintptr
	copyBytesFromNative       func(*byte, uintptr, uintptr) uintptr
	registry                  *registryAPI
	commonModes               uintptr
	keyCallbacks              uintptr
	valueCallbacks            uintptr
}

var nativeMonitor struct {
	once sync.Once
	api  *monitorAPI
	err  error
}

func monitorFunctions() (*monitorAPI, error) {
	nativeMonitor.once.Do(func() {
		registry, err := registryFunctions()
		if err != nil {
			nativeMonitor.err = err
			return
		}
		io, err := purego.Dlopen("/System/Library/Frameworks/IOKit.framework/IOKit", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			nativeMonitor.err = err
			return
		}
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			nativeMonitor.err = err
			return
		}
		libSystem, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			nativeMonitor.err = err
			return
		}

		a := &monitorAPI{registry: registry}
		bindings := []struct {
			library     uintptr
			destination any
			symbol      string
		}{
			{io, &a.checkAccess, "IOHIDCheckAccess"},
			{io, &a.requestAccess, "IOHIDRequestAccess"},
			{io, &a.managerCreate, "IOHIDManagerCreate"},
			{io, &a.managerSetDeviceMatching, "IOHIDManagerSetDeviceMatching"},
			{io, &a.managerRegisterMatching, "IOHIDManagerRegisterDeviceMatchingCallback"},
			{io, &a.managerRegisterRemoval, "IOHIDManagerRegisterDeviceRemovalCallback"},
			{io, &a.managerSchedule, "IOHIDManagerScheduleWithRunLoop"},
			{io, &a.managerUnschedule, "IOHIDManagerUnscheduleFromRunLoop"},
			{io, &a.managerOpen, "IOHIDManagerOpen"},
			{io, &a.managerClose, "IOHIDManagerClose"},
			{io, &a.managerCopyDevices, "IOHIDManagerCopyDevices"},
			{io, &a.deviceGetService, "IOHIDDeviceGetService"},
			{io, &a.deviceRegisterInputReport, "IOHIDDeviceRegisterInputReportCallback"},
			{cf, &a.createMutableDictionary, "CFDictionaryCreateMutable"},
			{cf, &a.dictionarySetValue, "CFDictionarySetValue"},
			{cf, &a.createNumber, "CFNumberCreate"},
			{cf, &a.createData, "CFDataCreate"},
			{cf, &a.setGetCount, "CFSetGetCount"},
			{cf, &a.setGetValues, "CFSetGetValues"},
			{cf, &a.retain, "CFRetain"},
			{cf, &a.release, "CFRelease"},
			{cf, &a.runLoopGetMain, "CFRunLoopGetMain"},
			{libSystem, &a.malloc, "malloc"},
			{libSystem, &a.free, "free"},
			{libSystem, &a.copyFromNative, "memcpy"},
			{libSystem, &a.copyBytesFromNative, "memcpy"},
		}
		for _, binding := range bindings {
			address, err := purego.Dlsym(binding.library, binding.symbol)
			if err != nil {
				nativeMonitor.err = fmt.Errorf("xiaomi HID: resolve %s: %w", binding.symbol, err)
				return
			}
			purego.RegisterFunc(binding.destination, address)
		}

		for symbol, destination := range map[string]*uintptr{
			"kCFTypeDictionaryKeyCallBacks":   &a.keyCallbacks,
			"kCFTypeDictionaryValueCallBacks": &a.valueCallbacks,
		} {
			address, err := purego.Dlsym(cf, symbol)
			if err != nil {
				nativeMonitor.err = fmt.Errorf("xiaomi HID: resolve %s: %w", symbol, err)
				return
			}
			*destination = address
		}

		commonModesSymbol, err := purego.Dlsym(cf, "kCFRunLoopCommonModes")
		if err != nil {
			nativeMonitor.err = fmt.Errorf("xiaomi HID: resolve kCFRunLoopCommonModes: %w", err)
			return
		}
		// Dlsym returns the address of the exported CFStringRef variable.
		// The pointed-to string is owned by CoreFoundation and is not released.
		a.copyFromNative(&a.commonModes, commonModesSymbol, unsafe.Sizeof(a.commonModes))
		if a.commonModes == 0 || a.keyCallbacks == 0 || a.valueCallbacks == 0 {
			nativeMonitor.err = errors.New("xiaomi HID: CoreFoundation exported constants are unavailable")
			return
		}
		nativeMonitor.api = a
	})
	return nativeMonitor.api, nativeMonitor.err
}

var callbackContexts = struct {
	sync.Mutex
	next    uintptr
	entries map[uintptr]callbackTarget
}{next: 1, entries: make(map[uintptr]callbackTarget)}

type callbackRole uint8

const (
	callbackRoleManager callbackRole = iota + 1
	callbackRoleReport
)

type callbackTarget struct {
	monitor    *darwinMonitor
	role       callbackRole
	device     uintptr
	registryID uint64
}

type nativeContext struct {
	id uintptr
}

func newNativeContext(target callbackTarget) (nativeContext, error) {
	callbackContexts.Lock()
	defer callbackContexts.Unlock()
	if callbackContexts.next == 0 {
		return nativeContext{}, errors.New("xiaomi HID: native callback token exhausted")
	}
	id := callbackContexts.next
	if id == ^uintptr(0) {
		callbackContexts.next = 0
	} else {
		callbackContexts.next++
	}
	callbackContexts.entries[id] = target
	return nativeContext{id: id}, nil
}

func lookupNativeContext(pointer uintptr) (callbackTarget, bool) {
	if pointer == 0 {
		return callbackTarget{}, false
	}
	callbackContexts.Lock()
	target, ok := callbackContexts.entries[pointer]
	callbackContexts.Unlock()
	return target, ok
}

func dropNativeContext(context *nativeContext) {
	if context == nil || context.id == 0 {
		return
	}
	callbackContexts.Lock()
	delete(callbackContexts.entries, context.id)
	callbackContexts.Unlock()
	*context = nativeContext{}
}

var (
	nativeMatchingCallback = purego.NewCallback(func(context uintptr, result int32, _ uintptr, device uintptr) {
		target, ok := lookupNativeContext(context)
		if !ok || target.role != callbackRoleManager || target.monitor == nil {
			return
		}
		target.monitor.deviceMatched(result, device)
	})
	nativeRemovalCallback = purego.NewCallback(func(context uintptr, result int32, _ uintptr, device uintptr) {
		target, ok := lookupNativeContext(context)
		if !ok || target.role != callbackRoleManager || target.monitor == nil {
			return
		}
		target.monitor.deviceRemoved(result, device)
	})
	nativeInputReportCallback = purego.NewCallback(handleNativeInputReport)
)

func handleNativeInputReport(context uintptr, result int32, sender uintptr, reportType int32, reportID uint32, report uintptr, reportLength int64) {
	target, ok := lookupNativeContext(context)
	if !ok || target.role != callbackRoleReport || target.monitor == nil {
		return
	}
	if target.monitor.stats != nil {
		target.monitor.stats.nativeReports.Add(1)
	}
	target.monitor.inputReport(target, result, sender, reportType, reportID, report, reportLength)
}

var monitorSlot struct {
	sync.Mutex
	claimed         bool
	restartRequired bool
}

// A failed IOHIDManagerClose has no safe path to release native callback
// buffers and retained CF/IOKit objects. Keep the graph reachable until process
// exit and leave monitorSlot claimed so another manager cannot be started.
var failedCloseMonitors struct {
	sync.Mutex
	values []*darwinMonitor
}

var listenAccessRequest struct {
	sync.Mutex
	attempted bool
}

var connectionGeneration struct {
	sync.Mutex
	value uint64
}

func claimMonitorSlot() error {
	monitorSlot.Lock()
	defer monitorSlot.Unlock()
	if monitorSlot.restartRequired {
		return ErrMonitorRestartRequired
	}
	if monitorSlot.claimed {
		return ErrMonitorAlreadyRunning
	}
	monitorSlot.claimed = true
	return nil
}

func releaseMonitorSlot() {
	monitorSlot.Lock()
	if !monitorSlot.restartRequired {
		monitorSlot.claimed = false
	}
	monitorSlot.Unlock()
}

func closeMonitorAndReleaseSlot(impl *darwinMonitor) error {
	err := impl.close()
	if err == nil {
		releaseMonitorSlot()
	}
	return err
}

func retainFailedClose(impl *darwinMonitor) {
	failedCloseMonitors.Lock()
	failedCloseMonitors.values = append(failedCloseMonitors.values, impl)
	failedCloseMonitors.Unlock()
	monitorSlot.Lock()
	monitorSlot.claimed = true
	monitorSlot.restartRequired = true
	monitorSlot.Unlock()
}

func nextConnectionGeneration() (uint64, error) {
	connectionGeneration.Lock()
	defer connectionGeneration.Unlock()
	if connectionGeneration.value == ^uint64(0) {
		return 0, errors.New("xiaomi HID: connection generation exhausted")
	}
	connectionGeneration.value++
	return connectionGeneration.value, nil
}

func ensureListenAccess(api *monitorAPI) error {
	return ensureListenAccessWithRequest(api, true)
}

func ensureListenAccessWithRequest(api *monitorAPI, requestPermission bool) error {
	access := api.checkAccess(listenEventRequestType)
	if access == accessGranted {
		return nil
	}
	if access == accessDenied {
		return fmt.Errorf("%w: access is denied", ErrInputMonitoringRequired)
	}
	if access != accessUnknown {
		return fmt.Errorf("%w: unrecognized access result %d", ErrInputMonitoringRequired, access)
	}
	if !requestPermission {
		return fmt.Errorf("%w: access is unknown", ErrInputMonitoringRequired)
	}

	listenAccessRequest.Lock()
	if listenAccessRequest.attempted {
		listenAccessRequest.Unlock()
		return fmt.Errorf("%w: a permission request was already made; grant access in System Settings", ErrInputMonitoringRequired)
	}
	listenAccessRequest.attempted = true
	listenAccessRequest.Unlock()

	requested := api.requestAccess(listenEventRequestType)
	after := api.checkAccess(listenEventRequestType)
	if after == accessGranted {
		return nil
	}
	return fmt.Errorf("%w: access is not granted (request result %t, check result %d)", ErrInputMonitoringRequired, requested, after)
}

// StartMonitor begins an explicit, nonexclusive IOHIDManager subscription for
// the one observed BLE descriptor profile. Call on the main thread; callbacks
// are delivered on CFRunLoopGetMain and invoke onEvent synchronously inside
// the native callback. Do not call StartMonitor or Monitor.Close from onEvent;
// schedule such work after that callback returns. This does not create a
// microphone, map button meanings, or synthesize keyboard input.
func StartMonitor(onEvent func(MonitorEvent)) (*Monitor, error) {
	return startMonitor(onEvent, true)
}

// StartMonitorWithoutPrompt begins the same HID subscription without ever
// asking macOS to display an Input Monitoring permission prompt. It succeeds
// only when access has already been granted.
func StartMonitorWithoutPrompt(onEvent func(MonitorEvent)) (*Monitor, error) {
	return startMonitor(onEvent, false)
}

func startMonitor(onEvent func(MonitorEvent), requestPermission bool) (*Monitor, error) {
	if onEvent == nil {
		return nil, errors.New("xiaomi HID: event callback is required")
	}
	if err := claimMonitorSlot(); err != nil {
		return nil, err
	}
	keepSlot := false
	defer func() {
		if !keepSlot {
			releaseMonitorSlot()
		}
	}()

	api, err := monitorFunctions()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMonitorUnavailable, err)
	}
	if err := ensureListenAccessWithRequest(api, requestPermission); err != nil {
		return nil, err
	}

	impl := &darwinMonitor{
		api:          api,
		onEvent:      onEvent,
		stats:        &monitorStats{},
		candidates:   make(map[uint64]*monitorCandidate),
		byDeviceRef:  make(map[uintptr]uint64),
		unidentified: make(map[uintptr]*monitorCandidate),
	}
	monitor := &Monitor{stats: impl.stats}
	monitor.reserveVoiceFn = func(path string) (*VoiceKeyReservation, error) {
		if impl.closed || impl.active == nil {
			return nil, ErrMonitorUnavailable
		}
		return reserveVoiceKey(impl.active.candidate.info.RegistryID, path)
	}
	monitor.closeFn = func() error {
		return closeMonitorAndReleaseSlot(impl)
	}
	if err := impl.start(); err != nil {
		if closeErr := monitor.Close(); closeErr != nil {
			if errors.Is(closeErr, ErrMonitorRestartRequired) {
				keepSlot = true
			}
			err = errors.Join(err, closeErr)
		}
		return nil, err
	}
	keepSlot = true
	return monitor, nil
}

type monitorCandidate struct {
	device    uintptr
	info      HIDDevice
	supported bool
	reason    string
}

type activeMonitorDevice struct {
	candidate    *monitorCandidate
	device       bridge.Device
	normalizer   *Normalizer
	context      nativeContext
	reportBuffer uintptr
	held         map[bridge.PressToken]struct{}
}

type darwinMonitor struct {
	api               *monitorAPI
	onEvent           func(MonitorEvent)
	stats             *monitorStats
	manager           uintptr
	runLoop           uintptr
	matching          uintptr
	managerCtx        nativeContext
	scheduled         bool
	openAttempted     bool
	closed            bool
	candidates        map[uint64]*monitorCandidate
	byDeviceRef       map[uintptr]uint64
	unidentified      map[uintptr]*monitorCandidate
	suppressReconcile bool
	active            *activeMonitorDevice
	retiredBuffers    []uintptr
	lastState         string
}

func (d *darwinMonitor) start() error {
	d.manager = d.api.managerCreate(0, 0)
	if d.manager == 0 {
		return errors.New("xiaomi HID: IOHIDManagerCreate returned null")
	}
	d.runLoop = d.api.runLoopGetMain()
	if d.runLoop == 0 {
		return errors.New("xiaomi HID: CFRunLoopGetMain returned null")
	}
	matching, err := d.makeMatchingDictionary()
	if err != nil {
		return err
	}
	d.matching = matching
	d.api.managerSetDeviceMatching(d.manager, d.matching)

	context, err := newNativeContext(callbackTarget{monitor: d, role: callbackRoleManager})
	if err != nil {
		return err
	}
	d.managerCtx = context
	d.suppressReconcile = true
	d.api.managerRegisterMatching(d.manager, nativeMatchingCallback, d.managerCtx.id)
	d.api.managerRegisterRemoval(d.manager, nativeRemovalCallback, d.managerCtx.id)
	d.api.managerSchedule(d.manager, d.runLoop, d.api.commonModes)
	d.scheduled = true
	status := d.openManager()
	if status != 0 {
		if access := d.api.checkAccess(listenEventRequestType); access != accessGranted {
			return fmt.Errorf("%w: manager open returned IOReturn 0x%08x and access check is %d", ErrInputMonitoringRequired, uint32(status), access)
		}
		return fmt.Errorf("xiaomi HID: IOHIDManagerOpen(options=0) failed with IOReturn 0x%08x", uint32(status))
	}
	if err := d.enumerateCurrentDevices(); err != nil {
		return err
	}
	d.suppressReconcile = false
	d.reconcile()
	return nil
}

func (d *darwinMonitor) openManager() int32 {
	// IOHIDManagerOpen can partially open current devices before returning an
	// error, so cleanup must attempt IOHIDManagerClose after every call.
	d.openAttempted = true
	return d.api.managerOpen(d.manager, 0)
}

func (d *darwinMonitor) enumerateCurrentDevices() error {
	devices := d.api.managerCopyDevices(d.manager)
	if devices == 0 {
		return nil
	}
	defer d.api.release(devices)
	count := d.api.setGetCount(devices)
	if count < 0 || count > 4096 {
		return fmt.Errorf("xiaomi HID: unexpected current device count %d", count)
	}
	if count == 0 {
		return nil
	}
	wordSize := unsafe.Sizeof(uintptr(0))
	values := d.api.malloc(uintptr(count) * wordSize)
	if values == 0 {
		return errors.New("xiaomi HID: allocate current device list failed")
	}
	defer d.api.free(values)
	d.api.setGetValues(devices, values)
	for i := int64(0); i < count; i++ {
		var device uintptr
		offset := uintptr(i) * wordSize
		d.api.copyFromNative(&device, values+offset, wordSize)
		runtime.KeepAlive(&device)
		if device != 0 {
			d.deviceMatched(0, device)
		}
	}
	return nil
}

func (d *darwinMonitor) makeMatchingDictionary() (uintptr, error) {
	dictionary := d.api.createMutableDictionary(0, 4, d.api.keyCallbacks, d.api.valueCallbacks)
	if dictionary == 0 {
		return 0, errors.New("xiaomi HID: CFDictionaryCreateMutable returned null")
	}
	cleanup := func(key, value uintptr) {
		if key != 0 {
			d.api.release(key)
		}
		if value != 0 {
			d.api.release(value)
		}
	}
	setString := func(name, value string) error {
		key := d.api.registry.makeString(0, name, cfStringEncodingUTF8)
		if key == 0 {
			return fmt.Errorf("xiaomi HID: create matching key %q failed", name)
		}
		item := d.api.registry.makeString(0, value, cfStringEncodingUTF8)
		if item == 0 {
			cleanup(key, 0)
			return fmt.Errorf("xiaomi HID: create matching value for %q failed", name)
		}
		d.api.dictionarySetValue(dictionary, key, item)
		cleanup(key, item) // the CFType callbacks retain both in the dictionary
		return nil
	}
	setNumber := func(name string, value int32) error {
		key := d.api.registry.makeString(0, name, cfStringEncodingUTF8)
		if key == 0 {
			return fmt.Errorf("xiaomi HID: create matching key %q failed", name)
		}
		item := d.api.createNumber(0, cfNumberSInt32Type, &value)
		runtime.KeepAlive(value)
		if item == 0 {
			cleanup(key, 0)
			return fmt.Errorf("xiaomi HID: create matching number for %q failed", name)
		}
		d.api.dictionarySetValue(dictionary, key, item)
		cleanup(key, item)
		return nil
	}

	if err := setNumber("VendorID", 0x2717); err != nil {
		d.api.release(dictionary)
		return 0, err
	}
	if err := setNumber("ProductID", 0x32b8); err != nil {
		d.api.release(dictionary)
		return 0, err
	}
	if err := setString("Transport", "Bluetooth Low Energy"); err != nil {
		d.api.release(dictionary)
		return 0, err
	}
	descriptor, err := hex.DecodeString(observedDescriptor)
	if err != nil {
		d.api.release(dictionary)
		return 0, fmt.Errorf("xiaomi HID: invalid observed descriptor: %w", err)
	}
	if len(descriptor) == 0 {
		d.api.release(dictionary)
		return 0, errors.New("xiaomi HID: observed descriptor is empty")
	}
	descriptorKey := d.api.registry.makeString(0, "ReportDescriptor", cfStringEncodingUTF8)
	if descriptorKey == 0 {
		d.api.release(dictionary)
		return 0, errors.New("xiaomi HID: create ReportDescriptor matching key failed")
	}
	descriptorData := d.api.createData(0, &descriptor[0], int64(len(descriptor)))
	runtime.KeepAlive(descriptor)
	if descriptorData == 0 {
		d.api.release(descriptorKey)
		d.api.release(dictionary)
		return 0, errors.New("xiaomi HID: CFDataCreate for ReportDescriptor failed")
	}
	d.api.dictionarySetValue(dictionary, descriptorKey, descriptorData)
	d.api.release(descriptorKey)
	d.api.release(descriptorData)
	return dictionary, nil
}

func (d *darwinMonitor) inspectDevice(device uintptr) (*monitorCandidate, bool) {
	candidate := &monitorCandidate{device: device}
	service := d.api.deviceGetService(device)
	if service == 0 {
		candidate.reason = "未能取得设备的 I/O Registry 服务，已拒绝接入"
		return candidate, true
	}
	registry := d.api.registry
	vendor, vendorOK := registry.integer(service, "VendorID")
	product, productOK := registry.integer(service, "ProductID")
	if vendorOK && vendor != 0x2717 || productOK && product != 0x32b8 {
		return nil, false
	}
	var registryID uint64
	if registry.entryID(service, &registryID) == 0 {
		candidate.info.RegistryID = registryID
	}
	if vendorOK {
		candidate.info.VendorID = uint16(vendor)
	}
	if productOK {
		candidate.info.ProductID = uint16(product)
	}
	candidate.info.Transport = registry.text(service, "Transport")
	candidate.info.Name = registry.text(service, "Product")
	candidate.info.Manufacturer = registry.text(service, "Manufacturer")
	if !vendorOK || !productOK || vendor != 0x2717 || product != 0x32b8 {
		candidate.reason = "未能通过 I/O Registry 复核目标 VID/PID，已拒绝接入"
	} else if candidate.info.RegistryID == 0 {
		candidate.reason = "未能取得设备 registry ID，已拒绝接入"
	} else if candidate.info.Transport != "Bluetooth Low Energy" {
		candidate.reason = "只支持蓝牙低功耗传输的已实测 profile"
	} else if !registry.knownLayout(service) {
		candidate.reason = "HID descriptor 不匹配已实测 profile"
	} else {
		candidate.info.KnownReportLayout = true
		candidate.supported = true
	}
	return candidate, true
}

func (d *darwinMonitor) deviceMatched(result int32, device uintptr) {
	if d.closed {
		return
	}
	if result != 0 {
		d.emit(MonitorStateReportError, "HID 设备匹配回调失败", bridge.Device{}, nil,
			fmt.Errorf("xiaomi HID: match callback IOReturn 0x%08x", uint32(result)))
		return
	}
	if device == 0 {
		return
	}
	candidate, target := d.inspectDevice(device)
	if !target {
		return
	}
	if candidate.info.RegistryID != 0 {
		if _, exists := d.candidates[candidate.info.RegistryID]; exists {
			return
		}
	} else if _, exists := d.unidentified[device]; exists {
		return
	}
	d.api.retain(device) // the callback's IOHIDDeviceRef is borrowed
	if candidate.info.RegistryID != 0 {
		d.candidates[candidate.info.RegistryID] = candidate
		d.byDeviceRef[device] = candidate.info.RegistryID
	} else {
		d.unidentified[device] = candidate
	}
	d.reconcile()
}

func (d *darwinMonitor) deviceRemoved(result int32, device uintptr) {
	if d.closed {
		return
	}
	if result != 0 {
		d.emit(MonitorStateReportError, "HID 设备移除回调失败", bridge.Device{}, nil,
			fmt.Errorf("xiaomi HID: removal callback IOReturn 0x%08x", uint32(result)))
		return
	}
	if registryID, exists := d.byDeviceRef[device]; exists {
		candidate := d.candidates[registryID]
		delete(d.byDeviceRef, device)
		delete(d.candidates, registryID)
		if d.active != nil && d.active.candidate == candidate {
			d.deactivate(MonitorStateDisconnected, "小米 HID 已断开", true)
		}
		d.api.release(candidate.device)
	} else if candidate, exists := d.unidentified[device]; exists {
		delete(d.unidentified, device)
		d.api.release(candidate.device)
	} else {
		return
	}
	d.reconcile()
}

func (d *darwinMonitor) reconcile() {
	if d.closed || d.suppressReconcile {
		return
	}
	matchCount := len(d.candidates) + len(d.unidentified)
	if matchCount > 1 {
		if d.active != nil {
			d.deactivate(MonitorStateDisconnected, "多个目标设备同时匹配，已停止当前连接", true)
		}
		d.emit(MonitorStateAmbiguous, "发现多个 VID 2717 / PID 32B8 HID 匹配项，未选择任何设备", bridge.Device{}, nil, ErrAmbiguousDevice)
		return
	}
	if matchCount == 0 {
		// StartMonitor's caller owns the initial waiting UI state. Once we have
		// observed a match, though, publish the transition back to waiting so a
		// stale ambiguous/unsupported/disconnected state cannot remain visible.
		if d.lastState != "" && d.lastState != MonitorStateWaiting {
			d.emit(MonitorStateWaiting, "等待支持的 HID 设备连接", bridge.Device{}, nil, nil)
		}
		return
	}
	if len(d.unidentified) != 0 {
		candidate := firstUnidentified(d.unidentified)
		if d.active != nil {
			d.deactivate(MonitorStateDisconnected, "目标设备无法通过 registry ID 复核", true)
		}
		d.emit(MonitorStateUnsupported, candidate.reason, bridge.Device{}, nil, ErrUnsupportedDevice)
		return
	}
	var candidate *monitorCandidate
	for _, found := range d.candidates {
		candidate = found
		break
	}
	if candidate == nil {
		return
	}
	if !candidate.supported {
		if d.active != nil {
			d.deactivate(MonitorStateDisconnected, "目标设备 profile 复核失败", true)
		}
		d.emit(MonitorStateUnsupported, candidate.reason, bridge.Device{}, nil, ErrUnsupportedDevice)
		return
	}
	if d.active != nil && d.active.candidate == candidate {
		return
	}
	if d.active != nil {
		d.deactivate(MonitorStateDisconnected, "目标 HID 连接发生变化", true)
	}
	if d.closed {
		return
	}
	if err := d.activate(candidate); err != nil {
		d.emit(MonitorStateReportError, "无法启动 HID report 回调", bridge.Device{}, nil, err)
	}
}

func firstUnidentified(candidates map[uintptr]*monitorCandidate) *monitorCandidate {
	for _, candidate := range candidates {
		return candidate
	}
	return nil
}

func (d *darwinMonitor) activate(candidate *monitorCandidate) error {
	if d.closed {
		return ErrMonitorUnavailable
	}
	generation, err := nextConnectionGeneration()
	if err != nil {
		return err
	}
	device, err := bridge.NewDevice("xiaomi-hid:2717:32b8", generation)
	if err != nil {
		return err
	}
	normalizer, err := NewNormalizer(device)
	if err != nil {
		return err
	}
	buffer := d.api.malloc(knownReportBufferSize)
	if buffer == 0 {
		return errors.New("xiaomi HID: allocate input report buffer failed")
	}
	context, err := newNativeContext(callbackTarget{
		monitor: d, role: callbackRoleReport, device: candidate.device, registryID: candidate.info.RegistryID,
	})
	if err != nil {
		d.api.free(buffer)
		return err
	}
	active := &activeMonitorDevice{
		candidate: candidate, device: device, normalizer: normalizer,
		context: context, reportBuffer: buffer, held: make(map[bridge.PressToken]struct{}),
	}
	d.active = active
	d.api.deviceRegisterInputReport(candidate.device, buffer, knownReportBufferSize,
		nativeInputReportCallback, active.context.id)
	d.emit(MonitorStateConnected, "已连接到支持的 Xiaomi HID profile", device, nil, nil)
	return nil
}

func (d *darwinMonitor) deactivate(state, reason string, notify bool) {
	active := d.active
	if active == nil {
		return
	}
	active.normalizer.Retire()
	contextID := active.context.id
	dropNativeContext(&active.context)
	// Pass the original opaque token so IOKit removes the wrapper callback entry
	// associated with this report buffer. The token has already been removed
	// from callbackContexts, so any callback racing with removal is inert.
	d.api.deviceRegisterInputReport(active.candidate.device, active.reportBuffer, knownReportBufferSize, 0, contextID)
	d.active = nil
	var edges []Edge
	if notify {
		edges = disconnectEdges(active.held)
	}
	// The nil callback removes the wrapper callback entry; it does not guarantee
	// that the underlying device-interface read is stopped. Keep the native
	// buffer alive until manager close.
	d.retiredBuffers = append(d.retiredBuffers, active.reportBuffer)
	active.held = nil
	if notify {
		d.emit(state, reason, active.device, edges, nil)
	}
}

func (d *darwinMonitor) inputReport(target callbackTarget, result int32, sender uintptr, reportType int32, reportID uint32, report uintptr, reportLength int64) {
	active := d.active
	if d.closed || active == nil || target.device != active.candidate.device ||
		target.registryID != active.candidate.info.RegistryID {
		return
	}
	if sender != active.candidate.device {
		if d.stats != nil {
			d.stats.rejectedSender.Add(1)
		}
		return
	}
	if reportType != inputReportType || reportID != 1 {
		// Reports 6/7/8 are deliberately neither read nor copied.
		if d.stats != nil {
			d.stats.otherReports.Add(1)
		}
		return
	}
	if result != 0 {
		d.recordReportError()
		d.emit(MonitorStateReportError, "report 1 回调返回错误", active.device, nil,
			fmt.Errorf("xiaomi HID: report 1 IOReturn 0x%08x", uint32(result)))
		return
	}
	if reportLength != 7 || report == 0 {
		d.recordReportError()
		err := fmt.Errorf("%w: native report 1 length is %d, want 7 bytes including report ID", ErrMalformedReport, reportLength)
		d.emit(MonitorStateReportError, "report 1 长度异常，当前按压状态保持不变", active.device, nil, err)
		return
	}
	// The measured native callback includes the leading report ID byte. Copy
	// only the seven-byte report 1 record; never copy an opaque report payload.
	var native [7]byte
	d.api.copyBytesFromNative(&native[0], report, uintptr(len(native)))
	runtime.KeepAlive(&native)
	if native[0] != byte(reportID) {
		d.recordReportError()
		err := fmt.Errorf("%w: callback report ID is %d but raw report begins with %d", ErrMalformedReport, reportID, native[0])
		d.emit(MonitorStateReportError, "report 1 ID 与原始数据不一致，当前按压状态保持不变", active.device, nil, err)
		return
	}
	edges, err := active.normalizer.HandleNativeReport(native[:])
	if err != nil {
		d.recordReportError()
		d.emit(MonitorStateReportError, "report 1 无效，当前按压状态保持不变", active.device, nil, err)
		return
	}
	if len(edges) == 0 {
		return
	}
	for _, edge := range edges {
		if edge.Kind == EdgeDown {
			active.held[edge.Press] = struct{}{}
		} else if edge.Kind == EdgeUp {
			delete(active.held, edge.Press)
		}
	}
	d.emit(MonitorStateConnected, "收到已归一化的实体按钮边沿", active.device, edges, nil)
}

func (d *darwinMonitor) recordReportError() {
	if d.stats != nil {
		d.stats.reportErrors.Add(1)
	}
}

func (d *darwinMonitor) emit(state, reason string, device bridge.Device, edges []Edge, err error) {
	if d.closed {
		return
	}
	d.lastState = state
	dispatchMonitorEvent(d.onEvent, MonitorEvent{State: state, Reason: reason, Device: device, Edges: edges, Err: err})
}

func (d *darwinMonitor) close() error {
	if d == nil || d.closed {
		return nil
	}
	d.closed = true
	if d.manager != 0 {
		managerContextID := d.managerCtx.id
		dropNativeContext(&d.managerCtx)
		d.api.managerRegisterMatching(d.manager, 0, managerContextID)
		d.api.managerRegisterRemoval(d.manager, 0, managerContextID)
	}
	d.suppressReconcile = false
	if d.active != nil {
		active := d.active
		active.normalizer.Retire()
		contextID := active.context.id
		dropNativeContext(&active.context)
		d.api.deviceRegisterInputReport(active.candidate.device, active.reportBuffer, knownReportBufferSize, 0, contextID)
	}
	if d.scheduled {
		d.api.managerUnschedule(d.manager, d.runLoop, d.api.commonModes)
		d.scheduled = false
	}
	var closeErr error
	if d.manager != 0 && d.openAttempted {
		status := d.api.managerClose(d.manager, 0)
		if status != 0 {
			closeErr = fmt.Errorf("%w: IOHIDManagerClose(options=0) failed with IOReturn 0x%08x; native resources are retained and the monitor slot remains occupied; restart MiMac before starting another monitor", ErrMonitorRestartRequired, uint32(status))
		} else {
			d.openAttempted = false
		}
	}
	if closeErr != nil {
		// Callback tokens have been retired and registrations unscheduled, but a
		// failed manager close does not prove that IOKit stopped using its native
		// buffers or released its object graph. Preserve every native allocation
		// and retain this Go owner until process exit.
		retainFailedClose(d)
		return closeErr
	}

	// All native callbacks are unregistered and the manager is closed before
	// the report buffer, retained objects, or CF dictionary are released.
	if d.active != nil {
		active := d.active
		d.active = nil
		d.api.free(active.reportBuffer)
		active.held = nil
	}
	for _, buffer := range d.retiredBuffers {
		d.api.free(buffer)
	}
	d.retiredBuffers = nil
	for id, candidate := range d.candidates {
		d.api.release(candidate.device)
		delete(d.candidates, id)
	}
	for ref, candidate := range d.unidentified {
		d.api.release(candidate.device)
		delete(d.unidentified, ref)
	}
	clear(d.byDeviceRef)
	if d.matching != 0 {
		d.api.release(d.matching)
		d.matching = 0
	}
	if d.manager != 0 {
		d.api.release(d.manager)
		d.manager = 0
	}
	return nil
}

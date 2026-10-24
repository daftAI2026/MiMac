//go:build darwin

package xiaomi

import (
	"errors"
	"testing"

	"mimac.local/mimac/internal/bridge"
)

func TestDeactivateRetiresBufferAndUnregistersWithOriginalContext(t *testing.T) {
	device, err := bridge.NewDevice("xiaomi-hid:2717:32b8", 1)
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(device)
	if err != nil {
		t.Fatal(err)
	}
	monitor := &darwinMonitor{}
	context, err := newNativeContext(callbackTarget{
		monitor: monitor, role: callbackRoleReport, device: 0x1234, registryID: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	const reportBuffer = uintptr(0x5678)
	var unregisterCallback, unregisterContext uintptr
	var freed []uintptr
	monitor.api = &monitorAPI{
		deviceRegisterInputReport: func(_ uintptr, buffer uintptr, _ int64, callback uintptr, context uintptr) {
			if buffer != reportBuffer {
				t.Errorf("unregister buffer = %#x, want %#x", buffer, reportBuffer)
			}
			unregisterCallback, unregisterContext = callback, context
		},
		free: func(buffer uintptr) { freed = append(freed, buffer) },
	}
	monitor.active = &activeMonitorDevice{
		candidate: &monitorCandidate{device: 0x1234, info: HIDDevice{RegistryID: 9}},
		device:    device, normalizer: normalizer, context: context,
		reportBuffer: reportBuffer, held: make(map[bridge.PressToken]struct{}),
	}

	monitor.deactivate(MonitorStateDisconnected, "removed", true)

	if unregisterCallback != 0 {
		t.Fatalf("unregister callback = %#x, want nil", unregisterCallback)
	}
	if unregisterContext != context.id {
		t.Fatalf("unregister context = %#x, want original token %#x", unregisterContext, context.id)
	}
	if _, ok := lookupNativeContext(context.id); ok {
		t.Fatal("retired context token remains dispatchable")
	}
	if len(freed) != 0 {
		t.Fatalf("buffer freed before manager close: %#v", freed)
	}
	if len(monitor.retiredBuffers) != 1 || monitor.retiredBuffers[0] != reportBuffer {
		t.Fatalf("retired buffers = %#v, want %#x", monitor.retiredBuffers, reportBuffer)
	}
}

func TestCloseFreesRetiredBuffersAfterManagerClose(t *testing.T) {
	const reportBuffer = uintptr(0x5678)
	managerClosed := false
	var freed []uintptr
	monitor := &darwinMonitor{
		api: &monitorAPI{
			managerRegisterMatching: func(uintptr, uintptr, uintptr) {},
			managerRegisterRemoval:  func(uintptr, uintptr, uintptr) {},
			managerClose: func(uintptr, uint32) int32 {
				managerClosed = true
				return 0
			},
			release: func(uintptr) {},
			free: func(buffer uintptr) {
				if !managerClosed {
					t.Error("report buffer freed before manager close")
				}
				freed = append(freed, buffer)
			},
		},
		manager:        0x1111,
		openAttempted:  true,
		retiredBuffers: []uintptr{reportBuffer},
		candidates:     make(map[uint64]*monitorCandidate),
		byDeviceRef:    make(map[uintptr]uint64),
		unidentified:   make(map[uintptr]*monitorCandidate),
	}

	if err := monitor.close(); err != nil {
		t.Fatal(err)
	}
	if !managerClosed {
		t.Fatal("manager was not closed")
	}
	if len(freed) != 1 || freed[0] != reportBuffer {
		t.Fatalf("freed buffers = %#v, want %#x", freed, reportBuffer)
	}
}

func TestManagerCloseAttemptedAfterManagerOpenError(t *testing.T) {
	managerClosed := false
	monitor := &darwinMonitor{
		api: &monitorAPI{
			managerOpen:             func(uintptr, uint32) int32 { return -1 },
			managerRegisterMatching: func(uintptr, uintptr, uintptr) {},
			managerRegisterRemoval:  func(uintptr, uintptr, uintptr) {},
			managerClose: func(uintptr, uint32) int32 {
				managerClosed = true
				return 0
			},
			release: func(uintptr) {},
		},
		manager: 0x1111,
	}
	if status := monitor.openManager(); status == 0 {
		t.Fatal("fake manager open unexpectedly succeeded")
	}
	if !monitor.openAttempted {
		t.Fatal("failed open attempt was not recorded")
	}
	if err := monitor.close(); err != nil {
		t.Fatal(err)
	}
	if !managerClosed {
		t.Fatal("IOHIDManagerClose was not attempted after a failed open")
	}
}

func TestInputReportStatsSeparateSenderOtherAndMalformed(t *testing.T) {
	device, err := bridge.NewDevice("xiaomi-hid:2717:32b8", 13)
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(device)
	if err != nil {
		t.Fatal(err)
	}
	var events []MonitorEvent
	monitor := &darwinMonitor{
		stats:   &monitorStats{},
		onEvent: func(event MonitorEvent) { events = append(events, event) },
		active: &activeMonitorDevice{
			candidate:  &monitorCandidate{device: 0x2222, info: HIDDevice{RegistryID: 88}},
			device:     device,
			normalizer: normalizer,
			held:       make(map[bridge.PressToken]struct{}),
		},
	}
	context, err := newNativeContext(callbackTarget{
		monitor: monitor, role: callbackRoleReport, device: 0x2222, registryID: 88,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dropNativeContext(&context)

	// A sender mismatch is counted and rejected without reading report memory.
	handleNativeInputReport(context.id, 0, 0x3333, inputReportType, 1, 0, 7)
	// An opaque report is counted but its payload is neither required nor read.
	handleNativeInputReport(context.id, 0, 0x2222, inputReportType, 6, 0, 0)
	// A malformed report 1 increments the error counter without reading bytes.
	handleNativeInputReport(context.id, 0, 0x2222, inputReportType, 1, 0, 6)
	// A retired/unknown callback context is ignored before counter increments.
	handleNativeInputReport(^uintptr(0), 0, 0x2222, inputReportType, 1, 0, 7)

	want := MonitorStats{NativeReports: 3, RejectedSender: 1, OtherReports: 1, ReportErrors: 1}
	if got := monitor.stats.snapshot(); got != want {
		t.Fatalf("stats = %#v, want %#v", got, want)
	}
	if len(events) != 1 || events[0].State != MonitorStateReportError {
		t.Fatalf("events = %#v, want one malformed report error", events)
	}
}

func TestManagerCloseFailureRetainsNativeGraphAndBlocksRestart(t *testing.T) {
	monitorSlot.Lock()
	monitorSlot.claimed = false
	monitorSlot.restartRequired = false
	monitorSlot.Unlock()
	if err := claimMonitorSlot(); err != nil {
		t.Fatalf("could not claim monitor slot for test: %v", err)
	}
	t.Cleanup(func() {
		monitorSlot.Lock()
		monitorSlot.claimed = false
		monitorSlot.restartRequired = false
		monitorSlot.Unlock()
	})

	device, err := bridge.NewDevice("xiaomi-hid:2717:32b8", 11)
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(device)
	if err != nil {
		t.Fatal(err)
	}
	monitorImpl := &darwinMonitor{
		candidates: map[uint64]*monitorCandidate{
			77: {device: 0x3333, info: HIDDevice{RegistryID: 77}, supported: true},
		},
		byDeviceRef:    make(map[uintptr]uint64),
		unidentified:   make(map[uintptr]*monitorCandidate),
		retiredBuffers: []uintptr{0x5555},
		active: &activeMonitorDevice{
			candidate: &monitorCandidate{device: 0x3333, info: HIDDevice{RegistryID: 77}, supported: true},
			device:    device, normalizer: normalizer, reportBuffer: 0x4444,
			held: make(map[bridge.PressToken]struct{}),
		},
		manager: 0x1111, matching: 0x2222, runLoop: 0x6666, scheduled: true, openAttempted: true,
	}
	managerContext, err := newNativeContext(callbackTarget{monitor: monitorImpl, role: callbackRoleManager})
	if err != nil {
		t.Fatal(err)
	}
	monitorImpl.managerCtx = managerContext
	reportContext, err := newNativeContext(callbackTarget{monitor: monitorImpl, role: callbackRoleReport, device: 0x3333, registryID: 77})
	if err != nil {
		t.Fatal(err)
	}
	monitorImpl.active.context = reportContext
	var freed, released []uintptr
	monitorImpl.api = &monitorAPI{
		managerRegisterMatching:   func(uintptr, uintptr, uintptr) {},
		managerRegisterRemoval:    func(uintptr, uintptr, uintptr) {},
		deviceRegisterInputReport: func(uintptr, uintptr, int64, uintptr, uintptr) {},
		managerUnschedule:         func(uintptr, uintptr, uintptr) {},
		managerClose:              func(uintptr, uint32) int32 { return -1 },
		free:                      func(pointer uintptr) { freed = append(freed, pointer) },
		release:                   func(pointer uintptr) { released = append(released, pointer) },
	}
	monitor := &Monitor{closeFn: func() error { return closeMonitorAndReleaseSlot(monitorImpl) }}

	closeErr := monitor.Close()
	if !errors.Is(closeErr, ErrMonitorRestartRequired) {
		t.Fatalf("Close() error = %v, want ErrMonitorRestartRequired", closeErr)
	}
	if len(freed) != 0 || len(released) != 0 {
		t.Fatalf("freed=%#v released=%#v after failed manager close; want no native frees/releases", freed, released)
	}
	if monitorImpl.manager != 0x1111 || monitorImpl.matching != 0x2222 || monitorImpl.active == nil || len(monitorImpl.retiredBuffers) != 1 {
		t.Fatal("failed close did not preserve the native object graph and buffers")
	}
	if monitorImpl.scheduled {
		t.Fatal("manager remained scheduled after failed close")
	}
	if err := claimMonitorSlot(); !errors.Is(err, ErrMonitorRestartRequired) {
		t.Fatalf("claim after failed close = %v, want ErrMonitorRestartRequired", err)
	}
	failedCloseMonitors.Lock()
	retained := false
	for _, item := range failedCloseMonitors.values {
		if item == monitorImpl {
			retained = true
			break
		}
	}
	failedCloseMonitors.Unlock()
	if !retained {
		t.Fatal("failed native graph was not retained until process exit")
	}
}

func TestReconcileReturnsToWaitingAfterLastMatchDisappears(t *testing.T) {
	var events []MonitorEvent
	monitor := &darwinMonitor{
		onEvent:      func(event MonitorEvent) { events = append(events, event) },
		candidates:   make(map[uint64]*monitorCandidate),
		byDeviceRef:  make(map[uintptr]uint64),
		unidentified: make(map[uintptr]*monitorCandidate),
		lastState:    MonitorStateAmbiguous,
	}

	monitor.reconcile()

	if len(events) != 1 || events[0].State != MonitorStateWaiting {
		t.Fatalf("events = %#v, want one waiting transition", events)
	}
}

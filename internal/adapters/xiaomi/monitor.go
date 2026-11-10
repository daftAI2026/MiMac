package xiaomi

import (
	"errors"
	"sort"
	"sync/atomic"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
)

var (
	ErrInputMonitoringRequired = errors.New("xiaomi HID: Input Monitoring permission is required")
	ErrMonitorUnavailable      = errors.New("xiaomi HID: native report monitoring is unavailable")
	ErrMonitorAlreadyRunning   = errors.New("xiaomi HID: a monitor is already running")
	ErrMonitorRestartRequired  = errors.New("xiaomi HID: native monitor cleanup failed; restart required")
	ErrAmbiguousDevice         = errors.New("xiaomi HID: multiple matching devices are present")
	ErrUnsupportedDevice       = errors.New("xiaomi HID: device does not match the observed report profile")
)

// ListenAccessState is a read-only snapshot of the process's macOS Input
// Monitoring permission. Unknown includes platforms or native-query failures
// where MiMac cannot claim either grant or denial.
type ListenAccessState uint8

const (
	ListenAccessUnknown ListenAccessState = iota
	ListenAccessNotDetermined
	ListenAccessGranted
	ListenAccessDenied
)

// RequestListenAccess explicitly asks macOS for Input Monitoring access. It
// does not create an HID manager or start listening for device reports.
func RequestListenAccess() ListenAccessState {
	return requestListenAccess()
}

const (
	MonitorStateWaiting      = devices.InputStateWaiting
	MonitorStateConnected    = devices.InputStateConnected
	MonitorStateDisconnected = devices.InputStateDisconnected
	MonitorStateAmbiguous    = devices.InputStateAmbiguous
	MonitorStateUnsupported  = devices.InputStateUnsupported
	MonitorStateReportError  = devices.InputStateReportError
)

// MonitorEvent preserves the Xiaomi monitor API while sharing the common
// device input contract with the host coordinator.
type MonitorEvent = devices.InputEvent

// MonitorStats is a value snapshot of native report callback activity.
type MonitorStats struct {
	// NativeReports counts report callbacks with a live registered context.
	NativeReports uint64
	// RejectedSender counts reports whose native sender did not match the active device.
	RejectedSender uint64
	// OtherReports counts callbacks that are not input report 1.
	OtherReports uint64
	// ReportErrors counts rejected or failed input report 1 callbacks.
	ReportErrors uint64
}

type monitorStats struct {
	nativeReports  atomic.Uint64
	rejectedSender atomic.Uint64
	otherReports   atomic.Uint64
	reportErrors   atomic.Uint64
}

func (s *monitorStats) snapshot() MonitorStats {
	if s == nil {
		return MonitorStats{}
	}
	return MonitorStats{
		NativeReports:  s.nativeReports.Load(),
		RejectedSender: s.rejectedSender.Load(),
		OtherReports:   s.otherReports.Load(),
		ReportErrors:   s.reportErrors.Load(),
	}
}

// Monitor owns one explicit, nonexclusive HID report subscription. Start and
// Close must be called on the application's main thread. Native callbacks
// arrive on the main CFRunLoop and invoke onEvent synchronously there. Do not
// call StartMonitor or Close from onEvent; defer those operations until the
// callback returns to avoid changing native registrations inside their stack.
type Monitor struct {
	closeFn        func() error
	reserveVoiceFn func(string) (*VoiceKeyReservation, error)
	closed         bool
	stats          *monitorStats
}

// ReserveVoiceKey explicitly suppresses the connected remote's native voice
// key while preserving raw reports. Listening alone never calls this method.
// The host owns the returned reservation and must finish its session, then
// close the reservation before closing the monitor. A non-nil reservation on
// error still owns recovery resources and must be retained for cleanup.
func (m *Monitor) ReserveVoiceKey(snapshotPath string) (*VoiceKeyReservation, error) {
	if m == nil || m.closed || m.reserveVoiceFn == nil {
		return nil, ErrMonitorUnavailable
	}
	return m.reserveVoiceFn(snapshotPath)
}

// Stats returns a thread-safe value snapshot. It remains available after Close
// so the caller can inspect final callback counters.
func (m *Monitor) Stats() MonitorStats {
	if m == nil {
		return MonitorStats{}
	}
	return m.stats.snapshot()
}

// Close synchronously unregisters callbacks and closes the manager. If native
// manager close fails, it retains native resources and returns
// ErrMonitorRestartRequired; another monitor cannot start until the process
// restarts. It is safe to call more than once and does not enqueue work on a UI
// dispatcher.
func (m *Monitor) Close() error {
	if m == nil || m.closed {
		return nil
	}
	m.closed = true
	closeFn := m.closeFn
	m.closeFn = nil
	if closeFn == nil {
		return nil
	}
	return closeFn()
}

func dispatchMonitorEvent(onEvent func(MonitorEvent), event MonitorEvent) {
	if onEvent == nil {
		return
	}
	event.Edges = append([]Edge(nil), event.Edges...)
	// Do not let a consumer panic unwind through a native callback trampoline.
	defer func() { _ = recover() }()
	onEvent(event)
}

func disconnectEdges(held map[bridge.PressToken]struct{}) []Edge {
	edges := make([]Edge, 0, len(held))
	for press := range held {
		edges = append(edges, Edge{Kind: EdgeUp, Press: press})
	}
	sort.Slice(edges, func(i, j int) bool {
		left, right := edges[i].Press, edges[j].Press
		if left.PhysicalButtonID() != right.PhysicalButtonID() {
			return left.PhysicalButtonID() < right.PhysicalButtonID()
		}
		return left.PressID() < right.PressID()
	})
	return edges
}

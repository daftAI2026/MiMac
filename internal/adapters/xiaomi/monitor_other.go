//go:build !darwin

package xiaomi

import "fmt"

// ListenAccess is unknown off macOS. It never attempts to start a monitor.
func ListenAccess() ListenAccessState { return ListenAccessUnknown }

// requestListenAccess is unavailable off macOS and never requests access.
func requestListenAccess() ListenAccessState { return ListenAccessUnknown }

// StartMonitor requires the macOS IOHIDManager APIs. On other platforms it
// returns ErrMonitorUnavailable without attempting device access.
func StartMonitor(onEvent func(MonitorEvent)) (*Monitor, error) {
	if onEvent == nil {
		return nil, fmt.Errorf("%w: event callback is required", ErrMonitorUnavailable)
	}
	return nil, ErrMonitorUnavailable
}

// StartMonitorWithoutPrompt requires the macOS IOHIDManager APIs. On other
// platforms it returns ErrMonitorUnavailable without attempting device access.
func StartMonitorWithoutPrompt(onEvent func(MonitorEvent)) (*Monitor, error) {
	if onEvent == nil {
		return nil, fmt.Errorf("%w: event callback is required", ErrMonitorUnavailable)
	}
	return nil, ErrMonitorUnavailable
}

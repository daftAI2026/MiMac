// Package devices defines product-neutral physical input contracts.
package devices

import "mimac.local/mimac/internal/bridge"

const (
	InputStateWaiting      = "waiting"
	InputStateConnected    = "connected"
	InputStateDisconnected = "disconnected"
	InputStateAmbiguous    = "ambiguous"
	InputStateUnsupported  = "unsupported"
	InputStateReportError  = "report_error"
)

// EdgeKind identifies a normalized physical input edge. It carries no
// keyboard or action semantics.
type EdgeKind uint8

const (
	EdgeDown EdgeKind = iota + 1
	EdgeUp
)

// Edge carries the same press token for the physical press and release.
type Edge struct {
	Kind  EdgeKind
	Press bridge.PressToken
}

// InputEvent reports a device-neutral input lifecycle state and any
// normalized physical edges. State is a stable code; Reason is display-ready.
// Device is zero when no connection generation is active. Consumers should
// treat Edges as immutable after dispatch.
type InputEvent struct {
	State  string
	Reason string
	Device bridge.Device
	Edges  []Edge
	Err    error
}

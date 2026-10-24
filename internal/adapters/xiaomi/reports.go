// Package xiaomi normalizes one observed Xiaomi HID report descriptor
// profile: VID 0x2717, PID 0x32b8, report 1 with three little-endian 16-bit
// usages on page 0x0007, and opaque reports 6, 7, and 8 with 120-byte
// payloads. It does not infer button meanings and does not claim support for
// Xiaomi HID devices in general.
package xiaomi

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
)

const (
	reportIDKeys     byte   = 0x01
	reportID6        byte   = 0x06
	reportID7        byte   = 0x07
	reportID8        byte   = 0x08
	keyPayloadLen           = 3 * 2
	opaquePayloadLen        = 120
	maxUsage         uint16 = 0x00fe
)

var (
	ErrInvalidDevice       = errors.New("xiaomi HID: invalid bridge device")
	ErrInvalidNormalizer   = errors.New("xiaomi HID: invalid normalizer")
	ErrMalformedReport     = errors.New("xiaomi HID: malformed report")
	ErrUnsupportedReportID = errors.New("xiaomi HID: unsupported report ID")
	ErrRetired             = errors.New("xiaomi HID: connection retired")
	ErrPressIDExhausted    = errors.New("xiaomi HID: press ID exhausted")
)

// EdgeKind and Edge preserve the Xiaomi report API while sharing the common
// device input contract.
type EdgeKind = devices.EdgeKind
type Edge = devices.Edge

const (
	EdgeDown = devices.EdgeDown
	EdgeUp   = devices.EdgeUp
)

// Normalizer converts report snapshots to button edges. Use one instance for
// one connection generation and call it from a serialized owner. It is not
// concurrency-safe.
type Normalizer struct {
	device      bridge.Device
	nextPressID uint64
	held        map[uint16]bridge.PressToken
	retired     bool
}

// NewNormalizer binds state and press IDs to the supplied bridge connection
// generation. Create a fresh instance for each reconnect.
func NewNormalizer(device bridge.Device) (*Normalizer, error) {
	if strings.TrimSpace(device.ID()) == "" || device.Generation() == 0 {
		return nil, ErrInvalidDevice
	}
	return &Normalizer{
		device:      device,
		nextPressID: 1,
		held:        make(map[uint16]bridge.PressToken),
	}, nil
}

// HandleReport accepts a report ID separately from its payload. Report 1 is
// interpreted as three little-endian uint16 usage slots. Reports 6, 7, and 8
// are length-checked and ignored; their opaque contents are never decoded.
// Unknown IDs and malformed input leave the current held set unchanged.
func (n *Normalizer) HandleReport(reportID byte, payload []byte) ([]Edge, error) {
	if n == nil {
		return nil, ErrInvalidNormalizer
	}
	if strings.TrimSpace(n.device.ID()) == "" || n.device.Generation() == 0 {
		return nil, ErrInvalidNormalizer
	}
	if n.retired {
		return nil, ErrRetired
	}
	switch reportID {
	case reportIDKeys:
		if len(payload) != keyPayloadLen {
			return nil, fmt.Errorf("%w: report 1 has %d payload bytes, want %d", ErrMalformedReport, len(payload), keyPayloadLen)
		}
		return n.handleKeys(payload)
	case reportID6, reportID7, reportID8:
		if len(payload) != opaquePayloadLen {
			return nil, fmt.Errorf("%w: report %d has %d payload bytes, want %d", ErrMalformedReport, reportID, len(payload), opaquePayloadLen)
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("%w: 0x%02x", ErrUnsupportedReportID, reportID)
	}
}

// HandleNativeReport accepts a raw numbered HID report whose first byte is
// the report ID. It removes that byte before passing the payload to
// HandleReport; callers that already split the ID should use HandleReport.
func (n *Normalizer) HandleNativeReport(report []byte) ([]Edge, error) {
	if len(report) == 0 {
		return nil, fmt.Errorf("%w: missing report ID", ErrMalformedReport)
	}
	return n.HandleReport(report[0], report[1:])
}

// Retire ends this connection generation and rejects any later reports.
// The connection owner should also send bridge.DeviceDisconnected with the
// same device so the core can release its own held-key ownership.
func (n *Normalizer) Retire() {
	if n == nil || n.retired {
		return
	}
	n.retired = true
	n.held = nil
}

func (n *Normalizer) handleKeys(payload []byte) ([]Edge, error) {
	current := make(map[uint16]struct{}, 3)
	for slot := 0; slot < 3; slot++ {
		at := slot * 2
		usage := uint16(payload[at]) | uint16(payload[at+1])<<8
		if usage == 0 {
			continue
		}
		if usage >= 1 && usage <= 3 {
			return nil, fmt.Errorf("%w: keyboard rollover usage 0x%04x", ErrMalformedReport, usage)
		}
		if usage > maxUsage {
			return nil, fmt.Errorf("%w: usage 0x%04x exceeds descriptor logical maximum 0x%04x", ErrMalformedReport, usage, maxUsage)
		}
		current[usage] = struct{}{}
	}

	var released, pressed []uint16
	for usage := range n.held {
		if _, stillHeld := current[usage]; !stillHeld {
			released = append(released, usage)
		}
	}
	for usage := range current {
		if _, wasHeld := n.held[usage]; !wasHeld {
			pressed = append(pressed, usage)
		}
	}
	sort.Slice(released, func(i, j int) bool { return released[i] < released[j] })
	sort.Slice(pressed, func(i, j int) bool { return pressed[i] < pressed[j] })

	if len(pressed) > 0 {
		maxUint64 := ^uint64(0)
		if n.nextPressID == 0 || uint64(len(pressed)-1) > maxUint64-n.nextPressID {
			return nil, ErrPressIDExhausted
		}
	}

	// Build the whole next state before committing it so any construction
	// failure remains atomic with respect to both held usages and press IDs.
	nextHeld := make(map[uint16]bridge.PressToken, len(current))
	for usage := range current {
		if token, wasHeld := n.held[usage]; wasHeld {
			nextHeld[usage] = token
		}
	}
	candidateNextPressID := n.nextPressID
	for _, usage := range pressed {
		physicalID := fmt.Sprintf("hid:0007:%04x", usage)
		token, err := bridge.NewPressToken(n.device, physicalID, candidateNextPressID)
		if err != nil {
			return nil, fmt.Errorf("xiaomi HID: create press token: %w", err)
		}
		nextHeld[usage] = token
		if candidateNextPressID == ^uint64(0) {
			candidateNextPressID = 0
		} else {
			candidateNextPressID++
		}
	}

	edges := make([]Edge, 0, len(released)+len(pressed))
	// A report is a simultaneous snapshot. Emit releases before presses, and
	// sort usages within each group so consumers see a stable event sequence.
	for _, usage := range released {
		edges = append(edges, Edge{Kind: EdgeUp, Press: n.held[usage]})
	}
	for _, usage := range pressed {
		edges = append(edges, Edge{Kind: EdgeDown, Press: nextHeld[usage]})
	}

	n.held = nextHeld
	n.nextPressID = candidateNextPressID
	return edges, nil
}

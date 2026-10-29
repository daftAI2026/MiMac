// Package preferences presents native host preferences inside a MyGo window.
package preferences

// Layout positions the native preferences card within the existing window's
// content view. Distances are in points (DIP).
type Layout struct {
	Left   float64
	Top    float64
	Right  float64
	Bottom float64
}

// State is the host's source of truth. Appearance is system, light, or dark.
type State struct {
	Login        bool
	Dock         bool
	VoiceOverlay bool
	Appearance   string
	// Dark is the effective theme state and does not select the Appearance segment.
	Dark bool
	// TextSize is -1 for small, 0 for standard, and 1 for large.
	TextSize int
	// BodySize and CaptionSize are point sizes from the host appearance state.
	BodySize    float64
	CaptionSize float64
	Disabled    bool
	Error       string
}

// Callbacks report user changes to the host. The host applies changes and
// calls Update with the actual resulting state.
type Callbacks struct {
	Login        func(bool)
	Dock         func(bool)
	VoiceOverlay func(bool)
	Appearance   func(string)
	TextSize     func(int)
}

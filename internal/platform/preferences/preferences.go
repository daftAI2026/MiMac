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
	Login bool
	Dock  bool
	// VoicePresentation is the selection index exposed by the native Picker:
	// 0 MiMac sphere, 1 dot bar, 2 follow the selected voice tool.
	VoicePresentation int
	// ShowLiveText is an independent dot-bar interim caption preference.
	ShowLiveText bool
	// OnlyShowMiMacVoicePrompt is a global opt-in. Only the selected tool's
	// registered native presentation adapter can act on it.
	OnlyShowMiMacVoicePrompt bool
	// LiveTextSupported describes the selected tool's interim-text capability.
	// When false, the UI shows the switch off without changing ShowLiveText.
	LiveTextSupported bool
	// LiveTextStatus is shown only when the selected tool needs an explanation.
	LiveTextStatus string
	// DotAtBottom selects the screen edge for the dot-bar HUD.
	DotAtBottom bool
	Appearance  string
	// Dark is the effective theme state and does not select the Appearance segment.
	Dark bool
	// TextSize is -1 for small, 0 for standard, and 1 for large.
	TextSize int
	// BodySize and CaptionSize are point sizes from the host appearance state.
	BodySize    float64
	CaptionSize float64
	Permissions []PermissionRow
	Disabled    bool
	Error       string
}

type PermissionRow struct {
	ID      int32
	Title   string
	Status  string
	Granted bool
}

// Callbacks report user changes to the host. The host applies changes and
// calls Update with the actual resulting state.
type Callbacks struct {
	Login                    func(bool)
	Dock                     func(bool)
	VoicePresentation        func(int)
	ShowLiveText             func(bool)
	OnlyShowMiMacVoicePrompt func(bool)
	DotAtBottom              func(bool)
	Appearance               func(string)
	TextSize                 func(int)
	ManagePermissions        func(int32)
}

//go:build !darwin

package navigation

// Control is a no-op on platforms without the macOS title bar control.
type Control struct{}

// New is a no-op outside macOS so shared host code remains portable.
func New(_ uintptr, _, _ float64, _ func(delta int)) (*Control, error) {
	return &Control{}, nil
}

// SetEnabled has no effect outside macOS.
func (*Control) SetEnabled(_, _ bool) {}

// Close has no effect outside macOS.
func (*Control) Close() {}

func (*Control) SetTitle(string) {}

func (*Control) SetTitleSize(float64) {}

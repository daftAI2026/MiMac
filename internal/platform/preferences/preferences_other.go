//go:build !darwin

package preferences

// Control is inert outside macOS.
type Control struct{}

// New returns an inert control outside macOS.
func New(_ uintptr, _ Layout, _ State, _ Callbacks) (*Control, error) {
	return &Control{}, nil
}

// Update has no effect outside macOS.
func (*Control) Update(State) error { return nil }

// Close has no effect outside macOS.
func (*Control) Close() {}

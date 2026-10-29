//go:build !darwin || !arm64

package siriglass

// Renderer is inert where the captured native ABI is unsupported.
type Renderer struct{}

// Open reports that the native renderer is only available on the validated
// macOS arm64 build.
func Open(uintptr) (*Renderer, error) { return nil, ErrUnsupported }

// Update reports that the native renderer is unavailable on this host.
func (*Renderer) Update(bool, float32) error { return ErrUnsupported }

// Close is a no-op because Open cannot create a native resource here.
func (*Renderer) Close() error { return nil }

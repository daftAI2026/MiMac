//go:build !darwin

package textoutput

type unsupportedNative struct{}

func defaultNative() native { return unsupportedNative{} }

func hasAccessibilityAccess() bool     { return false }
func requestAccessibilityAccess() bool { return false }
func requestPostEventAccess() bool     { return false }

func (unsupportedNative) capture() (snapshot, error)    { return snapshot{}, ErrUnsupported }
func (unsupportedNative) insert(snapshot, string) error { return ErrUnsupported }
func (unsupportedNative) close(snapshot) error          { return nil }

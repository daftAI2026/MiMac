//go:build !darwin

package textoutput

import "context"

type unsupportedNative struct{}

func defaultNative() native { return unsupportedNative{} }

func hasAccessibilityAccess() bool       { return false }
func requestAccessibilityAccess() bool   { return false }
func requestPostEventAccess() bool       { return false }
func frontmostProcessID() (int32, error) { return 0, ErrUnsupported }

func (unsupportedNative) checkFocus(snapshot) error         { return ErrUnsupported }
func (unsupportedNative) sameField(snapshot, snapshot) bool { return false }
func (unsupportedNative) capture(context.Context, captureRequest) (snapshot, error) {
	return snapshot{}, ErrUnsupported
}
func (unsupportedNative) insert(snapshot, string) error { return ErrUnsupported }
func (unsupportedNative) close(snapshot) error          { return nil }

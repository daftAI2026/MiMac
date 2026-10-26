package processidentity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureAndCheckStableIdentity(t *testing.T) {
	path := executableFixture(t, "voice-tool")
	current := snapshot{
		startedAt:  startTime{seconds: 120, microseconds: 456},
		executable: path,
	}
	identity, err := captureWithReader(41, path, fixedReader(41, &current, nil))
	if err != nil {
		t.Fatalf("captureWithReader() error = %v", err)
	}
	if err := identity.Check(); err != nil {
		t.Fatalf("Check() error = %v", err)
	}
}

func TestCheckRejectsReusedPIDWithDifferentStartTime(t *testing.T) {
	path := executableFixture(t, "voice-tool")
	current := snapshot{
		startedAt:  startTime{seconds: 120, microseconds: 456},
		executable: path,
	}
	identity, err := captureWithReader(41, path, fixedReader(41, &current, nil))
	if err != nil {
		t.Fatalf("captureWithReader() error = %v", err)
	}
	current.startedAt = startTime{seconds: 121, microseconds: 12}
	if err := identity.Check(); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("Check() error = %v, want ErrIdentityChanged", err)
	}
}

func TestCheckRejectsChangedExecutable(t *testing.T) {
	first := executableFixture(t, "voice-tool")
	second := executableFixture(t, "different-tool")
	current := snapshot{
		startedAt:  startTime{seconds: 120, microseconds: 456},
		executable: first,
	}
	identity, err := captureWithReader(41, first, fixedReader(41, &current, nil))
	if err != nil {
		t.Fatalf("captureWithReader() error = %v", err)
	}
	current.executable = second
	if err := identity.Check(); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("Check() error = %v, want ErrIdentityChanged", err)
	}
}

func TestCaptureAndCheckFailClosedOnReadErrors(t *testing.T) {
	path := executableFixture(t, "voice-tool")
	readErr := errors.New("fake native read failed")
	if _, err := captureWithReader(41, path, fixedReader(41, nil, readErr)); !errors.Is(err, ErrReadFailed) {
		t.Fatalf("capture error = %v, want ErrReadFailed", err)
	}

	current := snapshot{
		startedAt:  startTime{seconds: 120, microseconds: 456},
		executable: path,
	}
	reader := fixedReader(41, &current, nil)
	identity, err := captureWithReader(41, path, reader)
	if err != nil {
		t.Fatalf("captureWithReader() error = %v", err)
	}
	readerErr := errors.New("fake native read failed after capture")
	identity.read = fixedReader(41, nil, readerErr)
	if err := identity.Check(); !errors.Is(err, ErrReadFailed) {
		t.Fatalf("Check() error = %v, want ErrReadFailed", err)
	}
}

func TestCaptureRequiresPositivePIDAndAbsoluteExecutable(t *testing.T) {
	path := executableFixture(t, "voice-tool")
	reader := fixedReader(41, &snapshot{
		startedAt:  startTime{seconds: 120, microseconds: 456},
		executable: path,
	}, nil)
	if _, err := captureWithReader(0, path, reader); !errors.Is(err, ErrInvalidPID) {
		t.Fatalf("capture with PID 0 error = %v, want ErrInvalidPID", err)
	}
	if _, err := captureWithReader(41, "relative/voice-tool", reader); !errors.Is(err, ErrInvalidExecutable) {
		t.Fatalf("capture with relative path error = %v, want ErrInvalidExecutable", err)
	}
}

func TestCaptureResolvesExpectedExecutableSymlink(t *testing.T) {
	target := executableFixture(t, "voice-tool")
	alias := filepath.Join(t.TempDir(), "voice-tool-alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	current := snapshot{
		startedAt:  startTime{seconds: 120, microseconds: 456},
		executable: target,
	}
	identity, err := captureWithReader(41, alias, fixedReader(41, &current, nil))
	if err != nil {
		t.Fatalf("captureWithReader() error = %v", err)
	}
	if err := identity.Check(); err != nil {
		t.Fatalf("Check() error = %v", err)
	}
}

func executableFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("fake executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func fixedReader(pid int32, value *snapshot, err error) processReader {
	return func(requestedPID int32) (snapshot, error) {
		if requestedPID != pid {
			return snapshot{}, errors.New("unexpected PID")
		}
		if err != nil {
			return snapshot{}, err
		}
		return *value, nil
	}
}

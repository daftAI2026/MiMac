// Package processidentity captures and rechecks the identity of one process.
package processidentity

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var (
	ErrUnsupported         = errors.New("process identity: unsupported platform")
	ErrInvalidPID          = errors.New("process identity: PID must be positive")
	ErrInvalidExecutable   = errors.New("process identity: expected executable must be an absolute existing path")
	ErrReadFailed          = errors.New("process identity: unable to read process identity")
	ErrExecutableMismatch  = errors.New("process identity: executable does not match expected path")
	ErrIdentityChanged     = errors.New("process identity: captured process identity changed")
	ErrInvalidProcessState = errors.New("process identity: invalid process identity data")
)

type startTime struct {
	seconds      uint64
	microseconds uint64
}

type snapshot struct {
	startedAt  startTime
	executable string
}

type processReader func(pid int32) (snapshot, error)

// Identity is a point-in-time identity for one process ID. Check reads only
// that PID and fails if it no longer describes the same process and executable.
type Identity struct {
	pid        int32
	startedAt  startTime
	executable string
	read       processReader
}

// Capture records the start time and executable path for pid. expectedExecutable
// must be an absolute path. Its symlinks are resolved before comparison.
func Capture(pid int32, expectedExecutable string) (*Identity, error) {
	if pid <= 0 {
		return nil, ErrInvalidPID
	}
	if !filepath.IsAbs(expectedExecutable) || strings.ContainsRune(expectedExecutable, '\x00') {
		return nil, ErrInvalidExecutable
	}
	reader, err := nativeProcessReader()
	if err != nil {
		return nil, err
	}
	return captureWithReader(pid, expectedExecutable, reader)
}

// Check verifies that the PID still has its captured start time and executable.
func (i *Identity) Check() error {
	if i == nil || i.pid <= 0 || i.read == nil {
		return ErrInvalidProcessState
	}
	current, err := i.read(i.pid)
	if err != nil {
		return fmt.Errorf("%w for PID %d: %v", ErrReadFailed, i.pid, err)
	}
	if err := validateSnapshot(current); err != nil {
		return err
	}
	currentPath, err := canonicalExecutable(current.executable)
	if err != nil {
		return fmt.Errorf("%w for PID %d: %v", ErrReadFailed, i.pid, err)
	}
	if current.startedAt != i.startedAt || currentPath != i.executable {
		return fmt.Errorf("%w for PID %d", ErrIdentityChanged, i.pid)
	}
	return nil
}

func captureWithReader(pid int32, expectedExecutable string, read processReader) (*Identity, error) {
	if pid <= 0 {
		return nil, ErrInvalidPID
	}
	if read == nil {
		return nil, ErrInvalidProcessState
	}
	expectedPath, err := canonicalExecutable(expectedExecutable)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidExecutable, err)
	}
	captured, err := read(pid)
	if err != nil {
		return nil, fmt.Errorf("%w for PID %d: %v", ErrReadFailed, pid, err)
	}
	if err := validateSnapshot(captured); err != nil {
		return nil, err
	}
	capturedPath, err := canonicalExecutable(captured.executable)
	if err != nil {
		return nil, fmt.Errorf("%w for PID %d: %v", ErrReadFailed, pid, err)
	}
	if capturedPath != expectedPath {
		return nil, fmt.Errorf("%w for PID %d", ErrExecutableMismatch, pid)
	}
	return &Identity{
		pid:        pid,
		startedAt:  captured.startedAt,
		executable: capturedPath,
		read:       read,
	}, nil
}

func canonicalExecutable(path string) (string, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
		return "", ErrInvalidExecutable
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(absolute) {
		return "", ErrInvalidExecutable
	}
	return filepath.Clean(absolute), nil
}

func validateSnapshot(value snapshot) error {
	if value.startedAt.seconds == 0 || value.startedAt.microseconds >= 1_000_000 || value.executable == "" {
		return ErrInvalidProcessState
	}
	return nil
}

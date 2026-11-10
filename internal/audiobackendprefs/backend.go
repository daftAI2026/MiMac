// Package audiobackendprefs stores the host's optional compatibility audio
// backend choice. It owns only one validated value; session readiness remains
// in MiMac's existing live-audio and device lifecycle.
package audiobackendprefs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const CompatibilityBackend = "mimac-compat"

const (
	fileVersion = 1
	maxFileSize = 4 * 1024
)

type Startup struct {
	Backend string
	Exit    bool
}

type document struct {
	Version int    `json:"version"`
	Backend string `json:"backend,omitempty"`
}

func Validate(backend string) error {
	if backend != "" && backend != CompatibilityBackend {
		return fmt.Errorf("audio backend must be %s or empty", CompatibilityBackend)
	}
	return nil
}

// Read returns the fail-closed default when the sidecar or its optional backend
// member is absent. Malformed, oversized, or unknown values are errors.
func Read(path string) (string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileSize {
		return "", errors.New("invalid audio backend preference file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxFileSize {
		return "", errors.New("audio backend preference file too large")
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value document
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return "", errors.New("trailing audio backend preference data")
	}
	if value.Version != fileVersion {
		return "", errors.New("unsupported audio backend preference version")
	}
	if err := Validate(value.Backend); err != nil {
		return "", err
	}
	return value.Backend, nil
}

// Write atomically replaces only this module's sidecar. A failed write leaves
// an existing value intact; it never touches host preferences or tools.json.
func Write(path, backend string) error {
	if err := Validate(backend); err != nil {
		return err
	}
	data, err := json.MarshalIndent(document{Version: fileVersion, Backend: backend}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".audio-backend-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Prepare resolves the startup backend before the caller initializes App.
// A set command writes the sidecar and returns Exit=true. Explicit runtime
// overrides bypass stored-file reads; absent overrides fail on invalid storage.
func Prepare(path, override string, overrideProvided bool, setBackend string, setProvided bool) (Startup, error) {
	if overrideProvided && setProvided {
		return Startup{}, errors.New("set-audio-backend cannot be combined with audio-backend")
	}
	if setProvided {
		return Startup{Exit: true}, Write(path, setBackend)
	}
	if overrideProvided {
		if err := Validate(override); err != nil {
			return Startup{}, err
		}
		return Startup{Backend: override}, nil
	}
	saved, err := Read(path)
	if err != nil {
		return Startup{}, err
	}
	return Startup{Backend: saved}, nil
}

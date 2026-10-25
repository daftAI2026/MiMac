// Package voicetools contains small, read-only discovery contracts shared by
// voice-tool adapters.
package voicetools

import (
	"errors"
	"os"
	"path/filepath"
)

type State string

const (
	Ready         State = "ready"
	NotFound      State = "not_found"
	NotRunning    State = "not_running"
	ConfigMissing State = "config_missing"
	Unsupported   State = "unsupported"
	ReadError     State = "read_error"
	Ambiguous     State = "ambiguous"
)

type Candidate struct {
	Raw       string
	Shortcut  string
	Supported bool
}

type Action struct {
	ID         string
	Name       string
	Scope      string
	Gesture    string
	Disabled   bool // An explicit configuration override disables the action's shortcut.
	Candidates []Candidate
	Control    *Control // Nil until the adapter has an explicit command contract.
}

type Snapshot struct {
	State        State
	Version      string
	AppPath      string
	SettingsPath string
	Source       string
	Actions      []Action
}

type Locator struct {
	Home            string
	ApplicationDirs []string
	InputMethodDirs []string

	// discoverSystemApplications is enabled only by DefaultLocator. Tests and
	// adapters using a constructed Locator remain isolated from host installs.
	discoverSystemApplications bool
}

func DefaultLocator() (Locator, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return Locator{}, errors.New("voice tools: home directory unavailable")
	}

	return Locator{
		Home:                       home,
		ApplicationDirs:            []string{filepath.Join(home, "Applications"), "/Applications"},
		InputMethodDirs:            []string{filepath.Join(home, "Library", "Input Methods"), "/Library/Input Methods"},
		discoverSystemApplications: true,
	}, nil
}

// ApplicationCandidates builds exact, shallow bundle paths in directory order.
// It never recursively scans a directory tree.
func ApplicationCandidates(dirs []string, bundleNames ...string) []string {
	out := make([]string, 0, len(dirs)*len(bundleNames))
	seen := make(map[string]struct{}, cap(out))
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		for _, name := range bundleNames {
			if name == "" || filepath.Base(name) != name {
				continue
			}
			path := filepath.Clean(filepath.Join(dir, name))
			if _, exists := seen[path]; exists {
				continue
			}
			seen[path] = struct{}{}
			out = append(out, path)
		}
	}
	return out
}

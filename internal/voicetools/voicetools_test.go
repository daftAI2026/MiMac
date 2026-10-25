package voicetools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaultLocatorOrdersUserPathsBeforeSystemPaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	t.Setenv("CODEX_HOME", codexHome)

	locator, err := DefaultLocator()
	if err != nil {
		t.Fatal(err)
	}
	if locator.Home != home {
		t.Fatalf("home = %q, want %q", locator.Home, home)
	}
	if got, want := locator.ApplicationDirs, []string{filepath.Join(home, "Applications"), "/Applications"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("application dirs = %#v, want %#v", got, want)
	}
	if got, want := locator.InputMethodDirs, []string{filepath.Join(home, "Library", "Input Methods"), "/Library/Input Methods"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("input method dirs = %#v, want %#v", got, want)
	}
	if !locator.discoverSystemApplications {
		t.Fatal("DefaultLocator must enable system application discovery")
	}
}

func TestApplicationCandidatesAreExactShallowPaths(t *testing.T) {
	dirs := []string{"/user/Applications", "/Applications", "/Applications"}
	got := ApplicationCandidates(dirs, "Typeless.app", "../wrong.app", "ChatGPT.app")
	want := []string{
		"/user/Applications/Typeless.app",
		"/user/Applications/ChatGPT.app",
		"/Applications/Typeless.app",
		"/Applications/ChatGPT.app",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidate paths = %#v, want %#v", got, want)
	}
}

func TestReadRegularFileHonorsCancellationAndSizeBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.json")
	data := []byte("[{\"command\":\"realtimeVoice\",\"key\":\"Ctrl+K\"}]")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ReadRegularFile(context.Background(), path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("read = %q, want %q", got, data)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadRegularFile(ctx, path, 1024); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read error = %v, want context.Canceled", err)
	}
	if _, err := ReadRegularFile(context.Background(), path, 8); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("oversized read error = %v, want ErrFileTooLarge", err)
	}
	if _, err := ReadRegularFile(context.Background(), filepath.Join(t.TempDir(), "missing.json"), 1024); !errors.Is(err, ErrFileMissing) {
		t.Fatalf("missing read error = %v, want ErrFileMissing", err)
	}
}

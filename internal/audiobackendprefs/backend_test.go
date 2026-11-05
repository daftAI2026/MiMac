package audiobackendprefs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadMissingAndMissingBackendFieldDefaultDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio-backend.json")
	if got, err := Read(path); err != nil || got != "" {
		t.Fatalf("missing file: got %q, err %v; want empty", got, err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := Read(path); err != nil || got != "" {
		t.Fatalf("missing backend field: got %q, err %v; want empty", got, err)
	}
}

func TestWriteAndReadCompatibilityValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "MiMac", "audio-backend.json")
	if err := Write(path, CompatibilityBackend); err != nil {
		t.Fatal(err)
	}
	if got, err := Read(path); err != nil || got != CompatibilityBackend {
		t.Fatalf("round trip: got %q, err %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("preference mode = %04o, want 0600", got)
	}
}

func TestReadRejectsUnknownBackendAndVersion(t *testing.T) {
	for _, raw := range []string{
		`{"version":1,"backend":"future-backend"}`,
		`{"version":2,"backend":"mimac-compat"}`,
		`{"version":1,"unexpected":true}`,
		`{"version":1} {"version":1}`,
	} {
		path := filepath.Join(t.TempDir(), "audio-backend.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := Read(path); err == nil || got != "" {
			t.Fatalf("Read(%s) = %q, %v; want fail-closed error", raw, got, err)
		}
	}
}

func TestPrepareHonorsExplicitEmptyAndCompatOverrides(t *testing.T) {
	cases := []struct {
		name, saved, override, want string
		provided                    bool
	}{
		{name: "saved on without override", saved: CompatibilityBackend, want: CompatibilityBackend},
		{name: "saved off without override", saved: "", want: ""},
		{name: "explicit empty clears for this run", saved: CompatibilityBackend, override: "", provided: true, want: ""},
		{name: "explicit compat enables for this run", saved: "", override: CompatibilityBackend, provided: true, want: CompatibilityBackend},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audio-backend.json")
			if err := Write(path, tc.saved); err != nil {
				t.Fatal(err)
			}
			startup, err := Prepare(path, tc.override, tc.provided, "", false)
			got := startup.Backend
			if err != nil || got != tc.want {
				t.Fatalf("Prepare() = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestExplicitEmptySetCommandClearsSavedBackend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio-backend.json")
	if err := Write(path, CompatibilityBackend); err != nil {
		t.Fatal(err)
	}
	startup, err := Prepare(path, "", false, "", true)
	if err != nil || !startup.Exit {
		t.Fatalf("explicit empty set = handled %t, err %v", startup.Exit, err)
	}
	if got, err := Read(path); err != nil || got != "" {
		t.Fatalf("explicit empty set left backend %q, err %v", got, err)
	}
	if _, err := Prepare(path, "", false, "unknown", true); err == nil {
		t.Fatal("set command accepted an unknown backend")
	}
	if got, err := Read(path); err != nil || got != "" {
		t.Fatalf("invalid set changed the disabled choice: got %q, err %v", got, err)
	}
}

func TestPrepareSetCommandRequestsEarlyExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio-backend.json")
	selection, err := Prepare(path, "", false, CompatibilityBackend, true)
	if err != nil || !selection.Exit || selection.Backend != "" {
		t.Fatalf("Prepare(set) = %+v, %v; want early exit without runtime backend", selection, err)
	}
	if got, err := Read(path); err != nil || got != CompatibilityBackend {
		t.Fatalf("setter did not persist its value: got %q, err %v", got, err)
	}
}

func TestPrepareRejectsConflictingCommandsAndInvalidOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio-backend.json")
	for _, tc := range []struct {
		override string
		set      bool
	}{
		{override: CompatibilityBackend, set: true},
		{override: "unknown"},
	} {
		startup, err := Prepare(path, tc.override, true, CompatibilityBackend, tc.set)
		if err == nil || startup != (Startup{}) {
			t.Fatalf("invalid command produced %+v, %v", startup, err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("invalid command wrote sidecar: %v", err)
		}
	}
}

func TestExplicitSetRepairsOnlyOwnedSidecar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audio-backend.json")
	other := filepath.Join(dir, "preferences.json")
	for _, file := range []string{path, other} {
		if err := os.WriteFile(file, []byte("invalid json"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if startup, err := Prepare(path, "", false, CompatibilityBackend, true); err != nil || !startup.Exit {
		t.Fatalf("explicit repair: %+v, %v", startup, err)
	}
	if got, err := Read(path); err != nil || got != CompatibilityBackend {
		t.Fatalf("repaired value = %q, %v", got, err)
	}
	if got, err := os.ReadFile(other); err != nil || string(got) != "invalid json" {
		t.Fatalf("unrelated preferences changed: %q, %v", got, err)
	}
}

func TestPrepareRuntimeOverrideDoesNotReadOrPersistSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(path, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	selection, err := Prepare(path, CompatibilityBackend, true, "", false)
	if err != nil || selection.Exit || selection.Backend != CompatibilityBackend {
		t.Fatalf("Prepare(explicit override) = %+v, %v", selection, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "not json" {
		t.Fatalf("runtime override modified stored preference: %q, %v", got, err)
	}
}

func TestPrepareStoredReadFailureDoesNotFallBackToCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio-backend.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"backend":"future"}`), 0600); err != nil {
		t.Fatal(err)
	}
	selection, err := Prepare(path, "", false, "", false)
	if err == nil || selection.Backend != "" || selection.Exit {
		t.Fatalf("Prepare(invalid stored choice) = %+v, %v; want an error and no backend", selection, err)
	}
}

func TestWriteFailureLeavesExistingDirectoryTargetIntact(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "audio-backend.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "keep")
	if err := os.WriteFile(marker, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, CompatibilityBackend); err == nil {
		t.Fatal("Write succeeded while target was a directory")
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "preserve" {
		t.Fatalf("failed atomic replacement changed target: got %q, err %v", got, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "audio-backend.json" {
		t.Fatalf("failed atomic replacement left temporary files: %v", entries)
	}
}

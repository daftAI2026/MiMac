package hex

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"mimac.local/mimac/internal/voicetools"
)

func TestLocateApplicationAcceptsStableAndTestIdentitiesWithoutVersionGate(t *testing.T) {
	requireDarwinBundleTests(t)
	for _, sample := range []struct {
		name, bundleName, identifier, version string
	}{
		{"stable", bundleName, bundleIdentifier, "0.0.0-dev-742"},
		{"test", testBundleName, testBundleIdentifier, "98.765-test"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			root := t.TempDir()
			app := filepath.Join(root, sample.bundleName)
			writeHexTestBundle(t, app, sample.identifier, sample.version)
			locator := voicetools.Locator{ApplicationDirs: []string{root}}

			bundle, state, err := LocateApplication(context.Background(), locator)
			if err != nil {
				t.Fatal(err)
			}
			if state != voicetools.Ready || bundle.Path != app || bundle.Identifier != sample.identifier || bundle.Version != sample.version {
				t.Fatalf("bundle/state = %#v/%q, want exact official identity", bundle, state)
			}
		})
	}
}

func TestLocateApplicationPrefersStableWhenTestBuildIsAlsoInstalled(t *testing.T) {
	requireDarwinBundleTests(t)
	root := t.TempDir()
	stable := filepath.Join(root, bundleName)
	testBuild := filepath.Join(root, testBundleName)
	writeHexTestBundle(t, stable, bundleIdentifier, "2.4.0")
	writeHexTestBundle(t, testBuild, testBundleIdentifier, "0.0.0-dev-742")

	bundle, state, err := LocateApplication(context.Background(), voicetools.Locator{ApplicationDirs: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	if state != voicetools.Ready || bundle.Path != stable || bundle.Identifier != bundleIdentifier {
		t.Fatalf("bundle/state = %#v/%q, want stable Hex when both identities are present", bundle, state)
	}
}

func TestLocateApplicationReportsDuplicateStableCopiesAsAmbiguous(t *testing.T) {
	requireDarwinBundleTests(t)
	userApps, systemApps := t.TempDir(), t.TempDir()
	user := filepath.Join(userApps, bundleName)
	system := filepath.Join(systemApps, bundleName)
	writeHexTestBundle(t, user, bundleIdentifier, "2.4.0")
	writeHexTestBundle(t, system, bundleIdentifier, "2.4.0")

	bundle, state, err := LocateApplication(context.Background(), voicetools.Locator{ApplicationDirs: []string{userApps, systemApps}})
	if err != nil {
		t.Fatal(err)
	}
	if state != voicetools.Ambiguous || bundle.Path != "" {
		t.Fatalf("bundle/state = %#v/%q, want ambiguous", bundle, state)
	}
}

func TestLocateApplicationDistinguishesMissingFromWrongIdentity(t *testing.T) {
	requireDarwinBundleTests(t)
	root := t.TempDir()
	locator := voicetools.Locator{ApplicationDirs: []string{root}}
	if _, state, err := LocateApplication(context.Background(), locator); err != nil || state != voicetools.NotFound {
		t.Fatalf("missing app state/error = %q/%v, want not_found", state, err)
	}
	writeHexTestBundle(t, filepath.Join(root, bundleName), "someone.elses.hex", "2.4.0")
	if _, state, err := LocateApplication(context.Background(), locator); err != nil || state != voicetools.Unsupported {
		t.Fatalf("wrong identity state/error = %q/%v, want unsupported", state, err)
	}
}

func requireDarwinBundleTests(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("Hex app bundle discovery uses macOS bundle metadata")
	}
}

func writeHexTestBundle(t *testing.T, path, identifier, version string) {
	t.Helper()
	info := filepath.Join(path, "Contents")
	if err := os.MkdirAll(info, 0o700); err != nil {
		t.Fatal(err)
	}
	plist := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
		"<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n" +
		"<plist version=\"1.0\"><dict>" +
		"<key>CFBundleIdentifier</key><string>" + identifier + "</string>" +
		"<key>CFBundleShortVersionString</key><string>" + version + "</string>" +
		"<key>CFBundleVersion</key><string>build-1</string>" +
		"</dict></plist>"
	if err := os.WriteFile(filepath.Join(info, "Info.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
}

//go:build darwin

package voicetools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeTestBundle(t *testing.T, path, identifier, version string) {
	t.Helper()
	infoDir := filepath.Join(path, "Contents")
	if err := os.MkdirAll(infoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	plist := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
		"<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n" +
		"<plist version=\"1.0\"><dict>" +
		"<key>CFBundleIdentifier</key><string>" + identifier + "</string>" +
		"<key>CFBundleShortVersionString</key><string>" + version + "</string>" +
		"<key>CFBundleVersion</key><string>build-1</string>" +
		"</dict></plist>"
	if err := os.WriteFile(filepath.Join(infoDir, "Info.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFindBundleVerifiesIdentityAndRejectsWrongID(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "Typeless.app")
	writeTestBundle(t, app, "wrong.identifier", "2.8.3")

	_, state, err := FindBundle(context.Background(), []string{app}, "now.typeless.desktop")
	if err != nil {
		t.Fatal(err)
	}
	if state != Unsupported {
		t.Fatalf("state = %q, want unsupported for a wrong bundle ID", state)
	}
}

func TestFindBundleReportsMultipleValidCopiesAmbiguous(t *testing.T) {
	userApp := filepath.Join(t.TempDir(), "Applications", "Typeless.app")
	systemApp := filepath.Join(t.TempDir(), "Applications", "Typeless.app")
	writeTestBundle(t, userApp, "now.typeless.desktop", "2.8.3")
	writeTestBundle(t, systemApp, "now.typeless.desktop", "2.8.3")

	bundle, state, err := FindBundle(context.Background(), []string{userApp, systemApp}, "now.typeless.desktop")
	if err != nil {
		t.Fatal(err)
	}
	if state != Ambiguous || bundle.Path != "" {
		t.Fatalf("bundle/state = %#v/%q, want no selected bundle and ambiguous", bundle, state)
	}
}

func TestFindBundleReturnsVerifiedPathAndVersion(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Typeless.app")
	writeTestBundle(t, app, "now.typeless.desktop", "2.8.3")

	bundle, state, err := FindBundle(context.Background(), []string{app}, "now.typeless.desktop")
	if err != nil {
		t.Fatal(err)
	}
	if state != Ready || bundle.Path != app || bundle.Identifier != "now.typeless.desktop" ||
		bundle.Version != "2.8.3" || bundle.Build != "build-1" {
		t.Fatalf("bundle/state = %#v/%q, want verified bundle", bundle, state)
	}
}

func TestFindBundleDeduplicatesRepeatedAndCleanEquivalentPaths(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Typeless.app")
	alias := filepath.Join(t.TempDir(), "Linked Typeless.app")
	writeTestBundle(t, app, "now.typeless.desktop", "2.8.3")
	if err := os.Symlink(app, alias); err != nil {
		t.Fatal(err)
	}

	bundle, state, err := FindBundle(context.Background(), []string{app, filepath.Join(filepath.Dir(app), ".", filepath.Base(app)), alias}, "now.typeless.desktop")
	if err != nil {
		t.Fatal(err)
	}
	if state != Ready || bundle.Path != app {
		t.Fatalf("bundle/state = %#v/%q, want one ready match despite duplicate paths", bundle, state)
	}
}

func TestFindBundleInLocatorFindsRegisteredBundleOutsideKnownFolders(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Portable Install", "Typeless.app")
	writeTestBundle(t, app, "now.typeless.desktop", "2.8.3")
	locator := Locator{discoverSystemApplications: true}
	calls := 0
	discover := func(_ context.Context, identifier string) ([]string, error) {
		calls++
		if identifier != "now.typeless.desktop" {
			t.Fatalf("bundle identifier = %q, want the expected identifier", identifier)
		}
		return []string{app, filepath.Join(filepath.Dir(app), ".", filepath.Base(app))}, nil
	}

	bundle, state, err := findBundleInLocator(context.Background(), locator, []string{filepath.Join(t.TempDir(), "Applications", "Typeless.app")}, "now.typeless.desktop", discover)
	if err != nil {
		t.Fatal(err)
	}
	if state != Ready || bundle.Path != app || bundle.Identifier != "now.typeless.desktop" || bundle.Version != "2.8.3" || bundle.Build != "build-1" {
		t.Fatalf("bundle/state = %#v/%q, want verified non-standard install", bundle, state)
	}
	if calls != 1 {
		t.Fatalf("system discovery calls = %d, want 1", calls)
	}
}

func TestFindBundleInLocatorDoesNotQuerySystemForCustomLocator(t *testing.T) {
	calls := 0
	discover := func(context.Context, string) ([]string, error) {
		calls++
		return nil, nil
	}

	_, state, err := findBundleInLocator(context.Background(), Locator{}, nil, "now.typeless.desktop", discover)
	if err != nil {
		t.Fatal(err)
	}
	if state != NotFound || calls != 0 {
		t.Fatalf("state/calls = %q/%d, want not_found and no system query", state, calls)
	}
}

func TestFindBundleInLocatorRejectsWrongIdentityFromSystemCandidate(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Portable Install", "Typeless.app")
	writeTestBundle(t, app, "wrong.identifier", "2.8.3")
	locator := Locator{discoverSystemApplications: true}
	discover := func(context.Context, string) ([]string, error) { return []string{app}, nil }

	bundle, state, err := findBundleInLocator(context.Background(), locator, nil, "now.typeless.desktop", discover)
	if err != nil {
		t.Fatal(err)
	}
	if state != Unsupported || bundle.Path != app {
		t.Fatalf("bundle/state = %#v/%q, want unsupported wrong identity", bundle, state)
	}
}

func TestFindBundleInLocatorReportsSeparateSystemCopyAsAmbiguous(t *testing.T) {
	known := filepath.Join(t.TempDir(), "Applications", "Typeless.app")
	registered := filepath.Join(t.TempDir(), "Volumes", "Portable", "Typeless.app")
	writeTestBundle(t, known, "now.typeless.desktop", "2.8.3")
	writeTestBundle(t, registered, "now.typeless.desktop", "2.8.3")
	locator := Locator{discoverSystemApplications: true}
	discover := func(context.Context, string) ([]string, error) { return []string{registered}, nil }

	bundle, state, err := findBundleInLocator(context.Background(), locator, []string{known}, "now.typeless.desktop", discover)
	if err != nil {
		t.Fatal(err)
	}
	if state != Ambiguous || bundle.Path != "" {
		t.Fatalf("bundle/state = %#v/%q, want ambiguous across known and registered copies", bundle, state)
	}
}

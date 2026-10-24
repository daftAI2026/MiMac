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

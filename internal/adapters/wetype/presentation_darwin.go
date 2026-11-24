//go:build darwin

package wetype

import (
	"os"
	"path/filepath"
	"strings"

	"howett.net/plist"
	"mimac.local/mimac/internal/adapters/windowlease"
	"mimac.local/mimac/internal/platform/processidentity"
)

func captureRecordingBarBaselineNative(pid int32, expectedExecutable string) *RecordingBarBaseline {
	if pid <= 0 || !isVerifiedWeTypeBundleExecutable(expectedExecutable) {
		return nil
	}
	identity, err := processidentity.Capture(pid, expectedExecutable)
	if err != nil {
		return nil
	}
	probe, err := windowlease.LoadDarwinProbe()
	if err != nil {
		return nil
	}
	baseline, err := windowlease.CaptureBaseline(pid, identity.Check, probe)
	if err != nil {
		return nil
	}
	return &RecordingBarBaseline{inner: baseline}
}

func isVerifiedWeTypeBundleExecutable(executable string) bool {
	if !filepath.IsAbs(executable) || strings.ContainsRune(executable, 0) {
		return false
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(executable))
	if err != nil {
		return false
	}
	macOSDir := filepath.Dir(canonical)
	contentsDir := filepath.Dir(macOSDir)
	appBundle := filepath.Dir(contentsDir)
	if filepath.Base(macOSDir) != "MacOS" || filepath.Base(contentsDir) != "Contents" || filepath.Ext(appBundle) != ".app" {
		return false
	}
	info, err := os.ReadFile(filepath.Join(contentsDir, "Info.plist"))
	if err != nil || len(info) == 0 || len(info) > 1024*1024 {
		return false
	}
	var fields map[string]any
	if _, err := plist.Unmarshal(info, &fields); err != nil {
		return false
	}
	return fields["CFBundleIdentifier"] == bundleIdentifier && fields["CFBundleExecutable"] == filepath.Base(canonical)
}

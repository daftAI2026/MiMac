//go:build darwin && arm64

package siriglass

import (
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestSupportedSystemDefersPrivateCompatibility(t *testing.T) {
	version, err := syscall.Sysctl("kern.osproductversion")
	if err != nil {
		t.Fatal(err)
	}
	majorText, _, _ := strings.Cut(strings.TrimSpace(version), ".")
	major, err := strconv.Atoi(majorText)
	if err != nil {
		t.Fatal(err)
	}
	if major < 26 {
		t.Skip("public native display API is unavailable")
	}
	if err := checkSupportedSystem(); err != nil {
		t.Fatalf("public API is available on %s; private capability validation must run in the native adapter: %v", version, err)
	}
}

func TestPublicDisplayVersionFloor(t *testing.T) {
	for _, version := range []string{"26.0", "26.4.1", "27.2.1", "30.0", " 27.2.1\n"} {
		if err := checkPublicDisplayVersion(version); err != nil {
			t.Errorf("%q: %v", version, err)
		}
	}
	for _, version := range []string{"25.9", "14.2", "", "unknown", "27x.0"} {
		if err := checkPublicDisplayVersion(version); err == nil {
			t.Errorf("accepted %q", version)
		}
	}
}

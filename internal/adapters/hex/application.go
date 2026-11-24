package hex

import (
	"context"
	"runtime"
	"strconv"
	"strings"

	"mimac.local/mimac/internal/voicetools"
)

const (
	bundleIdentifier     = "anomaly.hex"
	bundleName           = "Hex.app"
	testBundleIdentifier = "anomaly.hex.test"
	testBundleName       = "Hex Test.app"
	InstallURL           = "https://anoma.ly/hex"
)

// LocateApplication finds the official Hex bundle without launching it or
// touching its local API. Stable takes priority when both stable and test
// builds are installed; FindBundle still reports duplicate copies of either
// identity as ambiguous.
func LocateApplication(ctx context.Context, locator voicetools.Locator) (voicetools.Bundle, voicetools.State, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return voicetools.Bundle{}, voicetools.ReadError, err
	}
	if runtime.GOOS != "darwin" {
		return voicetools.Bundle{}, voicetools.Unsupported, nil
	}
	stable, stableState, err := findHexBundle(ctx, locator, bundleName, bundleIdentifier)
	if err != nil || stableState == voicetools.Ready || stableState == voicetools.Ambiguous || stableState == voicetools.ReadError {
		return stable, stableState, err
	}
	test, testState, err := findHexBundle(ctx, locator, testBundleName, testBundleIdentifier)
	if err != nil || testState == voicetools.Ready || testState == voicetools.Ambiguous || testState == voicetools.ReadError {
		return test, testState, err
	}
	if stableState == voicetools.Unsupported || testState == voicetools.Unsupported {
		return voicetools.Bundle{}, voicetools.Unsupported, nil
	}
	return voicetools.Bundle{}, voicetools.NotFound, nil
}

func findHexBundle(ctx context.Context, locator voicetools.Locator, name, identifier string) (voicetools.Bundle, voicetools.State, error) {
	candidates := voicetools.ApplicationCandidates(locator.ApplicationDirs, name)
	return voicetools.FindBundleInLocator(ctx, locator, candidates, identifier)
}

func discoveryDataBundle(identifier string) (string, bool) {
	switch identifier {
	case bundleIdentifier:
		return localAPIDataFolder, true
	case testBundleIdentifier:
		return "Library/Application Support/hex-test", true
	default:
		return "", false
	}
}

func isSupportedBundleIdentifier(identifier string) bool {
	_, ok := discoveryDataBundle(identifier)
	return ok
}

func discoveryAppPathForBundle(document Discovery, identifier string) string {
	if identifier == bundleIdentifier {
		return discoveryAppPath(document)
	}
	if identifier == testBundleIdentifier {
		return "hex-local-api:test:" + strconv.Itoa(int(document.Port)) + ":" + strconv.FormatUint(uint64(document.PID), 10)
	}
	return ""
}

func identifierForDiscoveryAppPath(path string) (string, bool) {
	parts := strings.Split(path, ":")
	// Preserve the original stable-build fingerprint for callers and saved
	// tests that captured it before test builds acquired their own data folder.
	if len(parts) == 3 && parts[0] == "hex-local-api" {
		return bundleIdentifier, true
	}
	if len(parts) != 4 || parts[0] != "hex-local-api" || parts[1] != "test" {
		return "", false
	}
	if _, err := strconv.ParseUint(parts[2], 10, 16); err != nil {
		return "", false
	}
	if _, err := strconv.ParseUint(parts[3], 10, 32); err != nil {
		return "", false
	}
	return testBundleIdentifier, true
}

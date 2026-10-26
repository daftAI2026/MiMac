package typeless

import (
	"context"
	"runtime"

	"mimac.local/mimac/internal/voicetools"
)

// LocateApplication finds the exact Typeless bundle without reading its user
// settings or starting the application.
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
	candidates := voicetools.ApplicationCandidates(locator.ApplicationDirs, bundleName)
	return voicetools.FindBundleInLocator(ctx, locator, candidates, bundleIdentifier)
}

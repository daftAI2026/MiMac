package chatgpt

import (
	"context"
	"runtime"

	"mimac.local/mimac/internal/voicetools"
)

// LocateApplication finds the ChatGPT/Codex bundle without reading its user
// keybindings or starting the application.
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
	return voicetools.FindBundleInLocator(ctx, locator, appCandidatePaths(locator), bundleIdentifier)
}

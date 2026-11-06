package main

import (
	"context"
	"path/filepath"

	"mimac.local/mimac/internal/adapters/wetype"
	"mimac.local/mimac/internal/voicetools"
)

// WeType prepares its input preference before device startup: changing it at
// Fn-down can race its warm/recording graph handoff. Presses only verify the
// preference. Device ownership, PCM delivery and retirement stay shared.
func wetypeAudioRouteFactory(ctx context.Context, config audioRouteConfig) (liveAudioRoute, error) {
	deps := defaultCompatAudioRouteDependencies()
	executablePath := filepath.Join(config.AppPath, "Contents", "MacOS", "WeType")
	deps = withCompatApplicationInspector(deps, "com.tencent.inputmethod.wetype", config.AppPath, executablePath)
	deps.readSelection = func(ctx context.Context, locator voicetools.Locator) (compatAudioSelection, error) {
		uid, err := wetype.ReadPreferredMicrophoneUID(ctx, locator)
		return compatAudioSelection{preferredUID: uid}, err
	}
	deps.requestSelection = wetype.RequestMicrophoneSelectionConfirmed
	deps.prepareSelection = wetype.RequestMicrophoneSelectionConfirmed
	deps.verifySelection = wetype.VerifyMicrophoneSelection
	return openLiveXiaomiRouteForTargetWithDependencies(ctx, config.Generation,
		filepath.Join(config.ResourcesPath, "libMiMacATVV.dylib"),
		compatAudioRouteTarget{bundleID: "com.tencent.inputmethod.wetype", bundleName: "WeType.app", executableName: "WeType", name: "WeType"},
		executablePath, config.Locator, deps)
}

package main

import (
	"context"
	"path/filepath"

	"mimac.local/mimac/internal/adapters/wetype"
	"mimac.local/mimac/internal/voicetools"
)

const compatWeTypeBundleID = "com.tencent.inputmethod.wetype"

// WeType prepares its input preference before device startup: changing it at
// Fn-down can race its warm/recording graph handoff. Presses only verify the
// preference. Device ownership, PCM delivery and retirement stay shared.
func wetypeAudioRouteFactory(ctx context.Context, config audioRouteConfig) (liveAudioRoute, error) {
	deps := defaultCompatAudioRouteDependencies()
	executablePath := filepath.Join(config.AppPath, "Contents", "MacOS", "WeType")
	deps = withCompatApplicationInspector(deps, compatWeTypeBundleID, config.AppPath, executablePath)
	deps.readSelection = func(ctx context.Context, locator voicetools.Locator) (compatAudioSelection, error) {
		uid, err := wetype.ReadPreferredMicrophoneUID(ctx, locator)
		return compatAudioSelection{preferredUID: uid}, err
	}
	deps.requestSelection = wetype.RequestMicrophoneSelectionConfirmed
	deps.prepareSelection = wetype.RequestMicrophoneSelectionConfirmed
	deps.verifySelection = wetype.VerifyMicrophoneSelection
	return openLiveXiaomiRouteForTargetWithDependencies(ctx, config.Generation,
		filepath.Join(config.ResourcesPath, "libMiMacATVV.dylib"),
		compatAudioRouteTarget{bundleID: compatWeTypeBundleID, bundleName: "WeType.app", executableName: "WeType", name: "WeType"},
		executablePath, config.Locator, deps)
}

func newWeTypeVoicePromptSuppressor(pid int32, executable string) voicePromptSuppressor {
	baseline := wetype.CaptureRecordingBarBaseline(pid, executable)
	if baseline == nil {
		return nil
	}
	return suppressRecordingWindow(func(ctx context.Context) (voicePromptLease, string, error) {
		lease, outcome, err := baseline.Suppress(ctx)
		if lease == nil {
			return nil, string(outcome), err
		}
		return lease, string(outcome), err
	})
}

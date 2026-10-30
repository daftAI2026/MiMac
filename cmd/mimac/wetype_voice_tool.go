package main

import (
	"context"
	"errors"
	"path/filepath"

	"mimac.local/mimac/internal/adapters/wetype"
	"mimac.local/mimac/internal/platform/runningapp"
	"mimac.local/mimac/internal/voicetools"
)

// WeType only supplies its identity and preference transport. Device ownership,
// PCM delivery, exact receiver UID checks and retirement use the shared route.
func wetypeAudioRouteFactory(ctx context.Context, config audioRouteConfig) (liveAudioRoute, error) {
	deps := defaultCompatAudioRouteDependencies()
	executablePath := filepath.Join(config.AppPath, "Contents", "MacOS", "WeType")
	nativeInspector := deps.newInspector
	deps.newInspector = func() (compatAudioInspector, error) {
		inspector, err := nativeInspector()
		if err != nil {
			return nil, err
		}
		return &compatApplicationInspector{
			compatAudioInspector: inspector,
			bundleID:             "com.tencent.inputmethod.wetype",
			resolve: func(ctx context.Context) (int32, error) {
				pid, err := runningapp.Find(ctx, "com.tencent.inputmethod.wetype", config.AppPath, executablePath)
				if errors.Is(err, runningapp.ErrNotRunning) {
					return 0, nil
				}
				return pid, err
			},
		}, nil
	}
	deps.readSelection = func(ctx context.Context, locator voicetools.Locator) (compatAudioSelection, error) {
		uid, err := wetype.ReadPreferredMicrophoneUID(ctx, locator)
		return compatAudioSelection{preferredUID: uid}, err
	}
	deps.requestSelection = wetype.RequestMicrophoneSelection
	return openLiveXiaomiRouteForTargetWithDependencies(ctx, config.Generation,
		filepath.Join(config.ResourcesPath, "libMiMacATVV.dylib"),
		compatAudioRouteTarget{bundleID: "com.tencent.inputmethod.wetype", bundleName: "WeType.app", executableName: "WeType", name: "WeType"},
		executablePath, config.Locator, deps)
}

package main

import (
	"context"
	"path/filepath"
)

func (recovery *doubaoEditorRecovery) audioRouteFactory(ctx context.Context, config audioRouteConfig) (liveAudioRoute, error) {
	return openLiveXiaomiRoute(
		ctx,
		config.Generation,
		filepath.Join(config.ResourcesPath, "libMiMacATVV.dylib"),
		compatDoubaoBundleID,
		config.AppPath,
		filepath.Join(config.AppPath, "Contents", "MacOS", compatDoubaoExecutableName),
		recovery,
		config.Locator,
	)
}

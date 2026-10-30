package main

import (
	"context"
	"path/filepath"
)

func doubaoAudioRouteFactory(ctx context.Context, config audioRouteConfig) (liveAudioRoute, error) {
	return openLiveXiaomiRoute(
		ctx,
		config.Generation,
		filepath.Join(config.ResourcesPath, "libMiMacATVV.dylib"),
		compatDoubaoBundleID,
		filepath.Join(config.AppPath, "Contents", "MacOS", compatDoubaoExecutableName),
		config.Locator,
	)
}

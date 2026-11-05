package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/adapters/applespeech"
	"mimac.local/mimac/internal/voicetools"
)

const appleSpeechToolID = "voice-apple-speech"
const appleSpeechCleanupPendingSource = "apple-speech:cleanup-pending"

func appleSpeechAudioRouteFactory(_ context.Context, config audioRouteConfig) (liveAudioRoute, error) {
	return openAppleAudioRoute(
		config.Generation,
		filepath.Join(config.ResourcesPath, "libMiMacATVV.dylib"),
		filepath.Join(config.ResourcesPath, "libMiMacSpeech.dylib"),
	)
}

// Discovery retains a partial owner if native cancellation has not joined.
// A later explicit check retries cleanup before preparing another model.
var appleModelProbe struct {
	sync.Mutex
	pending *applespeech.Sink
}

func discoverAppleSpeech(ctx context.Context, _ voicetools.Locator) voicetools.Snapshot {
	return inspectAppleSpeech(ctx, false)
}

func prepareAppleSpeechAssets(ctx context.Context, _ voicetools.Locator) voicetools.Snapshot {
	return inspectAppleSpeech(ctx, true)
}

func inspectAppleSpeech(ctx context.Context, install bool) voicetools.Snapshot {
	result := voicetools.Snapshot{State: voicetools.ReadError, Source: "apple-speech:zh-CN"}
	if !appleModelProbe.TryLock() {
		return result
	}
	defer appleModelProbe.Unlock()
	if appleModelProbe.pending != nil {
		if err := appleModelProbe.pending.Close(); err != nil {
			if errors.Is(err, applespeech.ErrCleanupPending) {
				result.Source = appleSpeechCleanupPendingSource
			}
			return result
		}
		appleModelProbe.pending = nil
	}
	resources, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return result
	}
	limit := 10 * time.Second
	if install {
		limit = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	sink, err := applespeech.Open(ctx, applespeech.Config{LibraryPath: filepath.Join(resources, "libMiMacSpeech.dylib"), Locale: "zh-CN", SampleRate: 16000, InstallAssets: install})
	if sink != nil {
		closeErr := sink.Close()
		if closeErr != nil {
			appleModelProbe.pending = sink
		}
		err = errors.Join(err, closeErr)
	}
	if errors.Is(err, applespeech.ErrCleanupPending) {
		result.Source = appleSpeechCleanupPendingSource
	}
	switch {
	case err == nil:
		result.State = voicetools.Ready
	case errors.Is(err, applespeech.ErrUnsupported):
		result.State = voicetools.Unsupported
	case errors.Is(err, applespeech.ErrAssetsUnavailable):
		result.State = voicetools.ConfigMissing
	}
	return result
}

func (s *shell) appleVoiceSelected() bool {
	if s.currentVoiceTool != appleSpeechToolID {
		return false
	}
	for _, tool := range s.tools {
		if tool.id == appleSpeechToolID {
			return tool.active && !tool.busy && tool.snapshot.State == voicetools.Ready
		}
	}
	return false
}

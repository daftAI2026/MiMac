package main

import (
	"context"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/voicetools"
)

type voiceToolIcon struct {
	path, version string
	bitmap        *ui.Bitmap
	cancel        context.CancelFunc
}

func (s *shell) voiceToolArtwork(c *ui.Context, a appearance, tool *voiceTool) {
	icon := s.voiceToolIcon(tool)
	if icon == nil {
		icon = s.systemSymbol("app", a.theme.Dark)
	}
	if icon != nil {
		ui.Image(c, icon).Width(settingsIconSize).Height(settingsIconSize).Fit(ui.Contain).Shrink(0)
	}
}

// Cache only view assets. Discovery owns application identity and location;
// MyGo asks the system for its icon without launching the application.
func (s *shell) voiceToolIcon(tool *voiceTool) *ui.Bitmap {
	cached := s.voiceToolIcons[tool.id]
	if tool.busy {
		if cached != nil {
			return cached.bitmap
		}
		return nil
	}
	if s.voiceToolIcons == nil {
		s.voiceToolIcons = make(map[string]*voiceToolIcon)
	}
	if !tool.active {
		if cached == nil {
			cached = &voiceToolIcon{bitmap: loadVoiceToolIcon("", tool.fallbackIconPNG)}
			s.voiceToolIcons[tool.id] = cached
			s.locateVoiceToolIcon(tool, cached)
		}
		return cached.bitmap
	}

	path, version := tool.snapshot.AppPath, tool.snapshot.Version
	if cached != nil && cached.cancel == nil && cached.path == path && cached.version == version {
		return cached.bitmap
	}
	if cached != nil && cached.cancel != nil {
		cached.cancel()
	}
	cached = &voiceToolIcon{path: path, version: version, bitmap: loadVoiceToolIcon(path, tool.fallbackIconPNG)}
	s.voiceToolIcons[tool.id] = cached
	return cached.bitmap
}

// Disabled adapters expose only bundle metadata for artwork. Their configuration
// discovery and actions stay disabled. Late results belong to this view cache.
func (s *shell) locateVoiceToolIcon(tool *voiceTool, icon *voiceToolIcon) {
	if tool.locateApplication == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	icon.cancel = cancel
	locate, locator, id, fallback := tool.locateApplication, s.toolLocator, tool.id, tool.fallbackIconPNG
	go func() {
		defer cancel()
		bundle, state, err := locate(ctx, locator)
		mygo.RunOnMain(func() {
			if ctx.Err() != nil || s.closing || s.voiceToolIcons[id] != icon {
				return
			}
			icon.cancel = nil
			if err == nil && state == voicetools.Ready {
				icon.path, icon.version = bundle.Path, bundle.Version
				icon.bitmap = loadVoiceToolIcon(bundle.Path, fallback)
			}
			s.invalidateDeviceView()
		})
	}()
}

func loadVoiceToolIcon(path string, fallback []byte) *ui.Bitmap {
	if path != "" {
		if data, err := mygo.Shell.FileIcon(path, int(settingsIconSize*2)); err == nil {
			if bitmap, err := ui.DecodeBitmap(data); err == nil {
				return bitmap
			}
		}
	}
	if len(fallback) == 0 {
		return nil
	}
	bitmap, _ := ui.DecodeBitmap(fallback)
	return bitmap
}

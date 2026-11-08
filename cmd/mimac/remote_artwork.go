package main

import "github.com/egoist/mygo/ui"

// Artwork and its failure belong to the same profile and settings-window
// lifetime. A failed image must not overwrite keyboard mapping errors.
type modelArtwork struct {
	bitmap *ui.Bitmap
	failed bool
}

func (s *shell) loadModelArtwork(plugin devicePlugin) modelArtwork {
	if plugin.Artwork == nil {
		return modelArtwork{}
	}
	if asset, exists := s.modelAssets[plugin.Profile.ID]; exists {
		return asset
	}
	pixels, err := plugin.Artwork()
	asset := modelArtwork{failed: err != nil}
	if err == nil {
		asset.bitmap = ui.NewBitmap(pixels)
	}
	if s.modelAssets == nil {
		s.modelAssets = make(map[string]modelArtwork)
	}
	s.modelAssets[plugin.Profile.ID] = asset
	return asset
}

func (s *shell) mappingFooterError() string {
	if s.mappingError != "" {
		return s.mappingError
	}
	if s.modelAssets[s.activeProfile().ID].failed {
		return "遥控器外观无法加载"
	}
	return ""
}

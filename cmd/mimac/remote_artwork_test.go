package main

import (
	"image"
	"os"
	"testing"

	"mimac.local/mimac/internal/devices"
)

func TestArtworkFailureFollowsProfileWithoutOverwritingMappingError(t *testing.T) {
	calls := 0
	broken := devicePlugin{Profile: devices.Profile{ID: "broken"}, Artwork: func() (image.Image, error) {
		calls++
		return nil, os.ErrNotExist
	}}
	plain := devicePlugin{Profile: devices.Profile{ID: "plain"}}
	s := &shell{deviceProfiles: []devicePlugin{broken, plain}, selectedProfile: "broken"}
	if asset := s.loadModelArtwork(broken); !asset.failed || asset.bitmap != nil {
		t.Fatalf("failed artwork = %+v", asset)
	}
	if s.mappingFooterError() != "遥控器外观无法加载" || s.mappingError != "" {
		t.Fatalf("artwork failure entered mapping state: footer=%q mapping=%q", s.mappingFooterError(), s.mappingError)
	}
	s.selectedProfile = "plain"
	if asset := s.loadModelArtwork(plain); asset.failed || s.mappingFooterError() != "" {
		t.Fatal("previous profile's image error leaked into an image-free profile")
	}
	s.selectedProfile = "broken"
	s.loadModelArtwork(broken)
	if calls != 1 || s.mappingFooterError() == "" {
		t.Fatalf("returning profile lost its failure or retried on draw: calls=%d", calls)
	}
	s.mappingError = "无法读取按键配置，原文件已保留"
	s.loadModelArtwork(broken)
	if s.mappingFooterError() != s.mappingError {
		t.Fatal("image failure obscured the mapping load error")
	}
}

func TestSettingsReleaseRetiresFailedArtworkAndAllowsReload(t *testing.T) {
	calls := 0
	plugin := devicePlugin{Profile: devices.Profile{ID: "device"}, Artwork: func() (image.Image, error) {
		calls++
		return nil, os.ErrPermission
	}}
	s := &shell{deviceProfiles: []devicePlugin{plugin}, selectedProfile: "device"}
	s.loadModelArtwork(plugin)
	s.mappingError = "保存失败，仍使用原映射"
	s.releaseSettingsAssets()
	if s.modelAssets != nil || s.mappingFooterError() != s.mappingError {
		t.Fatal("window release retained artwork or discarded mapping state")
	}
	s.loadModelArtwork(plugin)
	if calls != 2 {
		t.Fatalf("next window did not reload artwork: calls=%d", calls)
	}
}

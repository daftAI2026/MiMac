package main

import (
	"testing"

	"mimac.local/mimac/internal/adapters/hex"
	"mimac.local/mimac/internal/voicetools"
)

func TestHexRegistrationOffersOfficialInstallAndBundleDiscovery(t *testing.T) {
	var tool *voiceTool
	for _, candidate := range builtInVoiceTools() {
		if candidate.id == hexStreamToolID {
			tool = candidate
			break
		}
	}
	if tool == nil {
		t.Fatal("Hex tool registration missing")
	}
	if tool.locateApplication == nil || tool.discover == nil {
		t.Fatal("Hex needs separate installed-bundle and running-service discovery")
	}
	if tool.installURL != hex.InstallURL {
		t.Fatalf("Hex install link = %q, want official link %q", tool.installURL, hex.InstallURL)
	}
}

func TestHexOnboardingDistinguishesInstallationFromServiceReadiness(t *testing.T) {
	var tool *voiceTool
	for _, candidate := range builtInVoiceTools() {
		if candidate.id == hexStreamToolID {
			tool = candidate
			break
		}
	}
	if tool == nil {
		t.Fatal("Hex tool registration missing")
	}
	s := &shell{}
	s.onboarding.toolLocations = map[string]voicetools.State{hexStreamToolID: voicetools.NotFound}
	status, selectable, installState := s.onboardingVoiceToolStatus(tool)
	if status != "未安装" || selectable || installState != voicetools.NotFound {
		t.Fatalf("missing install = %q, selectable=%v, state=%q", status, selectable, installState)
	}

	tool.active = true
	tool.snapshot.State = voicetools.NotFound
	if got := voiceToolStatus(tool); got != "未安装" {
		t.Fatalf("missing installation displayed as %q in settings", got)
	}
	tool.active = false

	s.onboarding.toolLocations[hexStreamToolID] = voicetools.Ready
	status, selectable, installState = s.onboardingVoiceToolStatus(tool)
	if status != "已安装，未启用" || !selectable || installState != voicetools.Ready {
		t.Fatalf("installed app = %q, selectable=%v, state=%q", status, selectable, installState)
	}

	tool.active = true
	tool.snapshot.State = voicetools.NotRunning
	status, selectable, installState = s.onboardingVoiceToolStatus(tool)
	if status != "已安装，打开 Hex 以连接听写服务" || selectable || installState != voicetools.NotRunning {
		t.Fatalf("installed but offline = %q, selectable=%v, state=%q", status, selectable, installState)
	}
}

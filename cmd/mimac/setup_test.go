package main

import (
	"fmt"
	"testing"

	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
)

func TestSetupActionsKeepPermissionAndConnectionFailuresSeparate(t *testing.T) {
	for _, tt := range []struct {
		name  string
		state settingsState
		err   error
		want  setupAction
	}{
		{"input", settingsState{deviceNeedsPermission: true}, nil, setupInputPermission},
		{"output", settingsState{outputNeedsPermission: true}, nil, setupOutputPermission},
		{"Bluetooth permission", settingsState{}, fmt.Errorf("route: %w", atvv.ErrBluetoothPermission), setupBluetoothPermission},
		{"Bluetooth off", settingsState{}, atvv.ErrBluetoothPoweredOff, setupBluetoothConnection},
		{"disconnected", settingsState{}, atvv.ErrRemoteNotConnected, setupBluetoothConnection},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &shell{settingsState: tt.state, liveAudio: liveAudioState{backend: compatibilityAudioBackend, audioFailure: tt.err}}
			if got := s.nextSetupAction(); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

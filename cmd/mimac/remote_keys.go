package main

import (
	"errors"
	"sort"
)

// The device adapter owns native suppression. Core only sees physical edges
// and frozen action plans; neither it nor a tool plugin edits OS mappings.
type remoteKeyReservation interface {
	UpdateKeys(buttonIDs []string) error
}

func (s *shell) customRemoteKeys(profile mappingProfile) []string {
	active := s.activeProfile()
	var buttons []string
	for _, button := range active.Buttons {
		if button.ID == s.voiceButtonID() {
			continue // The existing voice reservation always owns this source.
		}
		_, action := profile.ToolActions[active.ID][button.ID]
		if profile.Profiles[active.ID][button.ID] != "" || action {
			buttons = append(buttons, button.ID)
		}
	}
	sort.Strings(buttons)
	return buttons
}

func (s *shell) reconcileRemoteKeys() error {
	owner := s.remoteState.reservation
	if owner == nil {
		return nil // Saved intent is applied when the device is next enabled.
	}
	buttons := s.customRemoteKeys(s.profile)
	updater, ok := owner.(remoteKeyReservation)
	if !ok {
		if len(buttons) == 0 {
			return nil // Voice-only device adapters need no ordinary-key contract.
		}
		return errors.New("remote control: device does not support custom key takeover")
	}
	s.remoteState.reserveInFlight = true
	defer func() { s.remoteState.reserveInFlight = false }()
	return updater.UpdateKeys(buttons)
}

func (s *shell) mappingChangeAllowed() bool {
	if s.closing || s.powerInputBlocked() || s.remoteBusy() || s.remoteState.stopPending {
		return false
	}
	if s.coordinator == nil {
		return true
	}
	snapshot := s.coordinator.core.Snapshot()
	return len(snapshot.PressedButtons) == 0 && !snapshot.HasSession &&
		(s.coordinator.sink == nil || len(s.coordinator.sink.Held()) == 0)
}

// Persisted intent may outlive a native failure. Keep its owner reachable for
// cleanup, block new output, and do not silently fall back to additive input.
func (s *shell) applySavedRemoteKeys() bool {
	if err := s.reconcileRemoteKeys(); err != nil {
		s.remoteState.err = err
		s.mappingError = "配置已保存，请停用后重新启用遥控器"
		s.endShortcutRecording()
		s.invalidateDeviceView()
		return false
	}
	return true
}

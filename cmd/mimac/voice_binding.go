package main

import (
	"errors"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/voicetools"
)

func (s *shell) syncVoiceBinding() {
	s.voiceStartBlocked = ""
	if s.coordinator == nil {
		return
	}
	s.blockUnpreparedLiveAudio()
	s.coordinator.voiceButton = s.voiceButtonID()
	s.coordinator.voiceDeferralIdentity = s.voiceDeferralIdentity
	s.coordinator.discardDeferredVoice = s.discardDeferredVoiceSource
	s.coordinator.resolveLocalVoice = func() (bridge.Binding, bool, error) {
		if !s.directTextVoiceSelected() {
			return bridge.Binding{}, false, nil
		}
		if !s.remoteHasReservation() || s.audioSession == nil || s.audioResourcesReady == nil || !s.audioResourcesReady() || s.closing {
			return bridge.Binding{}, true, errors.New("local speech audio is not ready")
		}
		return bridge.NewAudioBinding(), true, nil
	}
	s.coordinator.resolveVoiceAction = func() (bridge.ActionPlan, error) {
		if !s.remoteHasReservation() {
			return bridge.ActionPlan{}, errors.New("voice key is not reserved")
		}
		return s.selectedVoiceAction()
	}
	s.coordinator.validateDeferredVoice()
}

func (s *shell) discardDeferredVoiceSource(plan bridge.ActionPlan) {
	if s == nil || s.coordinator == nil || s.coordinator.core == nil {
		return
	}
	connection := s.liveAudio.connection
	if connection == nil {
		return
	}
	snapshot := s.coordinator.core.Snapshot()
	tool := s.desiredLiveAudioTool()
	if !snapshot.Connected || snapshot.Generation != s.liveAudio.generation || snapshot.Generation != s.remoteState.reservationGeneration ||
		tool == nil || tool.id != plan.Spec().ToolID || tool.id != s.liveAudio.toolID || tool.snapshot.AppPath == "" || tool.snapshot.AppPath != s.liveAudio.appPath {
		return
	}
	connection.DiscardUnclaimedPress()
}

// voiceDeferralIdentity limits a held pending gesture to the same HID
// generation, reservation, selected action revision and installed target.
// A missing or failed route is eligible only while its existing bounded
// recovery path is active; it does not initiate another connection attempt.
func (s *shell) voiceDeferralIdentity() (deferredAudioRouteIdentity, bool) {
	if s == nil || s.closing || s.coordinator == nil || s.coordinator.core == nil || !s.remoteHasReservation() {
		return deferredAudioRouteIdentity{}, false
	}
	snapshot := s.coordinator.core.Snapshot()
	tool := s.desiredLiveAudioTool()
	if !snapshot.Connected || snapshot.Generation == 0 || snapshot.Generation != s.remoteState.reservationGeneration || tool == nil || tool.snapshot.AppPath == "" {
		return deferredAudioRouteIdentity{}, false
	}
	if connection := s.liveAudio.connection; connection != nil {
		if s.liveAudio.toolID != tool.id || s.liveAudio.appPath != tool.snapshot.AppPath || s.liveAudio.generation != snapshot.Generation {
			return deferredAudioRouteIdentity{}, false
		}
		if connection.Err() != nil && !s.liveAudio.recoveryAttempted && !s.liveAudio.retiring {
			return deferredAudioRouteIdentity{}, false
		}
	} else if !s.liveAudio.retiring && !s.liveAudio.recoveryAttempted {
		return deferredAudioRouteIdentity{}, false
	}
	return deferredAudioRouteIdentity{generation: snapshot.Generation, toolID: tool.id, appPath: tool.snapshot.AppPath}, true
}

// Selection reads the adapter's current, verified command contract. It never
// invents F5/Fn, opens a target, or starts an audio source. Core freezes the
// resulting plan at key-down, so a refresh cannot change the key-up action.
func (s *shell) selectedVoiceAction() (bridge.ActionPlan, error) {
	unavailable := errors.New("selected voice tool has no unambiguous dictation action")
	if s.closing || s.voiceButtonID() == "" {
		return bridge.ActionPlan{}, unavailable
	}
	var plans [2][]bridge.ActionPlan
	for _, tool := range s.tools {
		if tool.id != s.currentVoiceTool || !tool.active || tool.busy || tool.snapshot.State != voicetools.Ready {
			continue
		}
		for _, action := range tool.snapshot.Actions {
			if action.Control == nil {
				continue
			}
			control := action.Control
			rank := -1
			if control.Trigger == bridge.ActionHold && control.Request == bridge.RequestStart && control.ReleaseRequest == bridge.RequestFinish {
				rank = 0
			} else if control.Trigger == bridge.ActionTap && control.Request == bridge.RequestToggle {
				rank = 1
			}
			if rank < 0 {
				continue
			}
			for _, candidate := range action.Candidates {
				ref := voicetools.ActionRef{ToolID: tool.id, ActionID: action.ID, CandidateRaw: candidate.Raw}
				if plan, err := s.resolveToolAction(ref); err == nil {
					plans[rank] = append(plans[rank], plan)
				}
			}
		}
	}
	for _, group := range plans {
		if len(group) == 1 {
			return group[0], nil
		}
		if len(group) > 1 {
			return bridge.ActionPlan{}, unavailable
		}
	}
	return bridge.ActionPlan{}, unavailable
}

func (s *shell) voiceSelectionLabel() string {
	for _, tool := range s.tools {
		if tool.id == s.currentVoiceTool && tool.active {
			return tool.name
		}
	}
	return "选择语音工具"
}

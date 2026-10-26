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
	s.coordinator.resolveLocalVoice = func() (bridge.Binding, bool, error) {
		if !s.appleVoiceSelected() {
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

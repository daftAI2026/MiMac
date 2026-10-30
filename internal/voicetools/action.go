package voicetools

import (
	"errors"
	"strings"
	"unicode/utf8"

	"mimac.local/mimac/internal/adapters/keyoutput"
	"mimac.local/mimac/internal/bridge"
)

// ActionRef is a persistent selection, not a cached shortcut. A changed or
// missing candidate never silently selects a different shortcut.
type ActionRef struct {
	ToolID       string `json:"tool_id"`
	ActionID     string `json:"action_id"`
	CandidateRaw string `json:"candidate_raw"`
}

func (r ActionRef) Validate() error {
	for _, value := range []string{r.ToolID, r.ActionID, r.CandidateRaw} {
		if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || strings.ContainsRune(value, '\x00') {
			return errors.New("voice tools: invalid action reference")
		}
	}
	return nil
}

// Control is an adapter's explicit command contract. Candidate.Supported only
// describes key syntax; Gesture is display metadata, never executable policy.
// A request is not a target acknowledgement or an ASR result.
type Control struct {
	Trigger        bridge.ActionTrigger
	Request        bridge.TargetRequest
	ReleaseRequest bridge.TargetRequest
	AllowFnKey     bool // Adapter-specific opt-in; the zero value keeps Fn unsupported.
}

// ResolveAction copies the selected command into an immutable press plan.
// The host checks plugin/current-target availability. App scoped actions stay
// unavailable until a target-bound output adapter exists.
func ResolveAction(ref ActionRef, revision uint64, snapshot Snapshot) (bridge.ActionPlan, error) {
	unavailable := errors.New("voice tools: action unavailable")
	if ref.Validate() != nil || revision == 0 || snapshot.State != Ready {
		return bridge.ActionPlan{}, unavailable
	}
	var selected *Action
	for i := range snapshot.Actions {
		if snapshot.Actions[i].ID == ref.ActionID {
			if selected != nil {
				return bridge.ActionPlan{}, unavailable
			}
			selected = &snapshot.Actions[i]
		}
	}
	if selected == nil || selected.Disabled || selected.Scope != "global" || selected.Control == nil {
		return bridge.ActionPlan{}, unavailable
	}
	var candidate *Candidate
	for i := range selected.Candidates {
		if selected.Candidates[i].Raw == ref.CandidateRaw {
			if candidate != nil {
				return bridge.ActionPlan{}, unavailable
			}
			candidate = &selected.Candidates[i]
		}
	}
	if candidate == nil || !candidate.Supported {
		return bridge.ActionPlan{}, unavailable
	}
	control := *selected.Control
	keys, err := keyoutput.ParseShortcutWithOptions(candidate.Shortcut, keyoutput.ParseOptions{AllowFnKey: control.AllowFnKey})
	if err != nil {
		return bridge.ActionPlan{}, unavailable
	}
	return bridge.NewActionPlan(bridge.ActionSpec{
		ToolID: ref.ToolID, ActionID: ref.ActionID, CandidateRaw: ref.CandidateRaw, Revision: revision,
		Scope: bridge.ActionGlobal, Trigger: control.Trigger, Request: control.Request, ReleaseRequest: control.ReleaseRequest,
	}, keys...)
}

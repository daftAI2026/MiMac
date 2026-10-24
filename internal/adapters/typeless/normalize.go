package typeless

import (
	"strings"

	"mimac.local/mimac/internal/adapters/keyoutput"
)

// ActionID identifies one of the Typeless actions exposed by this adapter.
type ActionID string

const (
	ActionDictation           ActionID = "dictationMode"
	ActionAskAnything         ActionID = "askAnythingMode"
	ActionTranslation         ActionID = "translationMode"
	ActionPasteLastTranscript ActionID = "pasteLastTranscript"
)

// Candidate retains Typeless's original shortcut and, when supported by
// keyoutput, its canonical MiMac spelling.
type Candidate struct {
	Raw      string
	Shortcut string
	Status   Status
}

// Action is one known Typeless feature and its configured shortcut candidates.
type Action struct {
	ID         ActionID
	Label      string
	Status     Status
	Candidates []Candidate
}

// Normalize maps the four known Typeless settings to MiMac actions. It does
// not supply defaults for settings that are absent, null, or malformed.
func Normalize(snapshot Snapshot) []Action {
	bindings := snapshot.FeatureShortcutBindings
	return []Action{
		normalizeAction(ActionDictation, "听写", bindings.Status == StatusPresent, bindings.DictationMode),
		normalizeAction(ActionAskAnything, "Ask Anything", bindings.Status == StatusPresent, bindings.AskAnythingMode),
		normalizeAction(ActionTranslation, "翻译", bindings.Status == StatusPresent, bindings.TranslationMode),
		normalizeAction(ActionPasteLastTranscript, "粘贴上次转写", bindings.Status == StatusPresent, bindings.PasteLastTranscript),
	}
}

func normalizeAction(id ActionID, label string, parentPresent bool, binding ActionBinding) Action {
	action := Action{ID: id, Label: label, Status: binding.Status}
	if !parentPresent || binding.Status != StatusPresent {
		return action
	}
	if len(binding.Values) == 0 {
		return action
	}

	action.Candidates = make([]Candidate, 0, len(binding.Values))
	for _, raw := range binding.Values {
		action.Candidates = append(action.Candidates, normalizeCandidate(raw))
	}
	return action
}

func normalizeCandidate(raw string) Candidate {
	parts := strings.Split(raw, "+")
	for i, part := range parts {
		switch part {
		case "Ctrl":
			parts[i] = "Control"
		case "Cmd":
			parts[i] = "Command"
		}
	}

	keys, err := keyoutput.ParseShortcut(strings.Join(parts, "+"))
	if err != nil {
		return Candidate{Raw: raw, Status: StatusUnsupported}
	}
	shortcut, err := keyoutput.FormatShortcut(keys)
	if err != nil {
		return Candidate{Raw: raw, Status: StatusUnsupported}
	}
	return Candidate{Raw: raw, Shortcut: shortcut, Status: StatusPresent}
}

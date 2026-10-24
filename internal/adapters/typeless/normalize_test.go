package typeless

import (
	"reflect"
	"testing"
)

func TestNormalizeCanonicalizesOnlyStrictModifierAliases(t *testing.T) {
	snapshot := Snapshot{FeatureShortcutBindings: ShortcutBindings{
		Status:        StatusPresent,
		DictationMode: ActionBinding{Status: StatusPresent, Values: []string{"Ctrl+K", "Ctrl+Cmd+V", "Option+Shift+A", "Control+Space"}},
	}}

	actions := Normalize(snapshot)
	if len(actions) != 4 {
		t.Fatalf("action count = %d, want 4", len(actions))
	}
	if got, want := actions[0].Candidates, []Candidate{
		{Raw: "Ctrl+K", Shortcut: "Control+K", Status: StatusPresent},
		{Raw: "Ctrl+Cmd+V", Shortcut: "Control+Command+V", Status: StatusPresent},
		{Raw: "Option+Shift+A", Shortcut: "Option+Shift+A", Status: StatusPresent},
		{Raw: "Control+Space", Shortcut: "Control+Space", Status: StatusPresent},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
}

func TestNormalizeKeepsUnsupportedShortcutText(t *testing.T) {
	values := []string{
		"Fn",
		"Fn+Space",
		"KeypadAdd",
		"NumpadAdd",
		"",
		"Ctrl+Ctrl+K",
		"Ctrl+K+V",
		"K+Ctrl",
		"Control++K",
		"+",
		"Control +K",
		"LeftCtrl+K",
		"Alt+Shift+A",
		"ctrl+K",
		"Ctrl+K,Cmd+V",
	}
	snapshot := Snapshot{FeatureShortcutBindings: ShortcutBindings{
		Status:        StatusPresent,
		DictationMode: ActionBinding{Status: StatusPresent, Values: values},
	}}

	action := Normalize(snapshot)[0]
	if len(action.Candidates) != len(values) {
		t.Fatalf("candidate count = %d, want %d", len(action.Candidates), len(values))
	}
	for i, raw := range values {
		candidate := action.Candidates[i]
		if candidate.Raw != raw || candidate.Shortcut != "" || candidate.Status != StatusUnsupported {
			t.Errorf("candidate[%d] = %#v, want original raw and unsupported", i, candidate)
		}
	}
}

func TestNormalizePreservesActionOrderLabelsAndSourceStatuses(t *testing.T) {
	snapshot := Snapshot{FeatureShortcutBindings: ShortcutBindings{
		Status:              StatusPresent,
		DictationMode:       ActionBinding{Status: StatusMissing},
		AskAnythingMode:     ActionBinding{Status: StatusNull},
		TranslationMode:     ActionBinding{Status: StatusMalformed},
		PasteLastTranscript: ActionBinding{Status: StatusPresent, Values: []string{}},
	}}

	actions := Normalize(snapshot)
	want := []Action{
		{ID: ActionDictation, Label: "听写", Status: StatusMissing},
		{ID: ActionAskAnything, Label: "Ask Anything", Status: StatusNull},
		{ID: ActionTranslation, Label: "翻译", Status: StatusMalformed},
		{ID: ActionPasteLastTranscript, Label: "粘贴上次转写", Status: StatusPresent},
	}
	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("actions = %#v, want %#v", actions, want)
	}
}

func TestNormalizeDoesNotExposeCandidatesWhenParentFieldIsUnavailable(t *testing.T) {
	snapshot := Snapshot{FeatureShortcutBindings: ShortcutBindings{
		Status:        StatusMalformed,
		DictationMode: ActionBinding{Status: StatusPresent, Values: []string{"Ctrl+K"}},
	}}

	action := Normalize(snapshot)[0]
	if action.Status != StatusPresent || len(action.Candidates) != 0 {
		t.Fatalf("action from unavailable parent = %#v, want preserved child status and no candidates", action)
	}
}

func TestNormalizeDoesNotModifySnapshot(t *testing.T) {
	snapshot := Snapshot{FeatureShortcutBindings: ShortcutBindings{
		Status:              StatusPresent,
		DictationMode:       ActionBinding{Status: StatusPresent, Values: []string{"Ctrl+K", "Fn"}, Interpretation: StatusUnknown},
		AskAnythingMode:     ActionBinding{Status: StatusPresent, Values: []string{}, Interpretation: StatusUnknown},
		TranslationMode:     ActionBinding{Status: StatusNull, Interpretation: StatusUnknown},
		PasteLastTranscript: ActionBinding{Status: StatusMissing, Interpretation: StatusUnknown},
	}}
	before := snapshot
	before.FeatureShortcutBindings.DictationMode.Values = cloneStrings(snapshot.FeatureShortcutBindings.DictationMode.Values)
	before.FeatureShortcutBindings.AskAnythingMode.Values = cloneStrings(snapshot.FeatureShortcutBindings.AskAnythingMode.Values)

	_ = Normalize(snapshot)
	if !reflect.DeepEqual(snapshot, before) {
		t.Fatalf("Normalize mutated its snapshot: got %#v, want %#v", snapshot, before)
	}
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

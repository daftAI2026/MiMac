package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"mimac.local/mimac/internal/voicetools"
)

func TestMappingV2ReadMigratesInMemoryWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.json")
	legacy := `{"version":2,"profiles":{"xiaomi-rc003":{"hid:0007:0052":"Control+Space"}}}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := readProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 3 || got.ToolActions != nil || got.Profiles[legacyXiaomiProfileID]["hid:0007:0052"] != "Control+Space" {
		t.Fatalf("version 2 migration = %+v", got)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != legacy {
		t.Fatalf("read migrated the on-disk file: %s, %v", unchanged, err)
	}
}

func TestToolActionMappingV3RoundTripAndUnknownRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.json")
	profile := mappingTestProfile()
	p := emptyProfile()
	p.Profiles[profile.ID] = map[string]string{"hid:0007:0051": "Command+1"}
	p.ToolActions = map[string]map[string]voicetools.ActionRef{
		profile.ID: {
			"hid:0007:0052": {ToolID: "voice-typeless", ActionID: "dictationMode", CandidateRaw: "Ctrl+Space"},
			"hid:0007:00ff": {ToolID: "future-tool", ActionID: "future-action", CandidateRaw: "raw:future"},
		},
		"future.device.v2": {
			"button:future": {ToolID: "future-tool", ActionID: "future-action", CandidateRaw: "raw:future"},
		},
	}
	if err := saveProfile(path, p); err != nil {
		t.Fatal(err)
	}
	got, err := readProfile(path)
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatalf("v3 action round trip: %+v, %v", got, err)
	}
	refs := got.actionRefs(profile)
	if len(refs) != 1 || refs["hid:0007:0052"] != p.ToolActions[profile.ID]["hid:0007:0052"] {
		t.Fatalf("active action refs included unknown buttons: %+v", refs)
	}
	keys, err := got.keys(profile)
	if err != nil || len(keys) != 1 || len(keys["hid:0007:0051"]) != 2 {
		t.Fatalf("action reference became an ordinary key mapping: %+v, %v", keys, err)
	}
	if _, exists := keys["hid:0007:0052"]; exists {
		t.Fatal("tool action was exposed as an ordinary key mapping")
	}
	if _, retained := got.ToolActions["future.device.v2"]["button:future"]; !retained {
		t.Fatal("unknown valid profile/button/action reference was discarded")
	}
}

func TestToolActionAndShortcutMappingsAreMutuallyExclusive(t *testing.T) {
	profile := mappingTestProfile()
	ref := voicetools.ActionRef{ToolID: "voice-typeless", ActionID: "dictationMode", CandidateRaw: "Ctrl+Space"}
	emptyMapped, err := emptyProfile().withToolAction(profile, "hid:0007:0052", ref)
	if err != nil || emptyMapped.ToolActions[profile.ID]["hid:0007:0052"] != ref {
		t.Fatalf("tool-action mapping from empty store = %+v, %v", emptyMapped, err)
	}

	p := emptyProfile()
	p.Profiles[profile.ID] = map[string]string{"hid:0007:0052": ""}
	p.ToolActions = map[string]map[string]voicetools.ActionRef{profile.ID: {"hid:0007:0052": ref}}
	if err := p.validate(); err == nil {
		t.Fatal("accepted action and explicitly empty shortcut at the same button")
	}

	p = emptyProfile()
	p.Profiles[profile.ID] = map[string]string{"hid:0007:0052": "Control+Space"}
	actionMapped, err := p.withToolAction(profile, "hid:0007:0052", ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := actionMapped.Profiles[profile.ID]["hid:0007:0052"]; exists {
		t.Fatal("tool-action override retained the ordinary mapping")
	}
	if actionMapped.ToolActions[profile.ID]["hid:0007:0052"] != ref {
		t.Fatal("tool-action override did not store the action reference")
	}
	if _, exists := p.Profiles[profile.ID]["hid:0007:0052"]; !exists {
		t.Fatal("tool-action update mutated original mapping")
	}
	if _, err := p.withToolAction(profile, "hid:0007:0052", voicetools.ActionRef{}); err == nil {
		t.Fatal("accepted an invalid tool-action reference")
	}
	if _, err := p.withToolAction(profile, "hid:0007:00ff", ref); err == nil {
		t.Fatal("accepted an unknown physical button")
	}

	shortcutMapped, _, err := actionMapped.withShortcut(profile, "hid:0007:0052", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := shortcutMapped.ToolActions[profile.ID]["hid:0007:0052"]; exists {
		t.Fatal("ordinary mapping override retained the action reference")
	}
	if shortcut, exists := shortcutMapped.Profiles[profile.ID]["hid:0007:0052"]; !exists || shortcut != "" {
		t.Fatal("explicit empty shortcut was not preserved")
	}
}

func TestInvalidToolActionSavePreservesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.json")
	p := emptyProfile()
	p.ToolActions = map[string]map[string]voicetools.ActionRef{
		legacyXiaomiProfileID: {
			"hid:0007:0052": {ToolID: "voice-typeless", ActionID: "dictationMode", CandidateRaw: "Ctrl+Space"},
		},
	}
	if err := saveProfile(path, p); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	invalid := cloneMappingProfile(p)
	invalid.ToolActions[legacyXiaomiProfileID]["hid:0007:0052"] = voicetools.ActionRef{ToolID: "voice-typeless", ActionID: "dictationMode"}
	if err := saveProfile(path, invalid); err == nil {
		t.Fatal("saved an invalid tool-action reference")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("invalid save changed existing file: %s, %v", after, err)
	}
}

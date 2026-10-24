package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/devices"
)

func mappingTestProfile() devices.Profile {
	return devices.Profile{
		ID:   legacyXiaomiProfileID,
		Name: "Xiaomi RC003",
		Buttons: []devices.Button{
			{ID: "hid:0007:0052", Label: "上"},
			{ID: "hid:0007:0051", Label: "下"},
		},
	}
}

func TestMappingRoundTripAndExplicitDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "MiMac", "mappings.json")
	p := emptyProfile()
	p.Profiles[legacyXiaomiProfileID] = map[string]string{
		"hid:0007:0052": "Control+Shift+Space",
		"hid:0007:0051": "",
	}
	p.Profiles["future.device.v2"] = map[string]string{"button:future": "Command+3"}
	if err := saveProfile(path, p); err != nil {
		t.Fatal(err)
	}
	got, err := readProfile(path)
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v", info.Mode())
	}
	if _, present := got.Profiles[legacyXiaomiProfileID]["hid:0007:0051"]; !present {
		t.Fatal("explicit disabled action lost")
	}
	if !reflect.DeepEqual(got.Profiles["future.device.v2"], map[string]string{"button:future": "Command+3"}) {
		t.Fatal("unknown but valid profile mapping was not preserved")
	}
}

func TestMappingV1ReadMigratesInMemoryAndWritesV3OnlyOnSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.json")
	legacy := `{"version":1,"buttons":{"hid:0007:0052":"Up","hid:0007:0051":""}}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 3 || !reflect.DeepEqual(got.Profiles[legacyXiaomiProfileID], map[string]string{
		"hid:0007:0052": "Up", "hid:0007:0051": "",
	}) {
		t.Fatalf("migration = %+v", got)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != legacy {
		t.Fatalf("read migrated the on-disk file: %s, %v", unchanged, err)
	}
	if err := saveProfile(path, got); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(written), `"profiles"`) || strings.Contains(string(written), `"buttons"`) || !strings.Contains(string(written), `"version": 3`) {
		t.Fatalf("save did not write v3: %s (%v)", written, err)
	}
}

func TestMappingProfileIsolationAndUnknownProfilePreservation(t *testing.T) {
	p := emptyProfile()
	p.Profiles[legacyXiaomiProfileID] = map[string]string{
		"hid:0007:0052": "Up",
		"hid:0007:00ff": "Down", // unknown button is retained but not executed
	}
	p.Profiles["future.device.v2"] = map[string]string{"button:future": "Command+3"}
	active := mappingTestProfile()

	keys, err := p.keys(active)
	if err != nil {
		t.Fatal(err)
	}
	gotKeys, exists := keys["hid:0007:0052"]
	if len(keys) != 1 || !exists || len(gotKeys) != 1 || string(gotKeys[0]) != "Up" {
		t.Fatalf("active mapping included unknown buttons or profiles: %v", keys)
	}

	next, nextKeys, err := p.withShortcut(active, "hid:0007:0051", "Command+1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Profiles[legacyXiaomiProfileID]["hid:0007:0051"] != "" {
		t.Fatal("withShortcut mutated the original store")
	}
	if next.Profiles[legacyXiaomiProfileID]["hid:0007:0051"] != "Command+1" || len(nextKeys) != 2 {
		t.Fatalf("updated current profile = %v, keys = %v", next.Profiles[legacyXiaomiProfileID], nextKeys)
	}
	if !reflect.DeepEqual(next.Profiles["future.device.v2"], p.Profiles["future.device.v2"]) {
		t.Fatal("updating current profile overwrote another profile")
	}
}

func TestMappingInvalidSavePreservesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.json")
	p := emptyProfile()
	p.Profiles[legacyXiaomiProfileID] = map[string]string{"hid:0007:0052": "Up"}
	if err := saveProfile(path, p); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	p.Profiles[legacyXiaomiProfileID]["hid:0007:0051"] = "Fn"
	if err := saveProfile(path, p); err == nil {
		t.Fatal("accepted unverified Fn")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("invalid save changed old file")
	}
}

func TestMappingUnknownSchemaAndTrailingDataFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.json")
	for _, data := range []string{
		`{"version":3,"profiles":{},"tool_actions":{"xiaomi-rc003":{"hid:0007:0052":{"tool_id":"tool","action_id":"action","candidate_raw":"raw","future":true}}}}`,
		`{"version":3,"profiles":{"xiaomi-rc003":{"hid:0007:0052":""}},"tool_actions":{"xiaomi-rc003":{"hid:0007:0052":{"tool_id":"tool","action_id":"action","candidate_raw":"raw"}}}}`,
		`{"version":3,"profiles":{},"tool_actions":null}`,
		`{"version":3,"profiles":{},"future":true}`,
		`{"version":2,"profiles":{},"future":true}`,
		`{"version":2,"profiles":{}} {}`,
		`{"version":2,"profiles":null}`,
		`{"version":2,"profiles":{"future.profile":null}}`,
		`{"version":1,"buttons":null}`,
		`{"version":1,"buttons":{},"profiles":{}}`,
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readProfile(path); err == nil {
			t.Fatalf("accepted %s", data)
		}
		got, _ := os.ReadFile(path)
		if string(got) != data {
			t.Fatal("reader changed file")
		}
	}
}

func TestDeviceDefaultsRemainPassthroughAfterRestoringMapping(t *testing.T) {
	profile, err := xiaomi.DeviceProfile()
	if err != nil {
		t.Fatal(err)
	}
	store := emptyProfile()
	keys, err := store.keys(profile)
	if err != nil || len(keys) != 0 {
		t.Fatal("device default labels created synthetic output", keys, err)
	}
	custom, _, err := store.withShortcut(profile, "hid:0007:0052", "Command+C")
	if err != nil {
		t.Fatal(err)
	}
	restored, keys, err := custom.withShortcut(profile, "hid:0007:0052", "")
	if err != nil || len(keys) != 0 {
		t.Fatal("restore default retained synthetic output", keys, err)
	}
	if restored.Profiles[profile.ID]["hid:0007:0052"] != "" {
		t.Fatal("restore did not clear custom action")
	}
}

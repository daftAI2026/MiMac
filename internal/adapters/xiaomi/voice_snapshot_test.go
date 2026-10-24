package xiaomi

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestVoiceSnapshotPreservesPropertyListAndRefusesOverwrite(t *testing.T) {
	s := &fileVoiceMappingSnapshots{path: filepath.Join(t.TempDir(), "recovery.plist"), bootID: "boot-a"}
	entries := []voiceMappingEntry{{"HIDKeyboardModifierMappingSrc": uint64(0x70000003e), "HIDKeyboardModifierMappingDst": uint64(0), "vendor": map[string]any{"bytes": []byte{1, 2}, "flags": []any{true, "extra"}}}}
	if s.HasPending() {
		t.Fatal("unexpected pending record")
	}
	if err := s.Save(42, entries); err != nil {
		t.Fatal(err)
	}
	if !s.HasPending() {
		t.Fatal("snapshot not retained")
	}
	if err := s.Save(43, nil); err == nil {
		t.Fatal("overwrote existing snapshot")
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.RegistryID != 42 || !reflect.DeepEqual(got.Original, entries) {
		t.Fatalf("lost snapshot properties: %#v", got)
	}
	info, err := os.Stat(s.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("snapshot permissions: %v %v", info, err)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if s.HasPending() {
		t.Fatal("snapshot still pending")
	}
}

func TestVoiceSnapshotRejectsStaleOrPartialRecovery(t *testing.T) {
	s := &fileVoiceMappingSnapshots{path: filepath.Join(t.TempDir(), "recovery.plist"), bootID: "boot-a"}
	if err := s.Save(42, nil); err != nil {
		t.Fatal(err)
	}
	s.bootID = "boot-b"
	if _, err := s.Load(); err == nil {
		t.Fatal("accepted previous boot's registry identity")
	}
	if !s.HasPending() {
		t.Fatal("discarded uncertain record")
	}
	if err := os.WriteFile(s.path, []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("accepted partial snapshot")
	}
	if err := s.Save(43, nil); err == nil {
		t.Fatal("overwrote partial snapshot")
	}
}

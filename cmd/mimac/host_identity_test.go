package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/audiobackendprefs"
)

func TestDevelopmentPreferencesDoNotTouchDailyData(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	originalName := mygo.App.Name()
	t.Cleanup(func() { mygo.App.SetName(originalName) })
	names := []string{"preferences.json", "mappings.json", "tools.json", "audio-backend.json", "voice-mapping-recovery.plist"}
	originals := map[string][]byte{}
	mygo.App.SetName("MiMac")
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		path, err := hostDataPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if path != filepath.Join(base, "MiMac", name) {
			t.Fatalf("daily path changed: %s", path)
		}
		data := []byte("daily-" + name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		originals[path] = data
	}
	mygo.App.SetName(developmentAppName)
	for _, name := range names {
		path, err := hostDataPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if path != filepath.Join(base, developmentAppName, name) {
			t.Fatalf("dev path = %s", path)
		}
		if err := os.WriteFile(path, []byte("development"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path, err := hostDataPath("audio-backend.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := audiobackendprefs.Write(path, compatibilityAudioBackend); err != nil {
		t.Fatal(err)
	}
	if got, err := audiobackendprefs.Read(path); err != nil || got != compatibilityAudioBackend {
		t.Fatalf("dev backend = %s, %v", got, err)
	}
	for path, want := range originals {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("daily file changed: %s", path)
		}
	}
}

func TestDevelopmentCannotStartGlobalInputPlugins(t *testing.T) {
	starts := 0
	s, _ := setupRemoteTest(t, func(_ func(xiaomi.MonitorEvent), _ *remoteTestMonitor) error { starts++; return nil })
	s.isolatedDevelopment = true
	s.enableRemote(true)
	if !errors.Is(s.remoteState.err, errDevelopmentInputDisabled) {
		t.Fatalf("enableRemote = %v", s.remoteState.err)
	}
	for _, id := range []string{xiaomiPlugin, outputPlugin, remotePlugin} {
		if err := s.plugins.Enable(id); !errors.Is(err, errDevelopmentInputDisabled) {
			t.Fatalf("%s = %v", id, err)
		}
	}
	if starts != 0 || s.monitor != nil || s.output != nil || s.plugins.Active(remotePlugin) {
		t.Fatal("development acquired global input")
	}
	if err := s.plugins.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDevelopmentToolSelectionDoesNotSwitchSystemInputSource(t *testing.T) {
	input := &fakeInputSourceController{current: "user-input-source"}
	s := &shell{isolatedDevelopment: true}
	s.inputSource = input
	tool := testInputSourceTool("voice-doubao")
	if err := s.ensureVoiceInputSource(tool); err != nil {
		t.Fatal(err)
	}
	if input.currentCalls != 0 || len(input.selectCalls) != 0 {
		t.Fatal("development accessed global input source")
	}
	// The same verified action retains the established behavior in daily mode.
	s.isolatedDevelopment = false
	if err := s.ensureVoiceInputSource(tool); err != nil {
		t.Fatal(err)
	}
	if len(input.selectCalls) != 1 || input.current != testDoubaoSourceID {
		t.Fatal("daily input selection was changed")
	}
}

func TestDevelopmentDoesNotPrepareAudioEvenWithSelectedReadyTool(t *testing.T) {
	s, _ := liveHostFixture(t, nil)
	if s.desiredLiveAudioTool() == nil {
		t.Fatal("fixture has no ready audio route")
	}
	s.isolatedDevelopment = true
	called := false
	s.liveAudio.factory = func(context.Context, uint64, string) (liveAudioRoute, error) {
		called = true
		return nil, errors.New("unexpected route construction")
	}
	s.refreshLiveAudio()
	if called || s.liveAudio.connection != nil || s.audioSession != nil {
		t.Fatal("development prepared shared audio")
	}
}

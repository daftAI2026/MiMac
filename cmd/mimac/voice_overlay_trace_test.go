package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/platform/siriglass"
)

func TestVoiceTraceDisabledDoesNotCreateOutput(t *testing.T) {
	trace, file, err := openVoiceOverlayTrace("")
	if trace != nil || file != nil || err != nil {
		t.Fatal("disabled trace created a resource")
	}
	trace.render(voiceOverlayFrame{}, nil)
	trace.sample(nil, bridge.SessionToken{}, time.Now(), siriglass.Spectrum{})
}

func TestVoiceTraceExclusivePrivateReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	trace, file, err := openVoiceOverlayTrace(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if trace.Binary == "" {
		t.Fatal("missing current binary identity")
	}
	if other, f, err := openVoiceOverlayTrace(path); !errors.Is(err, os.ErrExist) || other != nil || f != nil {
		t.Fatal("trace overwrote an existing report")
	}
	if err := trace.write(file); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("report permissions: %v %v", info, err)
	}
}

func TestVoiceTraceObservesWithoutChangingSpectrumOrSmoothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var with, without voiceSpectrumState
	trace := &voiceOverlayTrace{}
	with.trace = trace
	token := bridge.SessionToken{}
	with.setEnabled(true)
	without.setEnabled(true)
	a, b := with.observer(ctx, token, 16000), without.observer(ctx, token, 16000)
	pcm := testTone(320, 375, .25)
	original := append([]int16(nil), pcm...)
	now := time.Now()
	a.Observe(pcm, now)
	b.Observe(pcm, now)
	for i := range 3 {
		at := now.Add(time.Duration(i) * 10 * time.Millisecond)
		if got, want := with.snapshot(token, at), without.snapshot(token, at); got != want {
			t.Fatal("diagnostics changed spectrum or advanced smoothing twice")
		}
	}
	if !reflect.DeepEqual(pcm, original) || len(trace.Entries) != 3 {
		t.Fatal("PCM changed or frame missing")
	}
	if e := trace.Entries[0]; e.Raw == nil || e.Raw.Magnitudes[1] <= 0 || !e.TimestampFresh || !e.Usable || !e.TokenMatch || !e.Alive {
		t.Fatalf("fresh source evidence missing: %+v", e)
	}
	with.snapshot(token, now.Add(101*time.Millisecond))
	if e := trace.Entries[3]; e.TimestampFresh || e.Usable || e.Displayed.Samples != 0 || e.Raw.Magnitudes[1] <= 0 {
		t.Fatal("stale display cannot be distinguished from silent source")
	}
	core, err := bridge.NewCore(bridge.NewAudioBinding())
	if err != nil {
		t.Fatal(err)
	}
	device, err := bridge.NewDevice("test-only", 17)
	if err != nil {
		t.Fatal(err)
	}
	press, err := bridge.NewPressToken(device, "voice", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []bridge.Event{bridge.DeviceConnected(device), bridge.RouteAvailable(true), bridge.ScopeAvailable(true), bridge.VoiceBegan(press)} {
		if _, err := core.Step(event); err != nil {
			t.Fatal(err)
		}
	}
	other := core.Snapshot().Session.Token
	with.snapshot(other, now.Add(20*time.Millisecond))
	if e := trace.Entries[4]; e.TokenMatch || e.Usable || e.Raw != nil || e.Generation != 17 || e.Session == 0 {
		t.Fatal("old raw source attributed to a different session")
	}
	cancel()
	with.snapshot(token, now.Add(20*time.Millisecond))
	if e := trace.Entries[5]; e.Alive || e.Usable || e.Displayed.Count != 0 || !e.TimestampFresh {
		t.Fatal("cancelled context not distinguished")
	}
}

func TestVoiceTraceBoundedAndNoContentLabels(t *testing.T) {
	trace := &voiceOverlayTrace{Entries: make([]voiceOverlayTraceEntry, 0, voiceOverlayTraceLimit)}
	frame := voiceOverlayFrame{Visible: true, Label: "private spoken content must not be logged", Spectrum: siriglass.Spectrum{Count: 6, Sequence: 7}}
	for range voiceOverlayTraceLimit + 2 {
		trace.render(frame, nil)
	}
	if len(trace.Entries) != voiceOverlayTraceLimit || trace.Dropped != 2 {
		t.Fatal("unbounded trace or missing drop count")
	}
	if allocations := testing.AllocsPerRun(100, func() { trace.render(frame, nil) }); allocations != 0 {
		t.Fatalf("full trace kept allocating: %v", allocations)
	}
	var report bytes.Buffer
	if err := trace.write(&report); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(report.Bytes(), []byte(frame.Label)) || bytes.Contains(report.Bytes(), []byte(`"pcm"`)) {
		t.Fatal("report contains private content")
	}
	var decoded struct {
		GPUPresent bool                     `json:"gpu_present_ack_observed"`
		Entries    []voiceOverlayTraceEntry `json:"entries"`
	}
	if err := json.Unmarshal(report.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.GPUPresent || !decoded.Entries[0].Accepted || decoded.Entries[0].Sequence != 7 {
		t.Fatal("render success lost or misrepresented as GPU acknowledgement")
	}
	failed := &voiceOverlayTrace{}
	failed.render(frame, errors.New("rejected"))
	if failed.Entries[0].Accepted {
		t.Fatal("rejected render marked accepted")
	}
	failed.hidden(frame.Token, nil)
	if e := failed.Entries[1]; e.Kind != "hide" || e.Visible || e.Meters != nil || !e.Accepted {
		t.Fatal("hide fabricated visible audio meters")
	}
}

func TestVoiceTraceDisabledSnapshotDoesNotAllocate(t *testing.T) {
	var state voiceSpectrumState
	state.setEnabled(true)
	token := bridge.SessionToken{}
	observer := state.observer(context.Background(), token, 16000)
	now := time.Now()
	observer.Observe(testTone(320, 375, .25), now)
	if n := testing.AllocsPerRun(100, func() { state.snapshot(token, now) }); n != 0 {
		t.Fatalf("disabled diagnostics added hot-path allocations: %v", n)
	}
}

//go:build darwin && arm64

package siriglass

import (
	"encoding/json"
	"testing"
	"unsafe"
)

func TestInputUpdatePreservesLiveMetersAndCommitsOnlyOnSuccess(t *testing.T) {
	meters := Meters{Count: 2, Amplitudes: [MaxMeterBands]float32{1.25, 0.125}, StopFrequenciesHz: [MaxMeterBands]float32{200, 8000}}
	r := &Renderer{handle: 17, revision: 19, visible: true, db: initialDB, meters: meters}
	input := InputState{IsRecording: true, HasVoiceInputOwnership: true, SceneMode: "island"}
	var received glassParameters
	accept := int32(1)
	calls := 0
	r.api = &nativeAPI{
		update: func(handle uint64, data unsafe.Pointer, count int32) int32 {
			calls++
			if handle != 17 {
				t.Fatal("wrong handle")
			}
			if err := json.Unmarshal(unsafe.Slice((*byte)(data), int(count)), &received); err != nil {
				t.Fatal(err)
			}
			return accept
		},
		lastError: func(unsafe.Pointer, int32) int32 { return 0 },
	}
	if err := r.updateInputOnMain(&input); err != nil {
		t.Fatal(err)
	}
	if received.Revision != 20 || received.ReferenceChatInput == nil || *received.ReferenceChatInput != input || !received.Visible || received.AudioMeters == nil || received.AudioMeters.NormalizedPowerAmplitudes[0] != 1.25 || received.AudioMeters.StopFrequenciesHz[1] != 8000 {
		t.Fatalf("lost parameters: %+v", received)
	}
	if r.revision != 20 || r.meters != meters || !r.visible {
		t.Fatal("control update changed live audio state")
	}
	// nil must be omitted on the wire, not retain the previous decode target.
	received = glassParameters{}
	if err := r.updateInputOnMain(nil); err != nil {
		t.Fatal(err)
	}
	if received.ReferenceChatInput != nil || received.Revision != 21 {
		t.Fatal("old observer baseline retained")
	}
	accept = 0
	if err := r.updateInputOnMain(&input); err == nil || r.revision != 21 {
		t.Fatal("native refusal advanced revision")
	}
	r.revision = ^uint64(0)
	if err := r.updateInputOnMain(&input); err == nil || calls != 3 {
		t.Fatal("exhausted revision reached native code")
	}
	r.revision = 21
	r.closing = true
	if err := r.updateInputOnMain(&input); err == nil || calls != 3 {
		t.Fatal("closing renderer accepted input")
	}
}

func TestHiddenInputUpdateKeepsLegacyFloor(t *testing.T) {
	r := &Renderer{handle: 1, revision: 1, db: initialDB, api: &nativeAPI{update: func(_ uint64, data unsafe.Pointer, count int32) int32 {
		var p glassParameters
		if err := json.Unmarshal(unsafe.Slice((*byte)(data), int(count)), &p); err != nil {
			t.Fatal(err)
		}
		if p.Visible || p.AudioPowerDB != initialDB || p.AudioMeters != nil || p.ReferenceChatInput == nil {
			t.Fatalf("hidden parameters changed: %+v", p)
		}
		return 1
	}}}
	if err := r.updateInputOnMain(&InputState{}); err != nil {
		t.Fatal(err)
	}
}

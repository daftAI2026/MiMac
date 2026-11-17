package main

import (
	"errors"
	"mimac.local/mimac/internal/platform/siriglass"
	"testing"
)

type rejectingOverlayNative struct {
	inputs, clears, meters, hides, closes int
	meterErr, clearErr, closeErr          error
}

func (n *rejectingOverlayNative) UpdateInput(siriglass.InputState) error { n.inputs++; return nil }
func (n *rejectingOverlayNative) ClearInput() error                      { n.clears++; return n.clearErr }
func (n *rejectingOverlayNative) UpdateMeters(bool, float32, siriglass.Meters) error {
	n.meters++
	return n.meterErr
}
func (n *rejectingOverlayNative) Update(bool, float32) error { n.hides++; return nil }
func (n *rejectingOverlayNative) Close() error               { n.closes++; return n.closeErr }

func TestOverlayFirstFrameFailureStillClearsNativeInput(t *testing.T) {
	n := &rejectingOverlayNative{meterErr: errors.New("native meter refusal")}
	v := &mygoVoiceOverlay{renderer: n}
	f := voiceOverlayFrame{Visible: true, Input: siriglass.InputState{IsRecording: true, HasVoiceInputOwnership: true, SceneMode: "island"}}
	if err := v.updateNative(f); err == nil || v.visible || !v.inputAttached {
		t.Fatal("first-frame refusal lost native input ownership")
	}
	if err := v.Hide(); err != nil {
		t.Fatal(err)
	}
	if n.clears != 1 || n.hides != 1 || v.inputAttached {
		t.Fatal("invisible first frame skipped native clear")
	}
	if err := v.Hide(); err != nil {
		t.Fatal(err)
	}
	if n.clears != 1 {
		t.Fatal("idle hide repeatedly encoded JSON")
	}
}

func TestOverlayClearRefusalRetriesWithoutRevivingWindow(t *testing.T) {
	n := &rejectingOverlayNative{clearErr: errors.New("native clear refusal")}
	v := &mygoVoiceOverlay{renderer: n, visible: true, inputAttached: true}
	if err := v.Hide(); err == nil || v.visible || !v.inputAttached || n.hides != 1 {
		t.Fatal("failed clear lost its retry or skipped hide")
	}
	n.clearErr = nil
	if err := v.Hide(); err != nil {
		t.Fatal(err)
	}
	if n.clears != 2 || v.inputAttached || n.hides != 2 {
		t.Fatal("second hide did not clear retained native input")
	}
	n.closeErr = errors.New("destroy refused")
	if err := v.Close(); err == nil || v.closed {
		t.Fatal("destroy refusal discarded owner")
	}
	n.closeErr = nil
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if !v.closed || n.closes != 2 {
		t.Fatal("destroy retry did not complete")
	}
}

func TestOverlayUnchangedFramesKeepControlOffMeterHotPath(t *testing.T) {
	n := &rejectingOverlayNative{}
	v := &mygoVoiceOverlay{renderer: n}
	f := voiceOverlayFrame{Visible: true, Input: siriglass.InputState{IsRecording: true, HasVoiceInputOwnership: true, SceneMode: "island"}}
	for i := 0; i < 240; i++ {
		if err := v.updateNative(f); err != nil {
			t.Fatal(err)
		}
		v.visible = true
		v.frame = f // Same successful-frame commit performed by render.
	}
	if n.inputs != 1 || n.meters != 240 || n.clears != 0 {
		t.Fatalf("hot path controls=%d meters=%d clears=%d", n.inputs, n.meters, n.clears)
	}
	f.Input.IsRecording = false
	if err := v.updateNative(f); err != nil {
		t.Fatal(err)
	}
	if n.inputs != 2 {
		t.Fatal("capture exit not forwarded")
	}
}

package main

import (
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/platform/dotscope"
	"mimac.local/mimac/internal/testutil/dotfixture"
)

type fakeVoiceCaption struct {
	updates             []voiceCaptionUpdate
	hides               int
	closes              int
	updateErr, closeErr error
}

type voiceCaptionUpdate struct {
	stable, tentative string
	bounds            mygo.Rectangle
	opacity           float64
}

func (v *fakeVoiceCaption) Update(stable, tentative string, bounds mygo.Rectangle, opacity float64) error {
	v.updates = append(v.updates, voiceCaptionUpdate{stable, tentative, bounds, opacity})
	return v.updateErr
}
func (v *fakeVoiceCaption) Hide() error  { v.hides++; return nil }
func (v *fakeVoiceCaption) Close() error { v.closes++; return v.closeErr }

func TestVoiceCaptionPlacementKeepsTheFrozenHUDAndUsesSmallEnvelope(t *testing.T) {
	got := voiceCaptionBoundsForHUD(
		mygo.Rectangle{X: 872, Y: 30, Width: voiceDotWidth, Height: voiceDotHeight},
		mygo.Rectangle{X: 0, Y: 0, Width: 1440, Height: 900},
	)
	want := mygo.Rectangle{X: 680, Y: 60, Width: 560, Height: 44}
	if got != want {
		t.Fatalf("caption bounds = %+v, want %+v", got, want)
	}
	plate := mygo.Rectangle{X: 904, Y: 36, Width: 112, Height: int(dotscope.PlateHeight)}
	pill := mygo.Rectangle{X: got.X + 4, Y: got.Y + (voiceCaptionHeight-voiceCaptionPillHeight)/2,
		Width: got.Width - 8, Height: voiceCaptionPillHeight}
	if pill.Y < plate.Y+plate.Height || pill.Y-(plate.Y+plate.Height) != voiceCaptionGap {
		t.Fatalf("visible caption pill overlaps or misses the upstream gap: plate=%+v pill=%+v", plate, pill)
	}
	if got := voiceCaptionBoundsForHUD(mygo.Rectangle{X: 0, Y: 30, Width: 176, Height: 64},
		mygo.Rectangle{X: 0, Y: 0, Width: 500, Height: 800}); got.Width != 476 || got.X != 12 {
		t.Fatalf("narrow-display caption bounds = %+v", got)
	}
}

func TestVoiceCaptionTracksPartialAndFadesFor150ms(t *testing.T) {
	caption := &fakeVoiceCaption{}
	token := voiceInterimTestToken(t, 1)
	v := &voiceDotView{
		newCaption:   func() (voiceCaptionView, error) { return caption, nil },
		hudBounds:    mygo.Rectangle{X: 872, Y: 30, Width: voiceDotWidth, Height: voiceDotHeight},
		screenBounds: mygo.Rectangle{X: 0, Y: 0, Width: 1440, Height: 900},
	}
	frame := voiceOverlayFrame{
		Visible: true, ShowLiveText: true, Token: token,
		InterimStable: "先说的话", InterimTentative: " 正在说",
	}
	start := time.Now()
	if v.syncCaption(frame, start) || len(caption.updates) != 1 {
		t.Fatal("active partial did not create a caption update")
	}
	first := caption.updates[0]
	if first.stable != frame.InterimStable || first.tentative != frame.InterimTentative || first.opacity != 1 || first.bounds.Height != 44 {
		t.Fatalf("caption did not preserve the two partial styles and envelope: %+v", first)
	}

	frame.InterimStable, frame.InterimTentative = "", ""
	v.syncCaption(frame, start.Add(30*time.Millisecond))
	if v.captionDraft.Stable != "先说的话" || v.captionDraft.Tentative != " 正在说" {
		t.Fatal("blank initial/intermediate partial erased the last visible words")
	}

	frame.Visible = false
	if !v.syncCaption(frame, start.Add(time.Second)) {
		t.Fatal("ended successful transcript did not start its fade")
	}
	last := caption.updates[len(caption.updates)-1]
	if last.opacity != 1 {
		t.Fatalf("fade opacity at start = %v, want 1", last.opacity)
	}
	if !v.syncCaption(frame, start.Add(time.Second+75*time.Millisecond)) {
		t.Fatal("caption ended before the 150 ms fade")
	}
	last = caption.updates[len(caption.updates)-1]
	if last.opacity < 0.49 || last.opacity > 0.51 {
		t.Fatalf("caption opacity halfway through fade = %v, want 0.5", last.opacity)
	}
	if v.syncCaption(frame, start.Add(time.Second+voiceCaptionFade)) || caption.hides != 1 || v.captionDraft != (voiceInterimDraft{}) {
		t.Fatal("caption did not hide and clear after the fade")
	}
}

func TestVoiceCaptionCancellationAndPreferenceOffClearImmediately(t *testing.T) {
	caption := &fakeVoiceCaption{}
	token := voiceInterimTestToken(t, 1)
	v := testDotView(t)
	v.caption = caption
	v.captionDraft = voiceInterimDraft{Stable: "draft"}
	v.captionToken = token
	v.retire(time.Now())
	if caption.hides != 1 || v.captionDraft != (voiceInterimDraft{}) {
		t.Fatal("cancellation retained the interim caption")
	}
	v.captionDraft = voiceInterimDraft{Tentative: "next"}
	v.captionToken = token
	v.syncCaption(voiceOverlayFrame{ShowLiveText: false}, time.Now())
	if caption.hides != 2 || v.captionDraft != (voiceInterimDraft{}) {
		t.Fatal("turning the independent caption preference off retained text")
	}
}

func TestVoiceCaptionFailureDoesNotFailDotAndCloseRetries(t *testing.T) {
	caption := &fakeVoiceCaption{updateErr: errors.New("caption draw rejected"), closeErr: errors.New("caption close rejected")}
	model, err := dotscope.Open(dotfixture.Library(t, "../.."))
	if err != nil {
		t.Fatal(err)
	}
	model.Start(0, false)
	before := model.Snapshot().Pose
	v := &voiceDotView{model: model, caption: caption, captionDraft: voiceInterimDraft{Stable: "draft"}}
	frame := voiceOverlayFrame{Visible: true, ShowLiveText: true, Token: voiceInterimTestToken(t, 1), InterimStable: "draft"}
	v.syncCaption(frame, time.Now())
	if v.captionErr == nil || model.Snapshot().Pose != before {
		t.Fatal("caption rendering failure escaped its optional presentation path")
	}
	if err := v.Close(); err == nil || v.caption != caption || caption.closes != 1 {
		t.Fatal("caption close failure discarded the retry owner")
	}
	caption.closeErr = nil
	if err := v.Close(); err != nil || v.caption != nil || caption.closes != 2 {
		t.Fatal("caption close retry did not complete")
	}
}

func TestVoiceCaptionDetectsRTLReadingDirection(t *testing.T) {
	if !voiceCaptionIsRTL("", " مرحبا") || voiceCaptionIsRTL("Hello שלום", "") {
		t.Fatal("caption did not follow the first strong letter's reading direction")
	}
}

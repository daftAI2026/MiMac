package main

import (
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/bridge"
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

func TestVoiceCaptionTopAndBottomPlacementKeepsSafeInsetsAndGaps(t *testing.T) {
	screen := mygo.Rectangle{X: 0, Y: 0, Width: 1440, Height: 900}
	workArea := mygo.Rectangle{X: 0, Y: 30, Width: 1440, Height: 840}
	topDot, _ := voiceDotHUDBounds(screen, workArea, false)
	bottomDot, _ := voiceDotHUDBounds(screen, workArea, true)
	if topDot.Y != workArea.Y || bottomDot.Y != workArea.Y+workArea.Height-voiceDotHeight {
		t.Fatalf("top/bottom HUD bounds = %+v / %+v", topDot, bottomDot)
	}
	plateBounds := func(dot mygo.Rectangle, bottom bool) mygo.Rectangle {
		return mygo.Rectangle{
			X: dot.X + (dot.Width-112)/2, Y: dot.Y + voiceDotPlateTopOffset(bottom),
			Width: 112, Height: int(dotscope.PlateHeight),
		}
	}
	topPlate, bottomPlate := plateBounds(topDot, false), plateBounds(bottomDot, true)
	if topPlate.Y != workArea.Y+voiceDotGap || topPlate.Y+topPlate.Height != workArea.Y+voiceDotGap+int(dotscope.PlateHeight) ||
		bottomPlate.Y+bottomPlate.Height != workArea.Y+workArea.Height-voiceDotBottomGap ||
		bottomDot.Y < workArea.Y || bottomDot.Y+bottomDot.Height > workArea.Y+workArea.Height {
		t.Fatalf("visible plates = %+v / %+v", topPlate, bottomPlate)
	}

	got := voiceCaptionBoundsForHUD(
		topDot, screen, false,
	)
	want := mygo.Rectangle{X: 440, Y: 62, Width: 560, Height: 44}
	if got != want {
		t.Fatalf("caption bounds = %+v, want %+v", got, want)
	}
	pill := mygo.Rectangle{X: got.X + 4, Y: got.Y + (voiceCaptionHeight-voiceCaptionPillHeight)/2,
		Width: got.Width - 8, Height: voiceCaptionPillHeight}
	if pill.Y < topPlate.Y+topPlate.Height || pill.Y-(topPlate.Y+topPlate.Height) != voiceCaptionGap {
		t.Fatalf("top caption overlaps or misses the upstream gap: plate=%+v pill=%+v", topPlate, pill)
	}

	bottomCaption := voiceCaptionBoundsForHUD(bottomDot, screen, true)
	bottomPill := mygo.Rectangle{X: bottomCaption.X + 4,
		Y:     bottomCaption.Y + (voiceCaptionHeight-voiceCaptionPillHeight)/2,
		Width: bottomCaption.Width - 8, Height: voiceCaptionPillHeight}
	if bottomPill.Y+bottomPill.Height > bottomPlate.Y || bottomPlate.Y-(bottomPill.Y+bottomPill.Height) != voiceCaptionGap {
		t.Fatalf("bottom caption overlaps or misses the upstream gap: plate=%+v pill=%+v", bottomPlate, bottomPill)
	}
	if got := voiceCaptionBoundsForHUD(topDot, mygo.Rectangle{X: 0, Y: 0, Width: 500, Height: 800}, false); got.Width != 476 || got.X != 12 {
		t.Fatalf("narrow-display caption bounds = %+v", got)
	}
}

func TestVoiceCaptionRepositionsRetainedPreviewWhenDotMovesToBottom(t *testing.T) {
	caption := &fakeVoiceCaption{}
	token := voiceInterimTestToken(t, 1)
	workArea := mygo.Rectangle{X: 0, Y: 30, Width: 1440, Height: 840}
	dot, screen := voiceDotHUDBounds(
		mygo.Rectangle{X: 0, Y: 0, Width: 1440, Height: 900}, workArea, true,
	)
	v := &voiceDotView{
		caption: caption, captionDraft: voiceInterimDraft{Stable: "kept words"},
		captionToken: token, hudBounds: dot, screenBounds: screen,
	}
	frame := voiceOverlayFrame{Visible: true, ShowLiveText: true, Token: token, DotAtBottom: true}
	if v.syncCaption(frame, time.Now()) || len(caption.updates) != 1 {
		t.Fatal("blank interim did not reposition the retained preview")
	}
	want := voiceCaptionBoundsForHUD(dot, screen, true)
	if caption.updates[0].stable != "kept words" || caption.updates[0].bounds != want {
		t.Fatalf("retained caption update = %+v, want bounds %+v", caption.updates[0], want)
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

func TestVoiceCaptionExplicitMarkedRangeClearHidesImmediately(t *testing.T) {
	caption := &fakeVoiceCaption{}
	token := voiceInterimTestToken(t, 1)
	v := &voiceDotView{
		caption: caption, captionDraft: voiceInterimDraft{Stable: "当前预编辑"}, captionToken: token,
	}
	frame := voiceOverlayFrame{Visible: true, ShowLiveText: true, Token: token, InterimClear: true}
	if v.syncCaption(frame, time.Now()) || caption.hides != 1 || v.captionDraft != (voiceInterimDraft{}) {
		t.Fatal("explicit marked-range clear retained or faded the previous draft")
	}
}

func TestVoiceDotHideDropsCaptionWhenCoreSessionEnds(t *testing.T) {
	caption := &fakeVoiceCaption{}
	token := voiceInterimTestToken(t, 1)
	v := &voiceDotView{
		caption: caption, captionDraft: voiceInterimDraft{Tentative: "当前临时文字"}, captionToken: token,
	}
	if err := v.Hide(); err != nil || caption.hides != 1 || v.captionDraft != (voiceInterimDraft{}) ||
		v.captionToken != (bridge.SessionToken{}) {
		t.Fatalf("hiding the voice panel retained caption state: err=%v hides=%d draft=%+v token=%+v",
			err, caption.hides, v.captionDraft, v.captionToken)
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

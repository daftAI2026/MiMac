package main

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"mimac.local/mimac/internal/bridge"
)

type routedEffectsKeySink struct {
	batches    [][]bridge.Effect
	failAt     map[int]error
	releases   int
	releaseErr error
	order      *[]string
}

func (s *routedEffectsKeySink) Submit(effects []bridge.Effect) error {
	batch := append([]bridge.Effect(nil), effects...)
	s.batches = append(s.batches, batch)
	if s.order != nil {
		names := make([]string, 0, len(batch))
		for _, effect := range batch {
			names = append(names, effectName(effect))
		}
		*s.order = append(*s.order, "keys:"+fmt.Sprint(names))
	}
	if err := s.failAt[len(s.batches)]; err != nil {
		return err
	}
	return nil
}

func (s *routedEffectsKeySink) ReleaseAll() error {
	s.releases++
	return s.releaseErr
}

func (*routedEffectsKeySink) Close() error       { return nil }
func (*routedEffectsKeySink) Held() []bridge.Key { return nil }

type routedEffectsAudioSink struct {
	effects []bridge.Effect
	failAt  map[int]error
	order   *[]string
}

func (s *routedEffectsAudioSink) Submit(effect bridge.Effect) error {
	s.effects = append(s.effects, effect)
	if s.order != nil {
		*s.order = append(*s.order, "audio:"+effectName(effect))
	}
	return s.failAt[len(s.effects)]
}

func effectName(effect bridge.Effect) string {
	switch effect.(type) {
	case bridge.KeyDownEffect:
		return "KeyDownEffect"
	case bridge.KeyUpEffect:
		return "KeyUpEffect"
	case bridge.TargetTapEffect:
		return "TargetTapEffect"
	case bridge.PrepareSourceEffect:
		return "PrepareSourceEffect"
	case bridge.StopSourceEffect:
		return "StopSourceEffect"
	case bridge.OpenGateEffect:
		return "OpenGateEffect"
	case bridge.CloseGateEffect:
		return "CloseGateEffect"
	case bridge.DisposeResourceEffect:
		return "DisposeResourceEffect"
	default:
		return fmt.Sprintf("%T", effect)
	}
}

func newEffectsTestCoordinator(t *testing.T, policy bridge.EndPolicy, keys ...bridge.Key) (*deviceCoordinator, bridge.Device) {
	t.Helper()
	d := newDeviceCoordinator()
	d.sink = &routedEffectsKeySink{}
	d.audio = &routedEffectsAudioSink{}
	binding, err := bridge.NewBinding(policy, keys...)
	if err != nil {
		t.Fatal(err)
	}
	device, err := bridge.NewDevice("effects-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []bridge.Event{
		bridge.Configure(binding),
		bridge.RouteAvailable(true),
		bridge.ScopeAvailable(true),
		bridge.DeviceConnected(device),
	} {
		if err := d.step(event); err != nil {
			t.Fatal(err)
		}
	}
	return d, device
}

func TestEffectDispatcherPreservesMixedOrderAndBatchesAdjacentKeys(t *testing.T) {
	order := []string{}
	d, device := newEffectsTestCoordinator(t, bridge.PolicyExactEdge, "Control", "Space")
	keys := d.sink.(*routedEffectsKeySink)
	audio := d.audio.(*routedEffectsAudioSink)
	keys.order = &order
	audio.order = &order

	press, err := bridge.NewPressToken(device, "voice", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.step(bridge.VoiceBegan(press)); err != nil {
		t.Fatal(err)
	}
	token := d.core.Snapshot().Session.Token
	if err := d.step(bridge.SourcePrepared(token, "source-1")); err != nil {
		t.Fatal(err)
	}
	if err := d.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	if err := d.step(bridge.SourceDrained(token)); err != nil {
		t.Fatal(err)
	}

	wantOrder := []string{
		"keys:[KeyDownEffect KeyDownEffect]",
		"audio:PrepareSourceEffect",
		"audio:OpenGateEffect",
		"keys:[KeyUpEffect KeyUpEffect]",
		"audio:StopSourceEffect",
		"audio:CloseGateEffect",
		"audio:DisposeResourceEffect",
	}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("dispatch order = %v, want %v", order, wantOrder)
	}
	if len(keys.batches) != 2 || len(keys.batches[0]) != 2 || len(keys.batches[1]) != 2 {
		t.Fatalf("adjacent key effects were not submitted as complete batches: %#v", keys.batches)
	}
}

func TestEffectDispatcherKeyFailureRunsCoreAndOriginalCleanup(t *testing.T) {
	d, device := newEffectsTestCoordinator(t, bridge.PolicyExactEdge, "Control", "Space")
	keys := d.sink.(*routedEffectsKeySink)
	audio := d.audio.(*routedEffectsAudioSink)
	press, _ := bridge.NewPressToken(device, "voice", 1)
	if err := d.step(bridge.VoiceBegan(press)); err != nil {
		t.Fatal(err)
	}
	token := d.core.Snapshot().Session.Token
	if err := d.step(bridge.SourcePrepared(token, "source-1")); err != nil {
		t.Fatal(err)
	}
	keys.failAt = map[int]error{2: errors.New("keyup failed")}

	if err := d.step(bridge.VoiceEnded(press)); err == nil {
		t.Fatal("failed key output was hidden")
	}
	if d.core.Snapshot().OutputFailureID != 1 || keys.releases != 1 {
		t.Fatalf("key failure transition/release = failure %d, releases %d", d.core.Snapshot().OutputFailureID, keys.releases)
	}
	got := make([]string, 0, len(audio.effects))
	for _, effect := range audio.effects[2:] { // Prepare and OpenGate were already submitted.
		got = append(got, effectName(effect))
	}
	want := []string{"StopSourceEffect", "CloseGateEffect", "StopSourceEffect", "DisposeResourceEffect"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup tail after key failure = %v, want %v", got, want)
	}
}

func TestEffectDispatcherTapFailureMarksWholeChordUncertain(t *testing.T) {
	d, device := newEffectsTestCoordinator(t, bridge.PolicyExactEdge, "Space")
	keys := d.sink.(*routedEffectsKeySink)
	keys.failAt = map[int]error{1: errors.New("tap output failed")}
	press, _ := bridge.NewPressToken(device, "tool", 1)
	plan, err := bridge.NewActionPlan(bridge.ActionSpec{
		ToolID: "tool", ActionID: "toggle", CandidateRaw: "Control+Space", Revision: 1,
		Scope: bridge.ActionGlobal, Trigger: bridge.ActionTap, Request: bridge.RequestToggle,
		ReleaseRequest: bridge.RequestNone,
	}, "Control", "Space")
	if err != nil {
		t.Fatal(err)
	}

	if err := d.step(bridge.TargetActionBegan(press, plan)); err == nil {
		t.Fatal("failed target tap was hidden")
	}
	snapshot := d.core.Snapshot()
	if snapshot.OutputFailureID != 1 || !reflect.DeepEqual(snapshot.RecoveryKeys, []bridge.Key{"Control", "Space"}) || keys.releases != 1 {
		t.Fatalf("tap failure lost uncertainty chord or cleanup: snapshot=%+v releases=%d", snapshot, keys.releases)
	}
}

func TestEffectDispatcherAudioFailureCleansOnceAndClosesRouteOnCleanupFailure(t *testing.T) {
	d, device := newEffectsTestCoordinator(t, bridge.PolicyExactEdge, "Space")
	audio := d.audio.(*routedEffectsAudioSink)
	prepareErr := errors.New("source prepare failed")
	cleanupErr := errors.New("gate close failed")
	audio.failAt = map[int]error{1: prepareErr, 2: cleanupErr}
	press, _ := bridge.NewPressToken(device, "voice", 1)

	err := d.step(bridge.VoiceBegan(press))
	if !errors.Is(err, prepareErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("audio and cleanup failures not returned: %v", err)
	}
	if got := []string{effectName(audio.effects[0]), effectName(audio.effects[1]), effectName(audio.effects[2])}; !reflect.DeepEqual(got, []string{"PrepareSourceEffect", "CloseGateEffect", "StopSourceEffect"}) {
		t.Fatalf("audio cleanup sequence = %v", got)
	}
	snapshot := d.core.Snapshot()
	if snapshot.HasSession || !snapshot.RouteKnown || snapshot.RouteAvailable {
		t.Fatalf("cleanup failure did not close route after ending session: %+v", snapshot)
	}
	if keys := d.sink.(*routedEffectsKeySink).batches; len(keys) != 2 {
		t.Fatalf("source failure did not release submitted key lease: %#v", keys)
	}
}

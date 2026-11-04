package audiosession

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/bridge"
)

func TestInputLevelUsesFullScaleAndActualSampleCount(t *testing.T) {
	at := time.Now()
	for _, test := range []struct {
		name      string
		pcm       []int16
		rms, peak float32
	}{
		{"silence", []int16{0, 0}, 0, 0},
		{"negative full scale", []int16{-32768}, 1, 1},
		{"half scale", []int16{16384, -16384, 16384}, .5, .5},
		{"impulse", []int16{-32768, 0, 0, 0}, .5, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := measureInputLevel(test.pcm, at)
			if got.RMS != test.rms || got.Peak != test.peak || got.Samples != len(test.pcm) || got.ObservedAt != at {
				t.Fatalf("level = %+v, want RMS=%v peak=%v samples=%d", got, test.rms, test.peak, len(test.pcm))
			}
		})
	}
	if got := measureInputLevel(nil, at); got != (InputLevel{}) {
		t.Fatalf("empty block invented an observation: %+v", got)
	}
}

func TestRuntimeInputLevelFollowsPCMAndClearsBeforeDrain(t *testing.T) {
	for _, rate := range []int{8000, 16000} {
		t.Run(strconv.Itoa(rate), func(t *testing.T) {
			source, sink := newSourceStub(), newSinkStub()
			observer := &levelObserverStub{}
			drainRelease := make(chan struct{})
			sink.drainRelease = drainRelease
			r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
				return Resources{Source: source, Sink: sink, SampleRate: rate, Observer: observer}, nil
			})
			core, device := newCoreForSessionTest(t, "input-level")
			press, token, prepare := beginVoiceSession(t, core, device, 1)
			if err := r.Submit(prepare); err != nil {
				t.Fatal(err)
			}
			ready := waitCompletion(t, r)
			if _, ok := r.InputLevel(token); ok {
				t.Fatal("preparation invented PCM")
			}
			applySourceEffects(t, r, applyCompletion(t, core, ready))
			pcm := make([]int16, rate/50)
			for i := range pcm {
				pcm[i] = int16(math.Round(16384 * math.Sin(2*math.Pi*1000*float64(i)/float64(rate))))
			}
			source.reads <- readResult{pcm: pcm}
			if got := waitLevelWrite(t, sink); !reflect.DeepEqual(got, pcm) {
				t.Fatal("meter changed the delivered PCM")
			}
			if got := observer.samples(); !reflect.DeepEqual(got, pcm) {
				t.Fatal("observer did not receive the same PCM delivered to the sink")
			}
			level, ok := r.InputLevel(token)
			if !ok || math.Abs(float64(level.RMS)-math.Sqrt(.125)) > .00004 || level.Peak != .5 || level.Samples != len(pcm) || level.ObservedAt.IsZero() {
				t.Fatalf("source PCM missing or mismeasured: ok=%v level=%+v", ok, level)
			}
			if _, ok := r.InputLevel(bridge.SessionToken{}); ok {
				t.Fatal("unrelated token exposed a level")
			}
			// A second, silent short block replaces the value; no previous peak
			// or audio buffer is retained, and silence remains a real observation.
			source.reads <- readResult{pcm: []int16{0}}
			waitLevelWrite(t, sink)
			level, ok = r.InputLevel(token)
			if !ok || level.RMS != 0 || level.Peak != 0 || level.Samples != 1 {
				t.Fatalf("silence = %+v, %v", level, ok)
			}
			ended, err := core.Step(bridge.VoiceEnded(press))
			if err != nil {
				t.Fatal(err)
			}
			applySourceEffects(t, r, ended)
			waitSignal(t, sink.drainStarted)
			if observer.samples() != nil {
				t.Fatal("observer retained source data while output was draining")
			}
			if _, ok := r.InputLevel(token); ok {
				t.Fatal("EOF left an input level while final output was draining")
			}
			close(drainRelease)
			applySourceEffects(t, r, applyCompletion(t, core, waitCompletion(t, r)))
			if _, ok := r.InputLevel(token); ok {
				t.Fatal("retired session retained an input level")
			}
		})
	}
}

// The test deliberately copies borrowed samples to compare delivery; the
// production observer retains only its bounded analyzer window and spectrum.
type levelObserverStub struct {
	mu     sync.Mutex
	pcm    []int16
	clears int
}

func (o *levelObserverStub) Observe(pcm []int16, _ time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pcm = append([]int16(nil), pcm...)
}
func (o *levelObserverStub) Clear() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pcm = nil
	o.clears++
}

func TestObserverClearsWhenSessionNeverOpensPCMGate(t *testing.T) {
	for _, mode := range []string{"gate-cancel", "receiver-error"} {
		t.Run(mode, func(t *testing.T) {
			source, sink, observer := newSourceStub(), newSinkStub(), &levelObserverStub{}
			r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
				v := Resources{Source: source, Sink: sink, SampleRate: 16000, Observer: observer}
				if mode == "receiver-error" {
					v.WaitReceiver = func(context.Context) error { return errors.New("receiver unavailable") }
				}
				return v, nil
			})
			core, device := newCoreForSessionTest(t, "observer-preflight")
			_, token, prepare := beginVoiceSession(t, core, device, 1)
			if err := r.Submit(prepare); err != nil {
				t.Fatal(err)
			}
			completion := waitCompletion(t, r)
			if mode == "gate-cancel" {
				if completion.Err != nil {
					t.Fatal(completion.Err)
				}
				waitSignal(t, source.readStarted)
				if err := r.Submit(bridge.CloseGateEffect{Token: token}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := r.Close(ctx); err != nil {
				t.Fatal(err)
			}
			observer.mu.Lock()
			clears, n := observer.clears, len(observer.pcm)
			observer.mu.Unlock()
			if clears == 0 || n != 0 {
				t.Fatalf("unopened gate skipped observer retirement: clears=%d samples=%d", clears, n)
			}
			if sink.writeCalls.Load() != 0 {
				t.Fatalf("unopened gate delivered %d PCM writes", sink.writeCalls.Load())
			}
		})
	}
}
func (o *levelObserverStub) samples() []int16 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]int16(nil), o.pcm...)
}

func waitLevelWrite(t *testing.T, sink *sinkStub) []int16 {
	t.Helper()
	select {
	case pcm := <-sink.writes:
		return pcm
	case <-time.After(audioSessionTestTimeout):
		t.Fatal("source PCM never reached the sink")
		return nil
	}
}

type lateLevelSource struct {
	*sourceStub
	lateStarted chan struct{}
	lateRelease chan struct{}
	readsSeen   int
}

func (s *lateLevelSource) Read(ctx context.Context, pcm []int16) (int, error) {
	s.readsSeen++
	if s.readsSeen != 2 {
		return s.sourceStub.Read(ctx, pcm)
	}
	close(s.lateStarted)
	<-s.lateRelease // Deliberately models a native Read returning after cancel.
	pcm[0] = -32768
	return 1, nil
}

func TestRuntimeInputLevelRejectsCancelledReadAndRetiredToken(t *testing.T) {
	first := &lateLevelSource{sourceStub: newSourceStub(), lateStarted: make(chan struct{}), lateRelease: make(chan struct{})}
	second := newSourceStub()
	firstSink, secondSink := newSinkStub(), newSinkStub()
	calls := 0
	r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		calls++
		if calls == 1 {
			return Resources{Source: first, Sink: firstSink, SampleRate: 16000}, nil
		}
		return testResources(second, secondSink), nil
	})
	t.Cleanup(func() {
		select {
		case <-first.lateRelease:
		default:
			close(first.lateRelease)
		}
	})
	core, device := newCoreForSessionTest(t, "level-cancel")
	press, token, prepare := beginVoiceSession(t, core, device, 1)
	if err := r.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, r, applyCompletion(t, core, waitCompletion(t, r)))
	first.reads <- readResult{pcm: []int16{16384}}
	waitLevelWrite(t, firstSink)
	if level, ok := r.InputLevel(token); !ok || level.RMS != .5 {
		t.Fatalf("positive control missing: %+v, %v", level, ok)
	}
	waitSignal(t, first.lateStarted)
	r.mu.Lock()
	old := r.active
	r.mu.Unlock()
	effects, _ := core.Step(bridge.RouteAvailable(false))
	applySourceEffects(t, r, effects)
	if _, ok := r.InputLevel(token); ok {
		t.Fatal("cancellation retained a visible level")
	}
	close(first.lateRelease)
	waitSignal(t, old.done)
	if firstSink.writeCalls.Load() != 1 {
		t.Fatal("late cancelled PCM was delivered")
	}
	if _, ok := r.InputLevel(token); ok {
		t.Fatal("late Read revived the cancelled level")
	}
	for _, event := range []bridge.Event{bridge.VoiceEnded(press), bridge.RouteAvailable(true)} {
		effects, err := core.Step(event)
		if err != nil {
			t.Fatal(err)
		}
		applySourceEffects(t, r, effects)
	}
	_, nextToken, next := beginVoiceSession(t, core, device, 2)
	if err := r.Submit(next); err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, r, applyCompletion(t, core, waitCompletion(t, r)))
	if _, ok := r.InputLevel(nextToken); ok {
		t.Fatal("new session inherited the old peak")
	}
	second.reads <- readResult{pcm: []int16{8192}}
	waitLevelWrite(t, secondSink)
	if got, ok := r.InputLevel(nextToken); !ok || got.RMS != .25 {
		t.Fatalf("new PCM missing: %+v %v", got, ok)
	}
	if _, ok := r.InputLevel(token); ok {
		t.Fatal("old token read the new session's level")
	}
	second.reads <- readResult{err: errTestRead}
	failed := waitCompletion(t, r)
	if !errors.Is(failed.Err, errTestRead) {
		t.Fatalf("read error = %v", failed.Err)
	}
	if _, ok := r.InputLevel(nextToken); ok {
		t.Fatal("failed read retained its last level")
	}
}

type backpressuredLevelSink struct {
	*sinkStub
	attempted chan struct{}
	once      sync.Once
}

func (s *backpressuredLevelSink) TryWrite([]int16) error {
	s.once.Do(func() { close(s.attempted) })
	return audiooutput.ErrBackpressure
}

func TestRuntimeInputLevelDescribesSourceWhileSinkHasBackpressure(t *testing.T) {
	source := newSourceStub()
	sink := &backpressuredLevelSink{sinkStub: newSinkStub(), attempted: make(chan struct{})}
	r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return Resources{Source: source, Sink: sink, SampleRate: 16000}, nil
	})
	core, device := newCoreForSessionTest(t, "input-with-backpressure")
	_, token, prepare := beginVoiceSession(t, core, device, 1)
	if err := r.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, r, applyCompletion(t, core, waitCompletion(t, r)))
	source.reads <- readResult{pcm: []int16{16384, -16384}}
	waitSignal(t, sink.attempted)
	if level, ok := r.InputLevel(token); !ok || level.RMS != .5 || level.Peak != .5 {
		t.Fatalf("blocked output hid received source data: %+v, %v", level, ok)
	}
	select {
	case <-sink.writes:
		t.Fatal("fixture unexpectedly accepted PCM")
	default:
	}
	if err := r.Submit(bridge.CloseGateEffect{Token: token}); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.InputLevel(token); ok {
		t.Fatal("cancelled backpressure retained a level")
	}
}

func BenchmarkRecordInputLevel(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &resource{ctx: ctx}
	r := &Runtime{active: s}
	pcm := make([]int16, 320)
	for i := range pcm {
		pcm[i] = int16(i*101 - 16000)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(pcm) * 2))
	for b.Loop() {
		r.recordInputLevel(s, pcm)
	}
}

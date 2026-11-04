package audiosession

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
	"mimac.local/mimac/internal/bridge"
)

// This fixture uses the actual Runtime and ATVV Source with synthetic packets
// and callbacks. It never opens a native transport, target app, or audio device.
func TestRuntimeReadsATVVBeforeReceiverButHoldsPCMUntilCoreGate(t *testing.T) {
	_, transport, source := newRuntimeATVVFixture(t)
	sink := newRuntimeATVVTestSink()
	observer := &runtimeATVVObserver{}
	waitEntered := make(chan struct{})
	releaseReceiver := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseReceiver) }) }
	t.Cleanup(release)
	r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return Resources{
			Source:     source,
			Sink:       sink,
			Observer:   observer,
			SampleRate: 16000,
			WaitReceiver: func(ctx context.Context) error {
				close(waitEntered)
				select {
				case <-releaseReceiver:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
		}, nil
	})
	t.Cleanup(func() {
		if transport.audioStarted.Load() && !transport.audioStopSeen.Load() {
			_ = transport.emit(atvv.NativeEventControlValue, []byte{0x00})
		}
	})
	core, device := newCoreForSessionTest(t, "atvv-pre-receiver-stage")
	press, token, prepare := beginVoiceSession(t, core, device, 1)
	if err := r.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, waitEntered)

	// On the old Runtime no Source.Read is started while WaitReceiver is held.
	// Continue through packet 65 in that baseline so the failure records the
	// actual ATVV ring boundary instead of only a missing-read assertion.
	readerStarted := false
	select {
	case <-source.readStarted:
		readerStarted = true
	case <-time.After(300 * time.Millisecond):
	}
	if err := transport.emit(atvv.NativeEventControlValue, []byte{0x04, 0x03, 0x02, 0x0B}); err != nil {
		t.Fatal(err)
	}
	packets, expected := runtimeATVVPCMFixture(134)
	for i, packet := range packets {
		err := transport.emit(atvv.NativeEventAudioValue, packet)
		if err != nil {
			t.Fatalf("packet %d rejected: %v", i+1, err)
		}
		if readerStarted {
			waitATVVReadSamples(t, source, min((i+1)*len(expected)/len(packets), 2*16000))
		}
	}
	if !readerStarted {
		overflow := transport.emit(atvv.NativeEventAudioValue, make([]byte, 120))
		if !errors.Is(overflow, atvv.ErrSourceQueueFull) {
			t.Fatalf("old Runtime baseline packet 65 error = %v, want ErrSourceQueueFull", overflow)
		}
		t.Fatalf("Reader never started before WaitReceiver; old Runtime overflowed packet 65: %v", overflow)
	}
	if got := source.samples.Load(); got != 2*16000 {
		t.Fatalf("pre-gate stage samples = %d, want exact two-second capacity %d", got, 2*16000)
	}
	assertATVVNoOutputBeforeCoreGate(t, r, token, sink, observer)

	release()
	prepared := waitCompletion(t, r)
	if prepared.Err != nil {
		t.Fatalf("receiver-ready completion = %v", prepared.Err)
	}
	// SourcePrepared has reached the host, but Core has not processed it yet.
	assertATVVNoOutputBeforeCoreGate(t, r, token, sink, observer)
	applySourceEffects(t, r, applyCompletion(t, core, prepared))
	waitATVVSinkSamples(t, sink, len(expected))
	waitATVVReadSamples(t, source, len(expected))
	if got := source.maxActive.Load(); got != 1 {
		t.Fatalf("concurrent Source.Read calls = %d, want one active reader", got)
	}
	if got := sink.snapshot(); !reflect.DeepEqual(got, expected) {
		t.Fatalf("delivered PCM differs from ATVV decode: got %d samples, want %d", len(got), len(expected))
	}

	ended, err := core.Step(bridge.VoiceEnded(press))
	if err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, r, ended)
	waitSignal(t, transport.stopRequested)
	if err := transport.emit(atvv.NativeEventControlValue, []byte{0x00}); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, sink.drainStarted)
	if got := sink.drainCalls.Load(); got != 1 {
		t.Fatalf("sink Drain calls = %d, want exactly one", got)
	}
	applySourceEffects(t, r, applyCompletion(t, core, waitCompletion(t, r)))
}

// TestRuntimeKeepsATVVReadMovingWhilePacedSinkFlushesStartupStage reproduces
// a live stream arriving while the startup FIFO is written at the sink's
// four-buffer, real-time pace. A Runtime that hands Source.Read back only
// after flushing the entire FIFO leaves ATVV's 960 ms ring idle long enough
// to overflow.
func TestRuntimeKeepsATVVReadMovingWhilePacedSinkFlushesStartupStage(t *testing.T) {
	_, transport, source := newRuntimeATVVFixture(t)
	sink := newPacedRuntimeATVVTestSink()
	observer := &runtimeATVVObserver{}
	waitEntered := make(chan struct{})
	releaseReceiver := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseReceiver) }) }
	t.Cleanup(release)
	t.Cleanup(func() { _ = sink.Close() })
	r := newRuntimeForTest(t, func(context.Context, bridge.SessionToken) (Resources, error) {
		return Resources{
			Source:     source,
			Sink:       sink,
			Observer:   observer,
			SampleRate: 16000,
			WaitReceiver: func(ctx context.Context) error {
				close(waitEntered)
				select {
				case <-releaseReceiver:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
		}, nil
	})
	t.Cleanup(func() {
		if transport.audioStarted.Load() && !transport.audioStopSeen.Load() {
			_ = transport.emit(atvv.NativeEventControlValue, []byte{0x00})
		}
	})
	core, device := newCoreForSessionTest(t, "atvv-paced-stage-flush")
	press, _, prepare := beginVoiceSession(t, core, device, 1)
	if err := r.Submit(prepare); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, waitEntered)
	if err := transport.emit(atvv.NativeEventControlValue, []byte{0x04, 0x03, 0x02, 0x0B}); err != nil {
		t.Fatal(err)
	}
	allPackets, wantPCM := runtimeATVVPCMFixture(180)
	initialPackets := allPackets[:100]
	for i, packet := range initialPackets {
		if err := transport.emit(atvv.NativeEventAudioValue, packet); err != nil {
			t.Fatalf("initial packet %d rejected: %v", i+1, err)
		}
		waitATVVReadSamples(t, source, (i+1)*240)
	}
	if got := source.samples.Load(); got != 100*240 {
		t.Fatalf("pre-gate startup samples = %d, want %d", got, 100*240)
	}
	release()
	prepared := waitCompletion(t, r)
	if prepared.Err != nil {
		t.Fatalf("receiver-ready completion = %v", prepared.Err)
	}
	applySourceEffects(t, r, applyCompletion(t, core, prepared))

	// ATVV packets represent 15 ms each. Continue at that source cadence while
	// the sink consumes 320-sample buffers every 20 ms (four buffers queued).
	// This runs for 1.2 s, long enough for an idle 960 ms source ring to fail.
	morePackets := allPackets[100:]
	ticker := time.NewTicker(15 * time.Millisecond)
	defer ticker.Stop()
	for i, packet := range morePackets {
		<-ticker.C
		if err := transport.emit(atvv.NativeEventAudioValue, packet); err != nil {
			t.Fatalf("live packet %d rejected during staged flush: %v", i+1, err)
		}
	}

	waitATVVReadSamples(t, source, len(wantPCM))
	waitPacedATVVSinkSamples(t, sink, len(wantPCM))
	if got := sink.snapshot(); !reflect.DeepEqual(got, wantPCM) {
		t.Fatalf("paced sink PCM differs: got %d samples, want %d", len(got), len(wantPCM))
	}
	ended, err := core.Step(bridge.VoiceEnded(press))
	if err != nil {
		t.Fatal(err)
	}
	applySourceEffects(t, r, ended)
	waitSignal(t, transport.stopRequested)
	if err := transport.emit(atvv.NativeEventControlValue, []byte{0x00}); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, sink.drainStarted)
	if got := sink.drainCalls.Load(); got != 1 {
		t.Fatalf("paced sink Drain calls = %d, want exactly one", got)
	}
	applySourceEffects(t, r, applyCompletion(t, core, waitCompletion(t, r)))
}

func assertATVVNoOutputBeforeCoreGate(t *testing.T, r *Runtime, token bridge.SessionToken, sink *runtimeATVVTestSink, observer *runtimeATVVObserver) {
	t.Helper()
	if got := sink.writes.Load(); got != 0 {
		t.Fatalf("Sink.TryWrite calls before Core gate = %d, want 0", got)
	}
	if got := observer.observations.Load(); got != 0 {
		t.Fatalf("PCMObserver.Observe calls before Core gate = %d, want 0", got)
	}
	if _, ok := r.InputLevel(token); ok {
		t.Fatal("input level was recorded before Core gate")
	}
}

type runtimeATVVTransport struct {
	handler       atvv.NativeEventHandler
	stopRequested chan struct{}
	audioStarted  atomic.Bool
	audioStopSeen atomic.Bool
}

func newRuntimeATVVFixture(t *testing.T) (*atvv.DeviceOwner, *runtimeATVVTransport, *runtimeATVVSource) {
	t.Helper()
	key := atvv.StreamKey{Generation: 1, Session: 1}
	transport := &runtimeATVVTransport{stopRequested: make(chan struct{}, 1)}
	owner, err := atvv.NewDeviceOwner(key.Generation, func(handler atvv.NativeEventHandler) (atvv.DeviceTransport, error) {
		transport.handler = handler
		return transport, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// This cleanup is registered before Runtime cleanup, so the Runtime releases
	// the session lease first.
	t.Cleanup(func() { _ = owner.Close() })
	source, err := owner.NewSource(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return owner, transport, &runtimeATVVSource{
		inner:       source,
		readStarted: make(chan struct{}, 1),
		readChanged: make(chan struct{}, 1),
	}
}

func (t *runtimeATVVTransport) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := t.emit(atvv.NativeEventReady, nil); err != nil {
		return err
	}
	return t.emit(atvv.NativeEventControlValue, []byte{0x0B, 0x01, 0x00, 0x00, 0x03, 0x00, 0x78, 0x00, 0x00})
}

func (t *runtimeATVVTransport) emit(kind atvv.NativeEventKind, payload []byte) error {
	if t.handler == nil {
		return errors.New("test ATVV transport has no callback")
	}
	if kind == atvv.NativeEventControlValue && len(payload) > 0 {
		if payload[0] == 0x04 {
			t.audioStarted.Store(true)
		} else if payload[0] == 0x00 {
			t.audioStopSeen.Store(true)
		}
	}
	return t.handler(atvv.NativeEvent{Kind: kind, Bytes: payload})
}

func (t *runtimeATVVTransport) RequestStop(ctx context.Context, _ byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// RequestStop is a host request; the test emits the matching remote stop
	// separately to exercise ATVV's two-sided EOF contract.
	signal(t.stopRequested)
	return nil
}

func (*runtimeATVVTransport) Close() error { return nil }

type runtimeATVVSource struct {
	inner       *atvv.SessionSource
	readStarted chan struct{}
	readChanged chan struct{}
	reads       atomic.Int64
	active      atomic.Int64
	maxActive   atomic.Int64
	samples     atomic.Int64
}

func (s *runtimeATVVSource) Read(ctx context.Context, dst []int16) (int, error) {
	s.reads.Add(1)
	active := s.active.Add(1)
	for max := s.maxActive.Load(); active > max; max = s.maxActive.Load() {
		if s.maxActive.CompareAndSwap(max, active) {
			break
		}
	}
	defer s.active.Add(-1)
	signal(s.readStarted)
	n, err := s.inner.Read(ctx, dst)
	if n > 0 {
		s.samples.Add(int64(n))
	}
	signal(s.readChanged)
	return n, err
}

func (s *runtimeATVVSource) Stop(ctx context.Context) error { return s.inner.Stop(ctx) }
func (s *runtimeATVVSource) Close() error                   { return s.inner.Close() }
func (s *runtimeATVVSource) Err() error                     { return s.inner.Err() }

type runtimeATVVTestSink struct {
	mu           sync.Mutex
	pcm          []int16
	writes       atomic.Int64
	drainCalls   atomic.Int64
	drainStarted chan struct{}
	changed      chan struct{}
}

type pacedRuntimeATVVTestSink struct {
	mu           sync.Mutex
	pcm          []int16
	queued       int
	writes       atomic.Int64
	drainCalls   atomic.Int64
	drainStarted chan struct{}
	changed      chan struct{}
	stop         chan struct{}
	done         chan struct{}
	closeOnce    sync.Once
}

func newPacedRuntimeATVVTestSink() *pacedRuntimeATVVTestSink {
	sink := &pacedRuntimeATVVTestSink{
		drainStarted: make(chan struct{}, 1),
		changed:      make(chan struct{}, 1),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
	go sink.play()
	return sink
}

func (s *pacedRuntimeATVVTestSink) play() {
	defer close(s.done)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.mu.Lock()
			if s.queued > 0 {
				s.queued--
				s.signalChanged()
			}
			s.mu.Unlock()
		}
	}
}

func (s *pacedRuntimeATVVTestSink) TryWrite(pcm []int16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queued >= 4 {
		return audiooutput.ErrBackpressure
	}
	s.pcm = append(s.pcm, pcm...)
	s.queued++
	s.writes.Add(1)
	s.signalChanged()
	return nil
}

func (s *pacedRuntimeATVVTestSink) Drain(ctx context.Context) error {
	s.drainCalls.Add(1)
	signal(s.drainStarted)
	for {
		s.mu.Lock()
		queued := s.queued
		wake := s.changed
		s.mu.Unlock()
		if queued == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}

func (s *pacedRuntimeATVVTestSink) Close() error {
	s.closeOnce.Do(func() { close(s.stop) })
	<-s.done
	return nil
}

func (s *pacedRuntimeATVVTestSink) snapshot() []int16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int16(nil), s.pcm...)
}

func (s *pacedRuntimeATVVTestSink) signalChanged() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func newRuntimeATVVTestSink() *runtimeATVVTestSink {
	return &runtimeATVVTestSink{drainStarted: make(chan struct{}, 1), changed: make(chan struct{}, 1)}
}

func (s *runtimeATVVTestSink) TryWrite(pcm []int16) error {
	s.writes.Add(1)
	s.mu.Lock()
	s.pcm = append(s.pcm, pcm...)
	s.mu.Unlock()
	signal(s.changed)
	return nil
}

func (s *runtimeATVVTestSink) Drain(context.Context) error {
	s.drainCalls.Add(1)
	signal(s.drainStarted)
	return nil
}

func (*runtimeATVVTestSink) Close() error { return nil }

func (s *runtimeATVVTestSink) snapshot() []int16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int16(nil), s.pcm...)
}

type runtimeATVVObserver struct{ observations atomic.Int64 }

func (o *runtimeATVVObserver) Observe([]int16, time.Time) { o.observations.Add(1) }
func (*runtimeATVVObserver) Clear()                       {}

func runtimeATVVPCMFixture(packetCount int) ([][]byte, []int16) {
	decoder := &atvv.Decoder{}
	packets := make([][]byte, packetCount)
	expected := make([]int16, 0, packetCount*240)
	for i := range packets {
		packet := make([]byte, 120)
		for j := range packet {
			packet[j] = byte((i*29 + j*7 + 3) & 0xFF)
		}
		decoded := make([]int16, 240)
		n, err := decoder.Decode(decoded, packet)
		if err != nil {
			panic(err)
		}
		packets[i] = packet
		expected = append(expected, decoded[:n]...)
	}
	return packets, expected
}

func waitATVVReadSamples(t *testing.T, source *runtimeATVVSource, want int) {
	t.Helper()
	timer := time.NewTimer(audioSessionTestTimeout)
	defer timer.Stop()
	for source.samples.Load() < int64(want) {
		select {
		case <-source.readChanged:
		case <-timer.C:
			t.Fatalf("ATVV Reader delivered %d samples to Runtime, want %d", source.samples.Load(), want)
		}
	}
}

func waitATVVSinkSamples(t *testing.T, sink *runtimeATVVTestSink, want int) {
	t.Helper()
	deadline := time.NewTimer(audioSessionTestTimeout)
	defer deadline.Stop()
	for len(sink.snapshot()) < want {
		select {
		case <-deadline.C:
			t.Fatalf("sink accepted %d samples, want %d", len(sink.snapshot()), want)
		case <-sink.changed:
		}
	}
}

func waitPacedATVVSinkSamples(t *testing.T, sink *pacedRuntimeATVVTestSink, want int) {
	t.Helper()
	deadline := time.NewTimer(audioSessionTestTimeout)
	defer deadline.Stop()
	for len(sink.snapshot()) < want {
		select {
		case <-deadline.C:
			t.Fatalf("paced sink accepted %d samples, want %d", len(sink.snapshot()), want)
		case <-sink.changed:
		}
	}
}

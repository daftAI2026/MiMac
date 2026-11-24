package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	hexapi "mimac.local/mimac/internal/adapters/hex"
	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

type hexRouteTestSource struct{ *hostPCMSource }

func (*hexRouteTestSource) Ready(context.Context) (int, error) { return 16000, nil }

type hexRouteTestStream struct {
	id                 uint64
	partials           bool
	initial            []hexapi.Event
	readErr            error
	returnAfterInitial bool
	events             chan hexapi.Event
	final              string
	finishErr          error
	writes             atomic.Int32
	finishes           atomic.Int32
	cancels            atomic.Int32
}

func newHexRouteTestStream(id uint64, initial ...hexapi.Event) *hexRouteTestStream {
	return &hexRouteTestStream{id: id, partials: true, initial: initial, events: make(chan hexapi.Event, 4)}
}

func (s *hexRouteTestStream) ID() uint64     { return s.id }
func (s *hexRouteTestStream) Partials() bool { return s.partials }
func (s *hexRouteTestStream) WritePCM(context.Context, []int16) error {
	s.writes.Add(1)
	return nil
}
func (s *hexRouteTestStream) ReadEvents(ctx context.Context, receive func(hexapi.Event)) error {
	for _, event := range s.initial {
		receive(event)
		if event.Type == "final" || event.Type == "error" || event.Type == "cancelled" {
			return nil
		}
	}
	if s.returnAfterInitial {
		return s.readErr
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-s.events:
			receive(event)
			if event.Type == "final" || event.Type == "error" || event.Type == "cancelled" {
				return nil
			}
		}
	}
}
func (s *hexRouteTestStream) Finish(ctx context.Context) (string, error) {
	s.finishes.Add(1)
	if s.finishErr != nil {
		return "", s.finishErr
	}
	select {
	case s.events <- hexapi.Event{Type: "final", Text: s.final}:
		return s.final, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (*hexRouteTestStream) Heartbeat(context.Context) error { return nil }
func (s *hexRouteTestStream) Cancel(context.Context) error {
	s.cancels.Add(1)
	return nil
}

type hexRouteTestClient struct {
	mu      sync.Mutex
	streams []*hexRouteTestStream
	next    int
}

func (c *hexRouteTestClient) CreateStream(ctx context.Context) (hexStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.next >= len(c.streams) {
		return nil, errors.New("test client has no stream")
	}
	stream := c.streams[c.next]
	c.next++
	return stream, nil
}

type blockingHexStreamClient struct {
	started chan struct{}
	release chan struct{}
	stream  hexStream
}

func (c *blockingHexStreamClient) CreateStream(ctx context.Context) (hexStream, error) {
	close(c.started)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.release:
		return c.stream, nil
	}
}

func TestHexRouteKeepsPartialsOutOfEditorAndDeliversFinalOnce(t *testing.T) {
	partial := make(chan struct {
		token     bridge.SessionToken
		stable    string
		tentative string
	}, 4)
	first := newHexRouteTestStream(1, hexapi.Event{Type: "ready"}, hexapi.Event{Type: "partial", Stable: "settled", Tentative: "draft"})
	first.final = "final transcript"
	second := newHexRouteTestStream(2, hexapi.Event{Type: "ready"})
	second.final = ""
	third := newHexRouteTestStream(3, hexapi.Event{Type: "ready"})
	client := &hexRouteTestClient{streams: []*hexRouteTestStream{first, second, third}}
	targets := []*transcriptTargetTestStub{{}, {}, {}}
	var capture atomic.Int32
	route, err := newHexAudioRoute(1,
		func(context.Context, atvv.StreamKey) (audiosession.Source, error) {
			return &hexRouteTestSource{hostPCMSource: newHostPCMSource()}, nil
		}, func() error { return nil }, client,
		func(token bridge.SessionToken, stable, tentative string) bool {
			partial <- struct {
				token     bridge.SessionToken
				stable    string
				tentative string
			}{token, stable, tentative}
			return true
		}, func() (transcriptTarget, error) {
			index := int(capture.Add(1)) - 1
			if index >= len(targets) {
				return nil, errors.New("too many text target captures")
			}
			return targets[index], nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := route.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := route.Close(ctx); err != nil {
			t.Errorf("close Hex route: %v", err)
		}
	})
	waitHexRouteReady(t, route)

	tokens := hexRouteTestTokens(t, 3)
	firstToken := tokens[0]
	firstResources, firstSink := startHexRouteSession(t, route, firstToken)
	select {
	case got := <-partial:
		if got.token != firstToken || got.stable != "settled" || got.tentative != "draft" {
			t.Fatalf("partial callback = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Hex partial did not reach the interim callback")
	}
	if err := firstSink.TryWrite([]int16{1, -1}); err != nil {
		t.Fatal(err)
	}
	if err := firstSink.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	text, present, err := firstSink.Transcript()
	if err != nil || !present || text != first.final {
		t.Fatalf("first transcript = %q/%t, %v", text, present, err)
	}
	if got := targets[0].inserted(); len(got) != 0 {
		t.Fatalf("SSE partial was committed to the editor: %v", got)
	}
	if err := route.DeliverTranscript(firstToken, text); err != nil {
		t.Fatal(err)
	}
	if err := route.DeliverTranscript(firstToken, text); err == nil {
		t.Fatal("re-entrant final completion delivered twice")
	}
	if got, want := targets[0].inserted(), []string{first.final}; !equalStrings(got, want) {
		t.Fatalf("final editor inserts = %v, want %v", got, want)
	}
	closeHexRouteSession(t, firstResources)
	waitHexRouteReady(t, route)
	if first.cancels.Load() != 0 {
		t.Fatalf("completed upstream stream was cancelled %d times", first.cancels.Load())
	}

	secondToken := tokens[1]
	secondResources, secondSink := startHexRouteSession(t, route, secondToken)
	if err := secondSink.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	text, present, err = secondSink.Transcript()
	if err != nil || !present || text != "" {
		t.Fatalf("empty transcript = %q/%t, %v", text, present, err)
	}
	if err := route.DeliverTranscript(secondToken, text); err != nil {
		t.Fatal(err)
	}
	if got := targets[1].inserted(); len(got) != 0 {
		t.Fatalf("empty transcript wrote to the editor: %v", got)
	}
	closeHexRouteSession(t, secondResources)
	waitHexRouteReady(t, route)
	if err := route.DeliverTranscript(firstToken, "stale first selection"); err == nil {
		t.Fatal("retired selection delivered into a newer target")
	}

	thirdToken := tokens[2]
	thirdResources, thirdSink := startHexRouteSession(t, route, thirdToken)
	if err := thirdSink.Close(); err != nil {
		t.Fatal(err)
	}
	if third.cancels.Load() != 1 {
		t.Fatalf("cancelled upstream stream cancel count = %d, want 1", third.cancels.Load())
	}
	closeHexRouteSession(t, thirdResources)
	if err := route.DeliverTranscript(thirdToken, "late cancelled text"); err == nil {
		t.Fatal("cancelled session delivered late text")
	}
	for index, target := range targets {
		if target.closeCalls.Load() != 1 {
			t.Fatalf("target %d close count = %d, want 1", index, target.closeCalls.Load())
		}
	}
}

func TestHexSessionEOFBeforeReadyFailsAndCloseCancelsStream(t *testing.T) {
	stream := newHexRouteTestStream(1)
	stream.returnAfterInitial = true
	client := &hexRouteTestClient{streams: []*hexRouteTestStream{stream}}
	sink := &hexSessionSink{client: client, target: &transcriptTargetTestStub{}, token: preparedAudioToken(t)}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := sink.StartSession(ctx); err == nil {
		t.Fatal("SSE EOF before ready returned success")
	}
	closed := make(chan error, 1)
	go func() { closed <- sink.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close blocked after early SSE EOF")
	}
	if stream.cancels.Load() != 1 {
		t.Fatalf("early EOF stream cancel count = %d, want 1", stream.cancels.Load())
	}
}

func TestHexSessionLocalEventsFailureCancelsOrphanedStream(t *testing.T) {
	stream := newHexRouteTestStream(1, hexapi.Event{Type: "ready"})
	stream.returnAfterInitial = true
	stream.readErr = errors.New("local SSE connection closed")
	client := &hexRouteTestClient{streams: []*hexRouteTestStream{stream}}
	sink := &hexSessionSink{client: client, target: &transcriptTargetTestStub{}, token: preparedAudioToken(t)}
	_ = sink.StartSession(context.Background())
	select {
	case <-sink.eventsDone:
	case <-time.After(time.Second):
		t.Fatal("local SSE failure did not finish the event reader")
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if stream.cancels.Load() != 1 {
		t.Fatalf("local SSE failure cancel count = %d, want 1", stream.cancels.Load())
	}
}

func TestHexSessionCloseDuringCreateCancelsLateStream(t *testing.T) {
	stream := newHexRouteTestStream(1, hexapi.Event{Type: "ready"})
	client := &blockingHexStreamClient{started: make(chan struct{}), release: make(chan struct{}), stream: stream}
	sink := &hexSessionSink{client: client, target: &transcriptTargetTestStub{}, token: preparedAudioToken(t)}
	startResult := make(chan error, 1)
	go func() { startResult <- sink.StartSession(context.Background()) }()
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("stream creation did not start")
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- sink.Close() }()
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close blocked while stream creation was pending")
	}
	close(client.release)
	select {
	case err := <-startResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("late StartSession result = %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("late StartSession did not finish")
	}
	if stream.cancels.Load() != 1 {
		t.Fatalf("late stream cancel count = %d, want 1", stream.cancels.Load())
	}
}

func startHexRouteSession(t *testing.T, route *hexAudioRoute, token bridge.SessionToken) (audiosession.Resources, *hexSessionSink) {
	t.Helper()
	if err := route.BeforeStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	resources, err := route.Prepare(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	prepared, ok := resources.Sink.(preparedAudioSink)
	if !ok {
		t.Fatalf("route sink = %T, want prepared sink", resources.Sink)
	}
	sink, ok := prepared.inner.(*hexSessionSink)
	if !ok {
		t.Fatalf("route inner sink = %T, want Hex session sink", prepared.inner)
	}
	if err := resources.Sink.(audiosession.SessionSink).StartSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resources.DrainTimeout != hexStreamDrainTimeout {
		t.Fatalf("Hex drain timeout = %s, want %s", resources.DrainTimeout, hexStreamDrainTimeout)
	}
	return resources, sink
}

func closeHexRouteSession(t *testing.T, resources audiosession.Resources) {
	t.Helper()
	if resources.Source != nil {
		if err := resources.Source.Close(); err != nil {
			t.Errorf("close Hex source lease: %v", err)
		}
	}
	if resources.Sink != nil {
		if err := resources.Sink.Close(); err != nil {
			t.Errorf("close Hex sink lease: %v", err)
		}
	}
}

func waitHexRouteReady(t *testing.T, route *hexAudioRoute) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for !route.Ready() {
		select {
		case <-route.Changes():
		case <-timer.C:
			t.Fatal("timed out waiting for Hex route readiness")
		}
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func hexRouteTestTokens(t *testing.T, count int) []bridge.SessionToken {
	t.Helper()
	core, err := bridge.NewCore(bridge.NewAudioBinding())
	if err != nil {
		t.Fatal(err)
	}
	device, err := bridge.NewDevice("hex-route-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []bridge.Event{bridge.RouteAvailable(true), bridge.ScopeAvailable(true), bridge.DeviceConnected(device)} {
		if _, err := core.Step(event); err != nil {
			t.Fatal(err)
		}
	}
	tokens := make([]bridge.SessionToken, 0, count)
	for index := 1; index <= count; index++ {
		press, err := bridge.NewPressToken(device, "voice", uint64(index))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := core.Step(bridge.VoiceBegan(press)); err != nil {
			t.Fatal(err)
		}
		snapshot := core.Snapshot()
		if !snapshot.HasSession {
			t.Fatal("test Core did not create a Hex session")
		}
		token := snapshot.Session.Token
		tokens = append(tokens, token)
		if _, err := core.Step(bridge.VoiceEnded(press)); err != nil {
			t.Fatal(err)
		}
		if _, err := core.Step(bridge.SourceFailed(token)); err != nil {
			t.Fatal(err)
		}
	}
	return tokens
}

var _ hexStream = (*hexRouteTestStream)(nil)
var _ hexStreamClient = (*hexRouteTestClient)(nil)
var _ audiosession.Source = (*hexRouteTestSource)(nil)

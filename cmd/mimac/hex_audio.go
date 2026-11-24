package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"time"

	hexapi "mimac.local/mimac/internal/adapters/hex"
	"mimac.local/mimac/internal/adapters/textoutput"
	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

const (
	hexStreamToolID         = "voice-hex-stream"
	hexStreamDrainTimeout   = 122 * time.Second // Hex finish waits up to 120s; allow response and SSE handoff.
	hexStreamLeasePeriod    = 3 * time.Second
	hexStreamRequestLimit   = 5 * time.Second
	hexStreamReadyTimeout   = 3 * time.Second
	hexModelUnavailableText = "请在 Hex 中启动本地识别服务并加载听写模型后重试"
)

type hexStream interface {
	ID() uint64
	Partials() bool
	WritePCM(context.Context, []int16) error
	ReadEvents(context.Context, func(hexapi.Event)) error
	Finish(context.Context) (string, error)
	Heartbeat(context.Context) error
	Cancel(context.Context) error
}

type hexStreamClient interface {
	CreateStream(context.Context) (hexStream, error)
}

type nativeHexStreamClient struct{ client *hexapi.Client }

func (c nativeHexStreamClient) CreateStream(ctx context.Context) (hexStream, error) {
	return c.client.CreateStream(ctx)
}

func hexAudioRouteFactory(ctx context.Context, config audioRouteConfig) (liveAudioRoute, error) {
	if ctx == nil {
		return nil, errors.New("hex route: context is required")
	}
	if config.Locator.Home == "" {
		return nil, errors.New("hex route: user home is unavailable")
	}
	client, err := hexapi.NewClientForAppPath(ctx, config.Locator.Home, config.AppPath)
	if err != nil {
		return nil, err
	}
	owner, err := atvv.OpenDeviceOwner(config.Generation, filepath.Join(config.ResourcesPath, "libMiMacATVV.dylib"))
	if owner == nil {
		return nil, err
	}
	capture := func() (transcriptTarget, error) {
		return textoutput.BeginDictation(), nil
	}
	route, routeErr := newHexAudioRoute(config.Generation, func(ctx context.Context, key atvv.StreamKey) (audiosession.Source, error) {
		return owner.NewSource(ctx, key)
	}, owner.Close, nativeHexStreamClient{client: client}, config.OnPartial, capture)
	return route, errors.Join(err, routeErr)
}

// hexAudioRoute uses the same Xiaomi PCM owner as other verified live routes.
// Hex receives those samples over its authenticated local /streams API; it
// never opens a microphone or owns the remote/session state machine.
type hexAudioRoute struct {
	*xiaomiAudioRoute
	client    hexStreamClient
	capture   func() (transcriptTarget, error)
	onPartial func(bridge.SessionToken, string, string) bool

	mu       sync.Mutex
	pending  transcriptTarget
	sessions map[bridge.SessionToken]*hexSessionSink
}

func newHexAudioRoute(generation uint64, source xiaomiAudioSourceFactory, closeDevice func() error,
	client hexStreamClient, onPartial func(bridge.SessionToken, string, string) bool,
	capture func() (transcriptTarget, error)) (*hexAudioRoute, error) {
	if client == nil || capture == nil {
		return nil, errors.New("hex route: stream client and text target capture are required")
	}
	route := &hexAudioRoute{
		client: client, capture: capture, onPartial: onPartial,
		sessions: make(map[bridge.SessionToken]*hexSessionSink),
	}
	base, err := newXiaomiPCMRoute(generation, source, closeDevice, 16000, func(context.Context) (audiosession.Sink, error) {
		return &hexSessionSink{route: route, client: client, onPartial: onPartial}, nil
	})
	if err != nil {
		return nil, err
	}
	route.xiaomiAudioRoute = base
	return route, nil
}

func (r *hexAudioRoute) BeforeStart(ctx context.Context) error {
	if r == nil || r.capture == nil {
		return errors.New("hex route: text target capture is unavailable")
	}
	if ctx == nil {
		return errors.New("hex route: start context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := r.capture()
	if err != nil {
		if target != nil {
			err = errors.Join(err, target.Close())
		}
		return err
	}
	if target == nil {
		return errors.New("hex route: text target is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, target.Close())
	}
	r.mu.Lock()
	old := r.pending
	r.pending = target
	r.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return nil
}

func (r *hexAudioRoute) Prepare(ctx context.Context, token bridge.SessionToken) (audiosession.Resources, error) {
	resources, err := r.xiaomiAudioRoute.Prepare(ctx, token)
	if err != nil {
		return resources, err
	}
	lease, ok := resources.Sink.(preparedAudioSink)
	if !ok {
		return resources, errors.New("hex route: prepared stream sink lease is missing")
	}
	sink, ok := lease.inner.(*hexSessionSink)
	if !ok {
		return resources, errors.New("hex route: prepared stream sink is missing")
	}
	r.mu.Lock()
	target := r.pending
	r.pending = nil
	if target != nil {
		r.sessions[token] = sink
	}
	r.mu.Unlock()
	if target == nil {
		return resources, errors.New("hex route: text target was not captured before start")
	}
	sink.mu.Lock()
	sink.target, sink.token = target, token
	sink.mu.Unlock()
	resources.DrainTimeout = hexStreamDrainTimeout
	return resources, nil
}

func (r *hexAudioRoute) DeliverTranscript(token bridge.SessionToken, text string) error {
	r.mu.Lock()
	sink := r.sessions[token]
	r.mu.Unlock()
	if sink == nil {
		return errors.New("hex route: transcript belongs to a retired session")
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.closed || sink.target == nil {
		return errors.New("hex route: text target is already released")
	}
	if text == "" {
		return nil
	}
	if sink.delivered {
		return errors.New("hex route: transcript was already delivered")
	}
	sink.delivered = true
	return sink.target.Insert(text)
}

func (r *hexAudioRoute) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	pending := r.pending
	r.pending = nil
	r.mu.Unlock()
	if pending != nil {
		_ = pending.Close()
	}
	if r.xiaomiAudioRoute == nil {
		return nil
	}
	return r.xiaomiAudioRoute.Close(ctx)
}

type hexSessionSink struct {
	mu            sync.Mutex
	route         *hexAudioRoute
	client        hexStreamClient
	stream        hexStream
	ctx           context.Context
	cancel        context.CancelFunc
	ready         chan struct{}
	finalReady    chan struct{}
	eventsDone    chan struct{}
	heartbeatDone chan struct{}
	token         bridge.SessionToken
	target        transcriptTarget
	onPartial     func(bridge.SessionToken, string, string) bool

	readySignalled bool
	finalPresent   bool
	finalText      string
	eventErr       error
	terminal       bool
	finishing      bool
	delivered      bool
	closed         bool
}

func (s *hexSessionSink) StartSession(ctx context.Context) error {
	if s == nil || s.client == nil {
		return errors.New("hex session: stream client is unavailable")
	}
	if ctx == nil {
		return errors.New("hex session: context is required")
	}
	s.mu.Lock()
	if s.closed || s.stream != nil {
		s.mu.Unlock()
		return errors.New("hex session: stream session is already started or closed")
	}
	if s.target == nil || s.token.ID() == 0 {
		s.mu.Unlock()
		return errors.New("hex session: captured text target is missing")
	}
	s.mu.Unlock()
	createCtx, cancelCreate := context.WithTimeout(ctx, 3*time.Second)
	stream, err := s.client.CreateStream(createCtx)
	cancelCreate()
	if err != nil {
		return err
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.closed || ctx.Err() != nil {
		startErr := ctx.Err()
		if startErr == nil {
			startErr = context.Canceled
		}
		s.mu.Unlock()
		cancel()
		cancelCtx, cancelRemote := context.WithTimeout(context.Background(), 2*time.Second)
		cancelErr := stream.Cancel(cancelCtx)
		cancelRemote()
		if errors.Is(cancelErr, hexapi.ErrStreamNotFound) {
			cancelErr = nil
		}
		return errors.Join(startErr, cancelErr)
	}
	s.stream, s.ctx, s.cancel = stream, sessionCtx, cancel
	s.ready, s.finalReady, s.eventsDone = make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.mu.Unlock()
	go s.readEvents(sessionCtx, stream)
	readyTimeout := time.NewTimer(hexStreamReadyTimeout)
	defer readyTimeout.Stop()
	select {
	case <-s.ready:
		s.mu.Lock()
		if s.closed || sessionCtx.Err() != nil {
			s.mu.Unlock()
			return context.Canceled
		}
		done := make(chan struct{})
		s.heartbeatDone = done
		s.mu.Unlock()
		go s.heartbeat(sessionCtx, stream, done)
		return nil
	case <-s.eventsDone:
		return s.eventError()
	case <-ctx.Done():
		return ctx.Err()
	case <-readyTimeout.C:
		return errors.New("hex session: timed out waiting for the ready event")
	}
}

func (s *hexSessionSink) TryWrite(pcm []int16) error {
	s.mu.Lock()
	stream, ctx, eventErr, closed, terminal := s.stream, s.ctx, s.eventErr, s.closed, s.terminal
	s.mu.Unlock()
	if closed || stream == nil || ctx == nil {
		return errors.New("hex session: stream is not ready")
	}
	if eventErr != nil {
		return eventErr
	}
	if terminal {
		return hexapi.ErrStreamEnded
	}
	requestCtx, cancel := context.WithTimeout(ctx, hexStreamRequestLimit)
	defer cancel()
	return stream.WritePCM(requestCtx, pcm)
}

func (s *hexSessionSink) Drain(ctx context.Context) error {
	if ctx == nil {
		return errors.New("hex session: drain context is required")
	}
	s.mu.Lock()
	stream, eventErr, finalPresent := s.stream, s.eventErr, s.finalPresent
	if stream == nil || s.closed {
		s.mu.Unlock()
		return errors.New("hex session: stream is not ready")
	}
	if eventErr != nil && !finalPresent {
		s.mu.Unlock()
		return eventErr
	}
	if finalPresent {
		s.finishing = true
		s.mu.Unlock()
		return nil
	}
	s.finishing = true
	s.mu.Unlock()

	text, err := stream.Finish(ctx)
	if err == nil {
		return s.setFinal(text)
	}
	if !errors.Is(err, hexapi.ErrFinishTimeout) {
		s.mu.Lock()
		finalPresent = s.finalPresent
		s.mu.Unlock()
		if finalPresent {
			return nil
		}
		return err
	}
	select {
	case <-s.finalReady:
		return s.finalResult()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *hexSessionSink) Transcript() (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eventErr != nil && !s.finalPresent {
		return "", false, s.eventErr
	}
	if !s.finalPresent {
		return "", false, nil
	}
	return s.finalText, true, nil
}

func (s *hexSessionSink) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	stream, cancel, eventsDone, heartbeatDone := s.stream, s.cancel, s.eventsDone, s.heartbeatDone
	if cancel != nil {
		cancel()
	}
	s.mu.Unlock()
	if heartbeatDone != nil {
		<-heartbeatDone
	}
	if eventsDone != nil {
		<-eventsDone
	}
	var cancelErr error
	s.mu.Lock()
	remoteTerminal := s.terminal
	s.mu.Unlock()
	if stream != nil && !remoteTerminal {
		cancelCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cancelErr = stream.Cancel(cancelCtx)
		cancel()
		if errors.Is(cancelErr, hexapi.ErrStreamNotFound) {
			cancelErr = nil
		}
	}

	s.mu.Lock()
	var targetErr error
	if s.target != nil {
		targetErr = s.target.Close()
		if targetErr == nil {
			s.target = nil
		}
	}
	if cancelErr == nil && targetErr == nil {
		s.closed = true
	}
	closed := s.closed
	s.mu.Unlock()
	if closed && s.route != nil {
		s.route.mu.Lock()
		delete(s.route.sessions, s.token)
		s.route.mu.Unlock()
	}
	return errors.Join(cancelErr, targetErr)
}

func (s *hexSessionSink) readEvents(ctx context.Context, stream hexStream) {
	err := stream.ReadEvents(ctx, s.receiveEvent)
	s.mu.Lock()
	if ctx.Err() == nil {
		if err != nil {
			s.eventErr = err
		} else if !s.terminal && s.eventErr == nil {
			s.eventErr = errors.New("hex session: events ended without a final transcript")
		}
		if s.eventErr != nil {
			s.signalFinalLocked()
		}
	}
	close(s.eventsDone)
	s.mu.Unlock()
}

func (s *hexSessionSink) receiveEvent(event hexapi.Event) {
	s.mu.Lock()
	switch event.Type {
	case "ready":
		s.signalReadyLocked()
	case "partial":
		callback := s.onPartial
		token := s.token
		stable, tentative := event.Stable, event.Tentative
		partials := s.stream != nil && s.stream.Partials()
		s.mu.Unlock()
		if partials && callback != nil {
			callback(token, stable, tentative)
		}
		return
	case "final":
		s.finalPresent = true
		s.finalText = event.Text
		s.terminal = true
		s.signalFinalLocked()
	case "error", "cancelled":
		s.eventErr = event.Err()
		s.terminal = true
		s.signalFinalLocked()
	}
	s.mu.Unlock()
}

func (s *hexSessionSink) heartbeat(ctx context.Context, stream hexStream, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(hexStreamLeasePeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			heartbeatCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err := stream.Heartbeat(heartbeatCtx)
			cancel()
			if err != nil && ctx.Err() == nil {
				s.mu.Lock()
				if !s.terminal && s.eventErr == nil {
					s.eventErr = err
				}
				s.mu.Unlock()
				return
			}
		}
	}
}

func (s *hexSessionSink) eventError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eventErr != nil {
		return s.eventErr
	}
	return errors.New("hex session: events ended before ready")
}

func (s *hexSessionSink) setFinal(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finalPresent && s.finalText != text {
		return errors.New("hex session: finish response disagrees with final event")
	}
	s.finalPresent, s.finalText, s.terminal = true, text, true
	s.signalFinalLocked()
	return nil
}

func (s *hexSessionSink) finalResult() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eventErr != nil && !s.finalPresent {
		return s.eventErr
	}
	if !s.finalPresent {
		return errors.New("hex session: finish ended without a final transcript")
	}
	return nil
}

func (s *hexSessionSink) signalReadyLocked() {
	if s.ready != nil && !s.readySignalled {
		s.readySignalled = true
		close(s.ready)
	}
}

func (s *hexSessionSink) signalFinalLocked() {
	if s.finalReady != nil {
		select {
		case <-s.finalReady:
		default:
			close(s.finalReady)
		}
	}
}

var _ audiosession.Sink = (*hexSessionSink)(nil)
var _ audiosession.SessionSink = (*hexSessionSink)(nil)
var _ audiosession.TranscriptSink = (*hexSessionSink)(nil)
var _ liveAudioRoute = (*hexAudioRoute)(nil)

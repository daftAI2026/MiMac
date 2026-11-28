package main

import (
	"context"
	"errors"
	"sync"

	"mimac.local/mimac/internal/adapters/applespeech"
	"mimac.local/mimac/internal/adapters/textoutput"
	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

type transcriptTarget interface {
	Insert(string) error
	Close() error
}

type localRecognizer interface {
	audiosession.Sink
	FinalText() (string, error)
}

type localPartialRecognizer interface {
	SetPartialHandler(func(stable, tentative string)) error
}

type appleAudioRoute struct {
	*xiaomiAudioRoute
	mu               sync.Mutex
	pending          transcriptTarget
	sessions         map[bridge.SessionToken]*appleSessionSink
	capture          func() (transcriptTarget, error)
	onPartial        func(bridge.SessionToken, string, string) bool
	partialsEnabled  func() bool
	openWithPartials func(context.Context, bool) (localRecognizer, error)
}

type appleRoutePartialConfig struct {
	onPartial        func(bridge.SessionToken, string, string) bool
	enabled          func() bool
	openWithPartials func(context.Context, bool) (localRecognizer, error)
}

func openAppleAudioRoute(generation uint64, deviceLibrary, speechLibrary, locale string, onPartial func(bridge.SessionToken, string, string) bool, partialsEnabled func() bool) (liveAudioRoute, error) {
	owner, err := atvv.OpenDeviceOwner(generation, deviceLibrary)
	if owner == nil {
		return nil, err
	}
	openSpeech := func(ctx context.Context, enablePartials bool) (localRecognizer, error) {
		sink, err := applespeech.Open(ctx, applespeech.Config{LibraryPath: speechLibrary, Locale: locale, SampleRate: 16000, EnablePartials: enablePartials})
		if sink == nil {
			return nil, err
		}
		return sink, err
	}
	route, routeErr := newAppleAudioRoute(generation, func(ctx context.Context, key atvv.StreamKey) (audiosession.Source, error) {
		return owner.PrepareSource(ctx, key)
	}, owner.Close, func(ctx context.Context) (localRecognizer, error) {
		return openSpeech(ctx, false)
	}, func() (transcriptTarget, error) {
		return textoutput.BeginDictation(), nil
	}, appleRoutePartialConfig{onPartial: onPartial, enabled: partialsEnabled, openWithPartials: openSpeech})
	if route != nil {
		route.discardUnclaimed = owner.DiscardUnclaimedPress
	}
	return route, errors.Join(err, routeErr)
}

func newAppleAudioRoute(generation uint64, source xiaomiAudioSourceFactory, closeDevice func() error,
	open func(context.Context) (localRecognizer, error), capture func() (transcriptTarget, error),
	partials ...appleRoutePartialConfig) (*appleAudioRoute, error) {
	if open == nil || capture == nil {
		return nil, errors.New("local speech factories missing")
	}
	route := &appleAudioRoute{sessions: make(map[bridge.SessionToken]*appleSessionSink), capture: capture}
	if len(partials) > 0 {
		route.onPartial = partials[0].onPartial
		route.partialsEnabled = partials[0].enabled
		route.openWithPartials = partials[0].openWithPartials
	}
	base, err := newXiaomiPCMRoute(generation, source, closeDevice, 16000, func(context.Context) (audiosession.Sink, error) {
		// Only the device is prearmed. No speech task or input target exists yet.
		return &appleSessionSink{route: route, open: open}, nil
	})
	if err != nil {
		return nil, err
	}
	route.xiaomiAudioRoute = base
	return route, nil
}

func (r *appleAudioRoute) BeforeStart(ctx context.Context) error {
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
		return errors.New("text input target unavailable")
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

func (r *appleAudioRoute) Prepare(ctx context.Context, token bridge.SessionToken) (audiosession.Resources, error) {
	resources, err := r.xiaomiAudioRoute.Prepare(ctx, token)
	if err != nil {
		return resources, err
	}
	lease, ok := resources.Sink.(preparedAudioSink)
	if !ok {
		return resources, errors.New("local speech resource lease missing")
	}
	sink, ok := lease.inner.(*appleSessionSink)
	if !ok {
		return resources, errors.New("local speech consumer missing")
	}
	r.mu.Lock()
	target := r.pending
	r.pending = nil
	if target != nil {
		r.sessions[token] = sink
	}
	r.mu.Unlock()
	if target == nil {
		return resources, errors.New("text target was not captured at press")
	}
	sink.mu.Lock()
	sink.target, sink.token = target, token
	sink.mu.Unlock()
	return resources, nil
}

func (r *appleAudioRoute) DeliverTranscript(token bridge.SessionToken, text string) error {
	r.mu.Lock()
	sink := r.sessions[token]
	r.mu.Unlock()
	if sink == nil {
		return errors.New("text result belongs to a retired session")
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.closed || sink.target == nil {
		return errors.New("text target already released")
	}
	if text == "" {
		return nil
	}
	return sink.target.Insert(text)
}

func (r *appleAudioRoute) Close(ctx context.Context) error {
	r.mu.Lock()
	target := r.pending
	r.pending = nil
	r.mu.Unlock()
	if target != nil {
		_ = target.Close()
	}
	return r.xiaomiAudioRoute.Close(ctx)
}

type appleSessionSink struct {
	mu         sync.Mutex
	route      *appleAudioRoute
	token      bridge.SessionToken
	target     transcriptTarget
	open       func(context.Context) (localRecognizer, error)
	recognizer localRecognizer
	closed     bool
}

func (s *appleSessionSink) StartSession(ctx context.Context) error {
	// Runtime owns StartSession and joins it before Close; Open receives its
	// cancellation context and returns even partial native ownership here.
	s.mu.Lock()
	target := s.target
	s.mu.Unlock()
	if target == nil {
		return errors.New("local speech input target missing")
	}
	partialEnabled := s.route.partialsEnabled != nil && s.route.partialsEnabled()
	var recognizer localRecognizer
	var err error
	if s.route.openWithPartials != nil {
		recognizer, err = s.route.openWithPartials(ctx, partialEnabled)
	} else {
		recognizer, err = s.open(ctx)
	}
	s.mu.Lock()
	s.recognizer = recognizer
	s.mu.Unlock()
	if err == nil && recognizer == nil {
		err = errors.New("local recognizer missing")
	}
	if err == nil && recognizer != nil && partialEnabled && s.route.onPartial != nil {
		if partials, ok := recognizer.(localPartialRecognizer); ok {
			err = partials.SetPartialHandler(func(stable, tentative string) {
				s.mu.Lock()
				closed := s.closed
				activeToken := s.token
				s.mu.Unlock()
				if !closed && (s.route.partialsEnabled == nil || s.route.partialsEnabled()) {
					s.route.onPartial(activeToken, stable, tentative)
				}
			})
		}
	}
	return err
}

func (s *appleSessionSink) TryWrite(pcm []int16) error      { return s.recognizer.TryWrite(pcm) }
func (s *appleSessionSink) Drain(ctx context.Context) error { return s.recognizer.Drain(ctx) }
func (s *appleSessionSink) Transcript() (string, bool, error) {
	text, err := s.recognizer.FinalText()
	return text, true, err
}
func (s *appleSessionSink) Close() error {
	s.mu.Lock()
	s.closed = true
	var targetErr error
	if s.target != nil {
		targetErr = s.target.Close()
		if targetErr == nil {
			s.target = nil
		}
	}
	recognizer := s.recognizer
	s.mu.Unlock()
	s.route.mu.Lock()
	delete(s.route.sessions, s.token)
	s.route.mu.Unlock()
	if recognizer != nil {
		return errors.Join(targetErr, recognizer.Close())
	}
	return targetErr
}

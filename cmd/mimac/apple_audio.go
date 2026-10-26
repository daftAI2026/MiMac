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

type appleAudioRoute struct {
	*xiaomiAudioRoute
	mu       sync.Mutex
	pending  transcriptTarget
	sessions map[bridge.SessionToken]*appleSessionSink
	capture  func() (transcriptTarget, error)
}

func openAppleAudioRoute(generation uint64, deviceLibrary, speechLibrary string) (liveAudioRoute, error) {
	owner, err := atvv.OpenDeviceOwner(generation, deviceLibrary)
	if owner == nil {
		return nil, err
	}
	route, routeErr := newAppleAudioRoute(generation, func(ctx context.Context, key atvv.StreamKey) (audiosession.Source, error) {
		return owner.NewSource(ctx, key)
	}, owner.Close, func(ctx context.Context) (localRecognizer, error) {
		sink, err := applespeech.Open(ctx, applespeech.Config{LibraryPath: speechLibrary, Locale: "zh-CN", SampleRate: 16000})
		if sink == nil {
			return nil, err
		}
		return sink, err
	}, func() (transcriptTarget, error) {
		target, err := textoutput.Capture()
		if target == nil {
			return nil, err
		}
		return target, err
	})
	return route, errors.Join(err, routeErr)
}

func newAppleAudioRoute(generation uint64, source xiaomiAudioSourceFactory, closeDevice func() error,
	open func(context.Context) (localRecognizer, error), capture func() (transcriptTarget, error)) (*appleAudioRoute, error) {
	if open == nil || capture == nil {
		return nil, errors.New("local speech factories missing")
	}
	route := &appleAudioRoute{sessions: make(map[bridge.SessionToken]*appleSessionSink), capture: capture}
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
	recognizer, err := s.open(ctx)
	s.mu.Lock()
	s.recognizer = recognizer
	s.mu.Unlock()
	if err == nil && recognizer == nil {
		err = errors.New("local recognizer missing")
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

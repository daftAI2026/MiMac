package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"mimac.local/mimac/internal/adapters/audiooutput"
	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

var (
	errXiaomiAudioGeneration      = errors.New("xiaomi audio route: nonzero device generation is required")
	errXiaomiAudioSourceNil       = errors.New("xiaomi audio route: source factory returned nil")
	errXiaomiAudioSinkNil         = errors.New("xiaomi audio route: sink factory returned nil")
	errXiaomiAudioNoReady         = errors.New("xiaomi audio route: source does not expose negotiated readiness")
	errXiaomiAudioRateMismatch    = errors.New("xiaomi audio route: negotiated PCM rate does not match prepared sink")
	errXiaomiAudioTokenGeneration = errors.New("xiaomi audio route: session token belongs to a different device generation")
)

type xiaomiPreparedSink interface {
	audiosession.Sink
	Prepare(context.Context) error
}

type xiaomiAudioSourceFactory func(context.Context, atvv.StreamKey) (audiosession.Source, error)
type xiaomiAudioSinkFactory func(audiooutput.Config) (xiaomiPreparedSink, error)

type xiaomiAudioCloseAttempt struct {
	done chan struct{}
	err  error // Written before done is closed.
}

// xiaomiAudioRoute binds one connected-device generation to one explicit
// output format and a prepared-resource owner. It does not select an output
// UID, request MIC_OPEN, or start a target action.
type xiaomiAudioRoute struct {
	generation  uint64
	config      audiooutput.Config
	newSource   xiaomiAudioSourceFactory
	openSink    xiaomiAudioSinkFactory
	closeDevice func() error
	sequence    atomic.Uint64
	resources   *preparedAudioOwner

	mu            sync.Mutex
	deviceClosed  bool
	deviceClosing *xiaomiAudioCloseAttempt
}

func newXiaomiAudioRoute(generation uint64, device *atvv.DeviceOwner, config audiooutput.Config) (*xiaomiAudioRoute, error) {
	if device == nil {
		return nil, errors.New("xiaomi audio route: device owner is required")
	}
	return newXiaomiAudioRouteWithFactories(
		generation,
		func(ctx context.Context, key atvv.StreamKey) (audiosession.Source, error) {
			source, err := device.NewSource(ctx, key)
			return source, err
		},
		device.Close,
		config,
		openXiaomiAudioSink,
	)
}

// newXiaomiAudioRouteWithFactories is the fakeable composition seam for tests
// and host assembly. Production callers should pass the selected DeviceOwner,
// an explicit Config, and the system sink factory.
func newXiaomiAudioRouteWithFactories(
	generation uint64,
	newSource xiaomiAudioSourceFactory,
	closeDevice func() error,
	config audiooutput.Config,
	openSink xiaomiAudioSinkFactory,
) (*xiaomiAudioRoute, error) {
	if generation == 0 {
		return nil, errXiaomiAudioGeneration
	}
	if newSource == nil || closeDevice == nil || openSink == nil {
		return nil, errors.New("xiaomi audio route: source, close, and sink factories are required")
	}
	if strings.TrimSpace(config.DeviceUID) == "" || strings.ContainsRune(config.DeviceUID, '\x00') || !utf8.ValidString(config.DeviceUID) {
		return nil, errors.New("xiaomi audio route: explicit output device UID is required")
	}
	if config.SampleRate != 8000 && config.SampleRate != 16000 {
		return nil, fmt.Errorf("xiaomi audio route: unsupported output sample rate %d", config.SampleRate)
	}
	route := &xiaomiAudioRoute{
		generation:  generation,
		config:      config,
		newSource:   newSource,
		openSink:    openSink,
		closeDevice: closeDevice,
	}
	resources, err := newPreparedAudioOwner(route.prepareResources)
	if err != nil {
		return nil, err
	}
	route.resources = resources
	return route, nil
}

func openXiaomiAudioSink(config audiooutput.Config) (xiaomiPreparedSink, error) {
	sink, err := audiooutput.Open(config)
	if sink == nil {
		return nil, err
	}
	return sink, err
}

// Start begins pre-subscription, source arming, and explicit sink preparation
// off the host thread. Ready means the resources can be transferred; it does
// not mean the remote is capturing audio.
func (r *xiaomiAudioRoute) Start() error {
	if r == nil || r.resources == nil {
		return errPreparedAudioNil
	}
	return r.resources.Start()
}

func (r *xiaomiAudioRoute) Ready() bool {
	return r != nil && r.resources != nil && r.resources.Ready()
}

// Changes forwards the coalescing readiness notifications to the host. The
// host should dispatch UI refreshes to MyGo's main loop after receiving one.
func (r *xiaomiAudioRoute) Changes() <-chan struct{} {
	if r == nil || r.resources == nil {
		return nil
	}
	return r.resources.Changes()
}

func (r *xiaomiAudioRoute) Error() error {
	if r == nil || r.resources == nil {
		return errPreparedAudioNil
	}
	return r.resources.Error()
}

// Prepare adapts to audiosession.Prepare. The already-armed source and the
// prepared sink are transferred together; no fresh source is opened here.
func (r *xiaomiAudioRoute) Prepare(ctx context.Context, token bridge.SessionToken) (audiosession.Resources, error) {
	if r == nil || r.resources == nil {
		return audiosession.Resources{}, errPreparedAudioNil
	}
	if token.Device().Generation() != r.generation {
		return audiosession.Resources{}, errXiaomiAudioTokenGeneration
	}
	return r.resources.Prepare(ctx, token)
}

func (r *xiaomiAudioRoute) prepareResources(ctx context.Context) (audiosession.Resources, error) {
	sequence := r.sequence.Add(1)
	if sequence == 0 {
		return audiosession.Resources{}, atvv.ErrInvalidSessionKey
	}
	key := atvv.StreamKey{Generation: r.generation, Session: sequence}

	source, err := r.newSource(ctx, key)
	resources := audiosession.Resources{SampleRate: r.config.SampleRate}
	if source != nil {
		resources.Source = &xiaomiRateCheckedSource{inner: source, expectedRate: r.config.SampleRate}
	}
	if err != nil {
		return resources, err
	}
	if source == nil {
		return resources, errXiaomiAudioSourceNil
	}

	sink, err := r.openSink(r.config)
	if sink != nil {
		resources.Sink = sink
		if receiver, ok := sink.(interface{ WaitReceiver(context.Context) error }); ok {
			resources.WaitReceiver = receiver.WaitReceiver
		}
	}
	if err != nil {
		return resources, err
	}
	if sink == nil {
		return resources, errXiaomiAudioSinkNil
	}
	if err := sink.Prepare(ctx); err != nil {
		return resources, err
	}
	return resources, nil
}

// Close cleans prepared/leased resources before the device transport. If a
// completed, unleased resource cleanup fails, it closes the native transport
// to establish callback quiescence and retries the retained resource cleanup.
// A CleanupPending result is left to Runtime; it is never force-closed here.
// DeviceOwner.Close is run in a worker because its native close is synchronous.
func (r *xiaomiAudioRoute) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("xiaomi audio route: nil close context")
	}
	if err := r.resources.Close(ctx); err != nil {
		if errors.Is(err, audiosession.ErrCleanupPending) {
			return err
		}
		// A completed resource-close attempt failed. End the connection-owned
		// native transport (which provides callback quiescence), then retry the
		// retained resources. Never force the transport while Runtime still owns
		// a lease and has only reported CleanupPending.
		deviceErr := r.closeDeviceOwner(ctx)
		if deviceErr != nil {
			return errors.Join(err, deviceErr)
		}
		if retryErr := r.resources.Close(ctx); retryErr != nil {
			return errors.Join(err, retryErr)
		}
	}
	return r.closeDeviceOwner(ctx)
}

func (r *xiaomiAudioRoute) closeDeviceOwner(ctx context.Context) error {
	r.mu.Lock()
	if r.deviceClosed {
		r.mu.Unlock()
		return nil
	}
	attempt := r.deviceClosing
	if attempt != nil {
		select {
		case <-attempt.done:
			if attempt.err == nil {
				r.deviceClosed = true
				r.mu.Unlock()
				return nil
			}
			attempt = nil // A later Close call retries the retained device owner.
		default:
		}
	}
	if attempt == nil {
		attempt = &xiaomiAudioCloseAttempt{done: make(chan struct{})}
		r.deviceClosing = attempt
		go func() {
			attempt.err = r.closeDevice()
			if attempt.err == nil {
				r.mu.Lock()
				r.deviceClosed = true
				r.mu.Unlock()
			}
			close(attempt.done)
		}()
	}
	r.mu.Unlock()

	select {
	case <-attempt.done:
		return attempt.err
	case <-ctx.Done():
		return errors.Join(audiosession.ErrCleanupPending, ctx.Err())
	}
}

type xiaomiRateCheckedSource struct {
	inner        audiosession.Source
	expectedRate int
	checkMu      sync.Mutex // Serializes readiness negotiation, never health queries.
	mu           sync.Mutex
	checked      bool
	readyErr     error
}

func (s *xiaomiRateCheckedSource) Read(ctx context.Context, pcm []int16) (int, error) {
	if s == nil || s.inner == nil {
		return 0, atvv.ErrSourceClosed
	}
	s.checkMu.Lock()
	s.mu.Lock()
	checked, readyErr := s.checked, s.readyErr
	s.mu.Unlock()
	if !checked {
		ready, ok := s.inner.(interface {
			Ready(context.Context) (int, error)
		})
		if !ok {
			readyErr = errXiaomiAudioNoReady
		} else {
			actual, err := ready.Ready(ctx)
			if err != nil {
				readyErr = err
			} else if actual != s.expectedRate {
				readyErr = fmt.Errorf("%w: got %d Hz, want %d Hz", errXiaomiAudioRateMismatch, actual, s.expectedRate)
			}
		}
		s.mu.Lock()
		s.readyErr, s.checked = readyErr, true
		s.mu.Unlock()
	}
	s.checkMu.Unlock()
	if readyErr != nil {
		return 0, readyErr
	}
	return s.inner.Read(ctx, pcm)
}

func (s *xiaomiRateCheckedSource) Stop(ctx context.Context) error {
	if s == nil || s.inner == nil {
		return atvv.ErrSourceClosed
	}
	return s.inner.Stop(ctx)
}

func (s *xiaomiRateCheckedSource) Close() error {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.Close()
}

func (s *xiaomiRateCheckedSource) Err() error {
	if s == nil || s.inner == nil {
		return atvv.ErrSourceClosed
	}
	s.mu.Lock()
	readyErr := s.readyErr
	s.mu.Unlock()
	if health, ok := s.inner.(interface{ Err() error }); ok {
		return errors.Join(readyErr, health.Err())
	}
	return readyErr
}

var _ audiosession.Prepare = (*xiaomiAudioRoute)(nil).Prepare
var _ audiosession.Source = (*xiaomiRateCheckedSource)(nil)

// Package applespeech adapts Apple SpeechAnalyzer to a bounded mono PCM sink.
// It consumes caller-provided audio only and never captures a microphone.
package applespeech

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	RequiredSampleRate = 16000
	MaxBlockSamples    = 4096
	NativeQueueSamples = 32000
	MaxFinalTextBytes  = 64 * 1024

	nativeWaitInterval = 50 * time.Millisecond
	cleanupWaitLimit   = 2 * time.Second
)

var (
	ErrUnsupported        = errors.New("applespeech: unavailable on this system")
	ErrInvalidConfig      = errors.New("applespeech: invalid configuration")
	ErrInvalidLocale      = errors.New("applespeech: invalid locale")
	ErrSampleRate         = errors.New("applespeech: only 16000 Hz mono Int16 is supported")
	ErrQueueFull          = errors.New("applespeech: PCM queue is full")
	ErrBlockTooLarge      = errors.New("applespeech: block exceeds 4096 samples")
	ErrEmptyBlock         = errors.New("applespeech: empty PCM block")
	ErrNotReady           = errors.New("applespeech: session is not ready")
	ErrDraining           = errors.New("applespeech: session is draining")
	ErrClosed             = errors.New("applespeech: sink is closing or closed")
	ErrNotDrained         = errors.New("applespeech: final text is available only after a successful drain")
	ErrAssetsUnavailable  = errors.New("applespeech: required Apple Speech assets are unavailable")
	ErrCleanupPending     = errors.New("applespeech: native cleanup is pending; retain the sink and retry Close")
	ErrNativeFailure      = errors.New("applespeech: native session failed")
	ErrAlreadyDrained     = errors.New("applespeech: session was already drained")
	ErrInvalidNativeState = errors.New("applespeech: invalid native session state")
	ErrFinalTextOverflow  = errors.New("applespeech: final text exceeds 64 KiB")
)

// Config selects the native bridge and a supported Apple Speech locale.
// LibraryPath must identify the narrow Swift C-ABI dylib built by the host.
type Config struct {
	LibraryPath string
	Locale      string
	SampleRate  int
	// InstallAssets permits this session to ask macOS to install its model.
	// Leave false unless the user explicitly requested a model download.
	InstallAssets bool
}

type nativeState int32

const (
	nativePreparing nativeState = iota
	nativeReady
	nativeDrained
	nativeCancelled
	nativeFailed
)

type nativeBridge interface {
	isAvailable() (bool, error)
	create(locale string, sampleRate int, installAssets bool) (nativeOwner, error)
}

// nativeOwner remains allocated until Release succeeds after WaitTerminated.
// Its methods are safe to call concurrently where cancellation is documented.
type nativeOwner interface {
	waitReady(timeout time.Duration) (nativeState, error)
	tryWrite(samples []int16) error
	beginDrain() error
	waitDone(timeout time.Duration) (nativeState, error)
	requestCancel() error
	waitTerminated(timeout time.Duration) (bool, error)
	finalText() (string, error)
	errorMessage() string
	release() error
}

// IsAvailable dynamically checks the loaded Apple Speech bridge and runtime
// SpeechTranscriber availability. It does not prepare a session or fetch assets.
func IsAvailable(libraryPath string) (bool, error) {
	if err := validateLibraryPath(libraryPath); err != nil {
		return false, err
	}
	bridge, err := loadNativeBridge(libraryPath)
	if err != nil {
		return false, err
	}
	return bridge.isAvailable()
}

// Open creates a session and waits until Apple Speech has prepared it. Model
// installation is opt-in through Config.InstallAssets. If cancellation or
// failure cannot join the native owner within two seconds,
// Open returns that partially opened Sink alongside ErrCleanupPending; retain
// it and call Close again when convenient.
func Open(ctx context.Context, cfg Config) (*Sink, error) {
	return openWith(ctx, cfg, func(path string) (nativeBridge, error) {
		return loadNativeBridge(path)
	})
}

func openWith(ctx context.Context, cfg Config, load func(string) (nativeBridge, error)) (*Sink, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrInvalidConfig)
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bridge, err := load(cfg.LibraryPath)
	if err != nil {
		return nil, err
	}
	if bridge == nil {
		return nil, fmt.Errorf("%w: native bridge loader returned no bridge", ErrUnsupported)
	}
	available, err := bridge.isAvailable()
	if err != nil {
		return nil, err
	}
	if !available {
		return nil, ErrUnsupported
	}
	owner, err := bridge.create(cfg.Locale, cfg.SampleRate, cfg.InstallAssets)
	if owner == nil {
		if err == nil {
			err = fmt.Errorf("%w: native create returned no owner", ErrNativeFailure)
		}
		return nil, err
	}
	sink := &Sink{owner: owner}
	if err != nil {
		return sink.abortOpen(err)
	}

	for {
		if err := ctx.Err(); err != nil {
			return sink.abortOpen(err)
		}
		state, waitErr := owner.waitReady(nativeWaitInterval)
		if waitErr != nil {
			return sink.abortOpen(fmt.Errorf("applespeech: wait for preparation: %w", waitErr))
		}
		switch state {
		case nativeReady:
			if err := ctx.Err(); err != nil {
				return sink.abortOpen(err)
			}
			return sink, nil
		case nativePreparing:
			continue
		case nativeFailed:
			return sink.abortOpen(sink.nativeFailure("prepare"))
		case nativeCancelled:
			return sink.abortOpen(context.Canceled)
		default:
			return sink.abortOpen(fmt.Errorf("%w: preparation returned state %d", ErrInvalidNativeState, state))
		}
	}
}

func validateConfig(cfg Config) error {
	if err := validateLibraryPath(cfg.LibraryPath); err != nil {
		return err
	}
	if cfg.SampleRate != RequiredSampleRate {
		return fmt.Errorf("%w: got %d", ErrSampleRate, cfg.SampleRate)
	}
	if strings.TrimSpace(cfg.Locale) == "" || strings.ContainsRune(cfg.Locale, 0) || !utf8.ValidString(cfg.Locale) {
		return fmt.Errorf("%w: locale must be a non-empty UTF-8 identifier", ErrInvalidLocale)
	}
	return nil
}

func (s *Sink) abortOpen(cause error) (*Sink, error) {
	closeErr := s.Close()
	if errors.Is(closeErr, ErrCleanupPending) {
		return s, errors.Join(cause, closeErr)
	}
	return nil, errors.Join(cause, closeErr)
}

// Sink owns one analyzer and its result task. TryWrite copies the supplied
// samples into the native bounded queue before returning.
type Sink struct {
	mu   sync.Mutex
	opMu sync.Mutex

	owner          nativeOwner
	closeRequested bool
	drainStarted   bool
	drained        bool
	cancelled      bool
	released       bool
	finalText      string
}

// TryWrite makes a non-blocking write of one PCM block. Full queues return
// ErrQueueFull; the adapter never silently drops audio.
func (s *Sink) TryWrite(samples []int16) error {
	if s == nil {
		return ErrClosed
	}
	if len(samples) == 0 {
		return ErrEmptyBlock
	}
	if len(samples) > MaxBlockSamples {
		return ErrBlockTooLarge
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner == nil || s.closeRequested || s.released {
		return ErrClosed
	}
	if s.drainStarted || s.drained {
		return ErrDraining
	}
	if s.cancelled {
		return ErrClosed
	}
	return s.owner.tryWrite(samples)
}

// Drain finishes the input sequence and waits for all final-result work.
// Concurrent Close can request cancellation while Drain is waiting; final text
// is retained only if the native session completed normally and Close did not
// win that race.
func (s *Sink) Drain(ctx context.Context) error {
	if s == nil {
		return ErrClosed
	}
	if ctx == nil {
		return errors.New("applespeech: nil drain context")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	if s.released || s.owner == nil || s.closeRequested {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.drained {
		s.mu.Unlock()
		return nil
	}
	if s.cancelled {
		s.mu.Unlock()
		return context.Canceled
	}
	if s.drainStarted {
		s.mu.Unlock()
		return ErrAlreadyDrained
	}
	if err := ctx.Err(); err != nil {
		s.cancelled = true
		owner := s.owner
		s.mu.Unlock()
		return errors.Join(err, s.cancelAndJoin(owner))
	}
	s.drainStarted = true
	owner := s.owner
	if err := owner.beginDrain(); err != nil {
		s.cancelled = true
		s.mu.Unlock()
		return errors.Join(fmt.Errorf("applespeech: finish input: %w", err), s.cancelAndJoin(owner))
	}
	s.mu.Unlock()

	for {
		if err := ctx.Err(); err != nil {
			s.mu.Lock()
			s.cancelled = true
			s.mu.Unlock()
			return errors.Join(err, s.cancelAndJoin(owner))
		}
		state, err := owner.waitDone(nativeWaitInterval)
		if err != nil {
			s.mu.Lock()
			s.cancelled = true
			s.mu.Unlock()
			return errors.Join(fmt.Errorf("applespeech: wait for final results: %w", err), s.cancelAndJoin(owner))
		}
		switch state {
		case nativeReady:
			continue
		case nativePreparing:
			return s.failDrain(owner, fmt.Errorf("%w: drain returned preparation state", ErrInvalidNativeState))
		case nativeFailed:
			return s.failDrain(owner, s.nativeFailure("drain"))
		case nativeCancelled:
			s.mu.Lock()
			s.cancelled = true
			s.mu.Unlock()
			return context.Canceled
		case nativeDrained:
			text, textErr := owner.finalText()
			if textErr != nil {
				return s.failDrain(owner, fmt.Errorf("applespeech: copy final text: %w", textErr))
			}
			if len(text) > MaxFinalTextBytes {
				return s.failDrain(owner, ErrFinalTextOverflow)
			}
			if !utf8.ValidString(text) {
				return s.failDrain(owner, errors.New("applespeech: final text is not valid UTF-8"))
			}
			s.mu.Lock()
			if s.closeRequested || s.cancelled || s.released {
				s.mu.Unlock()
				return ErrClosed
			}
			s.finalText = text
			s.drained = true
			s.mu.Unlock()
			return nil
		default:
			return s.failDrain(owner, fmt.Errorf("%w: drain returned state %d", ErrInvalidNativeState, state))
		}
	}
}

func (s *Sink) failDrain(owner nativeOwner, cause error) error {
	s.mu.Lock()
	s.cancelled = true
	s.mu.Unlock()
	return errors.Join(cause, s.cancelAndJoin(owner))
}

func (s *Sink) cancelAndJoin(owner nativeOwner) error {
	if owner == nil {
		return nil
	}
	requestErr := owner.requestCancel()
	terminated, waitErr := owner.waitTerminated(cleanupWaitLimit)
	if waitErr != nil {
		return errors.Join(ErrCleanupPending, requestErr, waitErr)
	}
	if !terminated {
		return errors.Join(ErrCleanupPending, requestErr)
	}
	return requestErr
}

// FinalText returns the concatenated final transcription after successful
// Drain. Partial results, canceled sessions, and late results are not exposed.
func (s *Sink) FinalText() (string, error) {
	if s == nil {
		return "", ErrNotDrained
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.drained || s.cancelled {
		return "", ErrNotDrained
	}
	return s.finalText, nil
}

// Close requests cancellation without waiting for Drain's operation lock,
// then serializes the join and release. A timeout retains the owner so Close
// can be retried; it never frees a native session with unfinished tasks.
func (s *Sink) Close() error {
	if s == nil {
		return nil
	}
	s.requestClose()
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	if s.released || s.owner == nil {
		s.mu.Unlock()
		return nil
	}
	owner := s.owner
	s.mu.Unlock()

	terminated, err := owner.waitTerminated(cleanupWaitLimit)
	if err != nil || !terminated {
		return errors.Join(ErrCleanupPending, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != owner || s.released {
		return nil
	}
	if err := owner.release(); err != nil {
		return errors.Join(ErrCleanupPending, fmt.Errorf("applespeech: release native owner: %w", err))
	}
	s.owner = nil
	s.released = true
	return nil
}

func (s *Sink) requestClose() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeRequested = true
	if s.owner == nil || s.released || s.drained {
		return
	}
	s.cancelled = true
	_ = s.owner.requestCancel()
}

func (s *Sink) nativeFailure(stage string) error {
	s.mu.Lock()
	owner := s.owner
	s.mu.Unlock()
	message := ""
	if owner != nil {
		message = strings.TrimSpace(owner.errorMessage())
	}
	if message == "" {
		return fmt.Errorf("%w during %s", ErrNativeFailure, stage)
	}
	lowerMessage := strings.ToLower(message)
	if strings.Contains(lowerMessage, "final text exceeds") {
		return fmt.Errorf("%w during %s: %s", ErrFinalTextOverflow, stage, message)
	}
	if strings.Contains(lowerMessage, "unsupported apple speech locale") {
		return fmt.Errorf("%w: %s", ErrInvalidLocale, message)
	}
	if strings.Contains(lowerMessage, "asset") || strings.Contains(lowerMessage, "model") {
		return fmt.Errorf("%w during %s: %s", ErrAssetsUnavailable, stage, message)
	}
	return fmt.Errorf("%w during %s: %s", ErrNativeFailure, stage, message)
}

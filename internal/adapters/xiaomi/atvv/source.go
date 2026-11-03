package atvv

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	// observedAudioPacketBytes is only the capacity baseline, not an enforced
	// GATT notification or decoder frame size.
	observedAudioPacketBytes = 120
	pcmQueueSamples          = 64 * observedAudioPacketBytes * 2
	maxAudioPacket           = 512
	maxPacketSamples         = maxAudioPacket * 2
)

var (
	ErrSourceUnarmed     = errors.New("atvv source: notifications are not armed")
	ErrSourceClosed      = errors.New("atvv source: source is closed")
	ErrStaleSession      = errors.New("atvv source: event belongs to a different session")
	ErrAudioBeforeStart  = errors.New("atvv source: audio arrived before AUDIO_START")
	ErrAudioAfterStop    = errors.New("atvv source: audio arrived after AUDIO_STOP")
	ErrUnexpectedStream  = errors.New("atvv source: unexpected second stream")
	ErrUnexpectedStop    = errors.New("atvv source: AUDIO_STOP without an active stream")
	ErrSourceQueueFull   = errors.New("atvv source: PCM sample ring is full")
	ErrSourceNotReady    = errors.New("atvv source: stream is not ready")
	ErrUnsupportedRate   = errors.New("atvv source: unsupported codec sample rate")
	ErrInvalidSessionKey = errors.New("atvv source: invalid host session key")
)

// StreamKey is supplied by the host. Source does not allocate or recover
// session identifiers; both fields must match on every transport callback.
type StreamKey struct {
	Generation uint64
	Session    uint64
}

// StreamTransport owns CoreBluetooth and its callback lifetime. RequestStop
// sends at most one v1 MIC_CLOSE for an active stream and must serialize that
// decision with incoming AUDIO_STOP events. Close returns only after callbacks
// for this source can no longer arrive; on error the transport remains owned.
type StreamTransport interface {
	RequestStop(context.Context, byte) error
	Close() error
}

// SourceSnapshot is a value copy of one Source's bounded queue and lifetime
// counters. QueuedSamples is the current occupancy; IncomingSamples is the
// upper-bound sample request of the packet that overflowed, or zero when no
// overflow has occurred. Packets counts nonempty, size-valid audio
// notifications, including the overflowing notification. The timestamps are
// host-side Go times captured while handling AUDIO_START, the first packet,
// and overflow; they are not hardware capture timestamps. MonotonicElapsed is
// the AUDIO_START-to-overflow interval from Go's monotonic clock.
type SourceSnapshot struct {
	CapacitySamples  int           `json:"capacity_samples"`
	QueuedSamples    int           `json:"queued_samples"`
	IncomingSamples  int           `json:"incoming_samples"`
	HighWaterSamples int           `json:"high_water_samples"`
	SampleRate       int           `json:"sample_rate_hz"`
	DecodedSamples   uint64        `json:"decoded_samples"`
	ReadSamples      uint64        `json:"read_samples"`
	Packets          uint64        `json:"packets"`
	StartAt          time.Time     `json:"start_at,omitempty"`
	FirstPacketAt    time.Time     `json:"first_packet_at,omitempty"`
	OverflowAt       time.Time     `json:"overflow_at,omitempty"`
	MonotonicElapsed time.Duration `json:"monotonic_elapsed_ns,omitempty"`
}

// QueueOverflowError reports a full PCM ring while preserving the original
// ErrSourceQueueFull text and errors.Is behavior. Snapshot returns the frozen
// state captured at the overflow boundary.
type QueueOverflowError struct {
	snapshot SourceSnapshot
}

func (e *QueueOverflowError) Error() string { return ErrSourceQueueFull.Error() }

func (e *QueueOverflowError) Unwrap() error { return ErrSourceQueueFull }

func (e *QueueOverflowError) Snapshot() SourceSnapshot {
	if e == nil {
		return SourceSnapshot{}
	}
	return e.snapshot
}

type sourceState uint8

const (
	sourceUnarmed sourceState = iota
	sourceArmed
	sourceActive
	sourceStopped
	sourceFailed
	sourceClosed
)

// Source turns an already-armed, session-scoped ATVV stream into bounded PCM.
// It intentionally does not open a microphone: RC003's observed path emits
// AUDIO_START from the physical voice-key action without a host MIC_OPEN.
// The transport supplies that pre-armed session and forwards callbacks in
// their native serial order.
type Source struct {
	key       StreamKey
	transport StreamTransport

	mu          sync.Mutex
	transportMu sync.Mutex
	state       sourceState
	cause       error
	wake        chan struct{} // closed and replaced under mu to broadcast changes

	decoder       Decoder
	codec         Codec
	streamID      byte
	started       bool
	stopObserved  bool
	stopRequested bool
	closeSent     bool
	pendingSync   ControlMessage
	hasSync       bool

	pcm   [pcmQueueSamples]int16
	head  int
	count int

	startAt          time.Time
	firstPacketAt    time.Time
	overflowAt       time.Time
	monotonicElapsed time.Duration
	incomingSamples  int
	highWaterSamples int
	decodedSamples   uint64
	readSamples      uint64
	packets          uint64
}

// NewSource creates a source for the host's session key. The caller must arm
// it only after Control and Audio notifications are confirmed active.
func NewSource(key StreamKey, transport StreamTransport) (*Source, error) {
	if key.Generation == 0 || key.Session == 0 || transport == nil {
		return nil, ErrInvalidSessionKey
	}
	return &Source{key: key, transport: transport, wake: make(chan struct{})}, nil
}

// Arm validates the device's v1 CAPS payload. Calling Arm is the transport's
// assertion that the required Control and Audio notifications are active.
func (s *Source) Arm(key StreamKey, rawCapabilities []byte) error {
	if s == nil {
		return ErrSourceClosed
	}
	if key != s.key {
		return ErrStaleSession
	}
	_, err := ParseCapabilities(rawCapabilities)
	if err != nil {
		return s.fail(err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == sourceClosed {
		return ErrSourceClosed
	}
	if s.state == sourceFailed {
		return s.cause
	}
	if s.state != sourceUnarmed {
		return ErrSourceUnarmed
	}
	s.state = sourceArmed
	s.signalLocked()
	return nil
}

// HandleControl consumes one Control notification. 0x08 is observed but does
// not cause MIC_OPEN; the session waits for the device's AUDIO_START (0x04).
func (s *Source) HandleControl(key StreamKey, raw []byte) error {
	if s == nil {
		return ErrSourceClosed
	}
	if key != s.key {
		return ErrStaleSession
	}

	message, err := ParseControl(raw)
	if err != nil {
		return s.fail(err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpenLocked(); err != nil {
		return err
	}
	if s.state == sourceUnarmed {
		return s.failLocked(ErrSourceUnarmed)
	}

	switch message.Kind {
	case ControlStartSearch:
		// This is a device hint. The RC003 capture showed a later AUDIO_START
		// without a host MIC_OPEN, so the source must not infer a write here.
		return nil
	case ControlAudioStart:
		if s.started || s.stopObserved {
			return s.failLocked(ErrUnexpectedStream)
		}
		predictor, stepIndex := int16(0), uint8(0)
		if s.hasSync {
			if s.pendingSync.Codec != message.Codec {
				return s.failLocked(ErrUnsupportedCodec)
			}
			predictor, stepIndex = s.pendingSync.Predictor, s.pendingSync.StepIndex
		}
		if err := s.decoder.Reset(predictor, stepIndex); err != nil {
			return s.failLocked(err)
		}
		s.codec = message.Codec
		s.streamID = message.StreamID
		s.startAt = time.Now()
		s.started = true
		s.state = sourceActive
		s.hasSync = false
		s.signalLocked()
		return nil
	case ControlAudioSync:
		if s.stopObserved {
			return s.failLocked(ErrUnexpectedStream)
		}
		if !s.started {
			s.pendingSync = message
			s.hasSync = true
			return nil
		}
		if message.Codec != s.codec {
			return s.failLocked(ErrUnsupportedCodec)
		}
		if err := s.decoder.Reset(message.Predictor, message.StepIndex); err != nil {
			return s.failLocked(err)
		}
		return nil
	case ControlAudioStop:
		if !s.started || s.stopObserved {
			return s.failLocked(ErrUnexpectedStop)
		}
		s.stopObserved = true
		s.state = sourceStopped
		s.signalLocked()
		return nil
	case ControlMicrophoneOpenError:
		return s.failLocked(errors.New("atvv source: device reported a microphone-open error"))
	default:
		return s.failLocked(ErrUnknownOpcode)
	}
}

// HandleAudio decodes exactly one notification. It keeps decoder state across
// notifications and accepts any nonempty notification up to 512 bytes; the
// observed 120-byte size is not treated as a required packet boundary.
func (s *Source) HandleAudio(key StreamKey, raw []byte) error {
	if s == nil {
		return ErrSourceClosed
	}
	if key != s.key {
		return ErrStaleSession
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpenLocked(); err != nil {
		return err
	}
	if s.state == sourceUnarmed {
		return s.failLocked(ErrSourceUnarmed)
	}
	if s.stopObserved {
		return s.failLocked(ErrAudioAfterStop)
	}
	if !s.started || s.state != sourceActive {
		return s.failLocked(ErrAudioBeforeStart)
	}
	if len(raw) == 0 || len(raw) > maxAudioPacket {
		return s.failLocked(ErrMalformedPacket)
	}
	if s.firstPacketAt.IsZero() {
		s.firstPacketAt = time.Now()
	}
	s.packets++
	packetSampleCount := len(raw) * 2
	if packetSampleCount > len(s.pcm)-s.count {
		return s.failQueueOverflowLocked(packetSampleCount)
	}

	var packet [maxPacketSamples]int16
	count, err := s.decoder.Decode(packet[:], raw)
	if err != nil {
		return s.failLocked(err)
	}
	tail := (s.head + s.count) % len(s.pcm)
	first := copy(s.pcm[tail:], packet[:count])
	copy(s.pcm[:], packet[first:count])
	s.count += count
	s.decodedSamples += uint64(count)
	if s.count > s.highWaterSamples {
		s.highWaterSamples = s.count
	}
	s.signalLocked()
	return nil
}

// Snapshot returns current queue occupancy and cumulative Source counters.
// It copies timestamps and counters while holding the same lock as callbacks
// and reads; later Source operations do not mutate the returned value.
func (s *Source) Snapshot() SourceSnapshot {
	if s == nil {
		return SourceSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *Source) snapshotLocked() SourceSnapshot {
	sampleRateHz := 0
	if s.started {
		sampleRateHz, _ = sampleRate(s.codec)
	}
	return SourceSnapshot{
		CapacitySamples:  len(s.pcm),
		QueuedSamples:    s.count,
		IncomingSamples:  s.incomingSamples,
		HighWaterSamples: s.highWaterSamples,
		SampleRate:       sampleRateHz,
		DecodedSamples:   s.decodedSamples,
		ReadSamples:      s.readSamples,
		Packets:          s.packets,
		StartAt:          s.startAt,
		FirstPacketAt:    s.firstPacketAt,
		OverflowAt:       s.overflowAt,
		MonotonicElapsed: s.monotonicElapsed,
	}
}

func (s *Source) failQueueOverflowLocked(incomingSamples int) error {
	now := time.Now()
	s.incomingSamples = incomingSamples
	s.overflowAt = now
	if !s.startAt.IsZero() {
		s.monotonicElapsed = now.Sub(s.startAt)
	}
	return s.failLocked(&QueueOverflowError{snapshot: s.snapshotLocked()})
}

// Ready waits for the negotiated AUDIO_START codec, which supplies the actual
// PCM rate required by the existing audio-session factory.
func (s *Source) Ready(ctx context.Context) (int, error) {
	if s == nil || ctx == nil {
		return 0, ErrSourceNotReady
	}
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		s.mu.Lock()
		if s.state == sourceFailed {
			err := s.cause
			s.mu.Unlock()
			return 0, err
		}
		if s.state == sourceClosed {
			s.mu.Unlock()
			return 0, ErrSourceClosed
		}
		if s.started {
			rate, err := sampleRate(s.codec)
			s.mu.Unlock()
			return rate, err
		}
		if s.state == sourceUnarmed {
			wake := s.wake
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-wake:
			}
			continue
		}
		wake := s.wake
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-wake:
		}
	}
}

func sampleRate(codec Codec) (int, error) {
	switch codec {
	case CodecADPCM8k:
		return 8000, nil
	case CodecADPCM16k:
		return 16000, nil
	default:
		return 0, ErrUnsupportedRate
	}
}

// Read drains decoded packets in order. EOF requires both an observed remote
// AUDIO_STOP and the host's Stop request; it never uses a timer as a tail rule.
func (s *Source) Read(ctx context.Context, dst []int16) (int, error) {
	if s == nil || ctx == nil {
		return 0, ErrSourceClosed
	}
	if len(dst) == 0 {
		return 0, io.ErrShortBuffer
	}
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		s.mu.Lock()
		if s.state == sourceFailed {
			err := s.cause
			s.mu.Unlock()
			return 0, err
		}
		if s.state == sourceClosed {
			s.mu.Unlock()
			return 0, ErrSourceClosed
		}
		if s.count > 0 {
			firstCount := min(s.count, len(dst), len(s.pcm)-s.head)
			written := copy(dst[:firstCount], s.pcm[s.head:s.head+firstCount])
			remaining := min(len(dst)-written, s.count-written)
			if remaining > 0 {
				written += copy(dst[written:written+remaining], s.pcm[:remaining])
			}
			s.head = (s.head + written) % len(s.pcm)
			s.count -= written
			s.readSamples += uint64(written)
			s.mu.Unlock()
			return written, nil
		}
		if s.stopObserved && s.stopRequested {
			s.mu.Unlock()
			return 0, io.EOF
		}
		wake := s.wake
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-wake:
		}
	}
}

// Stop requests remote shutdown once, then waits for the serial transport
// reducer to deliver AUDIO_STOP. If the remote STOP arrived first, it only
// opens the local EOF gate and does not send MIC_CLOSE.
func (s *Source) Stop(ctx context.Context) error {
	if s == nil || ctx == nil {
		return ErrSourceClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	if err := s.checkOpenLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	if !s.started {
		s.mu.Unlock()
		// A physical release can arrive before AUDIO_START. Wait only within the
		// caller's bounded stop context; do not mark stop requested or send
		// MIC_CLOSE until the device has supplied the stream ID and codec.
		if _, err := s.Ready(ctx); err != nil {
			return err
		}
		s.mu.Lock()
		if err := s.checkOpenLocked(); err != nil {
			s.mu.Unlock()
			return err
		}
		if !s.started {
			s.mu.Unlock()
			return ErrSourceNotReady
		}
	}
	s.stopRequested = true
	shouldRequestClose := !s.stopObserved && !s.closeSent
	streamID := s.streamID
	if shouldRequestClose {
		s.closeSent = true
	}
	s.signalLocked()
	s.mu.Unlock()

	if shouldRequestClose {
		s.transportMu.Lock()
		err := s.transport.RequestStop(ctx, streamID)
		s.transportMu.Unlock()
		if err != nil {
			return s.fail(err)
		}
	}

	for {
		s.mu.Lock()
		if s.state == sourceFailed {
			err := s.cause
			s.mu.Unlock()
			return err
		}
		if s.state == sourceClosed {
			s.mu.Unlock()
			return ErrSourceClosed
		}
		if s.stopObserved {
			s.mu.Unlock()
			return nil
		}
		wake := s.wake
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}

// Close delegates callback quiescence to the transport. A failed native close
// leaves the Source live so Runtime can retry cleanup without losing handles.
func (s *Source) Close() error {
	if s == nil {
		return nil
	}
	s.transportMu.Lock()
	defer s.transportMu.Unlock()

	s.mu.Lock()
	if s.state == sourceClosed {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	if err := s.transport.Close(); err != nil {
		return err
	}
	s.mu.Lock()
	s.state = sourceClosed
	s.clearQueueLocked()
	s.signalLocked()
	s.mu.Unlock()
	return nil
}

func (s *Source) checkOpenLocked() error {
	if s.state == sourceClosed {
		return ErrSourceClosed
	}
	if s.state == sourceFailed {
		return s.cause
	}
	return nil
}

func (s *Source) fail(err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failLocked(err)
}

func (s *Source) failLocked(err error) error {
	if err == nil {
		return nil
	}
	if s.state == sourceClosed {
		return ErrSourceClosed
	}
	if s.state != sourceClosed && s.state != sourceFailed {
		s.state = sourceFailed
		s.cause = err
		s.signalLocked()
	}
	if s.cause != nil {
		return s.cause
	}
	return err
}

func (s *Source) signalLocked() {
	close(s.wake)
	s.wake = make(chan struct{})
}

func (s *Source) clearQueueLocked() {
	clear(s.pcm[:])
	s.head = 0
	s.count = 0
}

var _ interface {
	Read(context.Context, []int16) (int, error)
	Stop(context.Context) error
	Close() error
} = (*Source)(nil)

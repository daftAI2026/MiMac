package atvv

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrDeviceOwnerClosed        = errors.New("atvv device: device owner is closed")
	ErrDeviceOwnerClosing       = errors.New("atvv device: device owner is closing")
	ErrDeviceLeaseActive        = errors.New("atvv device: a session source is already leased")
	ErrDeviceOrphanStreamActive = errors.New("atvv device: an unleased remote stream is still active")
	ErrSessionLeaseDetached     = errors.New("atvv device: session lease is detached")
	ErrSessionCloseRequired     = errors.New("atvv device: session source close must use its lease")
	ErrSessionStreamActive      = errors.New("atvv device: session stream is still active")
)

const sessionCloseStopTimeout = 2 * time.Second

// DeviceTransport is the connected-device resource shared by successive
// SessionSources. Ready confirms subscriptions; CAPS arrives as a separate
// NativeEventControlValue and is cached by DeviceOwner. Close must stop any
// native stream and wait for its callback quiescence before returning nil.
type DeviceTransport interface {
	StreamTransport
	Ready(context.Context) error
}

// DeviceTransportFactory binds the owner's synchronous callback before the
// transport starts delivering native events. It should return a nil transport
// on error. If it returns both a transport and an error, NewDeviceOwner returns
// a nonnil owner containing that transport; callers must Close that owner even
// though construction also returned an error.
type DeviceTransportFactory func(NativeEventHandler) (DeviceTransport, error)

// DeviceOwner owns one subscribed device transport and leases an armed Source
// to at most one host session at a time. It consumes and caches CAPS itself;
// all other device notifications are synchronously forwarded to the current
// Source while the owner lock prevents detach from racing an in-flight event.
type DeviceOwner struct {
	generation uint64

	mu           sync.Mutex
	closeMu      sync.Mutex
	transport    DeviceTransport
	lease        *deviceLease
	closingLease *deviceLease
	capabilities []byte
	capsReady    bool
	capsChanged  chan struct{}
	fault        error
	orphanStream bool
	closing      bool
	closed       bool
}

type deviceLease struct {
	key     StreamKey
	source  *Source
	closing bool
	fault   error
}

// NewDeviceOwner creates a connected-device owner. The factory receives the
// synchronous event callback that must be passed to the native Transport.
func NewDeviceOwner(generation uint64, factory DeviceTransportFactory) (*DeviceOwner, error) {
	if generation == 0 || factory == nil {
		return nil, ErrInvalidSessionKey
	}
	owner := &DeviceOwner{
		generation:  generation,
		capsChanged: make(chan struct{}),
	}
	transport, err := factory(owner.handleNativeEvent)
	if err != nil {
		if transport != nil {
			owner.mu.Lock()
			owner.transport = transport
			owner.mu.Unlock()
			return owner, err
		}
		return nil, err
	}
	if transport == nil {
		return nil, errors.New("atvv device: transport factory returned nil")
	}
	owner.mu.Lock()
	owner.transport = transport
	owner.mu.Unlock()
	return owner, nil
}

// OpenDeviceOwner opens one native transport and retains it until DeviceOwner.Close.
func OpenDeviceOwner(generation uint64, libraryPath string) (*DeviceOwner, error) {
	return NewDeviceOwner(generation, func(handler NativeEventHandler) (DeviceTransport, error) {
		return OpenTransport(libraryPath, handler)
	})
}

// NewSource waits for notification readiness and a valid device CAPS value,
// then arms one session-scoped source with the exact cached CAPS payload.
// A pending lease reserves the device while either prerequisite is awaited.
func (d *DeviceOwner) NewSource(ctx context.Context, key StreamKey) (*SessionSource, error) {
	if d == nil || ctx == nil || key.Generation == 0 || key.Session == 0 {
		return nil, ErrInvalidSessionKey
	}
	if key.Generation != d.generation {
		return nil, ErrStaleSession
	}
	lease := &deviceLease{key: key}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, ErrDeviceOwnerClosed
	}
	if d.closing {
		d.mu.Unlock()
		return nil, ErrDeviceOwnerClosing
	}
	if d.fault != nil {
		err := d.fault
		d.mu.Unlock()
		return nil, err
	}
	if d.orphanStream {
		d.mu.Unlock()
		return nil, ErrDeviceOrphanStreamActive
	}
	if d.lease != nil {
		d.mu.Unlock()
		return nil, ErrDeviceLeaseActive
	}
	transport := d.transport
	if transport == nil {
		d.mu.Unlock()
		return nil, ErrDeviceOwnerClosed
	}
	d.lease = lease
	d.mu.Unlock()

	rollback := func(err error) (*SessionSource, error) {
		d.mu.Lock()
		if d.lease == lease {
			d.lease = nil
			d.signalCapsLocked()
		}
		d.mu.Unlock()
		return nil, err
	}

	if err := transport.Ready(ctx); err != nil {
		return rollback(err)
	}
	capabilities, err := d.waitCapabilities(ctx, lease)
	if err != nil {
		return rollback(err)
	}

	proxy := &leasedStreamTransport{owner: d, lease: lease}
	source, err := NewSource(key, proxy)
	if err != nil {
		return rollback(err)
	}
	if err := source.Arm(key, capabilities); err != nil {
		_ = source.Close()
		return rollback(err)
	}
	session := &SessionSource{source: source, owner: d, lease: lease}
	d.mu.Lock()
	if d.lease != lease || d.closing || d.closed || d.fault != nil {
		err := d.leaseErrorLocked(lease)
		if d.lease == lease {
			d.lease = nil
		}
		d.mu.Unlock()
		_ = source.Close()
		return nil, err
	}
	if d.orphanStream {
		d.lease = nil
		d.mu.Unlock()
		_ = source.Close()
		return nil, ErrDeviceOrphanStreamActive
	}
	lease.source = source
	d.mu.Unlock()
	return session, nil
}

func (d *DeviceOwner) leaseErrorLocked(lease *deviceLease) error {
	if d.closed {
		return ErrDeviceOwnerClosed
	}
	if d.closing {
		return ErrDeviceOwnerClosing
	}
	if d.fault != nil {
		return d.fault
	}
	if d.orphanStream {
		return ErrDeviceOrphanStreamActive
	}
	if d.lease != lease {
		return ErrSessionLeaseDetached
	}
	return ErrDeviceLeaseActive
}

// SessionSource exposes the existing source contract while making Close a
// bounded stream-stop followed by a Source detach. The underlying Source is
// intentionally private so callers cannot bypass that lease close policy.
type SessionSource struct {
	source *Source
	owner  *DeviceOwner
	lease  *deviceLease

	closeMu sync.Mutex
	closed  bool
}

func (s *SessionSource) Read(ctx context.Context, dst []int16) (int, error) {
	if s == nil || s.source == nil {
		return 0, ErrSourceClosed
	}
	return s.source.Read(ctx, dst)
}

func (s *SessionSource) Stop(ctx context.Context) error {
	if s == nil || s.source == nil {
		return ErrSourceClosed
	}
	return s.source.Stop(ctx)
}

// Ready waits for the device's AUDIO_START codec, as Source does. It does not
// report notification readiness alone.
func (s *SessionSource) Ready(ctx context.Context) (int, error) {
	if s == nil || s.source == nil {
		return 0, ErrSourceClosed
	}
	return s.source.Ready(ctx)
}

// Err returns the current terminal source error, if any. An armed source that
// has not yet received AUDIO_START is healthy and returns nil.
func (s *SessionSource) Err() error {
	if s == nil || s.source == nil {
		return ErrSourceClosed
	}
	s.source.mu.Lock()
	defer s.source.mu.Unlock()
	if s.source.state == sourceClosed {
		return ErrSourceClosed
	}
	if s.source.state == sourceFailed {
		return s.source.cause
	}
	return nil
}

// Close prevents a new lease, stops a stream already observed by this Source,
// then detaches it. If stop does not complete, the lease remains reserved so a
// later session cannot inherit notifications from the still-active stream.
func (s *SessionSource) Close() error {
	if s == nil || s.source == nil {
		return nil
	}
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return nil
	}
	if s.owner.isClosed() {
		s.closed = true
		return nil
	}
	if s.sourceTerminal() {
		if err := s.owner.closeFaultedLease(s.lease, s.source); err != nil {
			return err
		}
		s.closed = true
		return nil
	}

	if err := s.beginClosing(); err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		active, sourceClosed, err := s.owner.sourceCloseState(s.lease, s.source)
		if err != nil {
			if s.sourceTerminal() {
				if closeErr := s.owner.closeFaultedLease(s.lease, s.source); closeErr == nil {
					s.closed = true
					return nil
				} else {
					return errors.Join(err, closeErr)
				}
			}
			return err
		}
		if sourceClosed {
			s.closed = true
			return nil
		}
		if active {
			ctx, cancel := context.WithTimeout(context.Background(), sessionCloseStopTimeout)
			err := s.source.Stop(ctx)
			cancel()
			if err != nil {
				if s.sourceTerminal() {
					if closeErr := s.owner.closeFaultedLease(s.lease, s.source); closeErr == nil {
						s.closed = true
						return nil
					} else {
						return errors.Join(err, closeErr)
					}
				}
				if closeErr := s.owner.closeUnresponsiveLease(s.lease, s.source); closeErr == nil {
					s.closed = true
					return nil
				} else {
					return errors.Join(err, closeErr)
				}
			}
		}
		if err := s.source.Close(); err != nil {
			if errors.Is(err, ErrSessionStreamActive) {
				continue
			}
			return err
		}
		s.closed = true
		return nil
	}
	return ErrSessionStreamActive
}

func (s *SessionSource) sourceTerminal() bool {
	if s == nil || s.source == nil {
		return true
	}
	s.source.mu.Lock()
	failed := s.source.state == sourceFailed || s.source.state == sourceClosed
	s.source.mu.Unlock()
	if failed {
		return true
	}
	if s.owner == nil || s.lease == nil {
		return false
	}
	s.owner.mu.Lock()
	faulted := s.lease.fault != nil
	s.owner.mu.Unlock()
	return faulted
}

func (s *SessionSource) beginClosing() error {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	if s.owner.closed {
		return ErrDeviceOwnerClosed
	}
	if s.owner.closing {
		return ErrDeviceOwnerClosing
	}
	if s.owner.lease != s.lease {
		return ErrSessionLeaseDetached
	}
	s.lease.closing = true
	return nil
}

func (d *DeviceOwner) sourceCloseState(lease *deviceLease, source *Source) (active, closed bool, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lease != lease {
		if d.closed {
			return false, true, nil
		}
		return false, false, ErrSessionLeaseDetached
	}
	if lease.fault != nil {
		return false, false, lease.fault
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.state == sourceClosed {
		return false, true, nil
	}
	return source.started && !source.stopObserved, false, nil
}

func (d *DeviceOwner) isClosed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closed
}

// closeFaultedLease tears down the connection only for this exact terminal
// lease. The native Close call runs without d.mu held and provides the
// callback-quiescence barrier before the failed Source is cleared.
func (d *DeviceOwner) closeFaultedLease(lease *deviceLease, source *Source) error {
	return d.closeUnrecoverableLease(lease, source, false)
}

// closeUnresponsiveLease tears down an exact closing lease whose active stream
// failed to reach AUDIO_STOP after a MIC_CLOSE attempt and bounded wait.
func (d *DeviceOwner) closeUnresponsiveLease(lease *deviceLease, source *Source) error {
	return d.closeUnrecoverableLease(lease, source, true)
}

func (d *DeviceOwner) closeUnrecoverableLease(lease *deviceLease, source *Source, allowUnresponsiveStop bool) error {
	if d == nil || lease == nil || source == nil {
		return ErrSessionLeaseDetached
	}
	d.closeMu.Lock()
	defer d.closeMu.Unlock()

	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	if d.closing {
		if d.closingLease != lease {
			d.mu.Unlock()
			return ErrDeviceOwnerClosing
		}
	} else {
		if d.lease != lease || lease.source != source {
			d.mu.Unlock()
			return ErrSessionLeaseDetached
		}
		source.mu.Lock()
		terminal := lease.fault != nil || source.state == sourceFailed || source.state == sourceClosed
		if allowUnresponsiveStop && lease.closing && source.started && source.stopRequested && source.closeSent && !source.stopObserved {
			terminal = true
		}
		source.mu.Unlock()
		if !terminal {
			d.mu.Unlock()
			return ErrSessionStreamActive
		}
		d.closing = true
		d.closingLease = lease
		d.lease = nil
		d.signalCapsLocked()
	}
	transport := d.transport
	d.mu.Unlock()

	return d.finishClose(transport)
}

// Close closes the shared native transport. On failure, owner and transport
// handles remain retained; a later Close call retries the same resource.
func (d *DeviceOwner) Close() error {
	if d == nil {
		return nil
	}
	d.closeMu.Lock()
	defer d.closeMu.Unlock()

	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closing = true
	if d.closingLease == nil && d.lease != nil {
		d.closingLease = d.lease
	}
	d.lease = nil
	d.signalCapsLocked()
	transport := d.transport
	d.mu.Unlock()
	return d.finishClose(transport)
}

// finishClose is called with d.closeMu held. It keeps the transport and
// closing lease stored until native quiescence and Source cleanup both succeed.
func (d *DeviceOwner) finishClose(transport DeviceTransport) error {
	if transport != nil {
		if err := transport.Close(); err != nil {
			return err
		}
	}

	d.mu.Lock()
	closingLease := d.closingLease
	d.mu.Unlock()
	if closingLease != nil && closingLease.source != nil {
		if err := closingLease.source.Close(); err != nil {
			return err
		}
	}

	d.mu.Lock()
	d.closed = true
	d.closingLease = nil
	d.transport = nil
	d.signalCapsLocked()
	d.mu.Unlock()
	return nil
}

func (d *DeviceOwner) waitCapabilities(ctx context.Context, lease *deviceLease) ([]byte, error) {
	for {
		d.mu.Lock()
		if d.fault != nil {
			err := d.fault
			d.mu.Unlock()
			return nil, err
		}
		if d.closed {
			d.mu.Unlock()
			return nil, ErrDeviceOwnerClosed
		}
		if d.closing {
			d.mu.Unlock()
			return nil, ErrDeviceOwnerClosing
		}
		if d.lease != lease {
			d.mu.Unlock()
			return nil, ErrSessionLeaseDetached
		}
		if d.orphanStream {
			d.mu.Unlock()
			return nil, ErrDeviceOrphanStreamActive
		}
		if d.capsReady {
			capabilities := append([]byte(nil), d.capabilities...)
			d.mu.Unlock()
			return capabilities, nil
		}
		changed := d.capsChanged
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (d *DeviceOwner) handleNativeEvent(event NativeEvent) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closing || d.closed {
		return nil
	}
	switch event.Kind {
	case NativeEventReady:
		if event.Status != nativeStatusOK {
			return d.recordFaultLocked(fmt.Errorf("atvv device: notification readiness status=%d", event.Status))
		}
		return nil
	case NativeEventControlValue:
		if len(event.Bytes) > 0 && event.Bytes[0] == 0x0B {
			capabilities, err := ParseCapabilities(event.Bytes)
			if err != nil {
				return d.recordFaultLocked(fmt.Errorf("atvv device: invalid CAPS: %w", err))
			}
			d.capabilities = capabilities.Raw
			d.capsReady = true
			d.signalCapsLocked()
			return nil
		}
		if d.lease == nil || d.lease.source == nil {
			return d.handleUnleasedControlLocked(event.Bytes)
		}
		if err := d.lease.source.HandleControl(d.lease.key, event.Bytes); err != nil {
			d.lease.fault = err
			return err
		}
		return nil
	case NativeEventAudioValue:
		if d.lease == nil || d.lease.source == nil {
			return nil
		}
		if err := d.lease.source.HandleAudio(d.lease.key, event.Bytes); err != nil {
			d.lease.fault = err
			return err
		}
		return nil
	case NativeEventFailure:
		return d.recordFaultLocked(fmt.Errorf("atvv device: native failure status=%d", event.Status))
	case NativeEventStopWriteAck, NativeEventCloseDone:
		return nil
	default:
		return d.recordFaultLocked(fmt.Errorf("atvv device: unexpected native event kind=%d", event.Kind))
	}
}

func (d *DeviceOwner) recordFaultLocked(err error) error {
	if d.fault == nil {
		d.fault = err
	}
	if d.lease != nil {
		d.lease.fault = d.fault
		if d.lease.source != nil {
			d.lease.source.fail(d.fault)
		}
	}
	d.signalCapsLocked()
	return d.fault
}

func (d *DeviceOwner) handleUnleasedControlLocked(raw []byte) error {
	message, err := ParseControl(raw)
	if err != nil {
		if len(raw) > 0 && raw[0] == 0x04 {
			return d.recordFaultLocked(fmt.Errorf("atvv device: malformed unleased AUDIO_START: %w", err))
		}
		return nil
	}
	switch message.Kind {
	case ControlAudioStart:
		if d.orphanStream {
			return d.recordFaultLocked(ErrDeviceOrphanStreamActive)
		}
		d.orphanStream = true
		d.signalCapsLocked()
	case ControlAudioStop:
		if d.orphanStream {
			d.orphanStream = false
			d.signalCapsLocked()
		}
	}
	return nil
}

func (d *DeviceOwner) signalCapsLocked() {
	if d.capsChanged == nil {
		d.capsChanged = make(chan struct{})
		return
	}
	close(d.capsChanged)
	d.capsChanged = make(chan struct{})
}

type leasedStreamTransport struct {
	owner *DeviceOwner
	lease *deviceLease
}

func (t *leasedStreamTransport) RequestStop(ctx context.Context, streamID byte) error {
	if t == nil || t.owner == nil || ctx == nil {
		return ErrSessionLeaseDetached
	}
	t.owner.mu.Lock()
	if t.owner.closed {
		t.owner.mu.Unlock()
		return ErrDeviceOwnerClosed
	}
	if t.owner.closing {
		t.owner.mu.Unlock()
		return ErrDeviceOwnerClosing
	}
	if t.owner.lease != t.lease || t.lease.source == nil {
		t.owner.mu.Unlock()
		return ErrSessionLeaseDetached
	}
	transport := t.owner.transport
	t.owner.mu.Unlock()
	if transport == nil {
		return ErrDeviceOwnerClosed
	}
	return transport.RequestStop(ctx, streamID)
}

// Close is called by Source.Close. It only detaches the session lease; the
// shared connected-device Transport is closed exclusively by DeviceOwner.
func (t *leasedStreamTransport) Close() error {
	if t == nil || t.owner == nil {
		return nil
	}
	t.owner.mu.Lock()
	defer t.owner.mu.Unlock()
	if t.owner.lease != t.lease {
		return nil
	}
	if t.lease.fault != nil {
		return t.lease.fault
	}
	if t.lease.source != nil {
		if !t.lease.closing {
			return ErrSessionCloseRequired
		}
		t.lease.source.mu.Lock()
		active := t.lease.source.started && !t.lease.source.stopObserved
		t.lease.source.mu.Unlock()
		if active {
			return ErrSessionStreamActive
		}
	}
	t.owner.lease = nil
	t.owner.signalCapsLocked()
	return nil
}

var _ interface {
	Read(context.Context, []int16) (int, error)
	Stop(context.Context) error
	Close() error
} = (*SessionSource)(nil)

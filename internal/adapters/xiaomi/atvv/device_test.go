package atvv

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeDeviceTransport struct {
	mu sync.Mutex

	handler     NativeEventHandler
	readyErr    error
	stopErr     error
	stopIDs     []byte
	closeErrors []error
	closeCalls  int
	autoStop    bool
}

func (f *fakeDeviceTransport) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	err := f.readyErr
	f.mu.Unlock()
	return err
}

func (f *fakeDeviceTransport) RequestStop(ctx context.Context, streamID byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	f.stopIDs = append(f.stopIDs, streamID)
	err := f.stopErr
	autoStop := f.autoStop
	handler := f.handler
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if autoStop && handler != nil {
		return handler(NativeEvent{Kind: NativeEventControlValue, Bytes: []byte{0x00}})
	}
	return nil
}

func (f *fakeDeviceTransport) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := f.closeCalls
	f.closeCalls++
	if call < len(f.closeErrors) {
		return f.closeErrors[call]
	}
	return nil
}

func (f *fakeDeviceTransport) emit(event NativeEvent) error {
	f.mu.Lock()
	handler := f.handler
	f.mu.Unlock()
	if handler == nil {
		return errors.New("fake transport has no handler")
	}
	return handler(event)
}

func (f *fakeDeviceTransport) snapshot() (stopIDs []byte, closeCalls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.stopIDs...), f.closeCalls
}

func newFakeDeviceOwner(t *testing.T, generation uint64, transport *fakeDeviceTransport) *DeviceOwner {
	t.Helper()
	owner, err := NewDeviceOwner(generation, func(handler NativeEventHandler) (DeviceTransport, error) {
		transport.handler = handler
		return transport, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func emitTestCapabilities(t *testing.T, transport *fakeDeviceTransport) []byte {
	t.Helper()
	capabilities := []byte{0x0B, 0x01, 0x00, 0xA5, 0x5A, 0x00, 0x78, 0xCC}
	if err := transport.emit(NativeEvent{Kind: NativeEventControlValue, Bytes: capabilities}); err != nil {
		t.Fatal(err)
	}
	return capabilities
}

func newDeviceSession(t *testing.T, owner *DeviceOwner, key StreamKey) *SessionSource {
	t.Helper()
	session, err := owner.NewSource(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestDeviceOwnerWaitsForAndCachesActualCapabilities(t *testing.T) {
	transport := &fakeDeviceTransport{autoStop: true}
	owner := newFakeDeviceOwner(t, 11, transport)
	t.Cleanup(func() { _ = owner.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if _, err := owner.NewSource(ctx, StreamKey{Generation: 11, Session: 1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("NewSource before CAPS error = %v, want deadline", err)
	}

	want := emitTestCapabilities(t, transport)
	for i := range want {
		want[i] = 0 // the event payload is borrowed; the owner must retain its own CAPS copy.
	}
	owner.mu.Lock()
	got := append([]byte(nil), owner.capabilities...)
	ready := owner.capsReady
	owner.mu.Unlock()
	if !ready || len(got) == 0 {
		t.Fatalf("CAPS cache state = (ready=%v, bytes=%x), want cached payload", ready, got)
	}
	if got[0] != 0x0B || got[3] != 0xA5 || got[4] != 0x5A || got[7] != 0xCC {
		t.Fatalf("cached CAPS = %x, want a preserved copy of the device payload", got)
	}

	session := newDeviceSession(t, owner, StreamKey{Generation: 11, Session: 2})
	if err := transport.emit(NativeEvent{Kind: NativeEventControlValue, Bytes: []byte{0x04, 0x03, 0x02, 0x09}}); err != nil {
		t.Fatalf("AUDIO_START after cached CAPS: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close armed but inactive session: %v", err)
	}
}

func TestDeviceOwnerDoesNotAdoptAnUnleasedStartIntoTheNextSession(t *testing.T) {
	transport := &fakeDeviceTransport{}
	owner := newFakeDeviceOwner(t, 12, transport)
	t.Cleanup(func() { _ = owner.Close() })
	emitTestCapabilities(t, transport)

	if err := transport.emit(NativeEvent{Kind: NativeEventControlValue, Bytes: []byte{0x04, 0x03, 0x02, 0x17}}); err != nil {
		t.Fatalf("unleased AUDIO_START: %v", err)
	}
	if _, err := owner.NewSource(context.Background(), StreamKey{Generation: 12, Session: 1}); !errors.Is(err, ErrDeviceOrphanStreamActive) {
		t.Fatalf("NewSource during an unleased stream = %v, want ErrDeviceOrphanStreamActive", err)
	}

	if err := transport.emit(NativeEvent{Kind: NativeEventControlValue, Bytes: []byte{0x00}}); err != nil {
		t.Fatalf("unleased AUDIO_STOP: %v", err)
	}
	session := newDeviceSession(t, owner, StreamKey{Generation: 12, Session: 2})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if _, err := session.Ready(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("next session Ready without its own AUDIO_START = %v, want deadline", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close session that never started: %v", err)
	}
}

func TestDeviceOwnerReusesTransportAndClosesOnlyAtDeviceBoundary(t *testing.T) {
	transport := &fakeDeviceTransport{autoStop: true}
	owner := newFakeDeviceOwner(t, 3, transport)
	t.Cleanup(func() { _ = owner.Close() })
	emitTestCapabilities(t, transport)

	first := newDeviceSession(t, owner, StreamKey{Generation: 3, Session: 1})
	if err := first.Close(); err != nil {
		t.Fatalf("close first session: %v", err)
	}
	if stops, closes := transport.snapshot(); len(stops) != 0 || closes != 0 {
		t.Fatalf("inactive session close caused stops=%v transport closes=%d, want no device action", stops, closes)
	}

	second := newDeviceSession(t, owner, StreamKey{Generation: 3, Session: 2})
	if err := transport.emit(NativeEvent{Kind: NativeEventControlValue, Bytes: []byte{0x04, 0x03, 0x02, 0x07}}); err != nil {
		t.Fatalf("forward AUDIO_START: %v", err)
	}
	if err := transport.emit(NativeEvent{Kind: NativeEventAudioValue, Bytes: []byte{0x70}}); err != nil {
		t.Fatalf("forward audio packet: %v", err)
	}
	pcm := make([]int16, 2)
	if n, err := second.Read(context.Background(), pcm); err != nil || n != 2 || pcm[0] != 11 || pcm[1] != 13 {
		t.Fatalf("read forwarded audio = (%d, %v, %v), want (2, nil, [11 13])", n, err, pcm)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close active session: %v", err)
	}
	if stops, closes := transport.snapshot(); len(stops) != 1 || stops[0] != 7 || closes != 0 {
		t.Fatalf("active session close caused stops=%v transport closes=%d, want one MIC_CLOSE for stream 7 and no device close", stops, closes)
	}

	third := newDeviceSession(t, owner, StreamKey{Generation: 3, Session: 3})
	if err := third.Close(); err != nil {
		t.Fatalf("close third session: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close device owner: %v", err)
	}
	if _, closes := transport.snapshot(); closes != 1 {
		t.Fatalf("device Close calls=%d, want exactly 1", closes)
	}
	if _, err := owner.NewSource(context.Background(), StreamKey{Generation: 3, Session: 4}); !errors.Is(err, ErrDeviceOwnerClosed) {
		t.Fatalf("NewSource after device close = %v, want ErrDeviceOwnerClosed", err)
	}
}

func TestSessionSourceStopTimeoutBeforeStartDoesNotLeakOwnerLease(t *testing.T) {
	transport := &fakeDeviceTransport{}
	owner := newFakeDeviceOwner(t, 18, transport)
	t.Cleanup(func() { _ = owner.Close() })
	emitTestCapabilities(t, transport)

	first := newDeviceSession(t, owner, StreamKey{Generation: 18, Session: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := first.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("armed Stop without AUDIO_START = %v, want deadline", err)
	}
	if stops, _ := transport.snapshot(); len(stops) != 0 {
		t.Fatalf("armed Stop sent MIC_CLOSE without AUDIO_START: %v", stops)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close timed-out inactive session: %v", err)
	}

	second := newDeviceSession(t, owner, StreamKey{Generation: 18, Session: 2})
	if err := second.Close(); err != nil {
		t.Fatalf("new session could not use released owner lease: %v", err)
	}
	if stops, _ := transport.snapshot(); len(stops) != 0 {
		t.Fatalf("inactive session cleanup sent unexpected MIC_CLOSE: %v", stops)
	}
}

func TestDeviceOwnerRejectsConcurrentAndStaleSessionLeases(t *testing.T) {
	transport := &fakeDeviceTransport{}
	owner := newFakeDeviceOwner(t, 21, transport)
	t.Cleanup(func() { _ = owner.Close() })
	emitTestCapabilities(t, transport)

	first := newDeviceSession(t, owner, StreamKey{Generation: 21, Session: 4})
	firstProxy := first.source.transport.(*leasedStreamTransport)
	if _, err := owner.NewSource(context.Background(), StreamKey{Generation: 21, Session: 5}); !errors.Is(err, ErrDeviceLeaseActive) {
		t.Fatalf("second concurrent lease error = %v, want ErrDeviceLeaseActive", err)
	}
	if _, err := owner.NewSource(context.Background(), StreamKey{Generation: 20, Session: 5}); !errors.Is(err, ErrStaleSession) {
		t.Fatalf("wrong generation error = %v, want ErrStaleSession", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first lease: %v", err)
	}
	second := newDeviceSession(t, owner, StreamKey{Generation: 21, Session: 5})
	if err := firstProxy.RequestStop(context.Background(), 1); !errors.Is(err, ErrSessionLeaseDetached) {
		t.Fatalf("stale first proxy request error = %v, want ErrSessionLeaseDetached", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close second lease: %v", err)
	}
}

func TestDeviceOwnerKeepsLeaseWhenStopFailsAndRetriesDeviceClose(t *testing.T) {
	stopErr := errors.New("stop write failed")
	closeErr := errors.New("native close timed out")
	transport := &fakeDeviceTransport{stopErr: stopErr, closeErrors: []error{closeErr, nil}}
	owner := newFakeDeviceOwner(t, 31, transport)
	emitTestCapabilities(t, transport)
	session := newDeviceSession(t, owner, StreamKey{Generation: 31, Session: 1})
	if err := transport.emit(NativeEvent{Kind: NativeEventControlValue, Bytes: []byte{0x04, 0x03, 0x02, 0x09}}); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); !errors.Is(err, stopErr) || !errors.Is(err, closeErr) {
		t.Fatalf("session Close error = %v, want retained stop and device-close failures", err)
	}
	if _, err := owner.NewSource(context.Background(), StreamKey{Generation: 31, Session: 2}); !errors.Is(err, ErrDeviceOwnerClosing) {
		t.Fatalf("new lease after failed device teardown = %v, want ErrDeviceOwnerClosing", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("retry device Close: %v", err)
	}
	if _, closes := transport.snapshot(); closes != 2 {
		t.Fatalf("native close calls=%d, want 2 attempts", closes)
	}
}

func TestSessionSourceReadyAndErrReflectTerminalNativeFailure(t *testing.T) {
	t.Run("ready waiter", func(t *testing.T) {
		transport := &fakeDeviceTransport{}
		owner := newFakeDeviceOwner(t, 51, transport)
		t.Cleanup(func() { _ = owner.Close() })
		emitTestCapabilities(t, transport)
		session := newDeviceSession(t, owner, StreamKey{Generation: 51, Session: 1})
		if err := session.Err(); err != nil {
			t.Fatalf("armed, not-yet-started source Err() = %v, want nil", err)
		}

		readyDone := make(chan error, 1)
		go func() {
			_, err := session.Ready(context.Background())
			readyDone <- err
		}()
		failure := NativeEvent{Kind: NativeEventFailure, Status: 9}
		_ = transport.emit(failure)
		select {
		case err := <-readyDone:
			if err == nil || !strings.Contains(err.Error(), "native failure status=9") {
				t.Fatalf("Ready after native failure = %v, want native failure", err)
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatal("Ready did not wake on native failure")
		}
		if err := session.Err(); err == nil {
			t.Fatal("SessionSource.Err() = nil after native failure")
		}
	})

	t.Run("read waiter", func(t *testing.T) {
		transport := &fakeDeviceTransport{}
		owner := newFakeDeviceOwner(t, 52, transport)
		t.Cleanup(func() { _ = owner.Close() })
		emitTestCapabilities(t, transport)
		session := newDeviceSession(t, owner, StreamKey{Generation: 52, Session: 1})
		if err := transport.emit(NativeEvent{Kind: NativeEventControlValue, Bytes: []byte{0x04, 0x03, 0x02, 0x21}}); err != nil {
			t.Fatal(err)
		}
		if rate, err := session.Ready(context.Background()); err != nil || rate != 16000 {
			t.Fatalf("Ready before Read = (%d, %v), want (16000, nil)", rate, err)
		}
		readDone := make(chan error, 1)
		go func() {
			_, err := session.Read(context.Background(), make([]int16, 4))
			readDone <- err
		}()
		_ = transport.emit(NativeEvent{Kind: NativeEventFailure, Status: 10})
		select {
		case err := <-readDone:
			if err == nil || !strings.Contains(err.Error(), "native failure status=10") {
				t.Fatalf("Read after native failure = %v, want native failure", err)
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatal("Read did not wake on native failure")
		}
	})
}

func TestSessionSourceCloseTearsDownAndRetriesTerminalFault(t *testing.T) {
	closeErr := errors.New("native close timed out")
	transport := &fakeDeviceTransport{closeErrors: []error{closeErr, nil}}
	owner := newFakeDeviceOwner(t, 71, transport)
	emitTestCapabilities(t, transport)
	session := newDeviceSession(t, owner, StreamKey{Generation: 71, Session: 1})
	if err := transport.emit(NativeEvent{Kind: NativeEventFailure, Status: 17}); err == nil {
		t.Fatal("native failure did not fault the session")
	}

	if err := session.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("first faulted session Close = %v, want retained native-close failure", err)
	}
	if _, err := owner.NewSource(context.Background(), StreamKey{Generation: 71, Session: 2}); !errors.Is(err, ErrDeviceOwnerClosing) {
		t.Fatalf("new lease after failed fault cleanup = %v, want closing owner", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("retry faulted session Close: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("idempotent session Close after teardown: %v", err)
	}
	if _, closes := transport.snapshot(); closes != 2 {
		t.Fatalf("native close calls=%d, want failed attempt plus retry", closes)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("idempotent owner Close after fault teardown: %v", err)
	}
}

func TestSessionSourceCloseTearsDownWhenStopAckHasNoAudioStop(t *testing.T) {
	transport := &fakeDeviceTransport{}
	owner := newFakeDeviceOwner(t, 72, transport)
	emitTestCapabilities(t, transport)
	session := newDeviceSession(t, owner, StreamKey{Generation: 72, Session: 1})
	if err := transport.emit(NativeEvent{Kind: NativeEventControlValue, Bytes: []byte{0x04, 0x03, 0x02, 0x27}}); err != nil {
		t.Fatal(err)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close after bounded wait and native teardown: %v", err)
	}
	stops, closes := transport.snapshot()
	if len(stops) != 1 || stops[0] != 0x27 || closes != 1 {
		t.Fatalf("stop IDs=%v native closes=%d, want one close request and connection teardown", stops, closes)
	}
	if _, err := owner.NewSource(context.Background(), StreamKey{Generation: 72, Session: 2}); !errors.Is(err, ErrDeviceOwnerClosed) {
		t.Fatalf("new lease after missing AUDIO_STOP teardown = %v, want closed owner", err)
	}
}

func TestNewDeviceOwnerRetainsPartialTransportForCleanup(t *testing.T) {
	transport := &fakeDeviceTransport{}
	factoryErr := errors.New("factory setup failed after transport creation")
	owner, err := NewDeviceOwner(61, func(handler NativeEventHandler) (DeviceTransport, error) {
		transport.handler = handler
		return transport, factoryErr
	})
	if !errors.Is(err, factoryErr) || owner == nil {
		t.Fatalf("NewDeviceOwner(partial factory result) = (%v, %v), want owner plus factory error", owner, err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close retained partial transport: %v", err)
	}
	if _, closes := transport.snapshot(); closes != 1 {
		t.Fatalf("partial transport close calls=%d, want 1", closes)
	}
}

func TestDeviceOwnerCallbackAndSessionCloseAreMutuallyExclusive(t *testing.T) {
	transport := &fakeDeviceTransport{autoStop: true}
	owner := newFakeDeviceOwner(t, 41, transport)
	t.Cleanup(func() { _ = owner.Close() })
	emitTestCapabilities(t, transport)
	session := newDeviceSession(t, owner, StreamKey{Generation: 41, Session: 1})

	session.source.mu.Lock()
	eventDone := make(chan error, 1)
	go func() {
		eventDone <- transport.emit(NativeEvent{Kind: NativeEventControlValue, Bytes: []byte{0x04, 0x03, 0x02, 0x0B}})
	}()
	deadline := time.Now().Add(time.Second)
	for owner.mu.TryLock() {
		owner.mu.Unlock()
		if time.Now().After(deadline) {
			session.source.mu.Unlock()
			t.Fatal("native callback did not enter owner handler")
		}
		runtime.Gosched()
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- session.Close() }()
	select {
	case err := <-closeDone:
		session.source.mu.Unlock()
		t.Fatalf("session Close returned while callback was in flight: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	session.source.mu.Unlock()
	if err := <-eventDone; err != nil {
		t.Fatalf("in-flight AUDIO_START callback: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("session Close after callback drain: %v", err)
	}
	stops, _ := transport.snapshot()
	if len(stops) != 1 {
		t.Fatalf("close did not stop callback-observed stream: stop IDs=%v", stops)
	}
}

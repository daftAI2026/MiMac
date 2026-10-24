package atvv

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"
)

type fakeNativeTransportAPI struct {
	mu sync.Mutex

	token       uint64
	handle      uint64
	createCalls int
	stopCalls   []uint32
	closeCalls  int

	createZero  bool
	stopStatus  int32
	closeStatus int32
	closeHook   func(call int, token uint64)
}

func (f *fakeNativeTransportAPI) create(token uint64) uint64 {
	f.mu.Lock()
	f.token = token
	f.createCalls++
	handle := uint64(77)
	if f.createZero {
		handle = 0
	}
	f.handle = handle
	f.mu.Unlock()
	return handle
}

func (f *fakeNativeTransportAPI) requestStop(_ uint64, streamID uint32) int32 {
	f.mu.Lock()
	f.stopCalls = append(f.stopCalls, streamID)
	status := f.stopStatus
	f.mu.Unlock()
	return status
}

func (f *fakeNativeTransportAPI) close(_ uint64) int32 {
	f.mu.Lock()
	f.closeCalls++
	call, token, status, hook := f.closeCalls, f.token, f.closeStatus, f.closeHook
	f.mu.Unlock()
	if hook != nil {
		hook(call, token)
	}
	return status
}

func (f *fakeNativeTransportAPI) snapshot() (token uint64, stops []uint32, closes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.token, append([]uint32(nil), f.stopCalls...), f.closeCalls
}

func emitFakeNativeEvent(token uint64, kind NativeEventKind, status int32, data []byte) {
	var pointer unsafe.Pointer
	if len(data) > 0 {
		pointer = unsafe.Pointer(&data[0])
	}
	deliverNativeEvent(token, int32(kind), status, pointer, int32(len(data)))
}

func newFakeTransport(t *testing.T, api *fakeNativeTransportAPI, timeout time.Duration, handler NativeEventHandler) *Transport {
	t.Helper()
	api.mu.Lock()
	if api.closeHook == nil {
		api.closeHook = func(_ int, token uint64) {
			emitFakeNativeEvent(token, NativeEventCloseDone, nativeStatusOK, nil)
		}
	}
	api.mu.Unlock()
	transport, err := newTransportWithAPI(api, handler, timeout)
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	return transport
}

func sendReady(t *testing.T, api *fakeNativeTransportAPI) {
	t.Helper()
	token, _, _ := api.snapshot()
	emitFakeNativeEvent(token, NativeEventReady, nativeStatusOK, nil)
}

func TestTransportReadyIsOnlyNotificationAckAndCAPSIsSeparate(t *testing.T) {
	api := &fakeNativeTransportAPI{}
	events := make(chan NativeEventKind, 2)
	transport := newFakeTransport(t, api, time.Second, func(event NativeEvent) error {
		events <- event.Kind
		return nil
	})
	t.Cleanup(func() { _ = transport.Close() })
	token, _, _ := api.snapshot()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if err := transport.Ready(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Ready before notify ack = %v, want deadline", err)
	}
	capabilities := []byte{0x0b, 0x01, 0, 0, 3, 0, 0x78, 0, 0}
	emitFakeNativeEvent(token, NativeEventControlValue, 0, capabilities)
	if got := <-events; got != NativeEventControlValue {
		t.Fatalf("first event kind=%v, want control value", got)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel2()
	if err := transport.Ready(ctx2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Ready after CAPS but before notify ack = %v, want deadline", err)
	}
	emitFakeNativeEvent(token, NativeEventReady, 0, nil)
	if err := transport.Ready(context.Background()); err != nil {
		t.Fatalf("Ready after native notify ack: %v", err)
	}
	if got := <-events; got != NativeEventReady {
		t.Fatalf("second event kind=%v, want ready", got)
	}
}

func TestTransportEventBytesAreBorrowedForSynchronousHandler(t *testing.T) {
	api := &fakeNativeTransportAPI{}
	var wantAddress, gotAddress uintptr
	transport := newFakeTransport(t, api, time.Second, func(event NativeEvent) error {
		if event.Kind == NativeEventAudioValue && len(event.Bytes) > 0 {
			gotAddress = uintptr(unsafe.Pointer(&event.Bytes[0]))
		}
		return nil
	})
	t.Cleanup(func() { _ = transport.Close() })
	payload := []byte{1, 2, 3}
	wantAddress = uintptr(unsafe.Pointer(&payload[0]))
	token, _, _ := api.snapshot()
	emitFakeNativeEvent(token, NativeEventAudioValue, 0, payload)
	if gotAddress != wantAddress {
		t.Fatalf("payload address=%#x, want borrowed address %#x", gotAddress, wantAddress)
	}
}

func TestTransportAudioCallbackDoesNotAllocateWaiterChannels(t *testing.T) {
	api := &fakeNativeTransportAPI{}
	var callbacks int
	transport := newFakeTransport(t, api, time.Second, func(event NativeEvent) error {
		if event.Kind == NativeEventAudioValue {
			callbacks++
		}
		return nil
	})
	t.Cleanup(func() { _ = transport.Close() })
	token, _, _ := api.snapshot()
	payload := []byte{0x34, 0x12}
	var readyWake chan struct{}
	transport.mu.Lock()
	readyWake = transport.readyWake
	transport.mu.Unlock()

	allocs := testing.AllocsPerRun(100, func() {
		emitFakeNativeEvent(token, NativeEventAudioValue, 0, payload)
	})
	if allocs != 0 {
		t.Fatalf("audio notification allocated %.2f objects; want no per-event allocation", allocs)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if callbacks != 101 {
		t.Fatalf("audio callbacks=%d, want one warmup plus 100 measured callbacks", callbacks)
	}
	if transport.readyWake != readyWake {
		t.Fatal("audio event replaced the one-shot Ready channel")
	}
	if transport.closeWake != nil {
		t.Fatal("audio event allocated a Close wake channel without a waiter")
	}
}

func TestTransportStopRequiresMatchingStreamAck(t *testing.T) {
	api := &fakeNativeTransportAPI{}
	transport := newFakeTransport(t, api, time.Second, func(NativeEvent) error { return nil })
	t.Cleanup(func() { _ = transport.Close() })
	sendReady(t, api)
	done := make(chan error, 1)
	go func() { done <- transport.RequestStop(context.Background(), 0x2a) }()
	waitForStopCalls(t, api, 1)
	token, stops, _ := api.snapshot()
	if len(stops) != 1 || stops[0] != 0x2a {
		t.Fatalf("native stop calls=%v", stops)
	}
	emitFakeNativeEvent(token, NativeEventStopWriteAck, nativeStatusOK, []byte{0x29})
	select {
	case err := <-done:
		t.Fatalf("wrong-stream ack completed stop: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	emitFakeNativeEvent(token, NativeEventStopWriteAck, nativeStatusStopAlreadyObserved, []byte{0x2a})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("matching already-observed ack: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("matching stop ack did not complete request")
	}
}

func TestTransportStopTimeoutFaultsAndLateAckCannotSatisfyAnotherRequest(t *testing.T) {
	api := &fakeNativeTransportAPI{}
	transport := newFakeTransport(t, api, time.Second, func(NativeEvent) error { return nil })
	t.Cleanup(func() { _ = transport.Close() })
	sendReady(t, api)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := transport.RequestStop(ctx, 0x10)
	if !errors.Is(err, ErrTransportStopTimeout) {
		t.Fatalf("first RequestStop=%v, want timeout", err)
	}
	token, _, _ := api.snapshot()
	emitFakeNativeEvent(token, NativeEventStopWriteAck, nativeStatusOK, []byte{0x10})
	if err := transport.RequestStop(context.Background(), 0x11); !errors.Is(err, ErrTransportStopTimeout) {
		t.Fatalf("second RequestStop after late ack=%v, want permanent timeout fault", err)
	}
	_, stops, _ := api.snapshot()
	if len(stops) != 1 {
		t.Fatalf("native RequestStop calls after late ack=%v, want one", stops)
	}
}

func TestTransportAllowsOnlyOneInflightStop(t *testing.T) {
	api := &fakeNativeTransportAPI{}
	transport := newFakeTransport(t, api, time.Second, func(NativeEvent) error { return nil })
	t.Cleanup(func() { _ = transport.Close() })
	sendReady(t, api)
	done := make(chan error, 1)
	go func() { done <- transport.RequestStop(context.Background(), 4) }()
	waitForStopCalls(t, api, 1)
	if err := transport.RequestStop(context.Background(), 5); !errors.Is(err, ErrTransportStopBusy) {
		t.Fatalf("overlapping stop=%v, want busy", err)
	}
	token, _, _ := api.snapshot()
	emitFakeNativeEvent(token, NativeEventStopWriteAck, nativeStatusOK, []byte{4})
	if err := <-done; err != nil {
		t.Fatalf("first stop: %v", err)
	}
}

func TestTransportCloseTimeoutRetainsRegistryAndRetryWaitsForCloseDone(t *testing.T) {
	api := &fakeNativeTransportAPI{closeHook: func(int, uint64) {}}
	transport := newFakeTransport(t, api, 20*time.Millisecond, func(NativeEvent) error { return nil })
	token, _, _ := api.snapshot()
	if err := transport.Close(); !errors.Is(err, ErrTransportCloseTimeout) {
		t.Fatalf("first Close=%v, want timeout", err)
	}
	if lookupTransportCallback(token) != transport {
		t.Fatal("callback owner was released before CloseDone")
	}
	api.mu.Lock()
	api.closeHook = func(call int, token uint64) {
		if call == 2 {
			emitFakeNativeEvent(token, NativeEventCloseDone, nativeStatusOK, nil)
		}
	}
	api.mu.Unlock()
	if err := transport.Close(); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	if lookupTransportCallback(token) != nil {
		t.Fatal("callback owner remains after CloseDone and quiescence")
	}
	_, _, closeCalls := api.snapshot()
	if closeCalls != 2 {
		t.Fatalf("native close calls=%d, want retry count 2", closeCalls)
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
}

func TestTransportConcurrentCloseIsIdempotent(t *testing.T) {
	api := &fakeNativeTransportAPI{}
	transport := newFakeTransport(t, api, time.Second, func(NativeEvent) error { return nil })
	const callers = 8
	start := make(chan struct{})
	results := make(chan error, callers)
	var group sync.WaitGroup
	group.Add(callers)
	for range callers {
		go func() {
			defer group.Done()
			<-start
			results <- transport.Close()
		}()
	}
	close(start)
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent Close: %v", err)
		}
	}
	_, _, closeCalls := api.snapshot()
	if closeCalls != 1 {
		t.Fatalf("native close calls=%d, want one idempotent close", closeCalls)
	}
}

func TestTransportNativeCloseErrorRetainsCallbackOwner(t *testing.T) {
	api := &fakeNativeTransportAPI{closeStatus: -3}
	transport := newFakeTransport(t, api, time.Second, func(NativeEvent) error { return nil })
	token, _, _ := api.snapshot()
	if err := transport.Close(); err == nil || !strings.Contains(err.Error(), "status=-3") {
		t.Fatalf("Close native error=%v", err)
	}
	if lookupTransportCallback(token) != transport {
		t.Fatal("callback owner removed after unconfirmed native close")
	}
	api.mu.Lock()
	api.closeStatus = 0
	api.mu.Unlock()
	if err := transport.Close(); err != nil {
		t.Fatalf("Close retry: %v", err)
	}
}

func TestTransportCloseWaitsUntilCloseDoneCallbackReturns(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	api := &fakeNativeTransportAPI{}
	transport := newFakeTransport(t, api, time.Second, func(event NativeEvent) error {
		if event.Kind == NativeEventCloseDone {
			close(entered)
			<-release
		}
		return nil
	})
	token, _, _ := api.snapshot()
	api.mu.Lock()
	api.closeHook = func(_ int, token uint64) {
		emitFakeNativeEvent(token, NativeEventCloseDone, nativeStatusOK, nil)
	}
	api.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- transport.Close() }()
	<-entered
	select {
	case err := <-done:
		t.Fatalf("Close returned while callback active: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if lookupTransportCallback(token) != transport {
		t.Fatal("callback registry removed while CloseDone callback is running")
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close after callback returned: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after callback quiescence")
	}
}

func TestTransportRejectsMalformedPayloadsAndWakesReady(t *testing.T) {
	api := &fakeNativeTransportAPI{}
	called := 0
	transport := newFakeTransport(t, api, time.Second, func(NativeEvent) error {
		called++
		return nil
	})
	t.Cleanup(func() { _ = transport.Close() })
	token, _, _ := api.snapshot()
	tooLarge := make([]byte, maxNativeEventBytes+1)
	deliverNativeEvent(token, int32(NativeEventAudioValue), 0, unsafe.Pointer(&tooLarge[0]), int32(len(tooLarge)))
	if err := transport.Ready(context.Background()); !errors.Is(err, ErrInvalidNativeEvent) {
		t.Fatalf("Ready after malformed payload=%v, want malformed event", err)
	}
	if called != 0 {
		t.Fatalf("callback invoked %d times for malformed event", called)
	}
	deliverNativeEvent(token, int32(NativeEventAudioValue), 0, nil, 1)
	if err := transport.Ready(context.Background()); !errors.Is(err, ErrInvalidNativeEvent) {
		t.Fatalf("Ready after nil nonempty payload=%v, want malformed event", err)
	}
}

func TestTransportCreateFailureDoesNotLeaveCallbackToken(t *testing.T) {
	api := &fakeNativeTransportAPI{createZero: true}
	transport, err := newTransportWithAPI(api, func(NativeEvent) error { return nil }, time.Second)
	if err == nil || transport != nil {
		t.Fatalf("zero handle Create=(%v,%v), want failure", transport, err)
	}
	api.mu.Lock()
	token := api.token
	api.mu.Unlock()
	if lookupTransportCallback(token) != nil {
		t.Fatal("zero handle Create left callback token registered")
	}
}

func TestTransportNativeCallbackErrorIsLatched(t *testing.T) {
	api := &fakeNativeTransportAPI{}
	want := errors.New("consumer failed")
	transport := newFakeTransport(t, api, time.Second, func(NativeEvent) error { return want })
	t.Cleanup(func() { _ = transport.Close() })
	sendReady(t, api)
	if err := transport.Ready(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Ready after callback error=%v, want wrapped error", err)
	}
}

func TestTransportStopNativeImmediateErrorFaults(t *testing.T) {
	api := &fakeNativeTransportAPI{stopStatus: -19}
	transport := newFakeTransport(t, api, time.Second, func(NativeEvent) error { return nil })
	t.Cleanup(func() { _ = transport.Close() })
	sendReady(t, api)
	err := transport.RequestStop(context.Background(), 7)
	if err == nil || !strings.Contains(err.Error(), "status=-19") {
		t.Fatalf("immediate native stop error=%v", err)
	}
	api.mu.Lock()
	api.stopStatus = 0
	api.mu.Unlock()
	if err := transport.RequestStop(context.Background(), 8); err == nil || !strings.Contains(err.Error(), "status=-19") {
		t.Fatalf("retry after immediate stop failure=%v, want retained fault", err)
	}
}

func waitForStopCalls(t *testing.T, api *fakeNativeTransportAPI, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, stops, _ := api.snapshot()
		if len(stops) >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	_, stops, _ := api.snapshot()
	t.Fatalf("native stop calls=%v, want at least %d", stops, count)
}

package atvv

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"
)

const maxNativeEventBytes = 512

type NativeEventKind int32

const (
	NativeEventReady NativeEventKind = iota + 1
	NativeEventControlValue
	NativeEventAudioValue
	NativeEventStopWriteAck
	NativeEventFailure
	NativeEventCloseDone
)

// NativeEvent.Bytes borrows the native payload and is valid only until the
// callback returns. Process it synchronously; copy it if it must be retained.
type NativeEvent struct {
	Kind   NativeEventKind
	Status int32
	Bytes  []byte
}

// NativeEventHandler runs synchronously on the native CoreBluetooth callback
// queue. It must not retain event.Bytes or synchronously call Transport.Close.
type NativeEventHandler func(NativeEvent) error

var (
	ErrTransportUnsupported  = errors.New("atvv transport: unsupported platform")
	ErrTransportClosed       = errors.New("atvv transport: closed")
	ErrTransportNotReady     = errors.New("atvv transport: notifications are not ready")
	ErrTransportFault        = errors.New("atvv transport: faulted")
	ErrTransportStopBusy     = errors.New("atvv transport: another stop request is in flight")
	ErrTransportStopTimeout  = errors.New("atvv transport: stop acknowledgement timed out")
	ErrTransportCloseTimeout = errors.New("atvv transport: close acknowledgement timed out")
	ErrInvalidNativeEvent    = errors.New("atvv transport: malformed native event")
)

const (
	nativeStatusOK                  int32 = 0
	nativeStatusStopAlreadyObserved int32 = 1
)

type nativeTransportAPI interface {
	create(context uint64) uint64
	requestStop(handle uint64, streamID uint32) int32
	close(handle uint64) int32
}

type callbackRegistryState struct {
	sync.Mutex
	next    uint64
	entries map[uint64]*Transport
}

var nativeTransportCallbacks = callbackRegistryState{
	next:    1,
	entries: make(map[uint64]*Transport),
}

func registerTransportCallback(transport *Transport) (uint64, error) {
	nativeTransportCallbacks.Lock()
	defer nativeTransportCallbacks.Unlock()
	if nativeTransportCallbacks.next == 0 {
		return 0, errors.New("atvv transport: callback token space exhausted")
	}
	token := nativeTransportCallbacks.next
	if token == ^uint64(0) {
		nativeTransportCallbacks.next = 0
	} else {
		nativeTransportCallbacks.next++
	}
	nativeTransportCallbacks.entries[token] = transport
	return token, nil
}

func lookupTransportCallback(token uint64) *Transport {
	nativeTransportCallbacks.Lock()
	transport := nativeTransportCallbacks.entries[token]
	nativeTransportCallbacks.Unlock()
	return transport
}

func unregisterTransportCallback(token uint64) {
	if token == 0 {
		return
	}
	nativeTransportCallbacks.Lock()
	delete(nativeTransportCallbacks.entries, token)
	nativeTransportCallbacks.Unlock()
}

type stopRequest struct {
	streamID byte
	done     chan struct{}
	finished bool
	err      error
}

// Transport owns the native GATT transport and its callback token. It is a
// connected-device resource; a session Source is bound to it by the caller.
type Transport struct {
	api          nativeTransportAPI
	handle       uint64
	token        uint64
	handler      NativeEventHandler
	closeTimeout time.Duration

	mu              sync.Mutex
	readyWake       chan struct{}
	readySignaled   bool
	ready           bool
	readyErr        error
	fault           error
	stopFault       error
	pendingStop     *stopRequest
	nativeCloseDone bool
	closed          bool
	activeCallbacks int
	closeWake       chan struct{}

	closeMu sync.Mutex
}

const defaultTransportCloseTimeout = 2 * time.Second

func newTransportWithAPI(api nativeTransportAPI, handler NativeEventHandler, closeTimeout time.Duration) (*Transport, error) {
	if api == nil || handler == nil {
		return nil, errors.New("atvv transport: native API and callback are required")
	}
	if closeTimeout <= 0 {
		closeTimeout = defaultTransportCloseTimeout
	}
	t := &Transport{
		api:          api,
		handler:      handler,
		closeTimeout: closeTimeout,
		readyWake:    make(chan struct{}),
	}
	token, err := registerTransportCallback(t)
	if err != nil {
		return nil, err
	}
	t.token = token
	t.handle = api.create(token)
	if t.handle == 0 {
		unregisterTransportCallback(token)
		t.token = 0
		return nil, errors.New("atvv transport: native Create failed")
	}
	return t, nil
}

func (t *Transport) signalReadyLocked() {
	if t.readySignaled {
		return
	}
	t.readySignaled = true
	close(t.readyWake)
}

func (t *Transport) signalCloseLocked() {
	if t.closeWake != nil {
		close(t.closeWake)
		t.closeWake = nil
	}
}

// Ready waits only for the native notification-subscription acknowledgement.
// The subsequent CAPS response is delivered separately as a ControlValue.
func (t *Transport) Ready(ctx context.Context) error {
	if t == nil || ctx == nil {
		return ErrTransportClosed
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		t.mu.Lock()
		if t.closed || t.nativeCloseDone {
			t.mu.Unlock()
			return ErrTransportClosed
		}
		if t.fault != nil {
			err := t.fault
			t.mu.Unlock()
			return err
		}
		if t.readyErr != nil {
			err := t.readyErr
			t.mu.Unlock()
			return err
		}
		if t.ready {
			t.mu.Unlock()
			return nil
		}
		wake := t.readyWake
		t.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}

// RequestStop queues one MIC_CLOSE and waits for the matching stream-ID ACK.
// A timeout faults stop handling so a late ACK cannot satisfy a later request.
func (t *Transport) RequestStop(ctx context.Context, streamID byte) error {
	if t == nil || ctx == nil {
		return ErrTransportClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	if t.closed || t.nativeCloseDone {
		t.mu.Unlock()
		return ErrTransportClosed
	}
	if t.fault != nil {
		err := t.fault
		t.mu.Unlock()
		return err
	}
	if t.stopFault != nil {
		err := t.stopFault
		t.mu.Unlock()
		return err
	}
	if !t.ready {
		t.mu.Unlock()
		return ErrTransportNotReady
	}
	if t.pendingStop != nil {
		t.mu.Unlock()
		return ErrTransportStopBusy
	}
	req := &stopRequest{streamID: streamID, done: make(chan struct{})}
	t.pendingStop = req
	t.mu.Unlock()

	status := t.api.requestStop(t.handle, uint32(streamID))
	if status != nativeStatusOK {
		err := fmt.Errorf("atvv transport: native RequestStop status=%d", status)
		t.finishStopRequest(req, err, true)
		return err
	}

	select {
	case <-req.done:
		return req.err
	case <-ctx.Done():
		t.mu.Lock()
		if req.finished {
			err := req.err
			t.mu.Unlock()
			return err
		}
		err := fmt.Errorf("%w: %v", ErrTransportStopTimeout, ctx.Err())
		if t.pendingStop == req {
			t.pendingStop = nil
		}
		t.stopFault = err
		req.err = err
		req.finished = true
		close(req.done)
		t.mu.Unlock()
		return err
	}
}

func (t *Transport) finishStopRequest(req *stopRequest, err error, fault bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if req.finished {
		return
	}
	if t.pendingStop == req {
		t.pendingStop = nil
	}
	req.err = err
	req.finished = true
	if fault && err != nil && t.stopFault == nil {
		t.stopFault = err
	}
	close(req.done)
}

// Close requests native cancellation and waits for CloseDone plus callback
// quiescence. On timeout the token and owner remain registered, so a later
// Close call retries and lets the native shim escalate its cancellation.
func (t *Transport) Close() error {
	if t == nil {
		return nil
	}
	t.closeMu.Lock()
	defer t.closeMu.Unlock()

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	nativeDone := t.nativeCloseDone
	t.mu.Unlock()

	if !nativeDone {
		status := t.api.close(t.handle)
		if status != nativeStatusOK {
			return fmt.Errorf("atvv transport: native Close status=%d", status)
		}
	}

	timer := time.NewTimer(t.closeTimeout)
	defer timer.Stop()
	for {
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return nil
		}
		if t.nativeCloseDone && t.activeCallbacks == 0 {
			t.closed = true
			t.handler = nil
			t.mu.Unlock()
			unregisterTransportCallback(t.token)
			t.token = 0
			return nil
		}
		wake := t.closeWake
		if wake == nil {
			wake = make(chan struct{})
			t.closeWake = wake
		}
		t.mu.Unlock()
		select {
		case <-timer.C:
			return ErrTransportCloseTimeout
		case <-wake:
		}
	}
}

// deliverNativeEvent is the common C-callback boundary. Bytes aliases the
// native buffer and must not outlive handler(event)'s synchronous call.
func deliverNativeEvent(context uint64, rawKind int32, status int32, pointer unsafe.Pointer, length int32) {
	t := lookupTransportCallback(context)
	if t == nil {
		return
	}
	if length < 0 || length > maxNativeEventBytes || (length > 0 && pointer == nil) {
		t.recordNativeError(fmt.Errorf("%w: payload length=%d", ErrInvalidNativeEvent, length))
		return
	}
	kind := NativeEventKind(rawKind)
	if kind < NativeEventReady || kind > NativeEventCloseDone {
		t.recordNativeError(fmt.Errorf("%w: unknown kind=%d", ErrInvalidNativeEvent, rawKind))
		return
	}
	var bytes []byte
	if length > 0 {
		bytes = unsafe.Slice((*byte)(pointer), int(length))
	}
	t.handleNativeEvent(NativeEvent{Kind: kind, Status: status, Bytes: bytes})
}

func (t *Transport) recordNativeError(err error) {
	t.mu.Lock()
	if t.fault == nil {
		t.fault = err
	}
	if !t.ready {
		t.readyErr = err
	}
	if req := t.pendingStop; req != nil && !req.finished {
		t.pendingStop = nil
		req.err = err
		req.finished = true
		close(req.done)
	}
	t.signalReadyLocked()
	t.mu.Unlock()
}

func (t *Transport) handleNativeEvent(event NativeEvent) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.activeCallbacks++
	handler := t.handler
	t.mu.Unlock()

	var callbackErr error
	if handler != nil {
		callbackErr = callNativeEventHandler(handler, event)
		if callbackErr != nil {
			callbackErr = fmt.Errorf("atvv transport: event callback: %w", callbackErr)
		}
	}

	t.mu.Lock()
	if callbackErr != nil && t.fault == nil {
		t.fault = callbackErr
		if !t.ready {
			t.readyErr = callbackErr
		}
		if req := t.pendingStop; req != nil && !req.finished {
			t.pendingStop = nil
			req.err = callbackErr
			req.finished = true
			close(req.done)
		}
		if !t.ready {
			t.signalReadyLocked()
		}
	}

	switch event.Kind {
	case NativeEventReady:
		if event.Status != nativeStatusOK {
			err := fmt.Errorf("atvv transport: notification setup status=%d", event.Status)
			if t.readyErr == nil {
				t.readyErr = err
			}
		} else if t.fault == nil {
			t.ready = true
		}
		t.signalReadyLocked()
	case NativeEventFailure:
		err := nativeFailure("transport", event.Status)
		if t.fault == nil {
			t.fault = err
		}
		if !t.ready && t.readyErr == nil {
			t.readyErr = err
		}
		if req := t.pendingStop; req != nil && !req.finished {
			t.pendingStop = nil
			req.err = err
			req.finished = true
			close(req.done)
		}
		t.signalReadyLocked()
	case NativeEventStopWriteAck:
		if len(event.Bytes) != 1 {
			err := fmt.Errorf("%w: stop ack length=%d", ErrInvalidNativeEvent, len(event.Bytes))
			if t.fault == nil {
				t.fault = err
			}
			if req := t.pendingStop; req != nil && !req.finished {
				t.pendingStop = nil
				req.err = err
				req.finished = true
				close(req.done)
			}
		} else if req := t.pendingStop; req != nil && event.Bytes[0] == req.streamID {
			var err error
			if event.Status != nativeStatusOK && event.Status != nativeStatusStopAlreadyObserved {
				err = fmt.Errorf("atvv transport: native stop acknowledgement status=%d", event.Status)
			}
			t.pendingStop = nil
			req.err = err
			req.finished = true
			if err != nil && t.stopFault == nil {
				t.stopFault = err
			}
			close(req.done)
		}
	case NativeEventCloseDone:
		if event.Status == nativeStatusOK {
			t.nativeCloseDone = true
			t.signalReadyLocked()
			if req := t.pendingStop; req != nil && !req.finished {
				t.pendingStop = nil
				req.err = ErrTransportClosed
				req.finished = true
				close(req.done)
			}
		} else if t.fault == nil {
			t.fault = fmt.Errorf("atvv transport: native close completion status=%d", event.Status)
		}
	}
	if t.fault != nil && !t.ready {
		if t.readyErr == nil {
			t.readyErr = t.fault
		}
		t.signalReadyLocked()
	}
	t.activeCallbacks--
	t.signalCloseLocked()
	t.mu.Unlock()
}

func callNativeEventHandler(handler NativeEventHandler, event NativeEvent) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("callback panic: %v", recovered)
		}
	}()
	return handler(event)
}

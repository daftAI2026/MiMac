package processinput

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"mimac.local/mimac/internal/adapters/audiooutput"
)

type fakeOutput struct {
	events     *[]string
	closeErrs  []error
	prepareErr error
	writeErr   error
	drainErr   error
	prepares   int
	writes     int
	drains     int
	closes     int
}

func (f *fakeOutput) Prepare(context.Context) error {
	*f.events = append(*f.events, "queue.prepare")
	f.prepares++
	return f.prepareErr
}

func (f *fakeOutput) TryWrite(pcm []int16) error {
	*f.events = append(*f.events, "queue.write")
	f.writes++
	return f.writeErr
}

func (f *fakeOutput) Drain(context.Context) error {
	*f.events = append(*f.events, "queue.drain")
	f.drains++
	return f.drainErr
}

func (f *fakeOutput) Close() error {
	*f.events = append(*f.events, "queue.close")
	f.closes++
	if len(f.closeErrs) == 0 {
		return nil
	}
	err := f.closeErrs[0]
	f.closeErrs = f.closeErrs[1:]
	return err
}

type fakeEndpoint struct {
	events    *[]string
	uid       string
	closeErrs []error
	closes    int
}

func (f *fakeEndpoint) InputUID() string { return f.uid }

func (f *fakeEndpoint) Close() error {
	*f.events = append(*f.events, "endpoint.close")
	f.closes++
	if len(f.closeErrs) == 0 {
		return nil
	}
	err := f.closeErrs[0]
	f.closeErrs = f.closeErrs[1:]
	return err
}

func openTestSink(t *testing.T, queue *fakeOutput, input *fakeEndpoint, wait func(context.Context, string) error, endpointErr error) (*Sink, *[]string) {
	t.Helper()
	events := []string{}
	queue.events = &events
	if input != nil {
		input.events = &events
	}
	sink, err := openWith(
		audiooutput.Config{DeviceUID: "test-output", SampleRate: 16000}, "/tmp/process-tap.dylib", wait,
		func(config audiooutput.Config) (output, error) { return queue, nil },
		func(path string) (endpoint, error) {
			if path != "/tmp/process-tap.dylib" {
				t.Fatalf("endpoint path = %q", path)
			}
			events = append(events, "endpoint.open")
			return input, endpointErr
		},
	)
	if err != nil {
		t.Fatalf("openWith: %v", err)
	}
	return sink, &events
}

func TestPrepareParksQueueBeforeOpeningEndpointWithoutWritingPCM(t *testing.T) {
	queue := &fakeOutput{}
	input := &fakeEndpoint{uid: "input.exact.uid"}
	ctx := context.Background()
	var gotContext context.Context
	var gotUID string
	sink, eventLog := openTestSink(t, queue, input, func(got context.Context, uid string) error {
		gotContext, gotUID = got, uid
		return nil
	}, nil)

	if sink.InputUID() != "" {
		t.Fatal("unprepared UID exposed")
	}
	if err := sink.Prepare(ctx); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if sink.InputUID() != "input.exact.uid" {
		t.Fatal("prepared UID missing")
	}
	if got := *eventLog; !reflect.DeepEqual(got, []string{"queue.prepare", "endpoint.open"}) {
		t.Fatalf("prepare events = %v", got)
	}
	if queue.writes != 0 {
		t.Fatalf("Prepare submitted %d PCM writes", queue.writes)
	}
	if err := sink.TryWrite([]int16{7, -9}); !errors.Is(err, ErrReceiverNotReady) {
		t.Fatalf("TryWrite before receiver acceptance = %v", err)
	}
	if err := sink.Drain(ctx); !errors.Is(err, ErrReceiverNotReady) {
		t.Fatalf("Drain before receiver acceptance = %v", err)
	}
	if queue.writes != 0 || queue.drains != 0 {
		t.Fatalf("early write/drain reached output: writes=%d drains=%d", queue.writes, queue.drains)
	}

	receiverContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sink.WaitReceiver(receiverContext); err != nil {
		t.Fatalf("WaitReceiver: %v", err)
	}
	if gotContext != receiverContext || gotUID != "input.exact.uid" {
		t.Fatalf("receiver got context=%v uid=%q", gotContext == receiverContext, gotUID)
	}
	if err := sink.TryWrite([]int16{7, -9}); err != nil {
		t.Fatalf("TryWrite after receiver acceptance: %v", err)
	}
	if err := sink.Drain(ctx); err != nil {
		t.Fatalf("Drain after receiver acceptance: %v", err)
	}
	if queue.writes != 1 || queue.drains != 1 {
		t.Fatalf("accepted write/drain counts = %d/%d", queue.writes, queue.drains)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if sink.InputUID() != "" {
		t.Fatal("closed UID exposed")
	}
}

func TestOpenRetainsPartialOutputForCloseRetry(t *testing.T) {
	var events []string
	closeErr := errors.New("first output close failed")
	queue := &fakeOutput{events: &events, closeErrs: []error{closeErr}}
	sink, err := openWith(
		audiooutput.Config{DeviceUID: "test-output", SampleRate: 16000}, "/tmp/process-tap.dylib",
		func(context.Context, string) error { return nil },
		func(audiooutput.Config) (output, error) { return queue, errors.New("partial output creation") },
		func(string) (endpoint, error) {
			t.Fatal("endpoint factory called after partial output failure")
			return nil, nil
		},
	)
	if sink == nil || err == nil {
		t.Fatalf("partial Open = (%v, %v), want retained sink and error", sink, err)
	}
	if err := sink.Prepare(context.Background()); !errors.Is(err, ErrFaulted) {
		t.Fatalf("Prepare after partial Open = %v", err)
	}
	if err := sink.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("first Close = %v, want output close error", err)
	}
	if len(events) != 1 || events[0] != "queue.close" {
		t.Fatalf("events after failed output close = %v", events)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close retry: %v", err)
	}
	if queue.closes != 2 {
		t.Fatalf("output close calls = %d, want 2", queue.closes)
	}
}

func TestPartialEndpointCleanupRetriesAfterQueueClose(t *testing.T) {
	openErr := errors.New("endpoint created partially")
	queueCloseErr := errors.New("queue still owns resources")
	endpointCloseErr := errors.New("endpoint still owns resources")
	queue := &fakeOutput{closeErrs: []error{queueCloseErr}}
	input := &fakeEndpoint{uid: "partial.uid", closeErrs: []error{endpointCloseErr}}
	sink, events := openTestSink(t, queue, input, func(context.Context, string) error { return nil }, openErr)
	if err := sink.Prepare(context.Background()); !errors.Is(err, openErr) {
		t.Fatalf("Prepare error = %v", err)
	}
	if err := sink.Close(); !errors.Is(err, queueCloseErr) {
		t.Fatalf("first Close = %v", err)
	}
	if input.closes != 0 {
		t.Fatal("endpoint was closed after output queue close failed")
	}
	if err := sink.Close(); !errors.Is(err, endpointCloseErr) {
		t.Fatalf("second Close = %v", err)
	}
	if got := *events; !reflect.DeepEqual(got, []string{"queue.prepare", "endpoint.open", "queue.close", "queue.close", "endpoint.close"}) {
		t.Fatalf("events after endpoint close failure = %v", got)
	}
	if input.closes != 1 {
		t.Fatalf("endpoint close calls = %d, want 1", input.closes)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("endpoint Close retry: %v", err)
	}
	if input.closes != 2 || queue.closes != 2 {
		t.Fatalf("close calls after retry: queue=%d endpoint=%d", queue.closes, input.closes)
	}
}

func TestCloseDuringReceiverWaitRetainsResourcesAndRejectsLateSuccess(t *testing.T) {
	var events []string
	queue := &fakeOutput{events: &events}
	input := &fakeEndpoint{events: &events, uid: "input.late.uid"}
	waitEntered := make(chan string, 1)
	releaseWait := make(chan struct{})
	sink, _ := openTestSink(t, queue, input, func(_ context.Context, uid string) error {
		waitEntered <- uid
		<-releaseWait
		return nil
	}, nil)
	if err := sink.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- sink.WaitReceiver(context.Background()) }()
	if got := <-waitEntered; got != "input.late.uid" {
		t.Fatalf("wait UID = %q", got)
	}
	if err := sink.Close(); !errors.Is(err, ErrBusy) {
		t.Fatalf("Close during receiver wait = %v, want ErrBusy", err)
	}
	if queue.closes != 0 || input.closes != 0 {
		t.Fatalf("resources closed during callback: queue=%d endpoint=%d", queue.closes, input.closes)
	}
	close(releaseWait)
	if err := <-waitDone; !errors.Is(err, ErrClosed) {
		t.Fatalf("late successful WaitReceiver = %v, want ErrClosed", err)
	}
	if err := sink.TryWrite([]int16{1}); !errors.Is(err, ErrClosed) {
		t.Fatalf("late receiver was allowed to write: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close retry: %v", err)
	}
	if queue.closes != 1 || input.closes != 1 {
		t.Fatalf("final close calls: queue=%d endpoint=%d", queue.closes, input.closes)
	}
}

func TestOpenRejectsInvalidInputsBeforeOutputFactory(t *testing.T) {
	called := false
	makeOutput := func(audiooutput.Config) (output, error) {
		called = true
		return &fakeOutput{}, nil
	}
	makeEndpoint := func(string) (endpoint, error) { return nil, nil }
	if sink, err := openWith(audiooutput.Config{}, "relative.dylib", func(context.Context, string) error { return nil }, makeOutput, makeEndpoint); sink != nil || !errors.Is(err, ErrInvalidLibraryPath) {
		t.Fatalf("invalid path Open = (%v, %v)", sink, err)
	}
	if sink, err := openWith(audiooutput.Config{}, "/tmp/process-tap\x00.dylib", func(context.Context, string) error { return nil }, makeOutput, makeEndpoint); sink != nil || !errors.Is(err, ErrInvalidLibraryPath) {
		t.Fatalf("NUL path Open = (%v, %v)", sink, err)
	}
	if sink, err := openWith(audiooutput.Config{}, "/tmp/process-tap.dylib", nil, makeOutput, makeEndpoint); sink != nil || !errors.Is(err, ErrReceiverRequired) {
		t.Fatalf("nil receiver Open = (%v, %v)", sink, err)
	}
	if called {
		t.Fatal("output factory called before validating constructor inputs")
	}
}

func TestCanceledReceiverWaitDoesNotOpenWriting(t *testing.T) {
	var events []string
	queue := &fakeOutput{events: &events}
	input := &fakeEndpoint{events: &events, uid: "input.cancel.uid"}
	ctx, cancel := context.WithCancel(context.Background())
	sink, _ := openTestSink(t, queue, input, func(got context.Context, uid string) error {
		if got != ctx || uid != input.uid {
			t.Fatalf("receiver args context=%v uid=%q", got == ctx, uid)
		}
		cancel()
		return nil
	}, nil)
	if err := sink.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := sink.WaitReceiver(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitReceiver after cancellation = %v", err)
	}
	if err := sink.TryWrite([]int16{1}); !errors.Is(err, ErrReceiverNotReady) {
		t.Fatalf("TryWrite after canceled wait = %v", err)
	}
	if queue.writes != 0 {
		t.Fatalf("canceled wait submitted %d writes", queue.writes)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestReceiverFailureDoesNotEnableOutput(t *testing.T) {
	var events []string
	queue := &fakeOutput{events: &events}
	input := &fakeEndpoint{events: &events, uid: "input.failed.uid"}
	waitErr := errors.New("receiver rejected endpoint")
	sink, _ := openTestSink(t, queue, input, func(context.Context, string) error { return waitErr }, nil)
	if err := sink.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := sink.WaitReceiver(context.Background()); !errors.Is(err, waitErr) {
		t.Fatalf("WaitReceiver error = %v", err)
	}
	if err := sink.TryWrite([]int16{1}); !errors.Is(err, ErrReceiverNotReady) {
		t.Fatalf("TryWrite after receiver rejection = %v", err)
	}
	if err := sink.Drain(context.Background()); !errors.Is(err, ErrReceiverNotReady) {
		t.Fatalf("Drain after receiver rejection = %v", err)
	}
	if queue.writes != 0 || queue.drains != 0 {
		t.Fatalf("rejected receiver reached output: writes=%d drains=%d", queue.writes, queue.drains)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

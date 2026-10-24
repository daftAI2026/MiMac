package atvv

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type fakeStreamTransport struct {
	mu           sync.Mutex
	stopRequests []byte
	stopQueued   chan struct{}
	closeErr     error
	closeCalls   int
}

func newFakeStreamTransport() *fakeStreamTransport {
	return &fakeStreamTransport{stopQueued: make(chan struct{}, 4)}
}

func (t *fakeStreamTransport) RequestStop(ctx context.Context, streamID byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	t.stopRequests = append(t.stopRequests, streamID)
	t.mu.Unlock()
	select {
	case t.stopQueued <- struct{}{}:
	default:
	}
	return nil
}

func (t *fakeStreamTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closeCalls++
	return t.closeErr
}

func (t *fakeStreamTransport) snapshot() (stopRequests []byte, closeCalls int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.stopRequests...), t.closeCalls
}

func newArmedSource(t *testing.T, key StreamKey, transport StreamTransport) *Source {
	t.Helper()
	source, err := NewSource(key, transport)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Arm(key, []byte{0x0B, 0x01, 0x00, 0x00, 0x03, 0x00, 0x78, 0x00, 0x00}); err != nil {
		t.Fatal(err)
	}
	return source
}

func start16k(t *testing.T, source *Source, key StreamKey) {
	t.Helper()
	if err := source.HandleControl(key, []byte{0x04, 0x03, 0x02, 0x0B}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceRejectsUnarmedAndStaleSessionEvents(t *testing.T) {
	key := StreamKey{Generation: 7, Session: 12}
	transport := newFakeStreamTransport()
	source, err := NewSource(key, transport)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.HandleAudio(key, []byte{0x70}); !errors.Is(err, ErrSourceUnarmed) {
		t.Fatalf("unarmed audio error = %v, want ErrSourceUnarmed", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	// A retired callback has a different key and cannot alter this source.
	key2 := StreamKey{Generation: 7, Session: 13}
	source2 := newArmedSource(t, key2, newFakeStreamTransport())
	if err := source2.HandleControl(key, []byte{0x04, 0x03, 0x02, 0x0B}); !errors.Is(err, ErrStaleSession) {
		t.Fatalf("stale control error = %v, want ErrStaleSession", err)
	}
	start16k(t, source2, key2)
	if err := source2.HandleAudio(key, []byte{0x70}); !errors.Is(err, ErrStaleSession) {
		t.Fatalf("stale audio error = %v, want ErrStaleSession", err)
	}
	if err := source2.HandleAudio(key2, []byte{0x70}); err != nil {
		t.Fatalf("current-session audio was rejected after stale callback: %v", err)
	}
	if err := source2.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSourceReadyUsesAudioStartCodecAndDoesNotOpenMicrophone(t *testing.T) {
	key := StreamKey{Generation: 3, Session: 5}
	transport := newFakeStreamTransport()
	source := newArmedSource(t, key, transport)
	if err := source.HandleControl(key, []byte{0x08}); err != nil {
		t.Fatal(err)
	}
	start16k(t, source, key)
	rate, err := source.Ready(context.Background())
	if err != nil || rate != 16000 {
		t.Fatalf("Ready() = (%d, %v), want (16000, nil)", rate, err)
	}
	if err := source.HandleAudio(key, []byte{0x70}); err != nil {
		t.Fatal(err)
	}
	pcm := make([]int16, 2)
	n, err := source.Read(context.Background(), pcm)
	if err != nil || n != 2 {
		t.Fatalf("Read() = (%d, %v), want 2 samples", n, err)
	}
	if pcm[0] != 11 || pcm[1] != 13 {
		t.Fatalf("decoded samples = %v, want [11 13] from zero predictor/index", pcm)
	}
	requests, _ := transport.snapshot()
	if len(requests) != 0 {
		t.Fatalf("StartSearch triggered a host stop/open transport action: %v", requests)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSourceAppliesPreStartSyncAndKeepsPacketOrder(t *testing.T) {
	key := StreamKey{Generation: 1, Session: 9}
	source := newArmedSource(t, key, newFakeStreamTransport())
	if err := source.HandleControl(key, []byte{0x0A, 0x02, 0x00, 0x2A, 0xFF, 0xFF, 0x05}); err != nil {
		t.Fatal(err)
	}
	start16k(t, source, key)
	if err := source.HandleAudio(key, []byte{0x00}); err != nil {
		t.Fatal(err)
	}
	if err := source.HandleAudio(key, []byte{0x70}); err != nil {
		t.Fatal(err)
	}

	first := make([]int16, 1)
	n, err := source.Read(context.Background(), first)
	if err != nil || n != 1 || first[0] != 0 {
		t.Fatalf("first partial read = (%d, %v, %v), want (1, nil, [0])", n, err, first)
	}
	second := make([]int16, 3)
	n, err = source.Read(context.Background(), second)
	if err != nil || n != 3 {
		t.Fatalf("remaining read = (%d, %v), want 3 samples", n, err)
	}
	if second[0] != 1 || second[1] != 19 || second[2] != 21 {
		t.Fatalf("remaining samples = %v, want [1 19 21]", second)
	}
	if err := source.HandleControl(key, []byte{0x0A, 0x02, 0x00, 0x2B, 0x00, 0x0A, 0x00}); err != nil {
		t.Fatal(err)
	}
	if err := source.HandleAudio(key, []byte{0x11}); err != nil {
		t.Fatal(err)
	}
	postSync := make([]int16, 2)
	n, err = source.Read(context.Background(), postSync)
	if err != nil || n != 2 || postSync[0] != 11 || postSync[1] != 12 {
		t.Fatalf("post-START SYNC Read() = (%d, %v, %v), want (2, nil, [11 12])", n, err, postSync)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSourceStopBeforeHostReleaseDrainsThenReturnsEOF(t *testing.T) {
	key := StreamKey{Generation: 2, Session: 4}
	transport := newFakeStreamTransport()
	source := newArmedSource(t, key, transport)
	start16k(t, source, key)
	if err := source.HandleAudio(key, []byte{0x70}); err != nil {
		t.Fatal(err)
	}
	if err := source.HandleControl(key, []byte{0x00, 0x02}); err != nil {
		t.Fatal(err)
	}

	pcm := make([]int16, 2)
	n, err := source.Read(context.Background(), pcm)
	if err != nil || n != 2 {
		t.Fatalf("queued packet read = (%d, %v), want 2 samples", n, err)
	}
	readDone := make(chan error, 1)
	go func() {
		_, readErr := source.Read(context.Background(), pcm)
		readDone <- readErr
	}()
	select {
	case err := <-readDone:
		t.Fatalf("Read returned before host Stop request: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	if err := source.Stop(context.Background()); err != nil {
		t.Fatalf("Stop after remote STOP: %v", err)
	}
	select {
	case err := <-readDone:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("Read after STOP/Stop = %v, want EOF", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Read did not finish after the STOP boundary")
	}
	requests, _ := transport.snapshot()
	if len(requests) != 0 {
		t.Fatalf("MIC_CLOSE was sent after remote STOP: %v", requests)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSourceStopBroadcastWakesMultipleReadersAndStopWaiter(t *testing.T) {
	key := StreamKey{Generation: 2, Session: 5}
	transport := newFakeStreamTransport()
	source := newArmedSource(t, key, transport)
	start16k(t, source, key)

	readDone := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := source.Read(context.Background(), make([]int16, 320))
			readDone <- err
		}()
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- source.Stop(context.Background()) }()
	select {
	case <-transport.stopQueued:
	case <-time.After(time.Second):
		t.Fatal("Stop did not request MIC_CLOSE")
	}
	if err := source.HandleControl(key, []byte{0x00, 0x02}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop after AUDIO_STOP = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("AUDIO_STOP did not wake Stop waiter")
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-readDone:
			if !errors.Is(err, io.EOF) {
				t.Fatalf("reader %d returned %v, want EOF", i, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("AUDIO_STOP did not wake reader %d", i)
		}
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSourceCloseBroadcastWakesReadyAndReadWaiters(t *testing.T) {
	key := StreamKey{Generation: 2, Session: 6}
	source, err := NewSource(key, newFakeStreamTransport())
	if err != nil {
		t.Fatal(err)
	}
	readyDone := make(chan error, 1)
	readDone := make(chan error, 1)
	go func() {
		_, err := source.Ready(context.Background())
		readyDone <- err
	}()
	go func() {
		_, err := source.Read(context.Background(), make([]int16, 320))
		readDone <- err
	}()
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-readyDone:
		if !errors.Is(err, ErrSourceClosed) {
			t.Fatalf("Ready after Close = %v, want ErrSourceClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not wake Ready waiter")
	}
	select {
	case err := <-readDone:
		if !errors.Is(err, ErrSourceClosed) {
			t.Fatalf("Read after Close = %v, want ErrSourceClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not wake Read waiter")
	}
}

func TestSourceStopRequestsOneCloseAndWaitsForRemoteStop(t *testing.T) {
	key := StreamKey{Generation: 4, Session: 8}
	transport := newFakeStreamTransport()
	source := newArmedSource(t, key, transport)
	start16k(t, source, key)
	if err := source.HandleAudio(key, []byte{0x70}); err != nil {
		t.Fatal(err)
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- source.Stop(context.Background()) }()
	select {
	case <-transport.stopQueued:
	case <-time.After(time.Second):
		t.Fatal("Stop did not request MIC_CLOSE")
	}
	requests, _ := transport.snapshot()
	if len(requests) != 1 || requests[0] != 0x0B {
		t.Fatalf("stop requests = %v, want one close for stream 0x0b", requests)
	}
	select {
	case err := <-stopDone:
		t.Fatalf("Stop returned before AUDIO_STOP: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	if err := source.HandleControl(key, []byte{0x00, 0x02}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop after AUDIO_STOP: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not wake after AUDIO_STOP")
	}
	if err := source.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	requests, _ = transport.snapshot()
	if len(requests) != 1 {
		t.Fatalf("repeated Stop sent another close: %v", requests)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSourceStopCancellationDoesNotDuplicateClose(t *testing.T) {
	key := StreamKey{Generation: 4, Session: 9}
	transport := newFakeStreamTransport()
	source := newArmedSource(t, key, transport)
	start16k(t, source, key)
	ctx, cancel := context.WithCancel(context.Background())
	stopDone := make(chan error, 1)
	go func() { stopDone <- source.Stop(ctx) }()
	select {
	case <-transport.stopQueued:
	case <-time.After(time.Second):
		t.Fatal("Stop did not request MIC_CLOSE")
	}
	cancel()
	select {
	case err := <-stopDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Stop = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not honor cancellation")
	}
	if err := source.HandleControl(key, []byte{0x00, 0x02}); err != nil {
		t.Fatal(err)
	}
	if err := source.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	requests, _ := transport.snapshot()
	if len(requests) != 1 {
		t.Fatalf("cancelled Stop caused duplicate MIC_CLOSE: %v", requests)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSourceRejectsLateAudioAndSecondStream(t *testing.T) {
	key := StreamKey{Generation: 4, Session: 10}
	source := newArmedSource(t, key, newFakeStreamTransport())
	start16k(t, source, key)
	if err := source.HandleControl(key, []byte{0x00}); err != nil {
		t.Fatal(err)
	}
	if err := source.HandleAudio(key, []byte{0x70}); !errors.Is(err, ErrAudioAfterStop) {
		t.Fatalf("late audio error = %v, want ErrAudioAfterStop", err)
	}
	if _, err := source.Read(context.Background(), make([]int16, 2)); !errors.Is(err, ErrAudioAfterStop) {
		t.Fatalf("Read after late audio = %v, want ErrAudioAfterStop", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	second := newArmedSource(t, key, newFakeStreamTransport())
	start16k(t, second, key)
	if err := second.HandleControl(key, []byte{0x04, 0x03, 0x02, 0x0C}); !errors.Is(err, ErrUnexpectedStream) {
		t.Fatalf("second stream error = %v, want ErrUnexpectedStream", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSourceQueueOverflowAtFixedSampleCapacity(t *testing.T) {
	for _, tc := range []struct {
		name        string
		packetSize  int
		packetCount int
	}{
		{name: "observed 120-byte notifications", packetSize: observedAudioPacketBytes, packetCount: 64},
		{name: "larger arbitrary notifications", packetSize: maxAudioPacket, packetCount: 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := StreamKey{Generation: 5, Session: 11}
			source := newArmedSource(t, key, newFakeStreamTransport())
			start16k(t, source, key)
			packet := make([]byte, tc.packetSize)
			for i := 0; i < tc.packetCount; i++ {
				if err := source.HandleAudio(key, packet); err != nil {
					t.Fatalf("packet %d rejected before capacity: %v", i, err)
				}
			}
			if err := source.HandleAudio(key, []byte{0x00}); !errors.Is(err, ErrSourceQueueFull) {
				t.Fatalf("overflow error = %v, want ErrSourceQueueFull", err)
			}
			if _, err := source.Read(context.Background(), make([]int16, 4)); !errors.Is(err, ErrSourceQueueFull) {
				t.Fatalf("Read after overflow = %v, want ErrSourceQueueFull", err)
			}
			if err := source.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSourcePCMQueueWrapsAcrossNotificationBoundary(t *testing.T) {
	key := StreamKey{Generation: 8, Session: 15}
	source := newArmedSource(t, key, newFakeStreamTransport())
	start16k(t, source, key)
	source.head = len(source.pcm) - 1
	if err := source.HandleAudio(key, []byte{0x70}); err != nil {
		t.Fatal(err)
	}
	pcm := make([]int16, 2)
	n, err := source.Read(context.Background(), pcm)
	if err != nil || n != 2 || pcm[0] != 11 || pcm[1] != 13 {
		t.Fatalf("wrapped Read() = (%d, %v, %v), want (2, nil, [11 13])", n, err, pcm)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSourceHonorsCancellationAndRetriesFailedClose(t *testing.T) {
	key := StreamKey{Generation: 6, Session: 14}
	transport := newFakeStreamTransport()
	source, err := NewSource(key, transport)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Read(ctx, make([]int16, 2)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Read = %v, want context.Canceled", err)
	}
	if _, err := source.Read(context.Background(), nil); !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("empty Read = %v, want io.ErrShortBuffer", err)
	}

	if err := source.Arm(key, []byte{0x0B, 0x01, 0x00, 0x00, 0x03, 0x00, 0x78, 0x00, 0x00}); err != nil {
		t.Fatal(err)
	}
	start16k(t, source, key)
	transport.closeErr = errors.New("temporary callback barrier failure")
	if err := source.Close(); err == nil {
		t.Fatal("Close hid a failed transport barrier")
	}
	if err := source.HandleAudio(key, []byte{0x70}); err != nil {
		t.Fatalf("failed Close discarded the still-live source: %v", err)
	}
	transport.closeErr = nil
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.HandleAudio(key, []byte{0x70}); !errors.Is(err, ErrSourceClosed) {
		t.Fatalf("late event after successful Close = %v, want ErrSourceClosed", err)
	}
	if _, err := source.Read(context.Background(), make([]int16, 2)); !errors.Is(err, ErrSourceClosed) {
		t.Fatalf("Read after unconfirmed stream end = %v, want ErrSourceClosed", err)
	}
	_, closes := transport.snapshot()
	if closes != 2 {
		t.Fatalf("transport Close calls = %d, want retry after failed barrier", closes)
	}
}

func TestSourceReadyCancellationAndEightKCodec(t *testing.T) {
	key := StreamKey{Generation: 9, Session: 16}
	source := newArmedSource(t, key, newFakeStreamTransport())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Ready(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Ready = %v, want context.Canceled", err)
	}
	if err := source.HandleControl(key, []byte{0x04, 0x03, 0x01, 0x2A}); err != nil {
		t.Fatal(err)
	}
	rate, err := source.Ready(context.Background())
	if err != nil || rate != 8000 {
		t.Fatalf("8k Ready() = (%d, %v), want (8000, nil)", rate, err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
}

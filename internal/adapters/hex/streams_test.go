package hex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testOwnerToken = "hex_abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

func TestStreamSendsPCMReadsPartialsAndReturnsFinal(t *testing.T) {
	var mu sync.Mutex
	var gotAudio []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/streams":
			if r.Header.Get("Content-Type") != "application/json" || r.ContentLength <= 0 {
				t.Errorf("create request headers/content length = %q/%d", r.Header.Get("Content-Type"), r.ContentLength)
			}
			var request createStreamRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode create request: %v", err)
			}
			if request.SampleRate != 16000 || request.SampleFormat != "s16le" {
				t.Errorf("create request = %+v", request)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":9,"ownerToken":"` + testOwnerToken + `","sampleRate":16000,"sampleFormat":"s16le","partials":true,"leaseMs":10000}`))
		case r.Method == http.MethodPost && r.URL.Path == "/streams/9/audio":
			if r.Header.Get("X-Hex-Stream-Token") != testOwnerToken || r.Header.Get("Content-Type") != "application/octet-stream" || r.ContentLength <= 0 {
				t.Errorf("audio request headers/content length = %q/%q/%d", r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.ContentLength)
			}
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			gotAudio = append([]byte(nil), body...)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/streams/9/events":
			if r.Header.Get("X-Hex-Stream-Token") != testOwnerToken {
				http.Error(w, "missing owner", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			_, _ = io.WriteString(w, "data: {\"type\":\"ready\",\"id\":9,\"partials\":true}\n\n")
			_, _ = io.WriteString(w, "data: {\"type\":\"partial\",\"segment\":0,\"stable\":\"hello\",\"tentative\":\" there\"}\n\n")
			flusher.Flush()
			_, _ = io.WriteString(w, "data: {\"type\":\"final\",\"text\":\"hello there\",\"segments\":[],\"durationMs\":1000}\n\n")
		case r.Method == http.MethodPost && r.URL.Path == "/streams/9/finish":
			if r.Header.Get("X-Hex-Stream-Token") != testOwnerToken || r.ContentLength != 0 {
				t.Errorf("finish owner/content length = %q/%d", r.Header.Get("X-Hex-Stream-Token"), r.ContentLength)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"hello there","segments":[],"durationMs":1000}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client := clientForServer(server)
	stream, err := client.CreateStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stream.ID() != 9 || !stream.Partials() {
		t.Fatalf("stream id/partials = %d/%t", stream.ID(), stream.Partials())
	}
	if err := stream.WritePCM(context.Background(), []int16{0x1234, -2}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if got := string(gotAudio); got != string([]byte{0x34, 0x12, 0xfe, 0xff}) {
		mu.Unlock()
		t.Fatalf("audio payload = %v", gotAudio)
	}
	mu.Unlock()

	events := make(chan Event, 4)
	eventErr := make(chan error, 1)
	eventCtx, cancelEvents := context.WithCancel(context.Background())
	defer cancelEvents()
	go func() { eventErr <- stream.ReadEvents(eventCtx, func(event Event) { events <- event }) }()
	seenReady, seenPartial, seenFinal := false, false, false
	readComplete := false
	deadline := time.After(time.Second)
	for !(seenReady && seenPartial && seenFinal) {
		select {
		case event := <-events:
			switch event.Type {
			case "ready":
				seenReady = true
			case "partial":
				seenPartial = event.PartialText() == "hello there"
			case "final":
				seenFinal = event.Text == "hello there"
			}
		case err := <-eventErr:
			if err != nil {
				t.Fatal(err)
			}
			readComplete = true
		case <-deadline:
			t.Fatal("timed out waiting for SSE events")
		}
	}
	if !readComplete {
		select {
		case err := <-eventErr:
			if err != nil {
				t.Fatal(err)
			}
		case <-deadline:
			t.Fatal("timed out waiting for SSE reader completion")
		}
	}
	if !seenReady || !seenPartial || !seenFinal {
		t.Fatalf("events ready/partial/final = %t/%t/%t", seenReady, seenPartial, seenFinal)
	}

	text, err := stream.Finish(context.Background())
	if err != nil || text != "hello there" {
		t.Fatalf("finish = %q, %v", text, err)
	}
}

func TestStreamCancelSendsBothAuthenticationTokens(t *testing.T) {
	var create, cancel bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/streams" {
			create = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":7,"ownerToken":"`+testOwnerToken+`","sampleRate":16000,"sampleFormat":"s16le","partials":false}`)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/streams/7/cancel" {
			cancel = r.Header.Get("X-Hex-Stream-Token") == testOwnerToken && r.ContentLength == 0
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	stream, err := clientForServer(server).CreateStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !create || !cancel {
		t.Fatalf("create/cancel auth flow = %t/%t", create, cancel)
	}
}

func TestFinishTimeoutIsDistinguishedForSSEContinuation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/streams" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":5,"ownerToken":"`+testOwnerToken+`","sampleRate":16000,"sampleFormat":"s16le","partials":true}`)
			return
		}
		if r.URL.Path == "/streams/5/finish" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = io.WriteString(w, `{"code":"stream-finish-timeout"}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	stream, err := clientForServer(server).CreateStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Finish(context.Background()); !errors.Is(err, ErrFinishTimeout) {
		t.Fatalf("finish error = %v", err)
	}
}

func TestWritePCMRejectsEmptyAndOversizedAudio(t *testing.T) {
	stream := &Stream{client: &Client{baseURL: "http://127.0.0.1:1", token: testToken, http: http.DefaultClient}, id: 1, token: testOwnerToken}
	if err := stream.WritePCM(context.Background(), nil); !errors.Is(err, ErrInvalidPCM) {
		t.Fatalf("empty PCM error = %v", err)
	}
	if err := stream.WritePCM(context.Background(), make([]int16, streamAudioLimit/2+1)); !errors.Is(err, ErrInvalidPCM) {
		t.Fatalf("oversized PCM error = %v", err)
	}
}

func TestSSERejectsMalformedPayload(t *testing.T) {
	if err := emitSSE([]string{"not-json"}, nil); err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("malformed payload error = %v", err)
	}
	if err := emitSSE([]string{`{"text":"missing type"}`}, nil); err == nil || !strings.Contains(err.Error(), "omitted event type") {
		t.Fatalf("missing event type error = %v", err)
	}
}

func TestServiceUnavailableModelCodesAreMappedWithoutServerText(t *testing.T) {
	for _, code := range []string{"model-not-loaded", "engine-unavailable"} {
		if err := apiCodeError(http.StatusServiceUnavailable, code); !errors.Is(err, ErrModelUnavailable) {
			t.Fatalf("status/code %d/%q mapped to %v", http.StatusServiceUnavailable, code, err)
		}
	}
	const secret = "untrusted server detail and token"
	err := apiError(&http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       io.NopCloser(strings.NewReader(`{"code":"unknown-private-code","message":"` + secret + `"}`)),
	})
	if !errors.Is(err, ErrRequestRejected) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "unknown-private-code") {
		t.Fatalf("unrecognized server payload escaped into error: %v", err)
	}
}

func TestSSEErrorCodesAreAllowlistedAndMessagesStayPrivate(t *testing.T) {
	event := Event{Type: "error", Code: "engine-unavailable", Message: "do not surface"}
	if err := event.Err(); !errors.Is(err, ErrModelUnavailable) || strings.Contains(err.Error(), event.Message) {
		t.Fatalf("model error = %v", err)
	}
	event = Event{Type: "error", Code: "private-server-code", Message: "private detail"}
	if err := event.Err(); !errors.Is(err, ErrStreamFailed) || strings.Contains(err.Error(), event.Code) || strings.Contains(err.Error(), event.Message) {
		t.Fatalf("unknown SSE error = %v", err)
	}
}

func clientForServer(server *httptest.Server) *Client {
	port := server.Listener.Addr().(*net.TCPAddr).Port
	return newClient(Discovery{Port: uint16(port), Token: testToken, APIVersion: apiVersion, PID: 1}, server.Client())
}

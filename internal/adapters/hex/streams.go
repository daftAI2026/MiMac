package hex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

const (
	streamSampleRate = 16000
	streamAudioLimit = 256 * 1024
	maxEventLine     = 256 * 1024
	maxFinalResponse = 1024 * 1024
)

// Stream is one Hex-owned external PCM stream. Its owner token never leaves
// this adapter except in the required request header.
type Stream struct {
	client   *Client
	id       uint64
	token    string
	partials bool
}

type createStreamRequest struct {
	SampleFormat string `json:"sampleFormat"`
	SampleRate   int    `json:"sampleRate"`
}

type createStreamResponse struct {
	ID           uint64 `json:"id"`
	OwnerToken   string `json:"ownerToken"`
	SampleRate   int    `json:"sampleRate"`
	SampleFormat string `json:"sampleFormat"`
	Partials     bool   `json:"partials"`
}

// Event is the documented SSE event. Fields not needed by MiMac's interim
// display are intentionally not retained.
type Event struct {
	Type      string `json:"type"`
	Stable    string `json:"stable"`
	Tentative string `json:"tentative"`
	Text      string `json:"text"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

type finishResponse struct {
	Text string `json:"text"`
}

// CreateStream reserves Hex's single external stream slot. It never starts
// model loading or switches the model currently held by dictation.
func (c *Client) CreateStream(ctx context.Context) (*Stream, error) {
	if c == nil || ctx == nil {
		return nil, ErrInvalidDiscovery
	}
	body, err := json.Marshal(createStreamRequest{SampleFormat: "s16le", SampleRate: streamSampleRate})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/streams", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.do(request, "")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return nil, apiError(response)
	}
	var created createStreamResponse
	if err := decodeBounded(response, maxMetadataBytes, &created); err != nil {
		return nil, err
	}
	if created.ID == 0 || !validBearer(created.OwnerToken) || created.SampleRate != streamSampleRate || created.SampleFormat != "s16le" {
		return nil, ErrInvalidDiscovery
	}
	return &Stream{client: c, id: created.ID, token: created.OwnerToken, partials: created.Partials}, nil
}

func (s *Stream) ID() uint64 { return s.id }

func (s *Stream) Partials() bool { return s != nil && s.partials }

// WritePCM posts a complete block of signed little-endian samples. The caller
// chooses bounded chunks; the adapter refuses blocks beyond Hex's HTTP limit.
func (s *Stream) WritePCM(ctx context.Context, pcm []int16) error {
	if s == nil || s.client == nil || s.id == 0 || s.token == "" || ctx == nil {
		return ErrStreamNotReady
	}
	if len(pcm) == 0 || len(pcm)*2 > streamAudioLimit {
		return ErrInvalidPCM
	}
	body := make([]byte, len(pcm)*2)
	for index, value := range pcm {
		binary.LittleEndian.PutUint16(body[index*2:], uint16(value))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.path("audio"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := s.client.do(request, s.token)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return apiError(response)
	}
	return nil
}

// ReadEvents joins one SSE subscription. The API replays completed segments
// before live events and closes the stream after its terminal event.
func (s *Stream) ReadEvents(ctx context.Context, receive func(Event)) error {
	if s == nil || s.client == nil || s.id == 0 || s.token == "" || ctx == nil {
		return ErrStreamNotReady
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.path("events"), nil)
	if err != nil {
		return err
	}
	response, err := s.client.do(request, s.token)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return apiError(response)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		return errors.New("hex: unexpected events response")
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), maxEventLine)
	data := make([]string, 0, 2)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := emitSSE(data, receive); err != nil {
				return err
			}
			data = data[:0]
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			value = strings.TrimPrefix(value, " ")
			data = append(data, value)
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("hex: events response could not be read")
	}
	if err := emitSSE(data, receive); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func emitSSE(data []string, receive func(Event)) error {
	if len(data) == 0 {
		return nil
	}
	var event Event
	if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &event); err != nil {
		return errors.New("hex: events response contained invalid JSON")
	}
	if event.Type == "" {
		return errors.New("hex: events response omitted event type")
	}
	if receive != nil {
		receive(event)
	}
	return nil
}

// Finish seals the input and asks Hex to return the final concatenated text.
// The caller supplies the longer finish budget; other calls stay short.
func (s *Stream) Finish(ctx context.Context) (string, error) {
	if s == nil || s.client == nil || s.id == 0 || s.token == "" || ctx == nil {
		return "", ErrStreamNotReady
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.path("finish"), nil)
	if err != nil {
		return "", err
	}
	request.ContentLength = 0
	response, err := s.client.do(request, s.token)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", apiError(response)
	}
	var result finishResponse
	if err := decodeBounded(response, maxFinalResponse, &result); err != nil {
		return "", err
	}
	return result.Text, nil
}

func (s *Stream) Heartbeat(ctx context.Context) error {
	if s == nil || s.client == nil || s.id == 0 || s.token == "" || ctx == nil {
		return ErrStreamNotReady
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.path("heartbeat"), nil)
	if err != nil {
		return err
	}
	request.ContentLength = 0
	response, err := s.client.do(request, s.token)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return apiError(response)
	}
	return nil
}

// Cancel asks Hex to discard the stream. A 204 is only an accepted cancel
// request; callers must not infer that already-produced final text was undone.
func (s *Stream) Cancel(ctx context.Context) error {
	if s == nil || s.client == nil || s.id == 0 || s.token == "" {
		return nil
	}
	if ctx == nil {
		return ErrInvalidDiscovery
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.path("cancel"), nil)
	if err != nil {
		return err
	}
	request.ContentLength = 0
	response, err := s.client.do(request, s.token)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return apiError(response)
	}
	return nil
}

func (s *Stream) path(action string) string {
	return s.client.baseURL + "/streams/" + strconv.FormatUint(s.id, 10) + "/" + action
}

func (e Event) PartialText() string {
	stable := strings.TrimSpace(e.Stable)
	tentative := strings.TrimSpace(e.Tentative)
	if stable == "" {
		return tentative
	}
	if tentative == "" {
		return stable
	}
	return stable + " " + tentative
}

func (e Event) Err() error {
	if e.Type == "error" {
		switch e.Code {
		case "engine-unavailable", "model-not-loaded":
			return ErrModelUnavailable
		case "stream-backlog-full":
			return ErrStreamBacklog
		case "lease-expired":
			return ErrStreamLeaseEnded
		case "stream-cancelled":
			return ErrStreamCancelled
		default:
			return ErrStreamFailed
		}
	}
	if e.Type == "cancelled" {
		return ErrStreamCancelled
	}
	return nil
}

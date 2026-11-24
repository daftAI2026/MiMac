// Package hex contains MiMac's small client for Hex's local transcription API.
// It deliberately owns no microphone, model, key or editor lifecycle.
package hex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"mimac.local/mimac/internal/voicetools"
)

const (
	apiVersion         = "2"
	maxDiscoveryBytes  = 16 * 1024
	maxMetadataBytes   = 16 * 1024
	metadataTimeout    = 2 * time.Second
	localAPIFile       = "local-api.json"
	localAPIDataFolder = "Library/Application Support/hex"
)

var (
	ErrInvalidDiscovery = errors.New("hex: invalid local API discovery")
	ErrUnsupportedAPI   = errors.New("hex: unsupported local API version or capability")
	ErrConnectionFailed = errors.New("hex: local API connection failed")
	ErrStreamNotReady   = errors.New("hex: stream is not ready")
	ErrInvalidPCM       = errors.New("hex: invalid PCM block")
	ErrFinishTimeout    = errors.New("hex: stream finish timed out")
	ErrModelUnavailable = errors.New("hex: local transcription model is unavailable")
	ErrStreamBusy       = errors.New("hex: another live stream is active")
	ErrStreamBacklog    = errors.New("hex: live stream backlog is full")
	ErrStreamEnded      = errors.New("hex: stream has ended")
	ErrStreamNotFound   = errors.New("hex: stream is no longer available")
	ErrStreamCancelled  = errors.New("hex: stream was cancelled")
	ErrStreamLeaseEnded = errors.New("hex: stream lease expired")
	ErrStreamFailed     = errors.New("hex: stream failed")
	ErrUnauthorized     = errors.New("hex: local API authentication failed")
	ErrRequestRejected  = errors.New("hex: local API rejected the request")
)

// Discovery is the owner-only document published by the running Hex app.
// Token is never included in errors, logs, or user-facing snapshots.
type Discovery struct {
	Port       uint16 `json:"port"`
	Token      string `json:"token"`
	APIVersion string `json:"apiVersion"`
	PID        uint32 `json:"pid"`
}

// DiscoveryPath returns the documented macOS Hex local API discovery path.
func DiscoveryPath(home string) string {
	return DiscoveryPathForBundleIdentifier(home, bundleIdentifier)
}

// DiscoveryPathForBundleIdentifier returns the documented application-support
// path for an official stable or test build. Unknown identities have no path.
func DiscoveryPathForBundleIdentifier(home, identifier string) string {
	folder, ok := discoveryDataBundle(identifier)
	if !ok || home == "" {
		return ""
	}
	return filepath.Join(home, filepath.FromSlash(folder), localAPIFile)
}

// LoadDiscovery reads and validates the exact user-local discovery document.
// It never searches, starts, or opens the Hex app.
func LoadDiscovery(ctx context.Context, home string) (Discovery, string, error) {
	return LoadDiscoveryForBundleIdentifier(ctx, home, bundleIdentifier)
}

// LoadDiscoveryForBundleIdentifier reads the exact local API document owned by
// an official Hex app identity. Stable and test builds use separate data roots.
func LoadDiscoveryForBundleIdentifier(ctx context.Context, home, identifier string) (Discovery, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Discovery{}, "", err
	}
	if home == "" || !isSupportedBundleIdentifier(identifier) {
		return Discovery{}, "", ErrInvalidDiscovery
	}
	path := DiscoveryPathForBundleIdentifier(home, identifier)
	data, err := readOwnerDiscovery(ctx, path, maxDiscoveryBytes)
	if err != nil {
		return Discovery{}, path, err
	}
	var document Discovery
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return Discovery{}, path, ErrInvalidDiscovery
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Discovery{}, path, ErrInvalidDiscovery
	}
	if document.APIVersion != apiVersion {
		return Discovery{}, path, ErrUnsupportedAPI
	}
	if document.Port == 0 || document.PID == 0 || !validBearer(document.Token) {
		return Discovery{}, path, ErrInvalidDiscovery
	}
	return document, path, nil
}

// Discover reports a live, authenticated /streams capability. It does not
// create a stream, which would reserve Hex's single external stream slot.
func Discover(ctx context.Context, locator voicetools.Locator) voicetools.Snapshot {
	if ctx == nil {
		ctx = context.Background()
	}
	result := voicetools.Snapshot{State: voicetools.ReadError, Source: "hex-local-api"}
	bundle, bundleState, bundleErr := LocateApplication(ctx, locator)
	if bundleErr != nil || bundleState != voicetools.Ready {
		result.State = bundleState
		return result
	}
	document, _, err := LoadDiscoveryForBundleIdentifier(ctx, locator.Home, bundle.Identifier)
	if err != nil {
		switch {
		case errors.Is(err, voicetools.ErrFileMissing):
			result.State = voicetools.NotRunning
		case errors.Is(err, ErrUnsupportedAPI):
			result.State = voicetools.Unsupported
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			result.State = voicetools.ReadError
		default:
			result.State = voicetools.ReadError
		}
		return result
	}
	result.AppPath = discoveryAppPathForBundle(document, bundle.Identifier)
	result.Version = document.APIVersion
	client := newClient(document, nil)
	probeCtx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	capabilities, err := client.Capabilities(probeCtx)
	if err != nil {
		if errors.Is(err, ErrUnsupportedAPI) {
			result.State = voicetools.Unsupported
		} else if errors.Is(err, ErrConnectionFailed) {
			result.State = voicetools.NotRunning
		}
		return result
	}
	if !capabilities.Streams {
		result.State = voicetools.Unsupported
		return result
	}
	result.State = voicetools.Ready
	return result
}

// Capabilities is the small portion of the local API response needed by MiMac.
type Capabilities struct {
	Streams bool `json:"streams"`
}

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func newClient(document Discovery, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", document.Port),
		token:   document.Token,
		http:    httpClient,
	}
}

// NewClient reads the current discovery document for session setup. Capability
// probing remains a separate, read-only discovery operation.
func NewClient(ctx context.Context, home string) (*Client, error) {
	return NewClientForAppPath(ctx, home, "")
}

// NewClientForAppPath additionally fences session setup to the exact Hex
// process identity that the plugin last discovered.
func NewClientForAppPath(ctx context.Context, home, expectedAppPath string) (*Client, error) {
	identifier := bundleIdentifier
	if expectedAppPath != "" {
		var ok bool
		identifier, ok = identifierForDiscoveryAppPath(expectedAppPath)
		if !ok {
			return nil, ErrInvalidDiscovery
		}
	}
	document, _, err := LoadDiscoveryForBundleIdentifier(ctx, home, identifier)
	if err != nil {
		return nil, err
	}
	if expectedAppPath != "" && expectedAppPath != discoveryAppPathForBundle(document, identifier) {
		return nil, ErrInvalidDiscovery
	}
	return newClient(document, nil), nil
}

func discoveryAppPath(document Discovery) string {
	return fmt.Sprintf("hex-local-api:%d:%d", document.Port, document.PID)
}

func (c *Client) Capabilities(ctx context.Context) (Capabilities, error) {
	var result Capabilities
	if c == nil || ctx == nil {
		return result, ErrInvalidDiscovery
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/capabilities", nil)
	if err != nil {
		return result, err
	}
	response, err := c.do(request, "")
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, apiError(response)
	}
	if err := decodeBounded(response, maxMetadataBytes, &result); err != nil {
		return result, err
	}
	return result, nil
}

func (c *Client) do(request *http.Request, streamToken string) (*http.Response, error) {
	if c == nil || c.http == nil || c.token == "" {
		return nil, ErrInvalidDiscovery
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	if streamToken != "" {
		request.Header.Set("X-Hex-Stream-Token", streamToken)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, ErrConnectionFailed
	}
	return response, nil
}

func validBearer(token string) bool {
	if len(token) != 68 || !strings.HasPrefix(token, "hex_") {
		return false
	}
	for _, value := range token[4:] {
		if !((value >= '0' && value <= '9') || (value >= 'a' && value <= 'f')) {
			return false
		}
	}
	return true
}

// readOwnerDiscovery performs one no-follow open and validates owner and
// permissions on that same descriptor before trusting its bearer token.
func readOwnerDiscovery(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return nil, voicetools.ErrFileMissing
		}
		return nil, ErrInvalidDiscovery
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, ErrInvalidDiscovery
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxBytes {
		return nil, ErrInvalidDiscovery
	}
	identity, ok := before.Sys().(*syscall.Stat_t)
	if !ok || int(identity.Uid) != syscall.Geteuid() || before.Mode().Perm()&0o077 != 0 {
		return nil, ErrInvalidDiscovery
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return nil, ErrInvalidDiscovery
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !sameDiscoveryFile(before, after) || int64(len(data)) != after.Size() {
		return nil, ErrInvalidDiscovery
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !sameDiscoveryFile(after, pathInfo) {
		return nil, ErrInvalidDiscovery
	}
	return data, nil
}

func sameDiscoveryFile(left, right os.FileInfo) bool {
	return os.SameFile(left, right) && left.Size() == right.Size() &&
		left.Mode() == right.Mode() && left.ModTime().Equal(right.ModTime())
}

func apiError(response *http.Response) error {
	defer response.Body.Close()
	var payload struct {
		Code string `json:"code"`
	}
	_ = decodeBounded(response, 8*1024, &payload)
	return apiCodeError(response.StatusCode, payload.Code)
}

func apiCodeError(status int, code string) error {
	switch {
	case status == http.StatusUnauthorized:
		return ErrUnauthorized
	case status == http.StatusGatewayTimeout && code == "stream-finish-timeout":
		return ErrFinishTimeout
	case status == http.StatusServiceUnavailable && (code == "model-not-loaded" || code == "engine-unavailable"):
		return ErrModelUnavailable
	case status == http.StatusConflict && code == "stream-busy":
		return ErrStreamBusy
	case status == http.StatusConflict && code == "stream-ended":
		return ErrStreamEnded
	case status == http.StatusConflict && code == "stream-cancelled":
		return ErrStreamCancelled
	case status == http.StatusNotFound && code == "stream-not-found":
		return ErrStreamNotFound
	case status == http.StatusTooManyRequests && code == "stream-backlog-full":
		return ErrStreamBacklog
	case status == http.StatusBadRequest && (code == "unsupported-sample-format" || code == "unsupported-sample-rate"):
		return ErrUnsupportedAPI
	default:
		return ErrRequestRejected
	}
}

func decodeBounded(response *http.Response, limit int64, target any) error {
	if response == nil || response.Body == nil {
		return errors.New("hex: local API returned no response body")
	}
	data, err := readBounded(response.Body, limit)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return errors.New("hex: local API returned invalid JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("hex: local API returned trailing JSON")
	}
	return nil
}

func readBounded(response io.Reader, limit int64) ([]byte, error) {
	if limit < 0 {
		return nil, errors.New("hex: invalid response limit")
	}
	data, err := io.ReadAll(io.LimitReader(response, limit+1))
	if err != nil {
		return nil, errors.New("hex: local API response could not be read")
	}
	if int64(len(data)) > limit {
		return nil, errors.New("hex: local API response is too large")
	}
	return data, nil
}

//go:build darwin

package applespeech

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ebitengine/purego"
)

type speechAPI struct {
	isAvailable      func() int32
	create           func(*byte, int32, int32, *int32, *byte, int32) uintptr
	waitReady        func(uintptr, int32) int32
	tryWrite         func(uintptr, *int16, int32) int32
	beginDrain       func(uintptr) int32
	waitDone         func(uintptr, int32) int32
	requestCancel    func(uintptr) int32
	waitTerminated   func(uintptr, int32) int32
	copyFinalText    func(uintptr, *byte, int32, *int32) int32
	copyErrorMessage func(uintptr, *byte, int32, *int32) int32
	release          func(uintptr) int32
}

var speechLibraries = struct {
	sync.Mutex
	values map[string]*speechAPI
}{values: make(map[string]*speechAPI)}

func validateLibraryPath(path string) error {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return fmt.Errorf("%w: absolute Apple Speech bridge library path required", ErrInvalidConfig)
	}
	return nil
}

func loadNativeBridge(path string) (nativeBridge, error) {
	if err := validateLibraryPath(path); err != nil {
		return nil, err
	}
	api, err := loadSpeechAPI(path)
	if err != nil {
		return nil, err
	}
	return darwinBridge{api: api}, nil
}

func loadSpeechAPI(path string) (*speechAPI, error) {
	speechLibraries.Lock()
	defer speechLibraries.Unlock()
	if api := speechLibraries.values[path]; api != nil {
		return api, nil
	}
	library, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("applespeech: load bridge library: %w", err)
	}
	api := &speechAPI{}
	bindings := []struct {
		name string
		dst  any
	}{
		{"MiMacAppleSpeechIsAvailable", &api.isAvailable},
		{"MiMacAppleSpeechCreate", &api.create},
		{"MiMacAppleSpeechWaitReady", &api.waitReady},
		{"MiMacAppleSpeechTryWrite", &api.tryWrite},
		{"MiMacAppleSpeechBeginDrain", &api.beginDrain},
		{"MiMacAppleSpeechWaitDone", &api.waitDone},
		{"MiMacAppleSpeechRequestCancel", &api.requestCancel},
		{"MiMacAppleSpeechWaitTerminated", &api.waitTerminated},
		{"MiMacAppleSpeechCopyFinalText", &api.copyFinalText},
		{"MiMacAppleSpeechCopyErrorMessage", &api.copyErrorMessage},
		{"MiMacAppleSpeechRelease", &api.release},
	}
	for _, binding := range bindings {
		symbol, err := purego.Dlsym(library, binding.name)
		if err != nil {
			_ = purego.Dlclose(library)
			return nil, fmt.Errorf("applespeech: resolve %s: %w", binding.name, err)
		}
		purego.RegisterFunc(binding.dst, symbol)
	}
	// Keep this library and its registered function addresses alive for the
	// process lifetime. Native owners may outlive a failed Open cleanup attempt.
	speechLibraries.values[path] = api
	return api, nil
}

type darwinBridge struct{ api *speechAPI }

func (b darwinBridge) isAvailable() (bool, error) {
	state := b.api.isAvailable()
	switch state {
	case 1:
		return true, nil
	case 0:
		return false, nil
	default:
		return false, fmt.Errorf("applespeech: native availability check returned %d", state)
	}
}

func (b darwinBridge) create(locale string, sampleRate int, installAssets bool) (nativeOwner, error) {
	localeBytes := append([]byte(locale), 0)
	var status int32
	message := make([]byte, 8192)
	install := int32(0)
	if installAssets {
		install = 1
	}
	raw := b.api.create(&localeBytes[0], int32(sampleRate), install, &status, &message[0], int32(len(message)))
	runtime.KeepAlive(localeBytes)
	if raw == 0 {
		return nil, nativeCreateError(status, message)
	}
	owner := &darwinOwner{api: b.api, raw: raw}
	if status != 0 {
		return owner, nativeCreateError(status, message)
	}
	return owner, nil
}

func nativeCreateError(status int32, message []byte) error {
	text := cBufferText(message)
	if text == "" {
		return fmt.Errorf("applespeech: native create failed with status %d", status)
	}
	if strings.Contains(strings.ToLower(text), "asset") || strings.Contains(strings.ToLower(text), "model") {
		return fmt.Errorf("%w: %s", ErrAssetsUnavailable, text)
	}
	return fmt.Errorf("applespeech: native create failed with status %d: %s", status, text)
}

type darwinOwner struct {
	api *speechAPI
	raw uintptr
}

func (o *darwinOwner) waitReady(timeout time.Duration) (nativeState, error) {
	return o.wait(o.api.waitReady, timeout)
}

func (o *darwinOwner) waitDone(timeout time.Duration) (nativeState, error) {
	return o.wait(o.api.waitDone, timeout)
}

func (o *darwinOwner) wait(fn func(uintptr, int32) int32, timeout time.Duration) (nativeState, error) {
	if o.raw == 0 {
		return nativeCancelled, ErrClosed
	}
	status := fn(o.raw, durationMillis(timeout))
	if status < int32(nativePreparing) || status > int32(nativeFailed) {
		return nativePreparing, fmt.Errorf("applespeech: invalid native status %d", status)
	}
	return nativeState(status), nil
}

func (o *darwinOwner) tryWrite(samples []int16) error {
	if o.raw == 0 {
		return ErrClosed
	}
	status := o.api.tryWrite(o.raw, &samples[0], int32(len(samples)))
	runtime.KeepAlive(samples)
	switch status {
	case 0:
		return nil
	case 1:
		return ErrQueueFull
	case 2:
		return ErrBlockTooLarge
	case 3:
		return ErrNotReady
	case 4:
		return ErrDraining
	case 5:
		return ErrClosed
	default:
		return fmt.Errorf("applespeech: native write failed with status %d", status)
	}
}

func (o *darwinOwner) beginDrain() error {
	if o.raw == 0 {
		return ErrClosed
	}
	if status := o.api.beginDrain(o.raw); status != 0 {
		if status == 4 {
			return ErrDraining
		}
		return fmt.Errorf("applespeech: native finish failed with status %d", status)
	}
	return nil
}

func (o *darwinOwner) requestCancel() error {
	if o.raw == 0 {
		return nil
	}
	if status := o.api.requestCancel(o.raw); status != 0 {
		return fmt.Errorf("applespeech: native cancel request failed with status %d", status)
	}
	return nil
}

func (o *darwinOwner) waitTerminated(timeout time.Duration) (bool, error) {
	if o.raw == 0 {
		return true, nil
	}
	switch status := o.api.waitTerminated(o.raw, durationMillis(timeout)); status {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, fmt.Errorf("applespeech: native join failed with status %d", status)
	}
}

func (o *darwinOwner) finalText() (string, error) {
	if o.raw == 0 {
		return "", ErrClosed
	}
	buffer := make([]byte, MaxFinalTextBytes+1)
	var length int32
	if status := o.api.copyFinalText(o.raw, &buffer[0], int32(len(buffer)), &length); status != 0 {
		return "", fmt.Errorf("native final text status %d", status)
	}
	if length < 0 || int(length) > MaxFinalTextBytes {
		return "", fmt.Errorf("native final text length %d exceeds limit", length)
	}
	text := string(buffer[:length])
	if !utf8.ValidString(text) {
		return "", errors.New("native final text is not valid UTF-8")
	}
	return text, nil
}

func (o *darwinOwner) errorMessage() string {
	if o.raw == 0 {
		return ""
	}
	buffer := make([]byte, 8192)
	var length int32
	if status := o.api.copyErrorMessage(o.raw, &buffer[0], int32(len(buffer)), &length); status != 0 || length <= 0 || int(length) > len(buffer) {
		return ""
	}
	return string(buffer[:length])
}

func (o *darwinOwner) release() error {
	if o.raw == 0 {
		return nil
	}
	if status := o.api.release(o.raw); status != 0 {
		return fmt.Errorf("native release status %d", status)
	}
	o.raw = 0
	return nil
}

func durationMillis(timeout time.Duration) int32 {
	if timeout <= 0 {
		return 0
	}
	millis := timeout.Milliseconds()
	if millis < 1 {
		millis = 1
	}
	if millis > int64(^uint32(0)>>1) {
		millis = int64(^uint32(0) >> 1)
	}
	return int32(millis)
}

func cBufferText(buffer []byte) string {
	for index, value := range buffer {
		if value == 0 {
			return string(buffer[:index])
		}
	}
	return string(buffer)
}

//go:build darwin

package applespeech

import (
	"encoding/json"
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
	supportedLocales func(*byte, int32, *int32) int32
	create           func(*byte, int32, int32, *int32, *byte, int32) uintptr
	createPartials   func(*byte, int32, int32, int32, *int32, *byte, int32) uintptr
	waitReady        func(uintptr, int32) int32
	tryWrite         func(uintptr, *int16, int32) int32
	beginDrain       func(uintptr) int32
	waitDone         func(uintptr, int32) int32
	requestCancel    func(uintptr) int32
	waitTerminated   func(uintptr, int32) int32
	waitPartial      func(uintptr, uint64, int32, *byte, int32, *int32, *byte, int32, *int32, *uint64) int32
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
	if symbol, err := purego.Dlsym(library, "MiMacAppleSpeechCreateWithPartials"); err == nil {
		purego.RegisterFunc(&api.createPartials, symbol)
	}
	if symbol, err := purego.Dlsym(library, "MiMacAppleSpeechCopySupportedLocales"); err == nil {
		purego.RegisterFunc(&api.supportedLocales, symbol)
	}
	if symbol, err := purego.Dlsym(library, "MiMacAppleSpeechWaitPartial"); err == nil {
		purego.RegisterFunc(&api.waitPartial, symbol)
	}
	// Keep this library and its registered function addresses alive for the
	// process lifetime. Native owners may outlive a failed Open cleanup attempt.
	speechLibraries.values[path] = api
	return api, nil
}

type darwinBridge struct{ api *speechAPI }

func (b darwinBridge) supportedLocales() ([]Locale, error) {
	if b.api == nil || b.api.supportedLocales == nil {
		return nil, ErrLocalesUnavailable
	}
	const bufferSize = 128 * 1024
	buffer := make([]byte, bufferSize)
	var length int32
	status := b.api.supportedLocales(&buffer[0], int32(len(buffer)), &length)
	if status != 0 {
		return nil, fmt.Errorf("%w: native locale query returned %d", ErrLocalesUnavailable, status)
	}
	if length <= 0 || int(length) > len(buffer) {
		return nil, fmt.Errorf("%w: invalid native response length %d", ErrLocalesUnavailable, length)
	}
	var locales []Locale
	if err := json.Unmarshal(buffer[:length], &locales); err != nil {
		return nil, fmt.Errorf("%w: decode native locale list: %v", ErrLocalesUnavailable, err)
	}
	if len(locales) == 0 {
		return nil, ErrLocalesUnavailable
	}
	seen := make(map[string]struct{}, len(locales))
	for _, locale := range locales {
		if locale.Identifier == "" || strings.TrimSpace(locale.Identifier) != locale.Identifier || strings.ContainsRune(locale.Identifier, 0) ||
			locale.DisplayName == "" || strings.TrimSpace(locale.DisplayName) != locale.DisplayName {
			return nil, fmt.Errorf("%w: invalid locale row", ErrLocalesUnavailable)
		}
		if _, exists := seen[locale.Identifier]; exists {
			return nil, fmt.Errorf("%w: duplicate locale %q", ErrLocalesUnavailable, locale.Identifier)
		}
		seen[locale.Identifier] = struct{}{}
	}
	return locales, nil
}

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
	return b.createNative(locale, sampleRate, installAssets, false)
}

func (b darwinBridge) createWithPartials(locale string, sampleRate int, installAssets bool) (nativeOwner, error) {
	if b.api.createPartials == nil || b.api.waitPartial == nil {
		return nil, ErrPartialUnavailable
	}
	return b.createNative(locale, sampleRate, installAssets, true)
}

func (b darwinBridge) createNative(locale string, sampleRate int, installAssets, enablePartials bool) (nativeOwner, error) {
	localeBytes := append([]byte(locale), 0)
	var status int32
	message := make([]byte, 8192)
	install := int32(0)
	if installAssets {
		install = 1
	}
	var raw uintptr
	if enablePartials {
		raw = b.api.createPartials(&localeBytes[0], int32(sampleRate), install, 1, &status, &message[0], int32(len(message)))
	} else {
		raw = b.api.create(&localeBytes[0], int32(sampleRate), install, &status, &message[0], int32(len(message)))
	}
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
	lowerMessage := strings.ToLower(text)
	if strings.HasPrefix(lowerMessage, nativeUnsupportedLocalePrefix) {
		return fmt.Errorf("%w: %s", ErrInvalidLocale, text)
	}
	if strings.HasPrefix(lowerMessage, nativeAssetsUnsupportedPrefix) {
		return fmt.Errorf("%w: %s", ErrUnsupported, text)
	}
	if strings.HasPrefix(lowerMessage, nativeAssetsUnavailablePrefix) {
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

func (o *darwinOwner) waitPartial(afterVersion uint64, timeout time.Duration, stableBuffer, tentativeBuffer []byte) (partialSnapshot, error) {
	if o.raw == 0 || o.api.waitPartial == nil {
		return partialSnapshot{}, ErrClosed
	}
	if len(stableBuffer) == 0 || len(tentativeBuffer) == 0 {
		return partialSnapshot{}, ErrInvalidConfig
	}
	var stableLength, tentativeLength int32
	var version uint64
	status := o.api.waitPartial(
		o.raw, afterVersion, durationMillis(timeout),
		&stableBuffer[0], int32(len(stableBuffer)), &stableLength,
		&tentativeBuffer[0], int32(len(tentativeBuffer)), &tentativeLength,
		&version,
	)
	runtime.KeepAlive(stableBuffer)
	runtime.KeepAlive(tentativeBuffer)
	if stableLength < 0 || int(stableLength) > len(stableBuffer) || tentativeLength < 0 || int(tentativeLength) > len(tentativeBuffer) {
		return partialSnapshot{}, errors.New("applespeech: native partial snapshot length exceeds buffer")
	}
	stable := string(stableBuffer[:stableLength])
	tentative := string(tentativeBuffer[:tentativeLength])
	if !utf8.ValidString(stable) || !utf8.ValidString(tentative) {
		return partialSnapshot{}, errors.New("applespeech: native partial snapshot is not valid UTF-8")
	}
	switch status {
	case 0:
		return partialSnapshot{version: version}, nil
	case 1:
		return partialSnapshot{stable: stable, tentative: tentative, version: version, updated: true}, nil
	case 2:
		return partialSnapshot{stable: stable, tentative: tentative, version: version, terminal: true}, nil
	case 3:
		return partialSnapshot{}, errors.New("applespeech: native partial snapshot exceeds buffer")
	default:
		return partialSnapshot{}, fmt.Errorf("applespeech: native partial wait failed with status %d", status)
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

// Package typeless reads the small, known portion of Typeless app-settings
// needed by MiMac. It never loads Typeless code or writes its configuration.
package typeless

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"syscall"
	"unicode/utf8"
)

const MaxConfigBytes int64 = 1 << 20

type Status string

const (
	StatusPresent     Status = "present"
	StatusMissing     Status = "missing"
	StatusNull        Status = "null"
	StatusUnknown     Status = "unknown"
	StatusMalformed   Status = "malformed"
	StatusUnsupported Status = "unsupported"
	StatusError       Status = "error"
	StatusStale       Status = "stale"
)

type ErrorCode string

const (
	ErrorInvalidPath     ErrorCode = "invalid_path"
	ErrorRead            ErrorCode = "read"
	ErrorTooLarge        ErrorCode = "too_large"
	ErrorStale           ErrorCode = "stale"
	ErrorMalformedJSON   ErrorCode = "malformed_json"
	ErrorUnsupportedRoot ErrorCode = "unsupported_root"
	ErrorUnsupportedFile ErrorCode = "unsupported_file"
)

// ReadError exposes only a fixed status and code. It never includes the path,
// contents, or an underlying OS error string.
type ReadError struct {
	code ErrorCode
}

func (e *ReadError) Error() string {
	switch e.code {
	case ErrorInvalidPath:
		return "typeless config: invalid path"
	case ErrorRead:
		return "typeless config: read failed"
	case ErrorTooLarge:
		return "typeless config: file exceeds size limit"
	case ErrorStale:
		return "typeless config: file changed during read"
	case ErrorMalformedJSON:
		return "typeless config: malformed JSON"
	case ErrorUnsupportedRoot:
		return "typeless config: unsupported JSON root"
	case ErrorUnsupportedFile:
		return "typeless config: unsupported file type"
	default:
		return "typeless config: read failed"
	}
}

func (e *ReadError) Code() ErrorCode { return e.code }

func (e *ReadError) Status() Status {
	switch e.code {
	case ErrorStale:
		return StatusStale
	case ErrorMalformedJSON:
		return StatusMalformed
	case ErrorUnsupportedRoot, ErrorUnsupportedFile:
		return StatusUnsupported
	default:
		return StatusError
	}
}

type Field[T any] struct {
	Status Status `json:"status"`
	Value  T      `json:"value,omitempty"`
}

// ActionBinding preserves Typeless shortcut strings verbatim. Interpretation
// stays unknown because the reader does not parse key syntax or infer actions.
type ActionBinding struct {
	Status         Status   `json:"status"`
	Values         []string `json:"values,omitempty"`
	Interpretation Status   `json:"interpretation"`
}

type ShortcutBindings struct {
	Status              Status        `json:"status"`
	DictationMode       ActionBinding `json:"dictationMode"`
	AskAnythingMode     ActionBinding `json:"askAnythingMode"`
	TranslationMode     ActionBinding `json:"translationMode"`
	PasteLastTranscript ActionBinding `json:"pasteLastTranscript"`
}

type MicrophoneSelection struct {
	Status    Status        `json:"status"`
	DeviceID  Field[string] `json:"deviceId"`
	IDMapping Status        `json:"deviceIdMapping"`
}

type Snapshot struct {
	FeatureShortcutBindings ShortcutBindings    `json:"featureShortcutBindings"`
	SelectedMicrophone      MicrophoneSelection `json:"selectedMicrophoneDevice"`
}

// ReadFile reads an explicit path only. It does not search user directories or
// substitute Typeless defaults for missing settings. It reads through one open
// file descriptor and checks file metadata before and after the read plus path
// identity. Observed replacement or metadata changes return StatusStale; an
// in-place rewrite that preserves size and modification time can evade this
// lightweight check, so callers should re-read after configuration changes.
func ReadFile(path string) (Snapshot, error) {
	if strings.TrimSpace(path) == "" {
		return Snapshot{}, &ReadError{code: ErrorInvalidPath}
	}

	// syscall.O_NONBLOCK prevents a FIFO at an explicitly supplied path from
	// hanging before fstat can reject the non-regular file. This adapter targets
	// macOS and Linux, where the flag is available in Go's syscall package.
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return Snapshot{}, &ReadError{code: ErrorRead}
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return Snapshot{}, &ReadError{code: ErrorRead}
	}
	defer file.Close()

	before, err := file.Stat()
	if err != nil {
		return Snapshot{}, &ReadError{code: ErrorRead}
	}
	if !before.Mode().IsRegular() {
		return Snapshot{}, &ReadError{code: ErrorUnsupportedFile}
	}
	if before.Size() > MaxConfigBytes {
		return Snapshot{}, &ReadError{code: ErrorTooLarge}
	}

	data, err := io.ReadAll(io.LimitReader(file, MaxConfigBytes+1))
	if err != nil {
		return Snapshot{}, &ReadError{code: ErrorRead}
	}
	if int64(len(data)) > MaxConfigBytes {
		return Snapshot{}, &ReadError{code: ErrorTooLarge}
	}

	after, err := file.Stat()
	if err != nil {
		return Snapshot{}, &ReadError{code: ErrorRead}
	}
	if !sameFileVersion(before, after) || int64(len(data)) != after.Size() {
		return Snapshot{}, &ReadError{code: ErrorStale}
	}
	currentPath, err := os.Stat(path)
	if err != nil || !sameFileVersion(after, currentPath) {
		return Snapshot{}, &ReadError{code: ErrorStale}
	}

	return decodeSnapshot(data)
}

func sameFileVersion(left, right os.FileInfo) bool {
	return os.SameFile(left, right) && left.Size() == right.Size() &&
		left.Mode() == right.Mode() && left.ModTime().Equal(right.ModTime())
}

func decodeSnapshot(data []byte) (Snapshot, error) {
	if !utf8.Valid(data) {
		return Snapshot{}, &ReadError{code: ErrorMalformedJSON}
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	var rootJSON json.RawMessage
	if err := decoder.Decode(&rootJSON); err != nil {
		return Snapshot{}, &ReadError{code: ErrorMalformedJSON}
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Snapshot{}, &ReadError{code: ErrorMalformedJSON}
	}

	trimmedRoot := bytes.TrimSpace(rootJSON)
	if len(trimmedRoot) == 0 || trimmedRoot[0] != '{' {
		return Snapshot{}, &ReadError{code: ErrorUnsupportedRoot}
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(rootJSON, &root); err != nil || root == nil {
		return Snapshot{}, &ReadError{code: ErrorMalformedJSON}
	}

	return Snapshot{
		FeatureShortcutBindings: parseShortcutBindings(root),
		SelectedMicrophone:      parseMicrophoneSelection(root),
	}, nil
}

func parseShortcutBindings(root map[string]json.RawMessage) ShortcutBindings {
	raw, exists := root["featureShortcutBindings"]
	if !exists {
		return ShortcutBindings{
			Status:              StatusMissing,
			DictationMode:       actionWithStatus(StatusMissing),
			AskAnythingMode:     actionWithStatus(StatusMissing),
			TranslationMode:     actionWithStatus(StatusMissing),
			PasteLastTranscript: actionWithStatus(StatusMissing),
		}
	}
	if isNull(raw) {
		return ShortcutBindings{
			Status:              StatusNull,
			DictationMode:       actionWithStatus(StatusUnknown),
			AskAnythingMode:     actionWithStatus(StatusUnknown),
			TranslationMode:     actionWithStatus(StatusUnknown),
			PasteLastTranscript: actionWithStatus(StatusUnknown),
		}
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return ShortcutBindings{
			Status:              StatusMalformed,
			DictationMode:       actionWithStatus(StatusUnknown),
			AskAnythingMode:     actionWithStatus(StatusUnknown),
			TranslationMode:     actionWithStatus(StatusUnknown),
			PasteLastTranscript: actionWithStatus(StatusUnknown),
		}
	}

	return ShortcutBindings{
		Status:              StatusPresent,
		DictationMode:       parseActionBinding(object, "dictationMode"),
		AskAnythingMode:     parseActionBinding(object, "askAnythingMode"),
		TranslationMode:     parseActionBinding(object, "translationMode"),
		PasteLastTranscript: parseActionBinding(object, "pasteLastTranscript"),
	}
}

func actionWithStatus(status Status) ActionBinding {
	return ActionBinding{Status: status, Interpretation: StatusUnknown}
}

func parseActionBinding(object map[string]json.RawMessage, key string) ActionBinding {
	raw, exists := object[key]
	if !exists {
		return actionWithStatus(StatusMissing)
	}
	if isNull(raw) {
		return actionWithStatus(StatusNull)
	}
	var rawValues []json.RawMessage
	if err := json.Unmarshal(raw, &rawValues); err != nil || rawValues == nil {
		return actionWithStatus(StatusMalformed)
	}
	values := make([]string, len(rawValues))
	for index, rawValue := range rawValues {
		if isNull(rawValue) || json.Unmarshal(rawValue, &values[index]) != nil {
			return actionWithStatus(StatusMalformed)
		}
	}
	return ActionBinding{
		Status:         StatusPresent,
		Values:         values,
		Interpretation: StatusUnknown,
	}
}

func parseMicrophoneSelection(root map[string]json.RawMessage) MicrophoneSelection {
	raw, exists := root["selectedMicrophoneDevice"]
	if !exists {
		return MicrophoneSelection{Status: StatusMissing, DeviceID: Field[string]{Status: StatusUnknown}, IDMapping: StatusUnknown}
	}
	if isNull(raw) {
		return MicrophoneSelection{Status: StatusNull, DeviceID: Field[string]{Status: StatusUnknown}, IDMapping: StatusUnknown}
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return MicrophoneSelection{Status: StatusMalformed, DeviceID: Field[string]{Status: StatusUnknown}, IDMapping: StatusUnknown}
	}

	deviceID := Field[string]{Status: StatusMissing}
	if rawID, exists := object["deviceId"]; exists {
		switch {
		case isNull(rawID):
			deviceID.Status = StatusNull
		default:
			var value string
			if err := json.Unmarshal(rawID, &value); err != nil {
				deviceID.Status = StatusMalformed
			} else {
				deviceID.Status = StatusPresent
				deviceID.Value = value
			}
		}
	}
	return MicrophoneSelection{Status: StatusPresent, DeviceID: deviceID, IDMapping: StatusUnknown}
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

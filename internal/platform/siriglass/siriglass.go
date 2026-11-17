// Package siriglass lazily attaches the verified native Siri glass renderer
// to a MyGo window. The renderer is optional presentation only.
package siriglass

import "errors"

// ErrUnsupported means that the native renderer is unavailable on this host.
var ErrUnsupported = errors.New("siriglass: native renderer is unsupported on this host")

// InputState is a display input, never an owner of the business session.
// Thinking requires explicit target feedback; ending capture does not imply it.
type InputState struct {
	Thinking               bool   `json:"thinking"`
	IsRecording            bool   `json:"isRecording"`
	HasVoiceInputOwnership bool   `json:"hasVoiceInputOwnership"`
	SceneMode              string `json:"sceneMode,omitempty"`
}

//go:build !darwin

package doubao

import (
	"context"
)

func postSelectedMicrophoneIDNotification(context.Context, MicrophoneSelectionRequest) error {
	return ErrMicrophoneSelectionUnsupported
}

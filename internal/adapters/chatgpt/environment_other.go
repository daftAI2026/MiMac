//go:build !darwin

package chatgpt

import (
	"context"

	"mimac.local/mimac/internal/voicetools"
)

func resolveRunningConfigHome(ctx context.Context, appPath string) (string, voicetools.State, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return "", voicetools.ReadError, err
		}
	}
	return "", voicetools.Unsupported, nil
}

//go:build !darwin

package voicetools

import (
	"context"
	"errors"
)

func registeredApplicationCandidates(context.Context, string) ([]string, error) {
	return nil, errors.New("voicetools: system application discovery requires macOS")
}

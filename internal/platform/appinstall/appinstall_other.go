//go:build !darwin

package appinstall

import "context"

// MaybeInstall is intentionally inert outside macOS application bundles.
func MaybeInstall(context.Context) (Result, error) { return Result{Disposition: Continue}, nil }

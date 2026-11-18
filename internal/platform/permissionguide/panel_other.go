//go:build !darwin

package permissionguide

import "github.com/egoist/mygo"

func ConfigurePermissionPanel(window *mygo.Window) error { return nil }

func YieldToSystemSettings() {}

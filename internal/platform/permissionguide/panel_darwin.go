//go:build darwin

package permissionguide

import (
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
)

// ConfigurePermissionPanel tunes the existing host-owned panel. It stays visible
// beside System Settings and acquires keyboard focus only when a control needs it.
func ConfigurePermissionPanel(window *mygo.Window) {
	if window == nil || !window.IsPanel() {
		return
	}
	mygo.RunOnMain(func() {
		if window.IsDestroyed() || loadAppKitFramework() != nil {
			return
		}
		handle := window.NativeHandle()
		if handle == 0 {
			return
		}
		panel := objc.ID(handle)
		panel.Send(objc.RegisterName("setHidesOnDeactivate:"), false)
		panel.Send(objc.RegisterName("setBecomesKeyOnlyIfNeeded:"), true)
	})
}

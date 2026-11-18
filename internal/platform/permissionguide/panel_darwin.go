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

// YieldToSystemSettings gives the live Settings process the opportunity to
// take activation. It neither launches Settings nor activates a modal itself.
func YieldToSystemSettings() {
	pid, ok := systemSettingsPID()
	if !ok {
		return
	}
	app := objc.Send[objc.ID](objc.ID(objc.GetClass("NSApplication")), objc.RegisterName("sharedApplication"))
	running := objc.Send[objc.ID](objc.ID(objc.GetClass("NSRunningApplication")), objc.RegisterName("runningApplicationWithProcessIdentifier:"), pid)
	yield := objc.RegisterName("yieldActivationToApplication:")
	if app != 0 && running != 0 && objc.Send[bool](app, objc.RegisterName("respondsToSelector:"), yield) {
		app.Send(yield, running)
	}
}

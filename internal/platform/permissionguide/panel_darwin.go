//go:build darwin

package permissionguide

import (
	"errors"

	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
)

// ConfigurePermissionPanel applies AppKit's click-to-focus policy to the
// pointer-operated sidecar without changing MyGo's native window identity.
func ConfigurePermissionPanel(window *mygo.Window) error {
	if window == nil || !window.IsPanel() || window.IsDestroyed() {
		return errors.New("missing permission panel")
	}
	panel := objc.ID(window.NativeHandle())
	if panel == 0 {
		return errors.New("permission panel has no native window")
	}
	panel.Send(objc.RegisterName("setHidesOnDeactivate:"), false)
	panel.Send(objc.RegisterName("setBecomesKeyOnlyIfNeeded:"), true)
	return nil
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

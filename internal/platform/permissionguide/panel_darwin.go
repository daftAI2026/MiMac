//go:build darwin

package permissionguide

import "github.com/ebitengine/purego/objc"

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

// FinishSystemSettingsVisit asks Settings to quit normally and requests
// activation so the first-use window can receive input again.
func FinishSystemSettingsVisit() {
	if pid, ok := systemSettingsPID(); ok {
		running := objc.Send[objc.ID](objc.ID(objc.GetClass("NSRunningApplication")), objc.RegisterName("runningApplicationWithProcessIdentifier:"), pid)
		if running != 0 {
			_ = objc.Send[bool](running, objc.RegisterName("terminate"))
		}
	}
	app := objc.Send[objc.ID](objc.ID(objc.GetClass("NSApplication")), objc.RegisterName("sharedApplication"))
	activate := objc.RegisterName("activate")
	if app != 0 && objc.Send[bool](app, objc.RegisterName("respondsToSelector:"), activate) {
		app.Send(activate)
	}
}

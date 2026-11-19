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

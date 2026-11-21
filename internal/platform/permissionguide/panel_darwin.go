//go:build darwin

package permissionguide

import "github.com/ebitengine/purego/objc"

// YieldToSystemSettings cooperates with native drag and authentication.
func YieldToSystemSettings() { handOffToSystemSettings(false) }

// RevealExistingSystemSettings raises the current page once on an explicit
// first-use visit. It never launches Settings or selects a new page.
func RevealExistingSystemSettings() { handOffToSystemSettings(true) }

func handOffToSystemSettings(reveal bool) {
	app := objc.Send[objc.ID](objc.ID(objc.GetClass("NSApplication")), objc.RegisterName("sharedApplication"))
	if app == 0 || !objc.Send[bool](app, objc.RegisterName("isActive")) {
		return
	}
	pid, ok := systemSettingsPID()
	if !ok {
		return
	}
	target := objc.Send[objc.ID](objc.ID(objc.GetClass("NSRunningApplication")), objc.RegisterName("runningApplicationWithProcessIdentifier:"), pid)
	if target == 0 {
		return
	}
	yield := objc.RegisterName("yieldActivationToApplication:")
	if objc.Send[bool](app, objc.RegisterName("respondsToSelector:"), yield) {
		app.Send(yield, target)
	}
	if reveal {
		_ = objc.Send[bool](target, objc.RegisterName("activateWithOptions:"), uintptr(0))
	}
}

// FinishSystemSettingsVisit closes Settings normally after an explicit first-use
// visit. Ask Launch Services to activate the existing app so the request is not
// dependent on the exiting Settings app yielding keyboard ownership.
func FinishSystemSettingsVisit() {
	workspace := objc.Send[objc.ID](objc.ID(objc.GetClass("NSWorkspace")), objc.RegisterName("sharedWorkspace"))
	front := objc.Send[objc.ID](workspace, objc.RegisterName("frontmostApplication"))
	current := objc.Send[objc.ID](objc.ID(objc.GetClass("NSRunningApplication")), objc.RegisterName("currentApplication"))
	frontPID := objc.Send[int32](front, objc.RegisterName("processIdentifier"))
	currentPID := objc.Send[int32](current, objc.RegisterName("processIdentifier"))
	settingsPID, settingsRunning := systemSettingsPID()
	if settingsRunning {
		running := objc.Send[objc.ID](objc.ID(objc.GetClass("NSRunningApplication")), objc.RegisterName("runningApplicationWithProcessIdentifier:"), settingsPID)
		if running != 0 {
			_ = objc.Send[bool](running, objc.RegisterName("terminate"))
		}
	}
	if frontPID != currentPID && (!settingsRunning || frontPID != settingsPID) {
		return
	}
	bundleURL := objc.Send[objc.ID](current, objc.RegisterName("bundleURL"))
	config := objc.Send[objc.ID](objc.ID(objc.GetClass("NSWorkspaceOpenConfiguration")), objc.RegisterName("configuration"))
	if workspace == 0 || bundleURL == 0 || config == 0 {
		return
	}
	config.Send(objc.RegisterName("setActivates:"), true)
	config.Send(objc.RegisterName("setCreatesNewApplicationInstance:"), false)
	config.Send(objc.RegisterName("setAllowsRunningApplicationSubstitution:"), false)
	config.Send(objc.RegisterName("setAddsToRecentItems:"), false)
	workspace.Send(objc.RegisterName("openApplicationAtURL:configuration:completionHandler:"), bundleURL, config, uintptr(0))
}

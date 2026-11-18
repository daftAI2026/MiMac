//go:build darwin

package permissionguide

import (
	"errors"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
)

// ConfigurePermissionPanel applies only to this pointer-operated sidecar.
// It can receive clicks and file drags without becoming the keyboard window.
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

	const name = "MiMacPermissionSidecar"
	base := panel.Class()
	var class objc.Class
	if class = objc.GetClass(name); class == 0 {
		var err error
		class, err = objc.RegisterClass(name, base, nil, nil, []objc.MethodDef{{
			Cmd: objc.RegisterName("canBecomeKeyWindow"),
			Fn:  func(objc.ID, objc.SEL) bool { return false },
		}})
		if err != nil {
			return err
		}
	}
	if base == class {
		return nil
	}
	if class.SuperClass() != base {
		return errors.New("permission panel class has changed")
	}
	address, err := purego.Dlsym(purego.RTLD_DEFAULT, "object_setClass")
	if err != nil {
		return err
	}
	var setClass func(objc.ID, objc.Class) objc.Class
	purego.RegisterFunc(&setClass, address)
	setClass(panel, class)
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

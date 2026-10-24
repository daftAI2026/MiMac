//go:build darwin

package keyoutput

import (
	"github.com/ebitengine/purego"
)

func loadEventAPI() (*eventAPI, error) {
	cg, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, err
	}
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, err
	}
	api := &eventAPI{}
	purego.RegisterLibFunc(&api.preflight, cg, "CGPreflightPostEventAccess")
	purego.RegisterLibFunc(&api.request, cg, "CGRequestPostEventAccess")
	purego.RegisterLibFunc(&api.sourceCreate, cg, "CGEventSourceCreate")
	purego.RegisterLibFunc(&api.sourceUserData, cg, "CGEventSourceSetUserData")
	purego.RegisterLibFunc(&api.suppression, cg, "CGEventSourceSetLocalEventsSuppressionInterval")
	purego.RegisterLibFunc(&api.eventCreate, cg, "CGEventCreateKeyboardEvent")
	purego.RegisterLibFunc(&api.setFlags, cg, "CGEventSetFlags")
	purego.RegisterLibFunc(&api.setInteger, cg, "CGEventSetIntegerValueField")
	purego.RegisterLibFunc(&api.postGlobal, cg, "CGEventPost")
	purego.RegisterLibFunc(&api.postToPID, cg, "CGEventPostToPid")
	purego.RegisterLibFunc(&api.release, cf, "CFRelease")
	return api, nil
}

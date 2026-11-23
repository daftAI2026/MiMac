//go:build darwin || linux || freebsd || netbsd

package dotscope

import (
	"fmt"

	"github.com/ebitengine/purego"
)

func bindNative(path string, api *nativeAPI) (func(), error) {
	lib, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("voice dots: open model: %w", err)
	}
	unload := func() { _ = purego.Dlclose(lib) }
	for _, binding := range []struct {
		name string
		fn   any
	}{
		{"abi_version", &api.version}, {"create", &api.create}, {"destroy", &api.destroy},
		{"start", &api.start}, {"meter_rms", &api.meter}, {"submit", &api.submit},
		{"done", &api.done}, {"fail", &api.fail}, {"cancel", &api.cancel}, {"step", &api.step},
	} {
		symbol, err := purego.Dlsym(lib, "mi_dot_model_"+binding.name)
		if err != nil {
			unload()
			return nil, fmt.Errorf("voice dots: bind %s: %w", binding.name, err)
		}
		purego.RegisterFunc(binding.fn, symbol)
	}
	return unload, nil
}

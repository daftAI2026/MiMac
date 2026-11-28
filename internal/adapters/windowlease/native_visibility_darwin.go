//go:build darwin

package windowlease

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"mimac.local/mimac/internal/platform/runningapp"
)

// These symbols are resolved only for adapters that claim application visibility.
var visibilityRuntime struct {
	sync.Once
	booleanTrue, booleanFalse uintptr
	err                       error
}

func (p darwinPresentationProbe) loadVisibilityAPI() error {
	visibilityRuntime.Do(func() {
		api := p.api
		for name, dest := range map[string]*uintptr{"kCFBooleanTrue": &visibilityRuntime.booleanTrue, "kCFBooleanFalse": &visibilityRuntime.booleanFalse} {
			addr, err := purego.Dlsym(api.libraries[0], name)
			if err != nil {
				visibilityRuntime.err = err
				return
			}
			api.copyNative(dest, addr, unsafe.Sizeof(*dest))
			if *dest == 0 {
				visibilityRuntime.err = ErrUnavailable
				return
			}
		}
	})
	return visibilityRuntime.err
}

func (p darwinPresentationProbe) ApplicationVisibility(pid int32, element uintptr) (ApplicationVisibility, error) {
	var state ApplicationVisibility
	if err := p.loadVisibilityAPI(); err != nil {
		return state, err
	}
	api := p.api
	app := api.axCreateApplication(pid)
	if app == 0 {
		return state, ErrUnavailable
	}
	defer api.cfRelease(app)
	api.axSetTimeout(app, 0.15)
	// Hidden and the member list come from the input method. The focused app
	// comes from the system and needs no access to the editor's text or views.
	values, err := api.copyAttributes(app, "AXHidden", "AXWindows")
	if err != nil {
		return state, err
	}
	defer api.cfRelease(values)
	hidden, windows := api.cfArrayValue(values, 0), api.cfArrayValue(values, 1)
	if hidden == 0 || windows == 0 || api.cfGetTypeID(hidden) != api.cfBooleanTypeID() {
		return state, ErrUnavailable
	}
	state.Hidden = api.cfBooleanValue(hidden) != 0
	if api.cfGetTypeID(windows) != api.cfArrayTypeID() {
		return state, ErrUnavailable
	}
	count := api.cfArrayCount(windows)
	if count < 0 || count > presentationMaxAXWindows {
		return state, ErrUnavailable
	}
	state.WindowCount = int(count)
	if element != 0 {
		for i := int64(0); i < count; i++ {
			if item := api.cfArrayValue(windows, i); item != 0 && api.cfEqual(item, element) != 0 {
				state.ContainsElement = true
				break
			}
		}
	}
	front, err := runningapp.FrontmostPID()
	if err != nil {
		return state, err
	}
	state.FocusedPID = front
	return state, nil
}

func (p darwinPresentationProbe) SetApplicationHidden(pid int32, hidden bool) error {
	if err := p.loadVisibilityAPI(); err != nil {
		return err
	}
	api := p.api
	app := api.axCreateApplication(pid)
	if app == 0 {
		return ErrUnavailable
	}
	defer api.cfRelease(app)
	api.axSetTimeout(app, 0.15)
	value := visibilityRuntime.booleanFalse
	if hidden {
		value = visibilityRuntime.booleanTrue
	}
	if code := api.axSetAttribute(app, api.attributes["AXHidden"], value); code != 0 {
		return fmt.Errorf("AXHidden write returned %d", code)
	}
	return nil
}

type darwinApplicationEvents struct {
	*darwinWindowEventSource
	notices chan ApplicationNotice
}

func (s *darwinApplicationEvents) Events() <-chan ApplicationNotice { return s.notices }

func (s *darwinApplicationEvents) Close() {
	s.darwinWindowEventSource.Close()
	for notice := range s.notices {
		if notice.Element != 0 {
			s.api.cfRelease(notice.Element)
		}
	}
}

var applicationObservers sync.Map
var appAXCallback = purego.NewCallback(func(observer, element, notification, ref uintptr) {
	value, ok := applicationObservers.Load(observer)
	if !ok {
		return
	}
	source := value.(*darwinApplicationEvents)
	notice := ApplicationNotice{Event: ApplicationEvent(ref)}
	if notice.Event == ApplicationEventWindowCreated && element != 0 {
		notice.Element = source.api.cfRetain(element)
	}
	select {
	case source.notices <- notice:
	default:
		if notice.Element != 0 {
			source.api.cfRelease(notice.Element)
		}
	}
})

func (p darwinPresentationProbe) WatchApplication(pid int32) (ApplicationEventSource, error) {
	app := p.api.axCreateApplication(pid)
	if app == 0 {
		return nil, ErrUnavailable
	}
	source := &darwinApplicationEvents{
		darwinWindowEventSource: &darwinWindowEventSource{api: p.api, element: app, stop: make(chan struct{}), done: make(chan struct{})},
		notices:                 make(chan ApplicationNotice, 2),
	}
	ready := make(chan error, 1)
	go func() {
		defer close(source.done)
		defer close(source.notices)
		defer p.api.cfRelease(app)
		source.observe(pid, ready, appAXCallback, []axSubscription{
			{"AXWindowCreated", uintptr(ApplicationEventWindowCreated)},
			{"AXApplicationShown", uintptr(ApplicationEventShown)},
		}, func(observer uintptr) func() {
			applicationObservers.Store(observer, source)
			return func() { applicationObservers.Delete(observer) }
		})
	}()
	if err := <-ready; err != nil {
		source.Close()
		return nil, err
	}
	return source, nil
}

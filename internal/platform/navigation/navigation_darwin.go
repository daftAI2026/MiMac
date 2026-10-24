//go:build darwin

// Package navigation attaches a system toolbar to the existing MyGo window.
// MyGo still owns the window, event loop, and navigation state.
package navigation

import (
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
)

const targetName = "MiMacToolbarDelegate"

var (
	targetOnce  sync.Once
	targetClass objc.Class
	targetErr   error
	controlsMu  sync.Mutex
	controls    = make(map[objc.ID]*Control)
)

// Control owns only the native toolbar objects. All state is main-thread only.
type Control struct {
	window, toolbar, delegate, identifiers objc.ID
	spacer, group, titleItem, titleField   objc.ID
	back, forward                          objc.ID
	owned                                  []objc.ID
	onNavigate                             func(int)
	closed                                 bool
	zoom                                   *verticalZoom
}

func New(window uintptr, sidebarWidth, contentInset float64, onNavigate func(int)) (*Control, error) {
	if window == 0 || sidebarWidth < 100 || math.IsNaN(sidebarWidth) || math.IsInf(sidebarWidth, 0) || onNavigate == nil {
		return nil, errors.New("navigation: invalid window or layout")
	}
	var c *Control
	err := errors.New("navigation: MyGo main loop is unavailable")
	mygo.RunOnMain(func() { c, err = newOnMain(objc.ID(window), sidebarWidth, contentInset, onNavigate) })
	return c, err
}

func newOnMain(window objc.ID, sidebarWidth, contentInset float64, callback func(int)) (_ *Control, err error) {
	if err = ensureDelegate(); err != nil {
		return nil, err
	}
	c := &Control{window: window, onNavigate: callback}
	window.Send(sel("retain"))
	defer func() {
		if err != nil {
			c.closeOnMain()
		}
	}()
	c.delegate = objc.ID(targetClass).Send(sel("new"))
	controlsMu.Lock()
	controls[c.delegate] = c
	controlsMu.Unlock()
	c.toolbar = objc.ID(objc.GetClass("NSToolbar")).Send(sel("alloc")).Send(sel("initWithIdentifier:"), ns("dev.mimac.settings.toolbar"))
	if c.toolbar == 0 || c.delegate == 0 {
		return nil, errors.New("navigation: toolbar unavailable")
	}
	c.toolbar.Send(sel("setDelegate:"), c.delegate)
	c.toolbar.Send(sel("setAllowsUserCustomization:"), false)
	c.toolbar.Send(sel("setAutosavesConfiguration:"), false)
	c.toolbar.Send(sel("setDisplayMode:"), 2) // NSToolbarDisplayModeIconOnly
	c.toolbar.Send(sel("setShowsBaselineSeparator:"), false)
	if responds(c.toolbar, "setAllowsDisplayModeCustomization:") {
		c.toolbar.Send(sel("setAllowsDisplayModeCustomization:"), false)
	}

	c.spacer = c.item("mimac.sidebar")
	spacerView := c.own(objc.ID(objc.GetClass("NSView")).Send(sel("new")))
	// Unified toolbars reserve the native traffic-light group before their items.
	// The remaining spacer keeps the toolbar aligned with our fixed sidebar.
	c.fixedSize(spacerView, math.Max(1, sidebarWidth+contentInset-100), 32)
	c.spacer.Send(sel("setView:"), spacerView)
	c.spacer.Send(sel("setBordered:"), false)

	// Native circular controls inside one system glass group. A segmented
	// toolbar group gives half-capsule hit regions instead of round buttons.
	c.group = c.item("mimac.navigation")
	container := c.own(objc.ID(objc.GetClass("NSView")).Send(sel("new")))
	c.fixedSize(container, 64, 36)
	for i, name := range []string{"chevron.backward", "chevron.forward"} {
		label := []string{"返回", "前进"}[i]
		action := []string{"mimacGoBack:", "mimacGoForward:"}[i]
		button := c.own(objc.ID(objc.GetClass("NSButton")).Send(sel("buttonWithImage:target:action:"), symbol(name, label), c.delegate, sel(action)).Send(sel("retain")))
		button.Send(sel("setTitle:"), ns(""))
		button.Send(sel("setSymbolConfiguration:"), objc.ID(objc.GetClass("NSImageSymbolConfiguration")).Send(sel("configurationWithPointSize:weight:"), 20.0, 0.0))
		button.Send(sel("setToolTip:"), ns(label))
		button.Send(sel("setAccessibilityLabel:"), ns(label))
		button.Send(sel("setButtonType:"), 0)
		button.Send(sel("setBordered:"), true)
		button.Send(sel("setBezelStyle:"), 7) // circular, including older macOS
		if responds(button, "setBorderShape:") {
			button.Send(sel("setBezelStyle:"), 13) // accessory bar hover feedback
			button.Send(sel("setBorderShape:"), 3) // NSControlBorderShapeCircle
		}
		button.Send(sel("setShowsBorderOnlyWhileMouseInside:"), true)
		button.Send(sel("setEnabled:"), false)
		c.fixedSize(button, 28, 28)
		container.Send(sel("addSubview:"), button)
		button.Send(sel("centerYAnchor")).Send(sel("constraintEqualToAnchor:"), container.Send(sel("centerYAnchor"))).Send(sel("setActive:"), true)
		button.Send(sel("leadingAnchor")).Send(sel("constraintEqualToAnchor:constant:"), container.Send(sel("leadingAnchor")), float64(4+i*28)).Send(sel("setActive:"), true)
		if i == 0 {
			c.back = button
		} else {
			c.forward = button
		}
	}
	groupView := container
	if glassClass := objc.GetClass("NSGlassEffectView"); glassClass != 0 {
		glass := c.own(objc.ID(glassClass).Send(sel("new")))
		c.fixedSize(glass, 64, 36)
		glass.Send(sel("setCornerRadius:"), 18.0)
		glass.Send(sel("setContentView:"), container)
		groupView = glass
	}
	c.group.Send(sel("setView:"), groupView)
	c.group.Send(sel("setLabel:"), ns("导航"))
	c.group.Send(sel("setBordered:"), false)

	c.titleItem = c.item("mimac.title")
	c.titleField = c.own(objc.ID(objc.GetClass("NSTextField")).Send(sel("labelWithString:"), ns("按键映射")).Send(sel("retain")))
	c.titleField.Send(sel("setFont:"), objc.ID(objc.GetClass("NSFont")).Send(sel("systemFontOfSize:weight:"), 16.0, 0.3))
	c.titleField.Send(sel("setAccessibilityRole:"), ns("AXHeading"))
	// Intrinsic text height, centered against the same 36pt control container.
	// A fixed 24pt text field centers its frame but leaves its glyphs too high.
	titleContainer := c.own(objc.ID(objc.GetClass("NSView")).Send(sel("new")))
	c.fixedSize(titleContainer, 104, 36)
	c.titleField.Send(sel("setTranslatesAutoresizingMaskIntoConstraints:"), false)
	titleContainer.Send(sel("addSubview:"), c.titleField)
	for _, constraint := range []objc.ID{
		c.titleField.Send(sel("leadingAnchor")).Send(sel("constraintEqualToAnchor:"), titleContainer.Send(sel("leadingAnchor"))),
		c.titleField.Send(sel("trailingAnchor")).Send(sel("constraintEqualToAnchor:"), titleContainer.Send(sel("trailingAnchor"))),
		c.titleField.Send(sel("centerYAnchor")).Send(sel("constraintEqualToAnchor:"), titleContainer.Send(sel("centerYAnchor"))),
	} {
		constraint.Send(sel("setActive:"), true)
	}
	c.titleItem.Send(sel("setView:"), titleContainer)
	c.titleItem.Send(sel("setBordered:"), false)
	c.identifiers = array(ns("mimac.sidebar"), ns("mimac.navigation"), ns("mimac.title"), ns("NSToolbarFlexibleSpaceItem"))
	c.zoom, err = attachVerticalZoom(window)
	if err != nil {
		return nil, err
	}
	window.Send(sel("setTitleVisibility:"), 1)
	window.Send(sel("setToolbarStyle:"), 3) // NSWindowToolbarStyleUnified
	window.Send(sel("setTitlebarAppearsTransparent:"), true)
	window.Send(sel("setTitlebarSeparatorStyle:"), 1) // none
	window.Send(sel("setToolbar:"), c.toolbar)
	c.toolbar.Send(sel("setVisible:"), true)
	return c, nil
}

func (c *Control) own(id objc.ID) objc.ID {
	if id != 0 {
		c.owned = append(c.owned, id)
	}
	return id
}
func (c *Control) item(identifier string) objc.ID {
	item := c.own(objc.ID(objc.GetClass("NSToolbarItem")).Send(sel("alloc")).Send(sel("initWithItemIdentifier:"), ns(identifier)))
	item.Send(sel("setVisibilityPriority:"), 1000)
	return item
}
func (c *Control) fixedSize(view objc.ID, width, height float64) {
	view.Send(sel("setTranslatesAutoresizingMaskIntoConstraints:"), false)
	for _, constraint := range []objc.ID{
		view.Send(sel("widthAnchor")).Send(sel("constraintEqualToConstant:"), width),
		view.Send(sel("heightAnchor")).Send(sel("constraintEqualToConstant:"), height),
	} {
		constraint.Send(sel("setActive:"), true)
	}
}
func (c *Control) SetEnabled(back, forward bool) {
	if c == nil {
		return
	}
	mygo.RunOnMain(func() {
		if !c.closed {
			c.back.Send(sel("setEnabled:"), back)
			c.forward.Send(sel("setEnabled:"), forward)
		}
	})
}
func (c *Control) SetTitle(title string) {
	if c == nil {
		return
	}
	mygo.RunOnMain(func() {
		if !c.closed {
			c.titleField.Send(sel("setStringValue:"), ns(title))
			c.titleItem.Send(sel("setLabel:"), ns(title))
		}
	})
}
func (c *Control) SetTitleSize(size float64) {
	if c == nil {
		return
	}
	mygo.RunOnMain(func() {
		if !c.closed {
			c.titleField.Send(sel("setFont:"), objc.ID(objc.GetClass("NSFont")).Send(sel("systemFontOfSize:weight:"), size, 0.3))
		}
	})
}

func (c *Control) Close() {
	if c != nil {
		mygo.RunOnMain(c.closeOnMain)
	}
}
func (c *Control) closeOnMain() {
	if c.closed {
		return
	}
	c.closed = true
	controlsMu.Lock()
	delete(controls, c.delegate)
	controlsMu.Unlock()
	if c.toolbar != 0 {
		c.toolbar.Send(sel("setDelegate:"), objc.ID(0))
	}
	if c.zoom != nil {
		c.zoom.close()
		c.zoom = nil
	}
	for _, button := range []objc.ID{c.back, c.forward} {
		if button != 0 {
			button.Send(sel("setTarget:"), objc.ID(0))
			button.Send(sel("setAction:"), objc.SEL(0))
		}
	}
	if c.window != 0 && c.window.Send(sel("toolbar")) == c.toolbar {
		c.window.Send(sel("setToolbar:"), objc.ID(0))
	}
	for i := len(c.owned) - 1; i >= 0; i-- {
		c.owned[i].Send(sel("release"))
	}
	for _, id := range []objc.ID{c.identifiers, c.toolbar, c.delegate, c.window} {
		if id != 0 {
			id.Send(sel("release"))
		}
	}
	c.owned = nil
	c.onNavigate = nil
}
func ensureDelegate() error {
	targetOnce.Do(func() {
		targetClass, targetErr = objc.RegisterClass(targetName, objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
			{Cmd: sel("toolbarDefaultItemIdentifiers:"), Fn: toolbarIdentifiers},
			{Cmd: sel("toolbarAllowedItemIdentifiers:"), Fn: toolbarIdentifiers},
			{Cmd: sel("toolbar:itemForItemIdentifier:willBeInsertedIntoToolbar:"), Fn: toolbarItem},
			{Cmd: sel("mimacGoBack:"), Fn: navigateBack},
			{Cmd: sel("mimacGoForward:"), Fn: navigateForward},
		})
		if targetErr != nil {
			targetErr = fmt.Errorf("navigation: register delegate: %w", targetErr)
		}
	})
	return targetErr
}
func control(target objc.ID) *Control {
	controlsMu.Lock()
	defer controlsMu.Unlock()
	return controls[target]
}
func toolbarIdentifiers(target objc.ID, _ objc.SEL, _ objc.ID) objc.ID {
	if c := control(target); c != nil && !c.closed {
		return c.identifiers
	}
	return 0
}
func toolbarItem(target objc.ID, _ objc.SEL, _ objc.ID, identifier objc.ID, _ bool) objc.ID {
	if c := control(target); c != nil && !c.closed {
		for _, item := range []objc.ID{c.spacer, c.group, c.titleItem} {
			if objc.Send[bool](identifier, sel("isEqualToString:"), item.Send(sel("itemIdentifier"))) {
				return item
			}
		}
	}
	return 0
}
func navigateBack(target objc.ID, _ objc.SEL, _ objc.ID)    { navigate(target, -1) }
func navigateForward(target objc.ID, _ objc.SEL, _ objc.ID) { navigate(target, 1) }
func navigate(target objc.ID, delta int) {
	if c := control(target); c != nil && !c.closed && c.onNavigate != nil {
		c.onNavigate(delta)
	}
}
func array(items ...objc.ID) objc.ID {
	a := objc.ID(objc.GetClass("NSMutableArray")).Send(sel("new"))
	for _, item := range items {
		if item != 0 {
			a.Send(sel("addObject:"), item)
		}
	}
	return a
}
func symbol(name, label string) objc.ID {
	return objc.ID(objc.GetClass("NSImage")).Send(sel("imageWithSystemSymbolName:accessibilityDescription:"), ns(name), ns(label))
}
func responds(id objc.ID, name string) bool {
	return objc.Send[bool](id, sel("respondsToSelector:"), sel(name))
}
func ns(value string) objc.ID {
	return objc.ID(objc.GetClass("NSString")).Send(sel("stringWithUTF8String:"), value+"\x00")
}
func sel(name string) objc.SEL { return objc.RegisterName(name) }

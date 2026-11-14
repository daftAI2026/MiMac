package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"os"
)

// Setup cards are quieter than the denser settings surfaces. Controls keep
// their existing surface role, so they remain visible inside the card.
func onboardingGroup(c *ui.Context, a appearance, content func()) {
	a.theme.Surface = a.theme.Text.Alpha(.02).Over(a.theme.Background)
	settingsGroup(c, a, content)
}

// Ask the host for the installed system artwork rather than redistributing
// OS resources or recreating their color and material in application code.
func (s *shell) prepareOnboardingPermissionsArtwork() {
	if s.onboarding.permissionIcon != nil {
		return
	}
	const path = "/System/Library/ExtensionKit/Extensions/SecurityPrivacyExtension.appex"
	if _, err := os.Stat(path); err != nil {
		return
	}
	data, err := mygo.Shell.FileIcon(path, 96)
	if err != nil {
		return
	}
	if bitmap, err := ui.DecodeBitmap(data); err == nil {
		s.onboarding.permissionIcon = bitmap
	}
}

func (s *shell) onboardingPermissionsIcon(c *ui.Context, a appearance, granted bool) {
	ui.Box(c).Size(48, 48).Center().Children(func() {
		if s.onboarding.permissionIcon != nil {
			ui.Image(c, s.onboarding.permissionIcon).Size(48, 48).Fit(ui.Contain).Role(ui.RoleNone)
		} else {
			ui.Box(c).Size(42, 42).Background(a.theme.Text).Radius(10).Center().Children(func() {
				key := symbolKey{name: "hand.raised.fill", dark: a.theme.Dark, size: onboardingPermissionIconSize, tint: a.theme.Background}
				if icon := s.navigationSymbols[key]; icon != nil {
					ui.Image(c, icon).Size(onboardingPermissionIconSize, onboardingPermissionIconSize).Fit(ui.Contain)
				}
			})
		}
		if granted {
			color := s.permissionGuideCompletionBadgeColor()
			ui.Box(c).Absolute().Left(32).Top(32).Size(16, 16).Radius(8).Background(color).Center().Children(func() {
				key := symbolKey{name: "checkmark", dark: a.theme.Dark, size: 10, tint: ui.RGB(255, 255, 255)}
				if icon := s.navigationSymbols[key]; icon != nil {
					ui.Image(c, icon).Size(10, 10).Fit(ui.Contain).Role(ui.RoleNone)
				} else {
					ui.Text(c, "✓").FontSize(12).TextColor(ui.RGB(255, 255, 255)).Role(ui.RoleNone)
				}
			})
		}
	})
}

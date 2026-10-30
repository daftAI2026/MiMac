package main

import (
	"bytes"
	"image/png"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	brand "mimac.local/mimac/Assets/Brand"
	social "mimac.local/mimac/Assets/Social"
)

// Fixed destinations are app metadata, not URLs supplied by device plugins.
var aboutLinks = []struct{ name, icon, url string }{
	{"GitHub", "github", "https://github.com/daftAI2026/MiMac"},
	{"X / Twitter", "x", "https://twitter.com/i/user/871000155767488513"},
	{"小红书", "xiaohongshu", "https://www.xiaohongshu.com/user/profile/5f4696c800000000010011f6"},
}

func (s *shell) aboutSettings(c *ui.Context, a appearance) {
	if s.appIcon == nil {
		if img, err := png.Decode(bytes.NewReader(brand.AboutIconPNG)); err == nil {
			s.appIcon = ui.NewBitmap(img)
		}
	}
	if s.socialIcons == nil {
		s.socialIcons = map[string]*ui.SVG{
			"github": ui.MustParseSVG([]byte(social.GitHub)), "x": ui.MustParseSVG([]byte(social.X)), "xiaohongshu": ui.MustParseSVG([]byte(social.Xiaohongshu)),
		}
	}
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).FillWidth().Gap(sectionGap).Children(func() {
			ui.Column(c).FillWidth().Padding(spaceSM, 0, spaceLG, 0).Gap(spaceSM).AlignItems(ui.Center).Children(func() {
				if s.appIcon != nil {
					ui.Image(c, s.appIcon).Width(64).Height(64).Fit(ui.Contain).Label("MiMac Logo")
				}
				ui.Column(c).Gap(spaceXS).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "MiMac").FontSize(a.fonts.title).FontWeight(600)
					ui.Text(c, "版本 "+mygo.App.Version()).FontSize(a.fonts.body).TextColor(a.theme.TextMuted)
				})
			})
			ui.Column(c).FillWidth().Gap(spaceSM).Children(func() {
				ui.Text(c, "社交").FontSize(a.fonts.body).FontWeight(600).Padding(0, settingsRowInset).Role(ui.RoleHeading).Level(2)
				ui.Column(c).FillWidth().Radius(settingsGroupRadius).Background(a.theme.Surface).Children(func() {
					for i, link := range aboutLinks {
						if i > 0 {
							ui.Box(c).FillWidth().Padding(0, settingsRowInset).Children(func() { sectionLine(c, a) })
						}
						row := ui.ButtonBase(c).Key("social/"+link.icon).FillWidth().MinHeight(settingsRowHeight).
							Padding(settingsRowPadding, settingsRowInset).Gap(spaceSM).AlignItems(ui.Center).
							Justify(ui.Start).FocusRing(false).Cursor(ui.CursorPointer).Role(ui.RoleLink).Label(link.name)
						// The clickable row includes its horizontal padding. Only the group's
						// outside corners round, so hover and focus follow the same boundary.
						var top, bottom float32
						if i == 0 {
							top = settingsGroupRadius
						}
						if i == len(aboutLinks)-1 {
							bottom = settingsGroupRadius
						}
						row.Radius(top, top, bottom, bottom)
						if row.Hovered() {
							row.Background(a.theme.SurfaceHover)
						}
						if row.Pressed() {
							row.Background(a.theme.SurfacePressed)
						}
						if row.FocusVisible() {
							row.Border(lineWidth, a.theme.Focus)
						}
						row.Children(func() {
							ui.Box(c).Width(settingsIconSize).Height(settingsIconSize).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
								icon := ui.Icon(c, s.socialIcons[link.icon]).TextColor(a.theme.Text)
								if link.icon == "xiaohongshu" {
									icon.Width(settingsIconSize).Height(settingsIconSize / 2)
								} else {
									icon.Width(settingsIconSize).Height(settingsIconSize)
								}
							})
							ui.Text(c, link.name).FontSize(a.fonts.body).Grow(1)
							if icon := s.systemSymbol("arrow.up.right", a.theme.Dark, accessoryIconSize); icon != nil {
								ui.Image(c, icon).Width(accessoryIconSize).Height(accessoryIconSize).Fit(ui.Contain).Shrink(0)
							}
						})
						if row.Clicked() {
							s.aboutError = ""
							if err := mygo.Shell.OpenExternal(link.url); err != nil {
								s.aboutError = "链接打开失败"
							}
							c.Invalidate()
						}
					}
				})
			})
			if s.aboutError != "" {
				ui.Text(c, s.aboutError).FontSize(a.fonts.caption).TextColor(a.theme.Danger).Role(ui.RoleStatus)
			}
		})
	})
}

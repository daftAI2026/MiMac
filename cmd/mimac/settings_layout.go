package main

import "github.com/egoist/mygo/ui"

// settingsColumns shares equal column widths, spacing and top alignment across
// settings pages. An unmatched item keeps one column instead of stretching.
func settingsColumns(c *ui.Context, contents ...func()) ui.Element {
	return ui.Column(c).FillWidth().MinWidth(0).Gap(sectionGap).Children(func() {
		for row := 0; row < len(contents); row += 2 {
			ui.Row(c).FillWidth().MinWidth(0).Gap(sectionGap).AlignItems(ui.Start).Children(func() {
				for column := 0; column < 2; column++ {
					cell := ui.Column(c).Grow(1).Basis(0).MinWidth(0).Gap(sectionGap)
					if index := row + column; index < len(contents) {
						cell.Children(contents[index])
					}
				}
			})
		}
	})
}

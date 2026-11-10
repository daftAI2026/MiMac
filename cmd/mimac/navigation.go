package main

// Navigation history belongs to the settings window, not to device plugins.
// Opening a new destination clears the forward branch; repeated selections do
// not create entries. The cap prevents unbounded history in a menu-bar app.
const navigationHistoryLimit = 32

func (s *shell) navigate(page settingsPage) {
	if s.page == "" {
		s.page = pageMappings
	}
	if page == s.page {
		return
	}
	s.backPages = appendPage(s.backPages, s.page)
	s.forwardPages = nil
	s.page = page
	s.navigationChanged()
}

func appendPage(pages []settingsPage, page settingsPage) []settingsPage {
	pages = append(pages, page)
	if len(pages) > navigationHistoryLimit {
		pages = pages[len(pages)-navigationHistoryLimit:]
	}
	return pages
}

func (s *shell) navigateHistory(delta int) {
	from, to := &s.backPages, &s.forwardPages
	if delta == 1 {
		from, to = to, from
	} else if delta != -1 {
		return
	}
	if len(*from) == 0 {
		return
	}
	*to = appendPage(*to, s.page)
	s.page = (*from)[len(*from)-1]
	*from = (*from)[:len(*from)-1]
	s.navigationChanged()
}

func (s *shell) navigationChanged() {
	if s.page == pageGeneral {
		s.preferencesState.loginLoaded = false
	}
	if s.page == pageDevice && s.monitor == nil && !s.deviceReadBusy {
		s.detectDevice()
	}
	s.mappingError = ""
	s.endShortcutRecording()
	s.syncNavigation()
	s.syncGeneralControls()
	s.invalidateDeviceView()
}

func (s *shell) syncNavigation() {
	if s.navigation != nil {
		s.navigation.SetEnabled(len(s.backPages) > 0, len(s.forwardPages) > 0)
		page := s.page
		if page == "" {
			page = pageMappings
		}
		s.navigation.SetTitle(string(page))
		s.navigation.SetTitleSize(float64(textStyles(s.preferencesState.value.TextSize).title))
	}
}

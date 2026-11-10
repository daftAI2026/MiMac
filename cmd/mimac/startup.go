package main

import (
	"errors"
	"os"
)

type initialSettingsPlan struct {
	open     bool
	firstRun bool
}

func planInitialSettings(showSpecified, showRequested, firstRunCandidate bool) initialSettingsPlan {
	if showSpecified {
		return initialSettingsPlan{open: showRequested}
	}
	if firstRunCandidate {
		return initialSettingsPlan{open: true, firstRun: true}
	}
	return initialSettingsPlan{}
}

func firstRunSettingsPage(plan initialSettingsPlan) settingsPage {
	if plan.firstRun {
		return pageDevice
	}
	return ""
}

func firstRunSettingsRequired(preferencesLoadFailed bool, configPaths ...string) bool {
	if preferencesLoadFailed {
		return false
	}
	for _, path := range configPaths {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
			// Existing or unreadable state is treated as an existing installation.
			return false
		}
	}
	return true
}

func (s *shell) shouldPresentFirstRunSettings() bool {
	if s.preferencesState.loadFailed {
		return false
	}
	audioBackendPath, err := hostDataPath("audio-backend.json")
	if err != nil {
		return false
	}
	return firstRunSettingsRequired(
		s.preferencesState.loadFailed,
		s.preferencesState.path,
		s.mappingPath,
		s.toolPreferencesPath,
		audioBackendPath,
	)
}

// Persisting the default preferences after showSettings has created the window
// makes the existing preferences file suppress another automatic first run.
// The caller may ignore a persistence error: the settings window remains usable.
func (s *shell) markFirstRunSettingsShown() error {
	return s.savePreferences(s.preferencesState.value)
}

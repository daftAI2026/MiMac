package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
)

const preferencesVersion = 1

type hostPreferences struct {
	Version    int              `json:"version"`
	ShowInDock bool             `json:"show_in_dock"`
	Appearance mygo.ThemeSource `json:"appearance"`
	TextSize   int              `json:"text_size,omitempty"`
}

type preferencesState struct {
	value        hostPreferences
	path         string
	loadFailed   bool
	errorText    string
	loginError   string
	loginLoaded  bool
	loginEnabled bool
}

type preferencesDocument struct {
	Version    int               `json:"version"`
	ShowInDock *bool             `json:"show_in_dock"`
	Appearance *mygo.ThemeSource `json:"appearance"`
	TextSize   int               `json:"text_size,omitempty"`
}

func defaultHostPreferences() hostPreferences {
	return hostPreferences{Version: preferencesVersion, Appearance: mygo.ThemeSystem}
}

func hostPreferencesPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "MiMac", "preferences.json"), nil
}

func readHostPreferences(path string) (hostPreferences, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultHostPreferences(), nil
	}
	if err != nil {
		return hostPreferences{}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return hostPreferences{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16*1024 {
		return hostPreferences{}, errors.New("invalid preferences file")
	}
	data, err := io.ReadAll(io.LimitReader(f, 16*1024+1))
	if err != nil {
		return hostPreferences{}, err
	}
	if len(data) > 16*1024 {
		return hostPreferences{}, errors.New("preferences file too large")
	}

	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var doc preferencesDocument
	if err := d.Decode(&doc); err != nil {
		return hostPreferences{}, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return hostPreferences{}, errors.New("trailing preferences data")
	}
	if doc.Version != preferencesVersion || doc.ShowInDock == nil {
		return hostPreferences{}, errors.New("unsupported preferences file")
	}
	appearance := mygo.ThemeSystem
	if doc.Appearance != nil {
		appearance = *doc.Appearance
	}
	if !validAppearance(appearance) || !validTextSize(doc.TextSize) {
		return hostPreferences{}, errors.New("unsupported appearance preference")
	}
	return hostPreferences{Version: preferencesVersion, ShowInDock: *doc.ShowInDock, Appearance: appearance, TextSize: doc.TextSize}, nil
}

func saveHostPreferences(path string, value hostPreferences) error {
	if value.Version != preferencesVersion {
		return errors.New("unsupported preferences version")
	}
	if !validAppearance(value.Appearance) || !validTextSize(value.TextSize) {
		return errors.New("unsupported appearance preference")
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".preferences-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func validAppearance(source mygo.ThemeSource) bool {
	switch source {
	case mygo.ThemeSystem, mygo.ThemeLight, mygo.ThemeDark:
		return true
	default:
		return false
	}
}

// loadPreferences keeps a malformed or unreadable file intact and applies
// the fail-closed default (no Dock icon) for this process.
func (s *shell) loadPreferences() {
	p := &s.preferencesState
	p.value = defaultHostPreferences()
	p.loadFailed = false
	p.errorText = ""
	p.loginError = ""
	p.path, _ = hostPreferencesPath()
	if p.path == "" {
		p.loadFailed = true
		p.errorText = "设置文件位置不可用"
	} else {
		value, err := readHostPreferences(p.path)
		if err != nil {
			p.loadFailed = true
			p.errorText = "设置读取失败，原文件已保留"
		} else {
			p.value = value
		}
	}
	s.setDockActivationPolicy(p.value.ShowInDock)
}

// applySavedAppearance lets the host startup path honor the stored choice
// unless the user supplied an explicit appearance override.
func (s *shell) applySavedAppearance() {
	source := s.preferencesState.value.Appearance
	if !validAppearance(source) {
		source = mygo.ThemeSystem
	}
	mygo.Theme.SetSource(source)
}

func (s *shell) setDockActivationPolicy(enabled bool) {
	policy := mygo.ActivationPolicyAccessory
	if enabled {
		policy = mygo.ActivationPolicyRegular
	}
	mygo.App.SetActivationPolicy(policy)
}

// savePreferences commits one complete preferences value atomically and only
// updates memory after the rename succeeds.
func (s *shell) savePreferences(next hostPreferences) error {
	p := &s.preferencesState
	if p.loadFailed {
		if p.errorText == "" {
			p.errorText = "设置读取失败，原文件已保留"
		}
		return errors.New(p.errorText)
	}
	if p.path == "" {
		path, err := hostPreferencesPath()
		if err != nil {
			p.errorText = "设置文件位置不可用"
			return err
		}
		p.path = path
	}
	if err := saveHostPreferences(p.path, next); err != nil {
		p.errorText = "保存失败，设置未更改"
		return err
	}
	p.value = next
	p.errorText = ""
	return nil
}

// applyDockPreference persists before changing the live activation policy.
func (s *shell) applyDockPreference(enabled bool) error {
	next := s.preferencesState.value
	next.ShowInDock = enabled
	if err := s.savePreferences(next); err != nil {
		return err
	}
	s.setDockActivationPolicy(enabled)
	return nil
}

// applyAppearancePreference persists before changing MyGo's effective theme.
func (s *shell) applyAppearancePreference(source mygo.ThemeSource) error {
	if !validAppearance(source) {
		s.preferencesState.errorText = "外观选项无效"
		return errors.New("unsupported appearance preference")
	}
	next := s.preferencesState.value
	next.Appearance = source
	if err := s.savePreferences(next); err != nil {
		return err
	}
	mygo.Theme.SetSource(source)
	return nil
}

type loginItemController interface {
	OpenAtLogin() bool
	SetOpenAtLogin(bool) error
}

func setLoginAtStartup(controller loginItemController, enabled bool) error {
	if controller == nil {
		return errors.New("login item controller is unavailable")
	}
	if controller.OpenAtLogin() == enabled {
		return nil
	}
	if err := controller.SetOpenAtLogin(enabled); err != nil {
		return err
	}
	if controller.OpenAtLogin() != enabled {
		return errors.New("login item state did not change")
	}
	return nil
}

func (s *shell) applyLoginAtStartup(enabled bool) error {
	if err := setLoginAtStartup(mygo.App, enabled); err != nil {
		s.preferencesState.loginError = "登录启动设置失败"
		return err
	}
	s.preferencesState.loginError = ""
	return nil
}

func validTextSize(size int) bool { return size >= -1 && size <= 1 }

func (s *shell) applyTextSizePreference(size int) error {
	if !validTextSize(size) {
		return errors.New("unsupported text size")
	}
	next := s.preferencesState.value
	next.TextSize = size
	if err := s.savePreferences(next); err != nil {
		return err
	}
	s.syncNavigation()
	s.invalidateDeviceView()
	return nil
}

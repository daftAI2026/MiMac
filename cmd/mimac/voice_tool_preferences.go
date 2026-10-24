package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mimac.local/mimac/internal/voicetools"
)

type toolPreferences struct {
	Version int      `json:"version"`
	Enabled []string `json:"enabled"`
	Current string   `json:"current,omitempty"`
}

func validToolPreferences(value toolPreferences) bool {
	if value.Version != 1 || len(value.Enabled) > 64 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range value.Enabled {
		if id == "" || len(id) > 128 || strings.TrimSpace(id) != id || seen[id] {
			return false
		}
		seen[id] = true
	}
	return value.Current == "" || seen[value.Current]
}

func readToolPreferences(path string) (toolPreferences, error) {
	data, err := voicetools.ReadRegularFile(context.Background(), path, 16*1024)
	if errors.Is(err, voicetools.ErrFileMissing) {
		return toolPreferences{Version: 1}, nil
	}
	if err != nil {
		return toolPreferences{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value toolPreferences
	if err := decoder.Decode(&value); err != nil {
		return toolPreferences{}, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || !validToolPreferences(value) {
		return toolPreferences{}, errors.New("unsupported tool preferences")
	}
	return value, nil
}

func saveToolPreferences(path string, value toolPreferences) error {
	if path == "" || !validToolPreferences(value) {
		return errors.New("invalid tool preferences")
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".tools-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

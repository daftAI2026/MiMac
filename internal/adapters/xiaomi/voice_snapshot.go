package xiaomi

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"howett.net/plist"
)

// Registry IDs are only meaningful within one boot. Recovery must check both
// this identity and the current port before restoring any saved mapping.
type voiceMappingSnapshot struct {
	Version    uint64              `plist:"version"`
	BootID     string              `plist:"boot_id"`
	RegistryID uint64              `plist:"registry_id"`
	Original   []voiceMappingEntry `plist:"original"`
}

type fileVoiceMappingSnapshots struct {
	path   string
	bootID string
}

func (s *fileVoiceMappingSnapshots) HasPending() bool {
	_, err := os.Lstat(s.path)
	return !errors.Is(err, os.ErrNotExist)
}

func (s *fileVoiceMappingSnapshots) Save(registryID uint64, original []voiceMappingEntry) error {
	if s.bootID == "" || registryID == 0 {
		return errors.New("xiaomi voice mapping: recovery identity unavailable")
	}
	if original == nil {
		original = []voiceMappingEntry{}
	}
	data, err := plist.Marshal(voiceMappingSnapshot{Version: 1, BootID: s.bootID, RegistryID: registryID, Original: original}, plist.BinaryFormat)
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("xiaomi voice mapping: recovery record too large")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	// Never replace a previous recovery record, including a partial record
	// left by a crash. An incomplete record blocks writes until inspected.
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return s.syncDirectory()
}

func (s *fileVoiceMappingSnapshots) Load() (voiceMappingSnapshot, error) {
	var snapshot voiceMappingSnapshot
	info, err := os.Lstat(s.path)
	if err != nil {
		return snapshot, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return snapshot, errors.New("xiaomi voice mapping: invalid recovery record")
	}
	f, err := os.Open(s.path)
	if err != nil {
		return snapshot, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return snapshot, err
	}
	if len(data) > 1<<20 {
		return snapshot, errors.New("xiaomi voice mapping: recovery record too large")
	}
	if _, err := plist.Unmarshal(data, &snapshot); err != nil {
		return snapshot, err
	}
	if snapshot.Version != 1 || snapshot.RegistryID == 0 || s.bootID == "" || snapshot.BootID != s.bootID {
		return snapshot, fmt.Errorf("xiaomi voice mapping: recovery identity does not match this boot")
	}
	return snapshot, nil
}

func (s *fileVoiceMappingSnapshots) Clear() error {
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.syncDirectory()
}

func (s *fileVoiceMappingSnapshots) syncDirectory() error {
	f, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

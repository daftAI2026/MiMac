//go:build darwin

package xiaomi

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// reserveVoiceKey is called only for an explicit device takeover, never from
// discovery or merely enabling a tool adapter. Native identity is revalidated
// by the port for every mapping operation.
func reserveVoiceKey(rawRegistryID uint64, snapshotPath string) (*VoiceKeyReservation, error) {
	if snapshotPath == "" {
		return nil, errors.New("xiaomi voice mapping: snapshot path is required")
	}
	if err := os.MkdirAll(filepath.Dir(snapshotPath), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(snapshotPath+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, errors.Join(ErrVoiceMappingSnapshotPending, err)
	}
	keepLock := false
	defer func() {
		if !keepLock {
			_ = lock.Close()
		}
	}()
	bootID, err := syscall.Sysctl("kern.bootsessionuuid")
	if err != nil || bootID == "" {
		return nil, errors.Join(errors.New("xiaomi voice mapping: boot identity unavailable"), err)
	}
	port, err := openVoiceMappingPort(rawRegistryID)
	if err != nil {
		return nil, err
	}
	store := &fileVoiceMappingSnapshots{path: snapshotPath, bootID: bootID}
	if err := recoverVoiceMapping(port, store); err != nil {
		return nil, errors.Join(err, port.Close())
	}
	reservation := &VoiceKeyReservation{lease: newVoiceMappingLease(port, store), port: port, unlock: lock.Close}
	keepLock = true // The reservation now owns the descriptor on every outcome.
	if err := reservation.lease.acquire(); err != nil {
		if cleanupErr := reservation.Close(); cleanupErr != nil {
			// The caller must retain this owner even though acquisition failed.
			return reservation, errors.Join(err, cleanupErr)
		}
		return nil, err
	}
	return reservation, nil
}

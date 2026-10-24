package xiaomi

import (
	"errors"
	"fmt"
)

type closableVoiceMappingPort interface {
	voiceMappingPort
	Close() error
}

// voiceMappingGoneChecker is implemented only by the Darwin port, which can
// prove that the exact event-service registry ID is absent in this process's
// boot. A normal release error alone never permits dropping the snapshot.
type voiceMappingGoneChecker interface {
	voiceMappingServiceGone() (bool, error)
}

type voiceMappingSnapshotReader interface {
	Load() (voiceMappingSnapshot, error)
	currentBootID() string
}

// VoiceKeyReservation owns only this device's original voice-key mapping.
// The host must stop its active session before Close. A failed Close retains
// the lease for recovery; it must not be treated as successfully disabled.
type VoiceKeyReservation struct {
	lease                 *voiceMappingLease
	port                  closableVoiceMappingPort
	unlock                func() error
	goneSnapshotValidated bool
}

func (r *VoiceKeyReservation) Close() error {
	if r == nil {
		return nil
	}
	if r.port != nil {
		if r.lease == nil {
			return ErrVoiceMappingLeaseDependency
		}
		if releaseErr := r.lease.release(); releaseErr != nil {
			checker, ok := r.port.(voiceMappingGoneChecker)
			if !ok {
				return releaseErr
			}
			gone, err := checker.voiceMappingServiceGone()
			if err != nil {
				return errors.Join(releaseErr, fmt.Errorf("confirm removed voice mapping service: %w", err))
			}
			if !gone {
				return releaseErr
			}
			if err := r.clearSnapshotAfterServiceGone(); err != nil {
				return errors.Join(releaseErr, err)
			}
		}
		if err := r.port.Close(); err != nil {
			return err
		}
		r.port = nil
	}
	if r.unlock != nil {
		if err := r.unlock(); err != nil {
			return err
		}
		r.unlock = nil
	}
	return nil
}

func (r *VoiceKeyReservation) clearSnapshotAfterServiceGone() error {
	if r.lease == nil || r.lease.snapshots == nil || r.lease.baseline == nil || r.port == nil {
		return ErrVoiceMappingLeaseDependency
	}
	if r.lease.snapshots.HasPending() {
		reader, ok := r.lease.snapshots.(voiceMappingSnapshotReader)
		if !ok {
			return errors.New("xiaomi voice mapping: snapshot store cannot verify recovery identity")
		}
		snapshot, err := reader.Load()
		if err != nil {
			return fmt.Errorf("verify recovery snapshot after service removal: %w", err)
		}
		if snapshot.Version != 1 || snapshot.BootID == "" || snapshot.BootID != reader.currentBootID() || snapshot.RegistryID != r.port.RegistryID() ||
			!voiceMappingEntriesEqual(snapshot.Original, r.lease.baseline) {
			return ErrVoiceMappingOwnership
		}
		r.goneSnapshotValidated = true
	} else if !r.goneSnapshotValidated {
		return errors.New("xiaomi voice mapping: recovery snapshot disappeared before confirmed cleanup")
	}
	if err := r.lease.snapshots.Clear(); err != nil {
		return fmt.Errorf("clear recovery snapshot after confirmed service removal: %w", err)
	}
	r.lease.baseline = nil
	r.lease.owned = nil
	r.lease.active = false
	r.goneSnapshotValidated = false
	return nil
}

func (s *fileVoiceMappingSnapshots) currentBootID() string {
	return s.bootID
}

func recoverVoiceMapping(port voiceMappingPort, store *fileVoiceMappingSnapshots) error {
	if !store.HasPending() {
		return nil
	}
	snapshot, err := store.Load()
	if err != nil {
		return err
	}
	if snapshot.RegistryID != port.RegistryID() {
		return fmt.Errorf("%w: recovery record belongs to another device service", ErrVoiceMappingOwnership)
	}
	_, owned, err := installVoiceMapping(snapshot.Original)
	if err != nil {
		return err
	}
	baseline := cloneVoiceMappingEntries(snapshot.Original)
	if baseline == nil {
		baseline = []voiceMappingEntry{}
	}
	lease := &voiceMappingLease{port: port, snapshots: store, baseline: baseline, owned: owned}
	return lease.release()
}

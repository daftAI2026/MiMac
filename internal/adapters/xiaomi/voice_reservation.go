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

// VoiceKeyReservation owns this device's F5 mapping and any additional button
// mappings explicitly added with UpdateKeys. The host must stop its active
// session before updating or closing. A failed operation retains the owner for
// recovery; it must not be treated as successfully disabled.
type VoiceKeyReservation struct {
	lease                 *voiceMappingLease
	port                  closableVoiceMappingPort
	unlock                func() error
	goneSnapshotValidated bool
}

// UpdateKeys replaces the ordinary-button portion of the device mapping lease
// while retaining the mandatory F5 reservation. buttonIDs must be IDs from the
// active Xiaomi profile; the voice button is already included. The call is
// synchronous and must run at an idle boundary on the monitor's main thread.
// The host must keep all mapped keyboard output disabled until it succeeds.
func (r *VoiceKeyReservation) UpdateKeys(buttonIDs []string) error {
	if r == nil || r.port == nil || r.lease == nil {
		return ErrVoiceMappingLeaseDependency
	}
	sources, err := voiceMappingSourcesForButtonIDs(buttonIDs)
	if err != nil {
		return err
	}
	if r.lease.active && voiceMappingSourcesEqual(r.lease.managedSources(), sources) {
		// A new chord can keep the same physical source set. Confirm that
		// another mapper has not changed those entries before accepting it.
		current, err := r.port.Read()
		if err != nil {
			return fmt.Errorf("verify current device mapping lease: %w", err)
		}
		indexes, err := mappingIndexesForSources(current, sources)
		if err != nil {
			return err
		}
		for _, source := range sources {
			matches := indexes[source]
			if len(matches) != 1 || !voiceMappingEntryEqual(current[matches[0]], r.lease.ownedBySource[source]) {
				return ErrVoiceMappingOwnership
			}
		}
		return nil
	}
	if err := r.lease.release(); err != nil {
		return fmt.Errorf("release previous device mapping lease: %w", err)
	}

	updated := newVoiceMappingLeaseWithSources(r.port, r.lease.snapshots, sources)
	r.lease = updated // Retain the new recovery state even if its acquire fails.
	if err := updated.acquire(); err != nil {
		return fmt.Errorf("acquire updated device mapping lease: %w", err)
	}
	r.goneSnapshotValidated = false
	return nil
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
		sources, sourceErr := snapshot.managedSources()
		if sourceErr != nil || snapshot.BootID == "" || snapshot.BootID != reader.currentBootID() || snapshot.RegistryID != r.port.RegistryID() ||
			!voiceMappingSourcesEqual(sources, r.lease.managedSources()) || !voiceMappingEntriesEqual(snapshot.Original, r.lease.baseline) {
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
	r.lease.ownedBySource = nil
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
	sources, err := snapshot.managedSources()
	if err != nil {
		return fmt.Errorf("validate recovery source set: %w", err)
	}
	_, ownedBySource, err := installVoiceMappings(snapshot.Original, sources)
	if err != nil {
		return err
	}
	baseline := cloneVoiceMappingEntries(snapshot.Original)
	if baseline == nil {
		baseline = []voiceMappingEntry{}
	}
	lease := newVoiceMappingLeaseWithSources(port, store, sources)
	lease.baseline = baseline
	lease.ownedBySource = cloneVoiceMappingEntriesBySource(ownedBySource)
	return lease.release()
}

package xiaomi

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestVoiceMappingRecoversOnlySameBootAndService(t *testing.T) {
	store := &fileVoiceMappingSnapshots{path: filepath.Join(t.TempDir(), "recovery.plist"), bootID: "boot-a"}
	if err := store.Save(42, nil); err != nil {
		t.Fatal(err)
	}
	owned, _, err := installVoiceMapping(nil)
	if err != nil {
		t.Fatal(err)
	}
	port := &fakeVoiceMappingPort{registryID: 43, entries: owned}
	if err := recoverVoiceMapping(port, store); !errors.Is(err, ErrVoiceMappingOwnership) {
		t.Fatalf("wrong service recovery: %v", err)
	}
	if port.writes != 0 || !store.HasPending() {
		t.Fatal("changed wrong service or discarded record")
	}
	port.registryID = 42
	if err := recoverVoiceMapping(port, store); err != nil {
		t.Fatal(err)
	}
	if len(port.entries) != 0 || port.writes != 1 || store.HasPending() {
		t.Fatal("recovery did not restore and clear")
	}
}

type closingVoiceMappingPort struct {
	fakeVoiceMappingPort
	closes int
}

func (p *closingVoiceMappingPort) Close() error { p.closes++; return nil }

type disconnectAwareVoiceMappingPort struct {
	closingVoiceMappingPort
	disconnected bool
	gone         bool
	goneErr      error
	goneChecks   int
	goneID       uint64
}

func (p *disconnectAwareVoiceMappingPort) Read() ([]voiceMappingEntry, error) {
	if p.disconnected {
		return nil, errors.New("event service disconnected")
	}
	return p.fakeVoiceMappingPort.Read()
}

func (p *disconnectAwareVoiceMappingPort) voiceMappingServiceGone() (bool, error) {
	p.goneChecks++
	p.goneID = p.RegistryID()
	return p.gone, p.goneErr
}

func (s *fakeVoiceMappingSnapshots) Load() (voiceMappingSnapshot, error) {
	if !s.pending {
		return voiceMappingSnapshot{}, errors.New("no pending snapshot")
	}
	return voiceMappingSnapshot{
		Version: s.version, BootID: "test-boot", RegistryID: s.registryID,
		Original: cloneVoiceMappingEntries(s.original), Sources: append([]uint64(nil), s.sources...),
	}, nil
}

func (*fakeVoiceMappingSnapshots) currentBootID() string { return "test-boot" }

type mismatchedBootVoiceMappingSnapshots struct {
	*fakeVoiceMappingSnapshots
}

func (s *mismatchedBootVoiceMappingSnapshots) Load() (voiceMappingSnapshot, error) {
	snapshot, err := s.fakeVoiceMappingSnapshots.Load()
	if err != nil {
		return snapshot, err
	}
	snapshot.BootID = "previous-boot"
	return snapshot, nil
}

func (*mismatchedBootVoiceMappingSnapshots) currentBootID() string { return "current-boot" }

func TestVoiceReservationRetainsPortUntilRestoreSucceeds(t *testing.T) {
	p := &closingVoiceMappingPort{fakeVoiceMappingPort: fakeVoiceMappingPort{registryID: 42}}
	s := &fakeVoiceMappingSnapshots{}
	l := newVoiceMappingLease(p, s)
	if err := l.acquire(); err != nil {
		t.Fatal(err)
	}
	r := &VoiceKeyReservation{lease: l, port: p}
	p.writeErr = errors.New("restore failed")
	if err := r.Close(); err == nil || p.closes != 0 || !s.pending {
		t.Fatal("failed restoration discarded owner")
	}
	p.writeErr = nil
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if p.closes != 1 || s.pending {
		t.Fatal("Close was not idempotent after success")
	}
}

func TestVoiceReservationKeepsUncertainDisconnectAndSnapshot(t *testing.T) {
	p := &disconnectAwareVoiceMappingPort{closingVoiceMappingPort: closingVoiceMappingPort{
		fakeVoiceMappingPort: fakeVoiceMappingPort{registryID: 42},
	}}
	s := &fakeVoiceMappingSnapshots{}
	l := newVoiceMappingLease(p, s)
	if err := l.acquire(); err != nil {
		t.Fatal(err)
	}
	if p.writes != 1 {
		t.Fatalf("setup writes = %d, want one install write", p.writes)
	}
	p.disconnected = true
	r := &VoiceKeyReservation{lease: l, port: p}
	unlocks := 0
	r.unlock = func() error { unlocks++; return nil }

	if err := r.Close(); err == nil {
		t.Fatal("Close accepted an unconfirmed service loss")
	}
	if !s.pending || p.closes != 0 || unlocks != 0 || p.writes != 1 {
		t.Fatalf("uncertain close changed state: pending=%t closes=%d unlocks=%d writes=%d", s.pending, p.closes, unlocks, p.writes)
	}
	p.goneErr = errors.New("registry query unavailable")
	if err := r.Close(); err == nil {
		t.Fatal("Close accepted an inconclusive registry check")
	}
	if !s.pending || p.closes != 0 || unlocks != 0 || p.writes != 1 {
		t.Fatalf("inconclusive close changed state: pending=%t closes=%d unlocks=%d writes=%d", s.pending, p.closes, unlocks, p.writes)
	}
}

func TestVoiceReservationConfirmedGoneClearsSnapshotWithoutWriting(t *testing.T) {
	p := &disconnectAwareVoiceMappingPort{closingVoiceMappingPort: closingVoiceMappingPort{
		fakeVoiceMappingPort: fakeVoiceMappingPort{registryID: 42},
	}, gone: true}
	s := &fakeVoiceMappingSnapshots{}
	l := newVoiceMappingLease(p, s)
	if err := l.acquire(); err != nil {
		t.Fatal(err)
	}
	installWrites := p.writes
	p.disconnected = true
	r := &VoiceKeyReservation{lease: l, port: p}
	unlocks := 0
	r.unlock = func() error { unlocks++; return nil }
	if err := r.Close(); err != nil {
		t.Fatalf("Close() after confirmed service loss: %v", err)
	}
	if s.pending || s.clears != 1 || p.closes != 1 || unlocks != 1 {
		t.Fatalf("confirmed gone cleanup: pending=%t clears=%d closes=%d unlocks=%d", s.pending, s.clears, p.closes, unlocks)
	}
	if p.writes != installWrites {
		t.Fatalf("service gone cleanup wrote mappings: before=%d after=%d", installWrites, p.writes)
	}
	if p.goneChecks != 1 || p.goneID != 42 {
		t.Fatalf("gone checks = %d for service ID %d, want 1 for 42", p.goneChecks, p.goneID)
	}
}

func TestVoiceReservationGoneKeepsForeignSnapshotAndLock(t *testing.T) {
	p := &disconnectAwareVoiceMappingPort{closingVoiceMappingPort: closingVoiceMappingPort{
		fakeVoiceMappingPort: fakeVoiceMappingPort{registryID: 42},
	}, gone: true}
	s := &fakeVoiceMappingSnapshots{}
	l := newVoiceMappingLease(p, s)
	if err := l.acquire(); err != nil {
		t.Fatal(err)
	}
	s.registryID = 99 // Simulate a replaced recovery record for another service.
	p.disconnected = true
	r := &VoiceKeyReservation{lease: l, port: p}
	unlocks := 0
	r.unlock = func() error { unlocks++; return nil }
	if err := r.Close(); !errors.Is(err, ErrVoiceMappingOwnership) {
		t.Fatalf("Close() error = %v, want ownership refusal", err)
	}
	if !s.pending || p.closes != 0 || unlocks != 0 || p.writes != 1 {
		t.Fatalf("foreign snapshot cleanup changed state: pending=%t closes=%d unlocks=%d writes=%d", s.pending, p.closes, unlocks, p.writes)
	}
}

func TestVoiceReservationGoneKeepsPreviousBootSnapshotAndLock(t *testing.T) {
	p := &disconnectAwareVoiceMappingPort{closingVoiceMappingPort: closingVoiceMappingPort{
		fakeVoiceMappingPort: fakeVoiceMappingPort{registryID: 42},
	}, gone: true}
	baseStore := &fakeVoiceMappingSnapshots{}
	s := &mismatchedBootVoiceMappingSnapshots{fakeVoiceMappingSnapshots: baseStore}
	l := newVoiceMappingLease(p, s)
	if err := l.acquire(); err != nil {
		t.Fatal(err)
	}
	p.disconnected = true
	r := &VoiceKeyReservation{lease: l, port: p}
	unlocks := 0
	r.unlock = func() error { unlocks++; return nil }
	if err := r.Close(); !errors.Is(err, ErrVoiceMappingOwnership) {
		t.Fatalf("Close() error = %v, want boot ownership refusal", err)
	}
	if !baseStore.pending || p.closes != 0 || unlocks != 0 || p.writes != 1 {
		t.Fatalf("previous boot cleanup changed state: pending=%t closes=%d unlocks=%d writes=%d", baseStore.pending, p.closes, unlocks, p.writes)
	}
}

func TestVoiceReservationRetriesGoneSnapshotDirectorySync(t *testing.T) {
	p := &disconnectAwareVoiceMappingPort{closingVoiceMappingPort: closingVoiceMappingPort{
		fakeVoiceMappingPort: fakeVoiceMappingPort{registryID: 42},
	}, gone: true}
	s := &fakeVoiceMappingSnapshots{clearErr: errors.New("directory sync failed")}
	l := newVoiceMappingLease(p, s)
	if err := l.acquire(); err != nil {
		t.Fatal(err)
	}
	p.disconnected = true
	r := &VoiceKeyReservation{lease: l, port: p}
	unlocks := 0
	r.unlock = func() error { unlocks++; return nil }
	if err := r.Close(); err == nil {
		t.Fatal("Close succeeded before the snapshot clear was durable")
	}
	if !s.pending || p.closes != 0 || unlocks != 0 {
		t.Fatalf("failed clear released ownership: pending=%t closes=%d unlocks=%d", s.pending, p.closes, unlocks)
	}
	s.clearErr = nil
	if err := r.Close(); err != nil {
		t.Fatalf("retry Close(): %v", err)
	}
	if s.pending || p.closes != 1 || unlocks != 1 {
		t.Fatalf("retry cleanup: pending=%t closes=%d unlocks=%d", s.pending, p.closes, unlocks)
	}
}

func TestMonitorReservesOnlyOnExplicitRequestWhileOpen(t *testing.T) {
	calls := 0
	want := &VoiceKeyReservation{}
	m := &Monitor{reserveVoiceFn: func(path string) (*VoiceKeyReservation, error) {
		calls++
		if path != "recovery.plist" {
			t.Fatal("snapshot path changed")
		}
		return want, nil
	}}
	if calls != 0 {
		t.Fatal("monitor implicitly reserved voice")
	}
	got, err := m.ReserveVoiceKey("recovery.plist")
	if got != want || err != nil || calls != 1 {
		t.Fatalf("explicit reserve: %v, %v", got, err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReserveVoiceKey("recovery.plist"); !errors.Is(err, ErrMonitorUnavailable) || calls != 1 {
		t.Fatalf("closed monitor allowed reservation: %v", err)
	}
}

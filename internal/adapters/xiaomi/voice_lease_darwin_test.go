//go:build darwin

package xiaomi

import (
	"errors"
	"reflect"
	"testing"
)

type fakeVoiceMappingNative struct {
	owner             uintptr
	service           uintptr
	serviceRegistryID uint64
	openErr           error
	verifyErr         error
	verifyAfterFirst  error
	verifyCalls       int
	serviceGoneID     uint64
	serviceIsGone     bool
	serviceGoneErr    error
	entries           []voiceMappingEntry
	readErr           error
	writeErr          error
	readCalls         int
	writeCalls        int
	writes            [][]voiceMappingEntry
	released          []uintptr
}

func (f *fakeVoiceMappingNative) open(uint64) (uintptr, uintptr, uint64, error) {
	return f.owner, f.service, f.serviceRegistryID, f.openErr
}

func (f *fakeVoiceMappingNative) verify(rawRegistryID, serviceRegistryID uint64) error {
	f.verifyCalls++
	if rawRegistryID != 101 || serviceRegistryID != f.serviceRegistryID {
		return errVoiceMappingServiceChanged
	}
	if f.verifyCalls > 1 && f.verifyAfterFirst != nil {
		return f.verifyAfterFirst
	}
	return f.verifyErr
}

func (f *fakeVoiceMappingNative) serviceGone(serviceRegistryID uint64) (bool, error) {
	f.serviceGoneID = serviceRegistryID
	if serviceRegistryID != f.serviceRegistryID {
		return false, errVoiceMappingServiceChanged
	}
	return f.serviceIsGone, f.serviceGoneErr
}

func (f *fakeVoiceMappingNative) readMappings(uintptr) ([]voiceMappingEntry, error) {
	f.readCalls++
	if f.readErr != nil {
		return nil, f.readErr
	}
	return cloneVoiceMappingEntries(f.entries), nil
}

func (f *fakeVoiceMappingNative) writeMappings(_ uintptr, entries []voiceMappingEntry) error {
	f.writeCalls++
	if f.writeErr != nil {
		return f.writeErr
	}
	f.writes = append(f.writes, cloneVoiceMappingEntries(entries))
	return nil
}

func (f *fakeVoiceMappingNative) releaseCF(object uintptr) {
	f.released = append(f.released, object)
}

func TestVoiceMappingPortVerifiesAndReleasesOwnedReferences(t *testing.T) {
	native := &fakeVoiceMappingNative{
		owner: 11, service: 22, serviceRegistryID: 202,
		entries: []voiceMappingEntry{{"vendor-field": "keep"}},
	}
	port, err := openVoiceMappingPortWithNative(101, native)
	if err != nil {
		t.Fatalf("openVoiceMappingPortWithNative() error = %v", err)
	}
	if got := port.RegistryID(); got != 202 {
		t.Fatalf("RegistryID() = %d, want event-service ID 202", got)
	}
	read, err := port.Read()
	if err != nil || !reflect.DeepEqual(read, native.entries) {
		t.Fatalf("Read() = %#v, %v; want %#v, nil", read, err, native.entries)
	}
	write := []voiceMappingEntry{{"HIDKeyboardModifierMappingSrc": uint64(7), "extra": []any{"preserve"}}}
	if err := port.Write(write); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if native.readCalls != 1 || native.writeCalls != 1 || native.verifyCalls != 3 {
		t.Fatalf("native calls = verify:%d read:%d write:%d, want 3/1/1", native.verifyCalls, native.readCalls, native.writeCalls)
	}
	if err := port.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := port.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if !reflect.DeepEqual(native.released, []uintptr{22, 11}) {
		t.Fatalf("released references = %v, want service then owner [22 11]", native.released)
	}
	if _, err := port.Read(); !errors.Is(err, errVoiceMappingPortClosed) {
		t.Fatalf("Read() after Close() error = %v, want %v", err, errVoiceMappingPortClosed)
	}
}

func TestVoiceMappingPortRejectsChangedIdentityBeforePropertyAccess(t *testing.T) {
	native := &fakeVoiceMappingNative{
		owner: 11, service: 22, serviceRegistryID: 202,
		verifyAfterFirst: errVoiceMappingServiceChanged,
	}
	port, err := openVoiceMappingPortWithNative(101, native)
	if err != nil {
		t.Fatalf("openVoiceMappingPortWithNative() error = %v", err)
	}
	if _, err := port.Read(); !errors.Is(err, errVoiceMappingServiceChanged) {
		t.Fatalf("Read() after identity change error = %v, want %v", err, errVoiceMappingServiceChanged)
	}
	if err := port.Write(nil); !errors.Is(err, errVoiceMappingServiceChanged) {
		t.Fatalf("Write() after identity change error = %v, want %v", err, errVoiceMappingServiceChanged)
	}
	if native.readCalls != 0 || native.writeCalls != 0 {
		t.Fatalf("property access after identity change = read:%d write:%d, want 0/0", native.readCalls, native.writeCalls)
	}
	if err := port.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestVoiceMappingPortGoneCheckUsesConcreteEventServiceID(t *testing.T) {
	native := &fakeVoiceMappingNative{
		owner: 11, service: 22, serviceRegistryID: 202, serviceIsGone: true,
	}
	port, err := openVoiceMappingPortWithNative(101, native)
	if err != nil {
		t.Fatalf("openVoiceMappingPortWithNative() error = %v", err)
	}
	gone, err := port.voiceMappingServiceGone()
	if err != nil || !gone {
		t.Fatalf("voiceMappingServiceGone() = %v, %v; want true, nil", gone, err)
	}
	if native.serviceGoneID != 202 {
		t.Fatalf("gone check ID = %d, want event-service ID 202", native.serviceGoneID)
	}
	if err := port.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestVoiceMappingPortOpenErrorReleasesPartialReferences(t *testing.T) {
	wantErr := errors.New("resolver failed after acquiring handles")
	native := &fakeVoiceMappingNative{owner: 11, service: 22, serviceRegistryID: 202, openErr: wantErr}
	if _, err := openVoiceMappingPortWithNative(101, native); !errors.Is(err, wantErr) {
		t.Fatalf("openVoiceMappingPortWithNative() error = %v, want %v", err, wantErr)
	}
	if !reflect.DeepEqual(native.released, []uintptr{22, 11}) {
		t.Fatalf("released partial references = %v, want service then owner [22 11]", native.released)
	}
}

func TestVoiceMappingPortVerifyFailureReleasesOpenedReferences(t *testing.T) {
	wantErr := errors.New("event service moved to another device")
	native := &fakeVoiceMappingNative{
		owner: 11, service: 22, serviceRegistryID: 202, verifyErr: wantErr,
	}
	if _, err := openVoiceMappingPortWithNative(101, native); !errors.Is(err, wantErr) {
		t.Fatalf("openVoiceMappingPortWithNative() error = %v, want %v", err, wantErr)
	}
	if !reflect.DeepEqual(native.released, []uintptr{22, 11}) {
		t.Fatalf("released references after verify failure = %v, want [22 11]", native.released)
	}
}

func TestVoiceMappingPlistRoundTripPreservesUnknownEntryFields(t *testing.T) {
	original := []voiceMappingEntry{{
		voiceMappingSourceKey:      uint64(0x000000070000003e),
		voiceMappingDestinationKey: uint64(0),
		"vendor-private-field":     map[string]any{"mode": "opaque", "values": []any{uint64(9), true}},
	}}
	encoded, err := encodeVoiceMappingEntries(original)
	if err != nil {
		t.Fatalf("encodeVoiceMappingEntries() error = %v", err)
	}
	decoded, err := decodeVoiceMappingEntries(encoded)
	if err != nil {
		t.Fatalf("decodeVoiceMappingEntries() error = %v", err)
	}
	if !voiceMappingEntriesEqual(decoded, original) {
		t.Fatalf("round trip = %#v, want all original entry fields %#v", decoded, original)
	}
}

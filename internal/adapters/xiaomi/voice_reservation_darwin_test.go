//go:build darwin

package xiaomi

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func TestVoiceReservationRefusesAnAlreadyOwnedRecoveryFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.plist")
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	// Rejection must occur before resolving this invalid native device ID.
	owner, err := reserveVoiceKey(0, path)
	if owner != nil || !errors.Is(err, ErrVoiceMappingSnapshotPending) {
		t.Fatalf("duplicate reservation: %v, %v", owner, err)
	}
}

// Explicit opt-in only: this briefly suppresses the unique connected Xiaomi
// voice key, checks native readback, then restores it. No input is synthesized,
// no HID monitoring or microphone is opened, and ordinary CI skips this test.
func TestVoiceReservationNativeRoundTrip(t *testing.T) {
	mode := os.Getenv("MIMAC_VOICE_MAPPING_LIVE")
	if mode != "read" && mode != "roundtrip" {
		t.Skip("requires explicit connected-device opt-in")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	devices, err := DiscoverHID()
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || !devices[0].KnownReportLayout {
		t.Fatalf("need one verified Xiaomi device, got %d", len(devices))
	}
	port, err := openVoiceMappingPort(devices[0].RegistryID)
	if err != nil {
		t.Fatal(err)
	}
	defer port.Close()
	original, err := port.Read()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("raw_registry=%d event_service_registry=%d original_entries=%d", devices[0].RegistryID, port.RegistryID(), len(original))
	if mode == "read" {
		return
	}
	// Keep a failed restore record outside t.TempDir, which deletes evidence.
	dir, err := os.MkdirTemp("", "mimac-voice-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mapping.plist")
	reservation, err := reserveVoiceKey(devices[0].RegistryID, path)
	if reservation != nil {
		defer func() {
			if err := reservation.Close(); err != nil {
				t.Errorf("restore failed, recovery retained at %s: %v", path, err)
			}
		}()
	}
	if err != nil {
		t.Fatalf("reserve, recovery path %s: %v", path, err)
	}
	current, err := port.Read()
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := installVoiceMapping(original)
	if err != nil || !voiceMappingEntriesEqual(current, want) {
		t.Fatalf("native write readback mismatch: %v", err)
	}
	if err := reservation.Close(); err != nil {
		t.Fatalf("restore, recovery path %s: %v", path, err)
	}
	restored, err := port.Read()
	if err != nil || !voiceMappingEntriesEqual(restored, original) {
		t.Fatalf("native restore mismatch: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery record not cleared: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	t.Log("mapping restored and recovery snapshot cleared")
}

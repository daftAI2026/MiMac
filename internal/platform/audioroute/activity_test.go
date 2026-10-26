package audioroute

import (
	"errors"
	"testing"
)

func activityFixture(value []byte) *fakePropertyAPI {
	f := newFakePropertyAPI()
	f.deviceIDs["owned-endpoint"] = 19
	f.uids[19] = "owned-endpoint"
	putFakeProperty(f, 19, propertyAddress{selector: selectorDeviceRunningSomewhere, scope: scopeGlobal}, nil, value)
	return f
}

func TestDeviceActivityDistinguishesMissingIdleAndActive(t *testing.T) {
	for _, running := range []uint32{0, 1} {
		f := activityFixture(u32(running))
		got, err := newInspector(f).DeviceActivityByUID("owned-endpoint")
		if err != nil || !got.Exists || got.Running != (running == 1) {
			t.Fatalf("activity=%+v error=%v", got, err)
		}
		if len(f.uidReads) != 2 || len(f.propertyReads) != 1 {
			t.Fatalf("unexpected queries: uid=%v property=%v", f.uidReads, f.propertyReads)
		}
	}
	f := activityFixture(u32(0))
	delete(f.deviceIDs, "owned-endpoint")
	// Fake lookup requires an explicit unknown ID for absence.
	f.deviceIDs["owned-endpoint"] = 0
	got, err := newInspector(f).DeviceActivityByUID("owned-endpoint")
	if err != nil || got.Exists || got.Running || len(f.propertyReads) != 0 {
		t.Fatalf("missing=%+v error=%v", got, err)
	}
}

func TestDeviceActivityErrorsAreNotIdle(t *testing.T) {
	for _, data := range [][]byte{nil, {1, 0}, u32(2)} {
		_, err := newInspector(activityFixture(data)).DeviceActivityByUID("owned-endpoint")
		if !errors.Is(err, ErrMalformedData) {
			t.Fatalf("invalid property %v accepted: %v", data, err)
		}
	}
	f := activityFixture(u32(0))
	fault := errors.New("query unavailable")
	f.dataErr[makeFakeKey(19, propertyAddress{selector: selectorDeviceRunningSomewhere, scope: scopeGlobal}, nil)] = fault
	if _, err := newInspector(f).DeviceActivityByUID("owned-endpoint"); !errors.Is(err, fault) {
		t.Fatalf("query failure lost: %v", err)
	}
}

type replacedActivityAPI struct {
	propertyAPI
	reads int
}

func (f *replacedActivityAPI) DeviceUID(id uint32) (string, error) {
	f.reads++
	if f.reads > 1 {
		return "replacement", nil
	}
	return f.propertyAPI.DeviceUID(id)
}

func TestDeviceActivityRejectsRecycledObjectID(t *testing.T) {
	f := &replacedActivityAPI{propertyAPI: activityFixture(u32(0))}
	if _, err := newInspector(f).DeviceActivityByUID("owned-endpoint"); !errors.Is(err, ErrMalformedData) {
		t.Fatalf("accepted recycled device: %v", err)
	}
}

func TestDeviceActivityValidatesIdentityBeforePropertyRead(t *testing.T) {
	f := activityFixture(u32(0))
	f.uids[19] = "replacement"
	if _, err := newInspector(f).DeviceActivityByUID("owned-endpoint"); !errors.Is(err, ErrMalformedData) || len(f.propertyReads) != 0 {
		t.Fatalf("read wrong device: %v", err)
	}
	if _, err := newInspector(f).DeviceActivityByUID(""); !errors.Is(err, ErrMalformedData) {
		t.Fatalf("accepted empty UID: %v", err)
	}
}

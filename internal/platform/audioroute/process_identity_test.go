package audioroute

import (
	"errors"
	"testing"
)

type replacedProcessAPI struct {
	*fakePropertyAPI
	afterUID func()
}

func (f *replacedProcessAPI) DeviceUID(id uint32) (string, error) {
	uid, err := f.fakePropertyAPI.DeviceUID(id)
	if f.afterUID != nil {
		f.afterUID()
	}
	return uid, err
}
func processIdentityFixture() *fakePropertyAPI {
	f := newFakePropertyAPI()
	putFakeProperty(f, systemObjectID, propertyAddress{selector: selectorTranslatePID, scope: scopeGlobal}, u32(42), u32(90))
	putFakeProperty(f, 90, propertyAddress{selector: selectorProcessPID, scope: scopeGlobal}, nil, u32(42))
	putFakeProperty(f, 90, propertyAddress{selector: selectorProcessRunning, scope: scopeGlobal}, nil, u32(1))
	putFakeProperty(f, 90, propertyAddress{selector: selectorProcessDevices, scope: scopeInput}, nil, objectIDsData(21))
	f.uids[21] = "wanted-input"
	return f
}
func TestProcessInputRejectsReplacedProcessObject(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(map[bool]string{false: "before snapshot", true: "during snapshot"}[during], func(t *testing.T) {
			f := processIdentityFixture()
			replace := func() {
				putFakeProperty(f, 90, propertyAddress{selector: selectorProcessPID, scope: scopeGlobal}, nil, u32(99))
			}
			api := &replacedProcessAPI{fakePropertyAPI: f}
			if during {
				api.afterUID = replace
			} else {
				replace()
			}
			got, err := newInspector(api).ProcessInput(42)
			if !during {
				for _, read := range f.propertyReads {
					if read.address.selector == selectorProcessRunning || read.address.selector == selectorProcessDevices {
						t.Fatal("foreign process route queried")
					}
				}
			}
			if !errors.Is(err, ErrMalformedData) || got.Found {
				t.Fatalf("replacement accepted: %+v, %v", got, err)
			}
		})
	}
}
func TestProcessInputIdentityReadFailureIsNotAbsence(t *testing.T) {
	f := processIdentityFixture()
	fault := errors.New("identity unavailable")
	f.dataErr[makeFakeKey(90, propertyAddress{selector: selectorProcessPID, scope: scopeGlobal}, nil)] = fault
	got, err := newInspector(f).ProcessInput(42)
	if !errors.Is(err, fault) || got.Found {
		t.Fatalf("identity failure hidden: %+v %v", got, err)
	}
}
func TestProcessInputStableIdentityRemainsObservable(t *testing.T) {
	got, err := newInspector(processIdentityFixture()).ProcessInput(42)
	if err != nil || !got.Found || got.PID != 42 || !got.RunningInput || len(got.InputDeviceUIDs) != 1 || got.InputDeviceUIDs[0] != "wanted-input" {
		t.Fatalf("valid route rejected: %+v %v", got, err)
	}
}

func TestProcessInputRejectsMalformedActualPID(t *testing.T) {
	for _, value := range [][]byte{{1, 2}, u32(0), u32(0xffffffff)} {
		f := processIdentityFixture()
		putFakeProperty(f, 90, propertyAddress{selector: selectorProcessPID, scope: scopeGlobal}, nil, value)
		if _, err := newInspector(f).ProcessInput(42); !errors.Is(err, ErrMalformedData) {
			t.Fatalf("invalid PID %v accepted: %v", value, err)
		}
	}
}

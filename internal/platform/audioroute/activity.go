package audioroute

import "fmt"

const selectorDeviceRunningSomewhere uint32 = 0x676f6e65 // 'gone'

// DeviceActivity is a point-in-time device-level observation. Running means
// at least one process is running IO on this exact device, not which process
// or whether any PCM has arrived. Missing devices have Exists=false.
type DeviceActivity struct {
	Exists  bool
	Running bool
}

// DeviceActivityByUID never opens a stream or changes routing. This can veto
// endpoint teardown while IO is active, but it cannot authorize teardown on
// its own: the host still has to stop and join its consumers first. Another
// process can start IO after this snapshot. Read errors are not idle results.
func (i *Inspector) DeviceActivityByUID(uid string) (DeviceActivity, error) {
	if uid == "" {
		return DeviceActivity{}, fmt.Errorf("%w: empty device UID", ErrMalformedData)
	}
	id, err := i.api.DeviceIDForUID(uid)
	if err != nil {
		return DeviceActivity{}, fmt.Errorf("audioroute: translate activity UID: %w", err)
	}
	if id == unknownObject {
		return DeviceActivity{}, nil
	}
	// Native object IDs may be recycled. Require UID readback on both sides of
	// the activity property rather than accepting a replacement device's state.
	actual, err := i.api.DeviceUID(id)
	if err != nil {
		return DeviceActivity{}, fmt.Errorf("audioroute: verify activity UID: %w", err)
	}
	if actual != uid {
		return DeviceActivity{}, fmt.Errorf("%w: activity device identity changed", ErrMalformedData)
	}
	data, err := i.readProperty(id, propertyAddress{selector: selectorDeviceRunningSomewhere, scope: scopeGlobal}, nil)
	if err != nil {
		return DeviceActivity{}, fmt.Errorf("audioroute: read device activity: %w", err)
	}
	running, err := scalar(data, "device activity")
	if err != nil {
		return DeviceActivity{}, err
	}
	if running > 1 {
		return DeviceActivity{}, fmt.Errorf("%w: invalid device activity", ErrMalformedData)
	}
	actual, err = i.api.DeviceUID(id)
	if err != nil {
		return DeviceActivity{}, fmt.Errorf("audioroute: verify activity UID: %w", err)
	}
	if actual != uid {
		return DeviceActivity{}, fmt.Errorf("%w: activity device identity changed", ErrMalformedData)
	}
	return DeviceActivity{Exists: true, Running: running == 1}, nil
}

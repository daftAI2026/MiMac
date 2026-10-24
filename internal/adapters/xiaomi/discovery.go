package xiaomi

// HIDDevice is an on-demand IORegistry snapshot, not an open input stream.
// RegistryID identifies an interface during this boot, not a BLE peripheral.
// Product IDs alone do not establish the model or the ATVV capabilities.
type HIDDevice struct {
	RegistryID        uint64
	Name              string
	Manufacturer      string
	Transport         string
	VendorID          uint16
	ProductID         uint16
	KnownReportLayout bool
}

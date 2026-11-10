//go:build !darwin

package permissionguide

func MainBundleIdentifier() (string, bool)     { return "", false }
func ReadBluetoothAccess() BluetoothAccess     { return BluetoothAccessUnknown }
func RequestBluetoothAccess() BluetoothAccess  { return BluetoothAccessUnknown }
func CancelBluetoothRequest()                  {}
func SystemSettingsWindowBounds() (Rect, bool) { return Rect{}, false }

func PrimaryMouseButtonDown() bool { return false }

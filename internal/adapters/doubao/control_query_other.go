//go:build !darwin

package doubao

type unsupportedControlSettingsTransport struct{}

func newControlSettingsTransport() controlSettingsTransport {
	return unsupportedControlSettingsTransport{}
}

func (unsupportedControlSettingsTransport) subscribe(*controlSettingsQuerySession) (controlSettingsSubscription, error) {
	return nil, ErrControlSettingsUnsupported
}

func (unsupportedControlSettingsTransport) postRequest(*controlSettingsQuerySession) error {
	return ErrControlSettingsUnsupported
}

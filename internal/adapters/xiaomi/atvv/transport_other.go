//go:build !darwin

package atvv

func OpenTransport(_ string, _ NativeEventHandler) (*Transport, error) {
	return nil, ErrTransportUnsupported
}

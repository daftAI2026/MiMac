//go:build !darwin

package processidentity

func nativeProcessReader() (processReader, error) {
	return nil, ErrUnsupported
}

//go:build !darwin

package applespeech

func validateLibraryPath(string) error { return nil }

func loadNativeBridge(string) (nativeBridge, error) {
	return nil, ErrUnsupported
}

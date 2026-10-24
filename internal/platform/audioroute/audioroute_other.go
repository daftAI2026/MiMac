//go:build !darwin

package audioroute

func newNativeAPI() (propertyAPI, error) { return nil, ErrUnsupportedPlatform }

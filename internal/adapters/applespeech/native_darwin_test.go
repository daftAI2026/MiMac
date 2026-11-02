package applespeech

import (
	"errors"
	"testing"
)

func TestNativeCreateErrorUsesStableCategories(t *testing.T) {
	for _, test := range []struct {
		name        string
		message     string
		want        error
		mustNotWant error
	}{
		{
			name:        "unsupported assets",
			message:     "assets unsupported: Apple Speech configuration zh-CN (status: unsupported)",
			want:        ErrUnsupported,
			mustNotWant: ErrAssetsUnavailable,
		},
		{
			name:    "assets unavailable",
			message: "assets unavailable: install the Apple Speech model for zh-CN (status: supported)",
			want:    ErrAssetsUnavailable,
		},
		{
			name:        "ordinary model error",
			message:     "model format incompatibility",
			mustNotWant: ErrAssetsUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := nativeCreateError(7, []byte(test.message))
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("nativeCreateError() = %v, want %v", err, test.want)
			}
			if test.mustNotWant != nil && errors.Is(err, test.mustNotWant) {
				t.Fatalf("nativeCreateError() = %v, must not wrap %v", err, test.mustNotWant)
			}
		})
	}
}

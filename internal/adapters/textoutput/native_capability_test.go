//go:build darwin

package textoutput

import (
	"errors"
	"testing"
)

func TestOperationMarkerCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		namesCode, markerCode int32
		marker                uintptr
		wantPermission        bool
	}{
		{name: "no_parameterized_attributes", namesCode: -25213},
		{name: "no_marker", markerCode: axErrorNoValue},
		{name: "unsupported_marker", markerCode: axErrorAttrUnsupported},
		{name: "complete", marker: 4},
		{name: "permission", markerCode: axErrorAPIDisabled, wantPermission: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			released := map[uintptr]int{}
			api := &nativeAPI{
				copyParameterizedNames: func(_ uintptr, out *uintptr) int32 {
					if tc.namesCode == 0 {
						*out = 1
					}
					return tc.namesCode
				},
				cfRelease:              func(v uintptr) { released[v]++ },
				cfGetTypeID:            func(uintptr) uintptr { return 2 },
				cfArrayGetTypeID:       func() uintptr { return 2 },
				cfArrayGetCount:        func(uintptr) int64 { return 1 },
				cfArrayGetValueAtIndex: func(uintptr, int64) uintptr { return 3 },
				cfStringCreateCString:  func(uintptr, *byte, uint32) uintptr { return 3 },
				cfEqual: func(a, b uintptr) uint8 {
					if a == b {
						return 1
					}
					return 0
				},
				copyAttributeValue: func(_, _ uintptr, out *uintptr) int32 { *out = tc.marker; return tc.markerCode },
			}
			marker, err := operationMarker(api, 10)
			if tc.wantPermission {
				if !errors.Is(err, ErrPermissionDenied) {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil || marker != tc.marker {
				t.Fatalf("marker=%d err=%v", marker, err)
			}
			if tc.namesCode == 0 && released[1] != 1 {
				t.Fatal("capability list leaked")
			}
			if released[4] != 0 {
				t.Fatal("captured marker released before its owner closes")
			}
		})
	}
}

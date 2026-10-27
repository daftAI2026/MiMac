//go:build darwin

package textoutput

import (
	"errors"
	"testing"
)

// The marker-only case mirrors Chromium's advertised-but-no-op AXSelectedText
// setter. A CF transport success must not let Capture choose that fallback.
func TestOperationMarkerCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		namesCode, markerCode     int32
		advertised                bool
		marker                    uintptr
		wantError, wantPermission bool
	}{
		{name: "native_without_parameterized_attributes", namesCode: axErrorParamUnsupported, markerCode: axErrorAttrUnsupported},
		{name: "native_without_marker", markerCode: axErrorNoValue},
		{name: "chromium_marker_without_operation", marker: 4, wantError: true},
		{name: "marker_with_unavailable_operation_list", namesCode: axErrorParamUnsupported, marker: 4, wantError: true},
		{name: "advertised_without_marker", advertised: true, markerCode: axErrorNoValue, wantError: true},
		{name: "advertised_unsupported_marker", advertised: true, markerCode: axErrorAttrUnsupported, wantError: true},
		{name: "complete", advertised: true, marker: 4},
		{name: "permission", advertised: true, markerCode: axErrorAPIDisabled, wantError: true, wantPermission: true},
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
				cfRelease:        func(v uintptr) { released[v]++ },
				cfGetTypeID:      func(uintptr) uintptr { return 2 },
				cfArrayGetTypeID: func() uintptr { return 2 },
				cfArrayGetCount:  func(uintptr) int64 { return 1 },
				cfArrayGetValueAtIndex: func(uintptr, int64) uintptr {
					if tc.advertised {
						return 3
					}
					return 30
				},
				cfStringCreateCString: func(uintptr, *byte, uint32) uintptr { return 3 },
				cfEqual: func(a, b uintptr) uint8 {
					if a == b {
						return 1
					}
					return 0
				},
				copyAttributeValue: func(_, _ uintptr, out *uintptr) int32 { *out = tc.marker; return tc.markerCode },
			}
			marker, err := operationMarker(api, 10)
			if tc.wantError {
				if err == nil || marker != 0 {
					t.Fatalf("incomplete capability allowed: marker=%d err=%v", marker, err)
				}
				if tc.wantPermission && !errors.Is(err, ErrPermissionDenied) {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil || marker != tc.marker {
				t.Fatalf("marker=%d err=%v", marker, err)
			}
			if tc.namesCode == 0 && released[1] != 1 {
				t.Fatal("capability list leaked")
			}
			wantReleased := 0
			if tc.wantError && tc.marker != 0 {
				wantReleased = 1
			}
			if released[4] != wantReleased {
				t.Fatalf("marker release count %d, want %d", released[4], wantReleased)
			}
		})
	}
}

//go:build darwin

package xiaomi

import (
	"errors"
	"testing"
)

func TestEnsureListenAccessWithoutRequest(t *testing.T) {
	tests := []struct {
		name    string
		access  int32
		wantErr bool
	}{
		{name: "granted", access: accessGranted},
		{name: "unknown", access: accessUnknown, wantErr: true},
		{name: "denied", access: accessDenied, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requestCalls int
			var checkValues []int32
			api := &monitorAPI{
				checkAccess: func(requestType int32) int32 {
					if requestType != listenEventRequestType {
						t.Errorf("checkAccess request type = %d, want %d", requestType, listenEventRequestType)
					}
					checkValues = append(checkValues, tt.access)
					return tt.access
				},
				requestAccess: func(requestType int32) bool {
					requestCalls++
					if requestType != listenEventRequestType {
						t.Errorf("requestAccess request type = %d, want %d", requestType, listenEventRequestType)
					}
					return true
				},
			}

			err := ensureListenAccessWithRequest(api, false)
			if tt.wantErr {
				if !errors.Is(err, ErrInputMonitoringRequired) {
					t.Fatalf("ensureListenAccessWithRequest() error = %v, want ErrInputMonitoringRequired", err)
				}
			} else if err != nil {
				t.Fatalf("ensureListenAccessWithRequest() error = %v, want nil", err)
			}
			if requestCalls != 0 {
				t.Fatalf("permission request calls = %d, want 0", requestCalls)
			}
			if len(checkValues) != 1 || checkValues[0] != tt.access {
				t.Fatalf("permission values read = %v, want [%d]", checkValues, tt.access)
			}
		})
	}
}

func TestEnsureListenAccessRetainsPromptingBehavior(t *testing.T) {
	listenAccessRequest.Lock()
	previousAttempted := listenAccessRequest.attempted
	listenAccessRequest.attempted = false
	listenAccessRequest.Unlock()
	t.Cleanup(func() {
		listenAccessRequest.Lock()
		listenAccessRequest.attempted = previousAttempted
		listenAccessRequest.Unlock()
	})

	var requestCalls int
	var checkValues []int32
	api := &monitorAPI{
		checkAccess: func(requestType int32) int32 {
			if requestType != listenEventRequestType {
				t.Errorf("checkAccess request type = %d, want %d", requestType, listenEventRequestType)
			}
			value := accessUnknown
			if len(checkValues) > 0 {
				value = accessGranted
			}
			checkValues = append(checkValues, value)
			return value
		},
		requestAccess: func(requestType int32) bool {
			requestCalls++
			if requestType != listenEventRequestType {
				t.Errorf("requestAccess request type = %d, want %d", requestType, listenEventRequestType)
			}
			return true
		},
	}

	if err := ensureListenAccess(api); err != nil {
		t.Fatalf("ensureListenAccess() error = %v, want nil after permission is granted", err)
	}
	if requestCalls != 1 {
		t.Fatalf("permission request calls = %d, want 1", requestCalls)
	}
	if len(checkValues) != 2 || checkValues[0] != accessUnknown || checkValues[1] != accessGranted {
		t.Fatalf("permission values read = %v, want [%d %d]", checkValues, accessUnknown, accessGranted)
	}
}

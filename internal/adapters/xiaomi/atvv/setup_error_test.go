package atvv

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNativeSetupFailureSurvivesTransportCallbackWrapping(t *testing.T) {
	for _, tt := range []struct {
		status int32
		want   error
	}{
		{-21, ErrBluetoothPermission}, {-22, ErrBluetoothPoweredOff}, {-10, ErrRemoteNotConnected},
	} {
		api := &fakeNativeTransportAPI{}
		transport := newFakeTransport(t, api, time.Second, func(event NativeEvent) error {
			if event.Kind == NativeEventFailure {
				return nativeFailure("device", event.Status)
			}
			return nil
		})
		token, _, _ := api.snapshot()
		emitFakeNativeEvent(token, NativeEventFailure, tt.status, nil)
		if err := transport.Ready(context.Background()); !errors.Is(err, tt.want) {
			t.Fatalf("status %d lost actionable reason: %v", tt.status, err)
		}
		if err := transport.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUnknownNativeFailureRetainsStatusWithoutInventingPermission(t *testing.T) {
	err := nativeFailure("device", -99)
	if !strings.Contains(err.Error(), "status=-99") || errors.Is(err, ErrBluetoothPermission) {
		t.Fatal(err)
	}
}

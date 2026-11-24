package main

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestAudioTraceRecordRedactsTargetPayload(t *testing.T) {
	at := time.Date(2026, 11, 24, 16, 0, 0, 0, time.FixedZone("local", 8*60*60))
	for _, test := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("example dictated text: %w", errCompatReceiverStopped), "target_stopped"},
		{errors.New("private external payload"), "error"},
	} {
		got := audioTraceRecord(at, 42, 7, 3, 2, "failed", test.err)
		want := "time=2026-11-24T08:00:00Z pid=42 generation=7 press=3 session=2 event=failed stage= result=" + test.want
		if got != want {
			t.Errorf("record = %q, want %q", got, want)
		}
	}
}

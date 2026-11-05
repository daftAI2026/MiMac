package main

import (
	"context"
	"testing"
	"time"

	"mimac.local/mimac/internal/voicetools"
)

// This test deliberately uses only the pre-existing discovery and stop APIs.
// On the baseline, stop cancels the context but drops busy/cancel ownership
// before the discovery callback observes the native cleanup terminal.
func TestAppleAssetStopRetainsExistingDiscoveryOwner(t *testing.T) {
	started := make(chan context.Context, 1)
	finish := make(chan struct{})
	callbacks := make(chan func(), 1)
	tool := &voiceTool{
		active: true,
		prepareAssets: func(context.Context, voicetools.Locator) voicetools.Snapshot {
			return voicetools.Snapshot{State: voicetools.Ready}
		},
	}
	tool.refreshUsing(voicetools.Locator{}, func(callback func()) { callbacks <- callback }, nil, func(ctx context.Context, _ voicetools.Locator) voicetools.Snapshot {
		started <- ctx
		<-finish // Represents a native owner whose cancel request has not joined yet.
		return voicetools.Snapshot{State: voicetools.ReadError, Source: "native-cleanup-terminal"}
	})
	ctx := <-started
	tool.stop()
	retainedWhilePending := tool.busy && tool.cancel != nil
	cancelRequested := ctx.Err() == context.Canceled
	close(finish)
	select {
	case callback := <-callbacks:
		callback()
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the existing discovery completion")
	}
	terminalRecorded := !tool.busy && tool.snapshot.Source == "native-cleanup-terminal"
	if !cancelRequested || !retainedWhilePending || !terminalRecorded {
		t.Fatalf("stop owner lifecycle: contextCanceled=%t retainedWhilePending=%t terminalRecorded=%t tool=%+v", cancelRequested, retainedWhilePending, terminalRecorded, tool)
	}
}

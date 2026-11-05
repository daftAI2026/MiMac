package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"mimac.local/mimac/internal/voicetools"
)

func TestAppleAssetCancelIsIdempotentAndRetainsAttemptUntilTerminal(t *testing.T) {
	started := make(chan context.Context, 1)
	finish := make(chan struct{})
	callbacks := make(chan func(), 1)
	tool := &voiceTool{
		active: true,
		prepareAssets: func(context.Context, voicetools.Locator) voicetools.Snapshot {
			return voicetools.Snapshot{State: voicetools.Ready}
		},
	}
	changed := 0
	tool.refreshAssetsUsing(voicetools.Locator{}, func(callback func()) { callbacks <- callback }, func(uint64) { changed++ }, func(ctx context.Context, _ voicetools.Locator) voicetools.Snapshot {
		started <- ctx
		<-finish // Models a system operation that does not stop when context is canceled.
		return voicetools.Snapshot{State: voicetools.ReadError}
	})
	ctx := <-started
	if !tool.requestCancel() {
		t.Fatal("first explicit cancel was rejected")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("prepare context error = %v, want context.Canceled", ctx.Err())
	}
	if tool.requestCancel() {
		t.Fatal("repeated explicit cancel was accepted")
	}
	if !tool.busy || !tool.cancelRequested || tool.cancel == nil {
		t.Fatalf("cancellation released the attempt before terminal cleanup: %+v", tool)
	}
	close(finish)
	awaitAppleToolCallback(t, callbacks)
	if tool.busy || tool.cancel != nil || tool.preparingAssets || !tool.lastAttemptCancelled || tool.snapshot.State != voicetools.ReadError || changed != 1 {
		t.Fatalf("terminal cancellation state: %+v changed=%d", tool, changed)
	}
}

func TestAppleAssetStopRetainsOwnerAndReactivationWaitsForCleanup(t *testing.T) {
	started := make(chan context.Context, 2)
	finishFirst := make(chan struct{})
	finishSecond := make(chan struct{})
	callbacks := make(chan func(), 2)
	var attempts atomic.Int32
	tool := &voiceTool{
		active: true,
		prepareAssets: func(context.Context, voicetools.Locator) voicetools.Snapshot {
			return voicetools.Snapshot{State: voicetools.Ready}
		},
	}
	discover := func(ctx context.Context, _ voicetools.Locator) voicetools.Snapshot {
		attempt := attempts.Add(1)
		started <- ctx
		if attempt == 1 {
			<-finishFirst
			return voicetools.Snapshot{State: voicetools.ReadError}
		}
		<-finishSecond
		return voicetools.Snapshot{State: voicetools.Ready, Source: "new-attempt"}
	}
	changed := 0
	dispatch := func(callback func()) { callbacks <- callback }
	tool.refreshAssetsUsing(voicetools.Locator{}, dispatch, func(uint64) { changed++ }, discover)
	firstContext := <-started
	tool.stop()
	if firstContext.Err() != context.Canceled || tool.active || !tool.busy || !tool.cancelRequested || tool.cancel == nil {
		t.Fatalf("stop lost pending owner: ctx=%v tool=%+v", firstContext.Err(), tool)
	}

	tool.active = true // The plugin is re-enabled while the first worker is still pending.
	tool.refreshUsing(voicetools.Locator{}, dispatch, func(uint64) { changed++ }, discover)
	if attempts.Load() != 1 || !tool.busy {
		t.Fatalf("reactivation started overlapping preparation: attempts=%d tool=%+v", attempts.Load(), tool)
	}
	close(finishFirst)
	awaitAppleToolCallback(t, callbacks)
	if tool.busy || !tool.lastAttemptCancelled || tool.snapshot.State != voicetools.ReadError || changed != 1 {
		t.Fatalf("inactive completion was not recorded after cleanup: %+v changed=%d", tool, changed)
	}

	tool.refreshAssetsUsing(voicetools.Locator{}, dispatch, func(uint64) { changed++ }, discover)
	secondContext := <-started
	if secondContext.Err() != nil || attempts.Load() != 2 {
		t.Fatalf("retry did not start after cleanup: attempts=%d err=%v", attempts.Load(), secondContext.Err())
	}
	close(finishSecond)
	awaitAppleToolCallback(t, callbacks)
	if tool.busy || tool.lastAttemptCancelled || tool.snapshot.State != voicetools.Ready || tool.snapshot.Source != "new-attempt" || changed != 2 {
		t.Fatalf("new attempt result was not retained: %+v changed=%d", tool, changed)
	}
}

func TestAppleAssetStaleCompletionCannotOverwriteCurrentAttempt(t *testing.T) {
	changed := 0
	tool := &voiceTool{
		active:          true,
		busy:            true,
		preparingAssets: true,
		sequence:        2,
		cancel:          func() {},
		snapshot:        voicetools.Snapshot{State: voicetools.ReadError, Source: "current"},
	}
	if tool.completeAttempt(1, voicetools.Snapshot{State: voicetools.ConfigMissing, Source: "stale"}, func(uint64) { changed++ }) {
		t.Fatal("stale completion was accepted")
	}
	if tool.snapshot.Source != "current" || !tool.busy || changed != 0 {
		t.Fatalf("stale result overwrote current attempt: %+v changed=%d", tool, changed)
	}
	if !tool.completeAttempt(2, voicetools.Snapshot{State: voicetools.Ready, Source: "current-result"}, func(uint64) { changed++ }) {
		t.Fatal("current completion was rejected")
	}
	if tool.snapshot.Source != "current-result" || tool.busy || changed != 1 {
		t.Fatalf("current result missing: %+v changed=%d", tool, changed)
	}
}

func TestAppleAssetStatusDistinguishesCancellationAndPendingCleanup(t *testing.T) {
	tool := &voiceTool{id: appleSpeechToolID, active: true, busy: true, preparingAssets: true, cancelRequested: true}
	if got := voiceToolStatus(tool); got != "正在取消；等待系统模型操作结束" {
		t.Fatalf("cancel status = %q", got)
	}
	tool.busy, tool.cancelRequested = false, false
	tool.snapshot.Source = appleSpeechCleanupPendingSource
	if got := voiceToolStatus(tool); got != "等待系统模型操作结束，请重新检查" {
		t.Fatalf("pending cleanup status = %q", got)
	}
	tool.snapshot.Source = ""
	tool.lastAttemptCancelled = true
	if got := voiceToolStatus(tool); got != "准备已取消" {
		t.Fatalf("terminal cancel status = %q", got)
	}
}

func awaitAppleToolCallback(t *testing.T, callbacks <-chan func()) {
	t.Helper()
	select {
	case callback := <-callbacks:
		callback()
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Apple tool completion callback")
	}
}

func TestAppleAssetStopCompletionWhileInactivePreservesTerminalState(t *testing.T) {
	started := make(chan context.Context, 1)
	finish := make(chan struct{})
	callbacks := make(chan func(), 1)
	tool := &voiceTool{
		active: true,
		prepareAssets: func(context.Context, voicetools.Locator) voicetools.Snapshot {
			return voicetools.Snapshot{State: voicetools.Ready}
		},
	}
	changed := 0
	tool.refreshAssetsUsing(voicetools.Locator{}, func(callback func()) { callbacks <- callback }, func(uint64) { changed++ }, func(ctx context.Context, _ voicetools.Locator) voicetools.Snapshot {
		started <- ctx
		<-finish
		return voicetools.Snapshot{State: voicetools.ReadError}
	})
	ctx := <-started
	tool.stop()
	if ctx.Err() != context.Canceled || tool.busy == false || tool.active {
		t.Fatalf("stop did not retain a canceled pending attempt: ctx=%v tool=%+v", ctx.Err(), tool)
	}
	close(finish)
	awaitAppleToolCallback(t, callbacks)
	if tool.busy || tool.active || !tool.lastAttemptCancelled || tool.snapshot.State != voicetools.ReadError || changed != 0 {
		t.Fatalf("inactive completion was dropped or notified UI: %+v changed=%d", tool, changed)
	}
}

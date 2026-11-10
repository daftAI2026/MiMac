package main

import (
	"context"
	"testing"
)

func TestQuitCancelsInstallationBeforeAllowingHostExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &shell{appInstallState: appInstallState{installing: true, installCancel: cancel}}
	prevented := 0
	if !s.deferInstallQuit(func() { prevented++ }) || prevented != 1 {
		t.Fatal("host quit was not held while installation owns a transaction")
	}
	if ctx.Err() != context.Canceled || !s.installQuitPending {
		t.Fatal("the transaction was not cancelled for rollback before quitting")
	}
	// The result callback clears this only after the worker has cleaned up.
	s.installing, s.installCancel = false, nil
	if s.deferInstallQuit(func() { prevented++ }) || prevented != 1 {
		t.Fatal("completed transaction prevented the normal host quit")
	}
}

func TestInstalledHostQuitDoesNotEnterInstaller(t *testing.T) {
	s := &shell{}
	if s.deferInstallQuit(func() { t.Fatal("normal host quit was blocked") }) || s.installQuitPending {
		t.Fatal("ordinary app exit acquired installer state")
	}
}

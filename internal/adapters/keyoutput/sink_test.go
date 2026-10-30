package keyoutput

import (
	"errors"
	"os"
	"reflect"
	"testing"

	"mimac.local/mimac/internal/bridge"
)

type fakePosted struct {
	tap   int32
	event uintptr
}

type fakeEventAPI struct {
	access        bool
	requestResult bool
	requestCalls  int
	sourceCalls   int
	createCalls   int
	failCreateAt  int
	nextHandle    uintptr
	created       []struct {
		code uint16
		down bool
	}
	globalPosts []fakePosted
	pidPosts    []struct {
		pid   int32
		event uintptr
	}
	flags          []uint64
	released       []uintptr
	sourceReleased bool
	badSource      bool
	badMarker      bool
	ops            []string
	api            *eventAPI
}

func newFakeEventAPI() *fakeEventAPI {
	f := &fakeEventAPI{access: true, nextHandle: 100}
	f.api = &eventAPI{
		preflight: func() bool { f.ops = append(f.ops, "preflight"); return f.access },
		request:   func() bool { f.requestCalls++; f.ops = append(f.ops, "request"); return f.requestResult },
		sourceCreate: func(state int32) uintptr {
			f.sourceCalls++
			f.ops = append(f.ops, "source")
			if state != privateEventSourceState {
				f.badSource = true
			}
			return 99
		},
		sourceUserData: func(source uintptr, value int64) {
			f.ops = append(f.ops, "source-marker")
			if source != 99 || value != eventMarker {
				f.badMarker = true
			}
		},
		suppression: func(source uintptr, interval float64) {
			f.ops = append(f.ops, "suppression")
			if source != 99 || interval != 0 {
				f.badMarker = true
			}
		},
		eventCreate: func(_ uintptr, code uint16, down bool) uintptr {
			f.createCalls++
			f.ops = append(f.ops, "create")
			if f.failCreateAt != 0 && f.createCalls == f.failCreateAt {
				return 0
			}
			f.nextHandle++
			f.created = append(f.created, struct {
				code uint16
				down bool
			}{code: code, down: down})
			return f.nextHandle
		},
		setFlags: func(_ uintptr, flags uint64) {
			f.ops = append(f.ops, "flags")
			f.flags = append(f.flags, flags)
		},
		setInteger: func(_ uintptr, field uint32, value int64) {
			f.ops = append(f.ops, "marker")
			if field != eventSourceUserData || value != eventMarker {
				f.badMarker = true
			}
		},
		postGlobal: func(tap int32, event uintptr) {
			f.ops = append(f.ops, "global-post")
			f.globalPosts = append(f.globalPosts, fakePosted{tap: tap, event: event})
		},
		postToPID: func(pid int32, event uintptr) {
			f.ops = append(f.ops, "pid-post")
			f.pidPosts = append(f.pidPosts, struct {
				pid   int32
				event uintptr
			}{pid: pid, event: event})
		},
		release: func(handle uintptr) {
			f.ops = append(f.ops, "release")
			f.released = append(f.released, handle)
			if handle == 99 {
				f.sourceReleased = true
			}
		},
	}
	return f
}

func TestOpenChecksPermissionWithoutRequestingIt(t *testing.T) {
	f := newFakeEventAPI()
	f.access = false
	f.requestResult = true
	if _, err := openWithAPI(SelfTarget, f.api); !errors.Is(err, ErrPermissionRequired) {
		t.Fatalf("openWithAPI() error = %v, want ErrPermissionRequired", err)
	}
	if f.requestCalls != 0 || f.sourceCalls != 0 {
		t.Fatalf("Open prompted or created a source: requests=%d sources=%d", f.requestCalls, f.sourceCalls)
	}
	if hasAccess(f.api) {
		t.Fatal("HasAccess reported a denied permission as granted")
	}
	if !requestAccess(f.api) || f.requestCalls != 1 {
		t.Fatal("explicit requestAccess did not make the single request")
	}
}

func TestInvalidEffectBatchDoesNotAllocateOrPost(t *testing.T) {
	f := newFakeEventAPI()
	s, err := openWithAPI(SelfTarget, f.api)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Submit([]bridge.Effect{
		bridge.KeyDownEffect{Key: "Control"},
		bridge.BlockedEffect{},
	})
	if !errors.Is(err, ErrInvalidPlan) || f.createCalls != 0 || len(f.pidPosts) != 0 || len(s.Held()) != 0 {
		t.Fatalf("invalid batch reached native API: err=%v creates=%d posts=%v held=%v", err, f.createCalls, f.pidPosts, s.Held())
	}
	if err := s.Submit([]bridge.Effect{bridge.KeyDownEffect{Key: "F12"}}); !errors.Is(err, ErrSinkFaulted) {
		t.Fatalf("sink did not lock after invalid batch: %v", err)
	}
	if err := s.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGlobalTargetAllocatesWholeBatchBeforeHIDPosts(t *testing.T) {
	f := newFakeEventAPI()
	s, err := openWithAPI(GlobalTarget, f.api)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Submit([]bridge.Effect{
		bridge.KeyDownEffect{Key: "Control"},
		bridge.KeyDownEffect{Key: "F18"},
	}); err != nil {
		t.Fatal(err)
	}
	firstPost := indexOfOp(f.ops, "global-post")
	lastCreate := lastIndexOfOp(f.ops, "create")
	if firstPost < lastCreate || len(f.globalPosts) != 2 || len(f.pidPosts) != 0 {
		t.Fatalf("posts began before complete allocation or wrong target: ops=%v global=%v pid=%v", f.ops, f.globalPosts, f.pidPosts)
	}
	if f.badSource || f.badMarker {
		t.Fatalf("private source or event marker setup wrong: source=%v marker=%v", f.badSource, f.badMarker)
	}
	if !reflect.DeepEqual(f.flags, []uint64{flagControl, flagControl}) {
		t.Fatalf("flags = %#v", f.flags)
	}
	if len(s.Held()) != 2 {
		t.Fatalf("held after submitted batch = %v", s.Held())
	}
	copyHeld := s.Held()
	copyHeld[0] = "F19"
	if !reflect.DeepEqual(s.Held(), []bridge.Key{"Control", "F18"}) {
		t.Fatalf("Held() exposed mutable internal state: %v", s.Held())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if len(f.globalPosts) != 4 || s.source != 0 || len(s.Held()) != 0 || !f.sourceReleased {
		t.Fatalf("close did not release through HID target: posts=%d source=%d held=%v released=%v", len(f.globalPosts), s.source, s.Held(), f.released)
	}
	if got, want := []uint64{flagControl, flagControl, flagControl, 0}, f.flags; !reflect.DeepEqual(got, want) {
		t.Fatalf("flags including reverse cleanup = %#v, want %#v", want, got)
	}
	for _, post := range f.globalPosts {
		if post.tap != hidEventTapLocation {
			t.Fatalf("global post tap = %d, want HID tap %d", post.tap, hidEventTapLocation)
		}
	}
}

func TestFakeSinkPostsFnPressAndRelease(t *testing.T) {
	f := newFakeEventAPI()
	s, err := openWithAPI(GlobalTarget, f.api)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Submit([]bridge.Effect{bridge.KeyDownEffect{Key: "Fn"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	wantCreated := []struct {
		code uint16
		down bool
	}{{code: 0x3F, down: true}, {code: 0x3F, down: false}}
	if !reflect.DeepEqual(f.created, wantCreated) || !reflect.DeepEqual(f.flags, []uint64{1 << 23, 0}) {
		t.Fatalf("fake Fn output = created=%#v flags=%#v", f.created, f.flags)
	}
	if len(f.globalPosts) != 2 || len(s.Held()) != 0 {
		t.Fatalf("fake Fn posts=%#v held=%v", f.globalPosts, s.Held())
	}
}

func TestSelfTargetPostsOnlyToCurrentProcess(t *testing.T) {
	f := newFakeEventAPI()
	s, err := openWithAPI(SelfTarget, f.api)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Submit([]bridge.Effect{bridge.KeyDownEffect{Key: "F12"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if len(f.globalPosts) != 0 || len(f.pidPosts) != 2 {
		t.Fatalf("unexpected target selection: global=%v pid=%v", f.globalPosts, f.pidPosts)
	}
	for _, post := range f.pidPosts {
		if post.pid != int32(os.Getpid()) {
			t.Fatalf("self target PID = %d, want %d", post.pid, os.Getpid())
		}
	}
}

func TestAllocationFailurePostsNothingAndLocksUntilReleaseAll(t *testing.T) {
	f := newFakeEventAPI()
	s, err := openWithAPI(GlobalTarget, f.api)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Submit([]bridge.Effect{bridge.KeyDownEffect{Key: "Command"}}); err != nil {
		t.Fatal(err)
	}
	postsBefore := len(f.globalPosts)
	releaseBefore := len(f.released)
	f.failCreateAt = f.createCalls + 2
	err = s.Submit([]bridge.Effect{
		bridge.KeyDownEffect{Key: "Control"},
		bridge.KeyDownEffect{Key: "F18"},
	})
	if err == nil || len(f.globalPosts) != postsBefore || !reflect.DeepEqual(s.Held(), []bridge.Key{"Command"}) {
		t.Fatalf("allocation failure changed submission state: err=%v posts=%d held=%v", err, len(f.globalPosts), s.Held())
	}
	if len(f.released) != releaseBefore+1 {
		t.Fatalf("allocated event was not released: %v", f.released)
	}
	createBefore := f.createCalls
	if err := s.Submit([]bridge.Effect{bridge.KeyUpEffect{Key: "Command"}}); !errors.Is(err, ErrSinkFaulted) {
		t.Fatalf("Submit after failure error = %v, want ErrSinkFaulted", err)
	}
	if f.createCalls != createBefore || !reflect.DeepEqual(s.Held(), []bridge.Key{"Command"}) {
		t.Fatalf("faulted Submit acted or forgot held: creates=%d held=%v", f.createCalls, s.Held())
	}
	f.failCreateAt = 0
	if err := s.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	if len(s.Held()) != 0 {
		t.Fatalf("ReleaseAll retained held keys: %v", s.Held())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseAllFailureKeepsHeldAndSourceUntilRetry(t *testing.T) {
	f := newFakeEventAPI()
	s, err := openWithAPI(SelfTarget, f.api)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Submit([]bridge.Effect{
		bridge.KeyDownEffect{Key: "Command"},
		bridge.KeyDownEffect{Key: "C"},
	}); err != nil {
		t.Fatal(err)
	}
	known := []bridge.Key{"Command", "C"}
	f.access = false
	if err := s.Close(); !errors.Is(err, ErrPermissionRequired) {
		t.Fatalf("Close() error = %v, want ErrPermissionRequired", err)
	}
	if !reflect.DeepEqual(s.Held(), known) || s.source != 99 || f.sourceReleased {
		t.Fatalf("failed close discarded recovery state: held=%v source=%d sourceReleased=%v", s.Held(), s.source, f.sourceReleased)
	}
	postCount := len(f.pidPosts)
	if err := s.Submit([]bridge.Effect{bridge.KeyUpEffect{Key: "C"}}); !errors.Is(err, ErrSinkFaulted) {
		t.Fatalf("Submit after close failure = %v, want ErrSinkFaulted", err)
	}
	if len(f.pidPosts) != postCount {
		t.Fatal("faulted sink submitted more events")
	}

	f.access = true
	f.failCreateAt = f.createCalls + 2
	if err := s.ReleaseAll(); err == nil {
		t.Fatal("injected release event allocation failure was ignored")
	}
	if !reflect.DeepEqual(s.Held(), known) || s.source != 99 || f.sourceReleased {
		t.Fatalf("failed cleanup allocation discarded state: held=%v source=%d released=%v", s.Held(), s.source, f.sourceReleased)
	}
	f.failCreateAt = 0
	if err := s.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	if len(s.Held()) != 0 {
		t.Fatalf("successful recovery retained held: %v", s.Held())
	}
	if err := s.Submit([]bridge.Effect{bridge.KeyDownEffect{Key: "F12"}}); err != nil {
		t.Fatalf("sink did not resume after cleanup recovery: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s.source != 0 || len(s.Held()) != 0 || !f.sourceReleased {
		t.Fatalf("successful close did not release source: source=%d held=%v", s.source, s.Held())
	}
}

func indexOfOp(ops []string, wanted string) int {
	for i, op := range ops {
		if op == wanted {
			return i
		}
	}
	return -1
}

func lastIndexOfOp(ops []string, wanted string) int {
	for i := len(ops) - 1; i >= 0; i-- {
		if ops[i] == wanted {
			return i
		}
	}
	return -1
}

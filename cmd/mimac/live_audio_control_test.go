package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"mimac.local/mimac/internal/platform/audioroute"
	"mimac.local/mimac/internal/voicetools"
)

type orderedControlSink struct {
	compatRouteTestSink
	order *[]string
	err   error
}

func (s *orderedControlSink) Prepare(context.Context) error {
	*s.order = append(*s.order, "sink-prepare")
	return s.err
}

func TestControlPreparationFollowsSinkAndCannotHideFailure(t *testing.T) {
	want := errors.New("control service not ready")
	for _, sinkFails := range []bool{false, true} {
		order := []string{}
		base := &orderedControlSink{order: &order}
		if sinkFails {
			base.err = want
		}
		sink := &compatControlPreparedSink{xiaomiPreparedSink: base, prepareControl: func(context.Context) error { order = append(order, "control-prepare"); return want }}
		if err := sink.Prepare(context.Background()); !errors.Is(err, want) {
			t.Fatalf("failure hidden: %v", err)
		}
		expected := []string{"sink-prepare"}
		if !sinkFails {
			expected = append(expected, "control-prepare")
		}
		if !reflect.DeepEqual(order, expected) {
			t.Fatalf("order %v, want %v", order, expected)
		}
		if err := sink.Close(); err != nil || base.closes() != 1 {
			t.Fatalf("partial sink not closeable: %v", err)
		}
	}
}

func TestControlPreparationCancellationDoesNotCallService(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink := &compatControlPreparedSink{xiaomiPreparedSink: &compatRouteTestSink{}, prepareControl: func(context.Context) error { t.Fatal("service called after cancellation"); return nil }}
	if err := sink.Prepare(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
}

func TestCompatRouteWaitsForControlBeforePublishingResources(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	route, _, _, _, _, _, _ := compatRouteTestFixtureForTarget(t, doubaoAudioRouteTarget(), nil, nil, nil, nil, func(deps *compatAudioRouteDependencies, _ *compatRouteTestInspector) {
		deps.prepareControl = func(ctx context.Context, _ voicetools.Locator, pid int32) error {
			if pid != 42 {
				t.Errorf("PID %d", pid)
			}
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})
	if err := route.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("control hook was not called")
	}
	if route.Ready() {
		t.Fatal("resources ready before control service")
	}
	close(release)
	waitXiaomiRoute(t, route.Ready)
	if err := route.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyDoubaoOptsIntoControlServicePreparation(t *testing.T) {
	if defaultDoubaoAudioRouteDependencies().prepareControl == nil {
		t.Fatal("Doubao control service preparation is not connected")
	}
	if defaultCompatAudioRouteDependencies().prepareControl != nil {
		t.Fatal("other tools inherited Doubao-specific preparation")
	}
}

func TestCompatControlPreparationFailureOwnsAndClosesPartialResources(t *testing.T) {
	for _, mode := range []string{"cancel", "failure", "generation"} {
		t.Run(mode, func(t *testing.T) {
			entered, cancelObserved := make(chan struct{}), make(chan struct{})
			want := errors.New("controller preparation failed")
			var captured *compatRouteTestIdentity
			route, inspector, _, owner, requests, source, sink := compatRouteTestFixtureForTarget(t, doubaoAudioRouteTarget(), nil, nil, nil, nil, func(deps *compatAudioRouteDependencies, _ *compatRouteTestInspector) {
				capture := deps.captureIdentity
				deps.captureIdentity = func(pid int32, executable string) (compatProcessIdentity, error) {
					id, err := capture(pid, executable)
					captured = id.(*compatRouteTestIdentity)
					return id, err
				}
				deps.prepareControl = func(ctx context.Context, _ voicetools.Locator, _ int32) error {
					close(entered)
					switch mode {
					case "cancel":
						<-ctx.Done()
						close(cancelObserved)
						return ctx.Err()
					case "generation":
						captured.setError(want)
						return nil
					default:
						return want
					}
				}
			})
			if err := route.Start(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("control hook not entered")
			}
			if mode != "cancel" {
				waitXiaomiRoute(t, func() bool { return route.Error() != nil })
				if !errors.Is(route.Error(), want) {
					t.Fatalf("preparation error = %v", route.Error())
				}
			}
			if route.Ready() {
				t.Fatal("failed preparation published Ready")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := route.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if mode == "cancel" {
				select {
				case <-cancelObserved:
				default:
					t.Fatal("hook never observed cancellation")
				}
			}
			_, closed := owner.counts()
			if source.closes() != 1 || sink.closes() != 1 || closed != 1 {
				t.Fatalf("partial cleanup source/sink/owner = %d/%d/%d", source.closes(), sink.closes(), closed)
			}
			_, _, _, waits, _ := inspector.snapshot()
			if waits != 0 || len(requests.snapshot()) != 0 {
				t.Fatalf("failed preparation touched receiver or selection: %d/%d", waits, len(requests.snapshot()))
			}
		})
	}
}

func TestColdDoubaoApplicationPreparesControlBeforeFirstAudioClient(t *testing.T) {
	called := make(chan struct{}, 1)
	route, _, _, _, requests, source, sink := compatRouteTestFixtureForTarget(t, doubaoAudioRouteTarget(), nil, nil, nil, nil, func(deps *compatAudioRouteDependencies, base *compatRouteTestInspector) {
		base.processes, base.input = nil, audioroute.ProcessInput{PID: 42}
		*deps = withCompatApplicationInspector(*deps, compatDoubaoBundleID, "/Applications/Test.app", "/Applications/Test.app/Contents/MacOS/Test")
		wrapped := deps.newInspector
		deps.newInspector = func() (compatAudioInspector, error) {
			inspector, err := wrapped()
			if err != nil {
				return nil, err
			}
			inspector.(*compatApplicationInspector).resolve = func(context.Context) (int32, error) { return 42, nil }
			return inspector, nil
		}
		deps.prepareControl = func(context.Context, voicetools.Locator, int32) error { called <- struct{}{}; return nil }
	})
	if err := route.Start(); err != nil {
		t.Fatal(err)
	}
	waitXiaomiRoute(t, route.Ready)
	select {
	case <-called:
	default:
		t.Fatal("cold app skipped control preparation")
	}
	if len(requests.snapshot()) != 0 {
		t.Fatal("preparation changed target selection")
	}
	if err := route.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if source.closes() != 1 || sink.closes() != 1 {
		t.Fatal("cold partial resources were not released once")
	}
}

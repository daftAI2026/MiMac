package pluginrt

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNewValidatesManifestGraph(t *testing.T) {
	tests := []struct {
		name      string
		manifests []Manifest
		want      string
	}{
		{
			name: "duplicate ID",
			manifests: []Manifest{
				{ID: "a", Start: noopStart},
				{ID: "a", Start: noopStart},
			},
			want: "duplicate",
		},
		{
			name:      "missing dependency",
			manifests: []Manifest{{ID: "a", Requires: []string{"missing"}, Start: noopStart}},
			want:      "missing module",
		},
		{
			name: "cycle",
			manifests: []Manifest{
				{ID: "a", Requires: []string{"b"}, Start: noopStart},
				{ID: "b", Requires: []string{"a"}, Start: noopStart},
			},
			want: "cycle",
		},
		{
			name:      "missing start",
			manifests: []Manifest{{ID: "a"}},
			want:      "no Start",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.manifests...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("New() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestEnableOrderIdempotenceAndDisableGuard(t *testing.T) {
	var starts, stops []string
	r, err := New(
		manifest("feature", []string{"base"}, &starts, &stops),
		manifest("base", nil, &starts, &stops),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Enable("feature"); err != nil {
		t.Fatal(err)
	}
	if err := r.Enable("feature"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(starts, []string{"base", "feature"}) {
		t.Fatalf("start order = %v", starts)
	}
	if err := r.Disable("base"); err == nil {
		t.Fatal("Disable(base) succeeded while feature was active")
	}
	if len(stops) != 0 {
		t.Fatalf("dependent guard stopped modules: %v", stops)
	}
	if err := r.Disable("feature"); err != nil {
		t.Fatal(err)
	}
	if err := r.Disable("base"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stops, []string{"feature", "base"}) {
		t.Fatalf("stop order = %v", stops)
	}
}

func TestFailedEnableRollsBackOnlyNewModulesInReverse(t *testing.T) {
	var starts, stops []string
	startErr := errors.New("start failed")
	r, err := New(
		manifest("kept", nil, &starts, &stops),
		manifest("fresh", nil, &starts, &stops),
		Manifest{
			ID:       "feature",
			Requires: []string{"kept", "fresh"},
			Start: func() (StopFunc, error) {
				starts = append(starts, "feature")
				return func() error {
					stops = append(stops, "feature")
					return nil
				}, startErr
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Enable("kept"); err != nil {
		t.Fatal(err)
	}
	if err := r.Enable("feature"); !errors.Is(err, startErr) {
		t.Fatalf("Enable(feature) error = %v, want start error", err)
	}
	if !r.Active("kept") || r.Active("fresh") || r.Active("feature") {
		t.Fatalf("states kept=%v fresh=%v feature=%v", r.Active("kept"), r.Active("fresh"), r.Active("feature"))
	}
	if !reflect.DeepEqual(starts, []string{"kept", "fresh", "feature"}) {
		t.Fatalf("start order = %v", starts)
	}
	if !reflect.DeepEqual(stops, []string{"feature", "fresh"}) {
		t.Fatalf("rollback stop order = %v", stops)
	}
}

func TestFailedCleanupFaultsModuleAndPinsDependencies(t *testing.T) {
	var starts, stops []string
	cleanupErr := errors.New("device close failed")
	r, err := New(
		manifest("base", nil, &starts, &stops),
		Manifest{
			ID:       "feature",
			Requires: []string{"base"},
			Start: func() (StopFunc, error) {
				starts = append(starts, "feature")
				return func() error {
					stops = append(stops, "feature")
					return cleanupErr
				}, errors.New("start failed")
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Enable("feature"); !errors.Is(err, cleanupErr) {
		t.Fatalf("Enable(feature) error = %v, want cleanup error", err)
	}
	if !r.Active("base") || !r.Active("feature") {
		t.Fatalf("faulted module/dependency not retained: base=%v feature=%v", r.Active("base"), r.Active("feature"))
	}
	if err := r.Enable("feature"); !errors.Is(err, ErrModuleFaulted) {
		t.Fatalf("retry error = %v, want ErrModuleFaulted", err)
	}
	if err := r.Close(); !errors.Is(err, cleanupErr) {
		t.Fatalf("Close() error = %v, want original cleanup error", err)
	}
	if err := r.Close(); !errors.Is(err, cleanupErr) {
		t.Fatalf("second Close() error = %v, want retained cleanup error", err)
	}
	if !reflect.DeepEqual(stops, []string{"feature"}) {
		t.Fatalf("Stop was retried or dependency was stopped: %v", stops)
	}
}

func TestCloseContinuesIndependentModulesAndIsIdempotent(t *testing.T) {
	var starts, stops []string
	cleanupErr := errors.New("stop failed")
	r, err := New(
		manifest("base", nil, &starts, &stops),
		Manifest{
			ID:       "feature",
			Requires: []string{"base"},
			Start: func() (StopFunc, error) {
				starts = append(starts, "feature")
				return func() error {
					stops = append(stops, "feature")
					return cleanupErr
				}, nil
			},
		},
		manifest("independent", nil, &starts, &stops),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Enable("feature"); err != nil {
		t.Fatal(err)
	}
	if err := r.Enable("independent"); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); !errors.Is(err, cleanupErr) {
		t.Fatalf("Close() error = %v, want cleanup error", err)
	}
	if !r.Active("base") || !r.Active("feature") || r.Active("independent") {
		t.Fatalf("unexpected states after Close: base=%v feature=%v independent=%v", r.Active("base"), r.Active("feature"), r.Active("independent"))
	}
	if err := r.Enable("independent"); !errors.Is(err, ErrRuntimeClosed) {
		t.Fatalf("Enable after Close error = %v, want ErrRuntimeClosed", err)
	}
	if err := r.Close(); !errors.Is(err, cleanupErr) {
		t.Fatalf("second Close() error = %v, want retained cleanup error", err)
	}
	if !reflect.DeepEqual(stops, []string{"independent", "feature"}) {
		t.Fatalf("Stop order/retries = %v", stops)
	}
}

func TestFailedStartWithoutStopIsFaulted(t *testing.T) {
	startErr := errors.New("partial setup failed")
	r, err := New(Manifest{
		ID: "unclean",
		Start: func() (StopFunc, error) {
			return nil, startErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Enable("unclean"); !errors.Is(err, startErr) {
		t.Fatalf("Enable() error = %v, want start error", err)
	}
	if !r.Active("unclean") {
		t.Fatal("module without rollback Stop was not conservatively retained as active")
	}
	if err := r.Enable("unclean"); !errors.Is(err, ErrModuleFaulted) {
		t.Fatalf("retry error = %v, want ErrModuleFaulted", err)
	}
}

func TestSuccessfulStartWithoutStopIsFaultedAndPinsDependencies(t *testing.T) {
	var starts, stops []string
	r, err := New(
		manifest("base", nil, &starts, &stops),
		Manifest{
			ID:       "feature",
			Requires: []string{"base"},
			Start: func() (StopFunc, error) {
				starts = append(starts, "feature")
				return nil, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Enable("feature"); !errors.Is(err, ErrNilStopFunc) {
		t.Fatalf("Enable(feature) error = %v, want ErrNilStopFunc", err)
	}
	if !r.Active("base") || !r.Active("feature") {
		t.Fatalf("faulted module/dependency not retained: base=%v feature=%v", r.Active("base"), r.Active("feature"))
	}
	if !reflect.DeepEqual(starts, []string{"base", "feature"}) {
		t.Fatalf("start order = %v", starts)
	}
	if len(stops) != 0 {
		t.Fatalf("nil Stop was treated as a clean rollback: %v", stops)
	}
	if err := r.Enable("feature"); !errors.Is(err, ErrModuleFaulted) {
		t.Fatalf("retry error = %v, want ErrModuleFaulted", err)
	}
	if err := r.Close(); !errors.Is(err, ErrNilStopFunc) {
		t.Fatalf("Close() error = %v, want retained ErrNilStopFunc", err)
	}
	if r.Active("base") != true || r.Active("feature") != true {
		t.Fatal("Close released a dependency of a module with unknown cleanup state")
	}
}

func TestLifecycleCallbacksCannotReenterMutatingMethods(t *testing.T) {
	var r *Runtime
	var reentryErr error
	var err error
	r, err = New(Manifest{
		ID: "module",
		Start: func() (StopFunc, error) {
			reentryErr = r.Close()
			return func() error {
				reentryErr = r.Disable("module")
				return nil
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Enable("module"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(reentryErr, ErrRuntimeBusy) {
		t.Fatalf("reentrant Close() error = %v, want ErrRuntimeBusy", reentryErr)
	}
	if err := r.Disable("module"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(reentryErr, ErrRuntimeBusy) {
		t.Fatalf("reentrant Disable() error = %v, want ErrRuntimeBusy", reentryErr)
	}
}

func noopStart() (StopFunc, error) { return func() error { return nil }, nil }

func TestFailureRetainsCauseWithoutRetryingCleanup(t *testing.T) {
	want := errors.New("mapping restoration failed")
	stops := 0
	r, err := New(Manifest{ID: "device", Start: func() (StopFunc, error) {
		return func() error { stops++; return want }, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Failure("device") != nil || r.Failure("unknown") != nil || (*Runtime)(nil).Failure("device") != nil {
		t.Fatal("reported a failure without a recorded fault")
	}
	if err := r.Enable("device"); err != nil {
		t.Fatal(err)
	}
	if r.Failure("device") != nil {
		t.Fatal("healthy active module reported failure")
	}
	if err := r.Disable("device"); !errors.Is(err, want) {
		t.Fatalf("Disable: %v", err)
	}
	for i := 0; i < 3; i++ {
		if !r.Active("device") || !errors.Is(r.Failure("device"), want) {
			t.Fatal("lost retained resources or failure cause")
		}
	}
	if err := r.Close(); !errors.Is(err, want) {
		t.Fatalf("Close: %v", err)
	}
	if stops != 1 || !errors.Is(r.Failure("device"), want) {
		t.Fatalf("query/close retried cleanup or discarded cause: stops=%d", stops)
	}
}

func manifest(id string, requires []string, starts, stops *[]string) Manifest {
	return Manifest{
		ID:       id,
		Requires: requires,
		Start: func() (StopFunc, error) {
			*starts = append(*starts, id)
			return func() error {
				*stops = append(*stops, id)
				return nil
			}, nil
		},
	}
}

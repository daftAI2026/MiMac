//go:build darwin

package textoutput

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Native compatibility uses an explicitly launched, test-owned native/browser
// fixture. It never targets an arbitrary application or changes privacy grants.
// The GUI fixture writes only its own synthetic text; the adapter does not read
// input contents. Command/result exchange lives in a caller-selected cache dir.
func TestNativeEditableMatrix(t *testing.T) {
	if os.Getenv("MIMAC_TEXT_MATRIX_EXPECT_UNSUPPORTED") == "1" {
		t.Skip("explicit unsupported-target rejection run, not an editable-support test")
	}
	runNativeMatrix(t, false)
}

func TestNativeUnsupportedWebMatrix(t *testing.T) {
	if os.Getenv("MIMAC_TEXT_MATRIX_EXPECT_UNSUPPORTED") != "1" {
		t.Skip("requires explicit unsupported browser fixture")
	}
	if os.Getenv("MIMAC_TEXT_MATRIX_WEB_ONLY") != "1" {
		t.Fatal("unsupported-target matrix requires the web-only fixture")
	}
	runNativeMatrix(t, true)
}

func runNativeMatrix(t *testing.T, expectUnsupported bool) {
	dir := os.Getenv("MIMAC_TEXT_MATRIX_DIR")
	if dir == "" {
		t.Skip("requires explicit native TextMatrix fixture")
	}
	pidBytes, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil || pid <= 0 {
		t.Fatal("invalid fixture PID")
	}
	kinds := []string{"textview", "field", "input", "textarea", "editable"}
	rejectKinds := []string{"textview", "input", "textarea", "editable"}
	secureKinds := []string{"secure", "password"}
	if os.Getenv("MIMAC_TEXT_MATRIX_WEB_ONLY") == "1" {
		kinds = []string{"input", "textarea", "editable"}
		rejectKinds = kinds
		secureKinds = []string{"password"}
	}
	seq := 0
	if data, err := os.ReadFile(filepath.Join(dir, "result.json")); err == nil {
		var prior struct {
			Seq int `json:"seq"`
		}
		_ = json.Unmarshal(data, &prior)
		seq = prior.Seq
	}
	command := func(t *testing.T, action, target string) matrixResult {
		t.Helper()
		seq++
		data, _ := json.Marshal(map[string]any{"seq": seq, "action": action, "target": target})
		path := filepath.Join(dir, "command.json")
		if err := os.WriteFile(path+".tmp", data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".tmp", path); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if data, err := os.ReadFile(filepath.Join(dir, "result.json")); err == nil {
				var result matrixResult
				if json.Unmarshal(data, &result) == nil && result.Seq == seq {
					if result.PID != pid || result.Error != "" {
						t.Fatalf("fixture failed: %+v", result)
					}
					return result
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("fixture command %s timed out", action)
		return matrixResult{}
	}
	capture := func(t *testing.T, kind string) (*Target, error) {
		t.Helper()
		if _, err := loadNativeAPI(); err != nil {
			t.Fatal(err)
		}
		front, err := frontmostProcessID()
		if err != nil || front != int32(pid) {
			t.Fatalf("fixture must be frontmost: got %d want %d err %v", front, pid, err)
		}
		// Browser engines construct their AX tree lazily. Synchronize our fixture's
		// requested DOM focus before capture; never retry an output operation.
		if kind == "input" || kind == "textarea" || kind == "editable" || kind == "password" {
			api, _ := loadNativeAPI()
			name, err := api.makeString(kind)
			if err != nil {
				t.Fatal(err)
			}
			defer api.cfRelease(name)
			ready := false
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				element, _, err := focusedElement(api, int32(pid))
				if err == nil {
					identifier, _ := api.optionalAttribute(element, "AXDOMIdentifier")
					if identifier != 0 {
						ready = api.cfEqual(identifier, name) != 0
						api.cfRelease(identifier)
					}
					api.cfRelease(element)
				}
				if ready {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if !ready {
				t.Fatal("fixture AX focus did not settle")
			}
		}
		if expectUnsupported && kind != "password" {
			api, _ := loadNativeAPI()
			element, _, err := focusedElement(api, int32(pid))
			if err != nil {
				t.Fatal(err)
			}
			defer api.cfRelease(element)
			operation, opErr := hasTextOperation(api, element)
			marker, markerErr := api.optionalAttribute(element, "AXSelectedTextMarkerRange")
			if marker != 0 {
				defer api.cfRelease(marker)
			}
			if opErr != nil || markerErr != nil || operation || marker == 0 {
				t.Fatalf("fixture is not marker-only: operation=%t marker=%t errors=%v/%v", operation, marker != 0, opErr, markerErr)
			}
			t.Log("confirmed marker-only target; verifying rejection, not text support")
		}
		target, err := Capture()
		if target != nil && target.snapshot.processID != int32(pid) {
			_ = target.Close()
			t.Fatal("captured a different process")
		}
		return target, err
	}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			command(t, "reset", kind)
			target, err := capture(t, kind)
			if expectUnsupported {
				if target != nil {
					_ = target.Close()
				}
				if !errors.Is(err, ErrUnsupportedTarget) {
					t.Fatalf("unsupported target Capture = %v, want ErrUnsupportedTarget", err)
				}
				if state := command(t, "snapshot", kind); state.value(kind) != "L|old|R" || state.Other != "untouched" {
					t.Fatal("rejected target changed fixture text")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			api, _ := loadNativeAPI()
			marker, markerErr := api.optionalAttribute(target.snapshot.element, "AXSelectedTextMarkerRange")
			t.Logf("native text-operation capability: %t; selected marker: %t (error %v)", target.snapshot.marker != 0, marker != 0, markerErr)
			if marker != 0 {
				api.cfRelease(marker)
			}
			if err := target.Insert("mImAc听写🙂✓"); err != nil {
				t.Fatalf("Insert: %v; fixture after request: %q", err, command(t, "snapshot", kind).value(kind))
			}
			state := command(t, "snapshot", kind)
			deadline := time.Now().Add(2 * time.Second)
			for state.value(kind) != "L|mImAc听写🙂✓|R" && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
				state = command(t, "snapshot", kind)
			}
			if got := state.value(kind); got != "L|mImAc听写🙂✓|R" {
				t.Fatalf("actual fixture value %q, want exact selected-range replacement", got)
			}
			if err := target.Insert("duplicate"); !errors.Is(err, ErrAlreadySubmitted) {
				t.Fatalf("second insertion = %v", err)
			}
			if after := command(t, "snapshot", kind).value(kind); after != state.value(kind) {
				t.Fatal("one-shot insertion changed text twice")
			}
			if err := target.Close(); err != nil {
				t.Fatal(err)
			}
			next, err := capture(t, kind)
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close()
			if err := next.Insert("续写"); err != nil {
				t.Fatal(err)
			}
			deadline = time.Now().Add(2 * time.Second)
			for {
				state = command(t, "snapshot", kind)
				if state.value(kind) == "L|mImAc听写🙂✓续写|R" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("second dictation did not append: %q", state.value(kind))
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
	if expectUnsupported {
		rejectKinds = nil
	} // Capture rejects before an output lease exists.
	for _, kind := range rejectKinds {
		for _, action := range []string{"focus", "selection"} {
			t.Run("reject_"+kind+"_"+action, func(t *testing.T) {
				command(t, "reset", kind)
				target, err := capture(t, kind)
				if err != nil {
					t.Fatal(err)
				}
				defer target.Close()
				changed := kind
				if action == "focus" {
					changed = "other"
				}
				command(t, action, changed)
				if err := target.Insert("must not appear"); !errors.Is(err, ErrTargetChanged) {
					t.Fatalf("changed target Insert = %v", err)
				}
				state := command(t, "snapshot", changed)
				if state.value(kind) != "L|old|R" || state.Other != "untouched" {
					t.Fatal("changed target received text")
				}
			})
		}
	}
	for _, kind := range secureKinds {
		t.Run("reject_"+kind, func(t *testing.T) {
			command(t, "reset", kind)
			target, err := capture(t, kind)
			if target != nil {
				_ = target.Close()
			}
			if !errors.Is(err, ErrSecureInput) {
				t.Fatalf("secure target Capture = %v", err)
			}
		})
	}
	command(t, "reset", kinds[0])
}

type matrixResult struct {
	Seq      int               `json:"seq"`
	PID      int               `json:"pid"`
	Error    string            `json:"error"`
	TextView string            `json:"textview"`
	Field    string            `json:"field"`
	Other    string            `json:"other"`
	Web      map[string]string `json:"web"`
}

func (m matrixResult) value(kind string) string {
	switch kind {
	case "textview":
		return m.TextView
	case "field":
		return m.Field
	default:
		return m.Web[kind]
	}
}

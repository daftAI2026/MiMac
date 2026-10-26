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

// Native compatibility uses an explicitly launched, test-owned AppKit/WebKit
// fixture. It never targets an arbitrary application or changes privacy grants.
// The GUI fixture writes only its own synthetic text; the adapter does not read
// input contents. Command/result exchange lives in a caller-selected cache dir.
func TestNativeEditableMatrix(t *testing.T) {
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
		// WebKit constructs its AX tree lazily. Synchronize only our fixture's
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
		target, err := Capture()
		if target != nil && target.snapshot.processID != int32(pid) {
			_ = target.Close()
			t.Fatal("captured a different process")
		}
		return target, err
	}
	for _, kind := range []string{"textview", "field", "input", "textarea", "editable"} {
		t.Run(kind, func(t *testing.T) {
			command(t, "reset", kind)
			target, err := capture(t, kind)
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			if err := target.Insert("mImAc听写🙂✓"); err != nil {
				t.Fatal(err)
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
	for _, kind := range []string{"textview", "input", "textarea", "editable"} {
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
	for _, kind := range []string{"secure", "password"} {
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
	command(t, "reset", "textview")
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

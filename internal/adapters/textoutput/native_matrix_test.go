//go:build darwin

package textoutput

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	const matrixText = "mImAc听写🙂✓中文长句 AbCd é é 🇨🇳 👨‍👩‍👧‍👦 Привет 完整输出"
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
	kinds := []string{"textview", "field", "input", "textarea", "editable", "rich"}
	rejectKinds := []string{"textview", "input", "textarea", "editable", "rich"}
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
	if !expectUnsupported && os.Getenv("MIMAC_TEXT_MATRIX_WEB_ONLY") != "1" {
		// Launch a fresh test process while the rich contenteditable selection is
		// focused. Its first native text-output call is Capture itself.
		command(t, "reset", "rich")
		waitForMatrixDOMFocus(t, int32(pid), "rich")
		runNativeColdCaptureChild(t)
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
		if kind == "input" || kind == "textarea" || kind == "editable" || kind == "rich" || kind == "password" {
			waitForMatrixDOMFocus(t, int32(pid), kind)
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
				if state := command(t, "snapshot", kind); state.value(kind) != matrixValue(kind, "old") || state.Other != "untouched" {
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
			t.Logf("native text-operation capability: %t; selected marker: %t (error %v)", target.snapshot.marker != 0 && !target.snapshot.unicodeText, marker != 0, markerErr)
			if marker != 0 {
				api.cfRelease(marker)
			}
			if err := target.Insert(matrixText); err != nil {
				t.Fatalf("Insert: %v; fixture after request: %q", err, command(t, "snapshot", kind).value(kind))
			}
			state := command(t, "snapshot", kind)
			deadline := time.Now().Add(2 * time.Second)
			want := matrixValue(kind, matrixText)
			for state.value(kind) != want && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
				state = command(t, "snapshot", kind)
			}
			if got := state.value(kind); got != want {
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
			want = matrixValue(kind, matrixText+"续写")
			for {
				state = command(t, "snapshot", kind)
				if state.value(kind) == want {
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
				if state.value(kind) != matrixValue(kind, "old") || state.Other != "untouched" {
					t.Fatal("changed target received text")
				}
			})
		}
	}
	if !expectUnsupported && os.Getenv("MIMAC_TEXT_MATRIX_WEB_ONLY") == "1" {
		for _, kind := range kinds {
			t.Run("large_"+kind, func(t *testing.T) {
				command(t, "reset", kind)
				target, err := capture(t, kind)
				if err != nil {
					t.Fatal(err)
				}
				defer target.Close()
				text := strings.Repeat("A中", MaxTextBytes/4)
				if err := target.Insert(text); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(3 * time.Second)
				for {
					state := command(t, "snapshot", kind)
					if state.value(kind) == "L|"+text+"|R" && state.Web["enterCount"] == "0" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("large insert mismatch: bytes=%d enter=%s", len(state.value(kind)), state.Web["enterCount"])
					}
					time.Sleep(20 * time.Millisecond)
				}
			})
		}
		t.Run("focus_changes_during_dispatch", func(t *testing.T) {
			command(t, "reset", "input")
			target, err := capture(t, "input")
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			command(t, "redirect", "input")
			text := "首字后焦点改变，后续文字必须不进入别的输入框 Aa🙂"
			if err := target.Insert(text); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(2 * time.Second)
			var state matrixResult
			for {
				state = command(t, "snapshot", "input")
				if state.Web["focused"] == "other" || time.Now().After(deadline) {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if state.value("input") != "L|"+text+"|R" || state.Other != "untouched" || state.Web["focused"] != "other" || state.Web["enterCount"] != "0" {
				t.Fatalf("text dispatch crossed fields or submitted: original=%q other=%q focus=%q enter=%s", state.value("input"), state.Other, state.Web["focused"], state.Web["enterCount"])
			}
		})
		for name, text := range map[string]string{"newline": "line\nline", "tab": "a\tb", "escape": "a\x1bb"} {
			t.Run("reject_control_"+name, func(t *testing.T) {
				command(t, "reset", "input")
				target, err := capture(t, "input")
				if err != nil {
					t.Fatal(err)
				}
				defer target.Close()
				if !target.snapshot.unicodeText {
					t.Skip("control rejection applies to event transport only")
				}
				if err := target.Insert(text); !errors.Is(err, ErrUnsupportedText) {
					t.Fatalf("control Insert = %v", err)
				}
				state := command(t, "snapshot", "input")
				if state.value("input") != "L|old|R" || state.Other != "untouched" || state.Web["enterCount"] != "0" {
					t.Fatal("rejected control changed fixture")
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

func matrixValue(kind, text string) string {
	value := "L|" + text + "|R"
	if kind == "rich" {
		return value + "\n\nkeep"
	}
	return value
}

func waitForMatrixDOMFocus(t *testing.T, pid int32, kind string) {
	t.Helper()
	api, err := loadNativeAPI()
	if err != nil {
		t.Fatal(err)
	}
	name, err := api.makeString(kind)
	if err != nil {
		t.Fatal(err)
	}
	defer api.cfRelease(name)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		element, _, err := focusedElement(api, pid)
		if err == nil {
			identifier, _ := api.optionalAttribute(element, "AXDOMIdentifier")
			ready := identifier != 0 && api.cfEqual(identifier, name) != 0
			if identifier != 0 {
				api.cfRelease(identifier)
			}
			api.cfRelease(element)
			if ready {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fixture AX focus for %q did not settle", kind)
}

// TestNativeColdCaptureChild runs only in a fresh test process. Its first
// textoutput native operation is Capture, so it does not warm loadNativeAPI
// before the measurement.
func TestNativeColdCaptureChild(t *testing.T) {
	if os.Getenv("MIMAC_TEXT_MATRIX_COLD_CAPTURE_CHILD") != "1" {
		t.Skip("runs only as a child process of the native matrix")
	}
	dir := os.Getenv("MIMAC_TEXT_MATRIX_DIR")
	pidBytes, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	wantPID, err := strconv.Atoi(string(pidBytes))
	if err != nil || wantPID <= 0 {
		t.Fatal("invalid fixture PID")
	}
	started := time.Now()
	target, err := Capture()
	if target != nil {
		defer target.Close()
	}
	if err != nil {
		t.Fatalf("first cold Capture: %v", err)
	}
	if target.snapshot.processID != int32(wantPID) {
		t.Fatalf("cold Capture target PID = %d, want fixture PID %d", target.snapshot.processID, wantPID)
	}
	if kind := os.Getenv("MIMAC_TEXT_MATRIX_COLD_CAPTURE_KIND"); kind != "" {
		api, err := loadNativeAPI()
		if err != nil {
			t.Fatal(err)
		}
		identifier, err := api.optionalAttribute(target.snapshot.element, "AXDOMIdentifier")
		if err != nil {
			t.Fatal(err)
		}
		if identifier == 0 {
			t.Fatal("cold captured web target has no AXDOMIdentifier")
		}
		defer api.cfRelease(identifier)
		want, err := api.makeString(kind)
		if err != nil {
			t.Fatal(err)
		}
		defer api.cfRelease(want)
		if api.cfEqual(identifier, want) == 0 {
			t.Fatalf("cold Capture DOM ID differs from %q", kind)
		}
	}
	t.Logf("cold first Capture testPID=%d targetPID=%d domID=%s started=%s elapsed_ms=%d", os.Getpid(), target.snapshot.processID, os.Getenv("MIMAC_TEXT_MATRIX_COLD_CAPTURE_KIND"), started.Format(time.RFC3339Nano), time.Since(started).Milliseconds())
}

func runNativeColdCaptureChild(t *testing.T) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestNativeColdCaptureChild$")
	command.Env = append(os.Environ(), "MIMAC_TEXT_MATRIX_COLD_CAPTURE_CHILD=1", "MIMAC_TEXT_MATRIX_COLD_CAPTURE_KIND=rich")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("cold Capture child process failed: %v\n%s", err, output)
	}
	t.Logf("cold Capture child output: %s", strings.TrimSpace(string(output)))
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

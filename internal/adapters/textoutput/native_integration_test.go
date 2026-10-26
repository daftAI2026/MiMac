//go:build darwin

package textoutput

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// The explicit fixture PID prevents this opt-in check from typing into an
// unrelated user application. Its result file contains only test-owned text.
func TestNativeTextFixture(t *testing.T) {
	pidText, resultPath := os.Getenv("MIMAC_TEXT_FIXTURE_PID"), os.Getenv("MIMAC_TEXT_FIXTURE_RESULT")
	if pidText == "" || resultPath == "" {
		t.Skip("requires an explicitly launched native text fixture")
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 {
		t.Fatal("invalid text fixture PID")
	}
	if _, err := loadNativeAPI(); err != nil {
		t.Fatal(err)
	}
	front, err := frontmostProcessID()
	if err != nil || front != int32(pid) {
		t.Fatalf("fixture must be frontmost: front=%d expected=%d err=%v", front, pid, err)
	}
	target, err := Capture()
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if target.snapshot.processID != int32(pid) {
		t.Fatal("captured target is not the fixture")
	}
	const expected = "MiMac 本机文字测试"
	if err := target.Insert(expected); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(resultPath)
		if err == nil && string(data) == expected {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("native field did not contain the exact test text")
}

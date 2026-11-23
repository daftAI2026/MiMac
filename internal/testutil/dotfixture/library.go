// Package dotfixture builds the same dependency-free native source used in
// the application. Tests never discover or load a user's installed bundle.
package dotfixture

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func Library(t testing.TB, repo string) string {
	t.Helper()
	src := filepath.Join(repo, "native", "voice_dot_model", "lib.rs")
	lib := filepath.Join(t.TempDir(), "dots.dylib")
	cmd := exec.Command("rustc", "--edition=2024", "--crate-type=cdylib", "-C", "opt-level=2", "-o", lib, src)
	if log, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build dot model: %v\n%s", err, log)
	}
	return lib
}

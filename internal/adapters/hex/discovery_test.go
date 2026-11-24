package hex

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"mimac.local/mimac/internal/voicetools"
)

const testToken = "hex_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestDiscoverReadsAuthenticatedCapabilitiesWithoutCreatingStream(t *testing.T) {
	var creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/streams" {
			creates.Add(1)
			w.WriteHeader(http.StatusCreated)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/capabilities" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"streams":true,"partialTranscripts":true}`))
	}))
	t.Cleanup(server.Close)

	home := t.TempDir()
	writeDiscovery(t, home, server)
	snapshot := Discover(context.Background(), voicetools.Locator{Home: home})
	if snapshot.State != voicetools.Ready || snapshot.Source != "hex-local-api" || snapshot.Version != apiVersion {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if creates.Load() != 0 {
		t.Fatal("read-only discovery reserved the external stream slot")
	}
}

func TestDiscoverReportsUnsupportedAndNotRunningStates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"streams":false}`))
	}))
	t.Cleanup(server.Close)
	home := t.TempDir()
	writeDiscovery(t, home, server)
	if got := Discover(context.Background(), voicetools.Locator{Home: home}).State; got != voicetools.Unsupported {
		t.Fatalf("streams-disabled state = %q", got)
	}
	if got := Discover(context.Background(), voicetools.Locator{Home: t.TempDir()}).State; got != voicetools.NotRunning {
		t.Fatalf("missing discovery state = %q", got)
	}
}

func TestLoadDiscoveryRejectsInvalidOrUnsupportedDocuments(t *testing.T) {
	home := t.TempDir()
	path := DiscoveryPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"port":0,"token":"` + testToken + `","apiVersion":"2","pid":1}`,
		`{"port":1234,"token":"bad","apiVersion":"2","pid":1}`,
		`{"port":1234,"token":"` + testToken + `","apiVersion":"1","pid":1}`,
		`{"port":1234,"token":"` + testToken + `","apiVersion":"2","pid":0}`,
		`{"port":1234,"token":"` + testToken + `","apiVersion":"2","pid":1} {}`,
	} {
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadDiscovery(context.Background(), home); err == nil {
			t.Fatalf("accepted discovery document %s", raw)
		}
	}
}

func TestLoadDiscoveryRejectsUnsafeModeAndSymlink(t *testing.T) {
	home := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	writeDiscovery(t, home, server)
	path := DiscoveryPath(home)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadDiscovery(context.Background(), home); err == nil {
		t.Fatal("accepted group/world-readable discovery token")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	target := path + ".target"
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadDiscovery(context.Background(), home); err == nil {
		t.Fatal("followed a symlink discovery document")
	}
}

func TestClientSetupFencesDiscoveredHexProcess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	home := t.TempDir()
	writeDiscovery(t, home, server)
	document, _, err := LoadDiscovery(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	appPath := discoveryAppPath(document)
	if _, err := NewClientForAppPath(context.Background(), home, appPath); err != nil {
		t.Fatalf("client for current Hex service: %v", err)
	}
	if _, err := NewClientForAppPath(context.Background(), home, appPath+"-replaced"); err == nil {
		t.Fatal("session setup accepted a different Hex process than discovery")
	}
}

func writeDiscovery(t *testing.T, home string, server *httptest.Server) {
	t.Helper()
	port := server.Listener.Addr().(*net.TCPAddr).Port
	path := DiscoveryPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(Discovery{Port: uint16(port), Token: testToken, APIVersion: apiVersion, PID: 123})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, document, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryPathUsesHexApplicationSupportFolder(t *testing.T) {
	got := DiscoveryPath("/tmp/home")
	want := filepath.Join("/tmp/home", "Library", "Application Support", "hex", "local-api.json")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	if strings.Contains(got, "~") {
		t.Fatalf("path should be expanded from the locator home: %q", got)
	}
}

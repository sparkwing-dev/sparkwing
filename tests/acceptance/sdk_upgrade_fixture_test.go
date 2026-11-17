//go:build linux

package acceptance_test

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The published index lists versions oldest first, so the newest older SDK is not the first
// older one (reviewer repro).
func TestUpgradeE2ESelectsTheNewestOlderSDK(t *testing.T) {
	listed := []string{"v0.72.0", "v0.72.1", "v0.72.2", "v0.72.3", "v0.72.4"}
	if got := newestOlderSDK(listed, "v0.72.4"); got != "v0.72.3" {
		t.Fatalf("selected %s, want newest older SDK v0.72.3", got)
	}
	if got := newestOlderSDK(listed[:1], "v0.72.0"); got != "v0.72.0" {
		t.Fatalf("with no older SDK selected %s, want the scaffold's v0.72.0", got)
	}
}

type closeRecorder struct {
	http.RoundTripper
	closed int
}

func (c *closeRecorder) CloseIdleConnections() { c.closed++ }

// A kept-alive connection to the index leaves transport goroutines behind, which fails the
// package leak check (reviewer repro).
func TestUpgradeE2EClosesTheIndexConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"sdks":[{"language":"typescript","version":"v0.72.0"},{"language":"go","version":"v0.1.0"}]}`))
	}))
	defer server.Close()
	transport := &closeRecorder{RoundTripper: server.Client().Transport}
	listed, err := fetchListedSDKs(t.Context(), &http.Client{Transport: transport}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(listed["typescript"], []string{"v0.72.0"}) || !slices.Equal(listed["go"], []string{"v0.1.0"}) {
		t.Fatalf("listed = %v", listed)
	}
	if transport.closed != 1 {
		t.Fatalf("idle index connections closed %d times, want once before returning", transport.closed)
	}
}

// The CLI probes the user manager; the package guard fails any real manager command, so the
// fixture answers as a machine with no reachable session bus (reviewer repro).
func TestUpgradeE2EUserHasNoReachableUserManager(t *testing.T) {
	u := newUpgradeUser(t, "/nonexistent-cli")
	for _, name := range []string{"systemctl", "systemd-run", "launchctl"} {
		command := exec.CommandContext(t.Context(), "sh", "-c", name+" --user status")
		command.Env = u.env
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "Failed to connect to bus") {
			t.Fatalf("%s reached something other than the fixture's no-bus manager: %v: %s", name, err, output)
		}
	}
}

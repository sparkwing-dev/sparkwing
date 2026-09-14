package jobs

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func listeningUnixSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gate-socket-")
	if err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "api.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on %s: %v", path, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return path
}

func TestTheTestStepKeepsLiveServicesAwayFromTheSuitesItRuns(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()

	writeGoFile(t, filepath.Join(root, "internal", "ambient_probe_test.go"), ambientServiceProbe)
	gitAddAll(t, root)

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "dev.env"),
		[]byte("SPARKWING_CONTROLLER_URL=http://127.0.0.1:4344\n"), 0o600); err != nil {
		t.Fatalf("write dev.env: %v", err)
	}
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv(wingwire.APISocketEnv, listeningUnixSocket(t))
	t.Setenv("SPARKWING_CONTROLLER_URL", "http://127.0.0.1:4344")
	t.Setenv("SPARKWING_LOGS_URL", "http://127.0.0.1:4345")
	unsetForTest(t, devEnvDisableVar)

	if err := forEachGoModule(ctx, "go test", "go test ./...", false); err == nil {
		t.Fatal("the probe must fail while the ambient bindings reach it, or the pass below proves nothing")
	}
	if err := runTest(ctx); err != nil {
		t.Fatalf("the test step handed its suites a live socket or the dev.env fallback: %v", err)
	}
}

func TestTheDevEnvKillSwitchNamesTheProductConstant(t *testing.T) {
	root := sourceTreeRoot()
	if root == "" {
		t.Fatal("could not locate the repository root from this file's compile-time path; " +
			"if this package moved, check sourceTreeRoot's markers")
	}
	source, err := os.ReadFile(filepath.Join(root, "internal", "orchestrator", "devenv.go"))
	if err != nil {
		t.Fatalf("read the dev.env resolver: %v", err)
	}
	want := `const DevEnvDisableEnv = "` + devEnvDisableVar + `"`
	if !strings.Contains(string(source), want) {
		t.Fatalf("the pipeline module pins %q, which internal/orchestrator no longer declares as %s",
			devEnvDisableVar, want)
	}
}

const ambientServiceProbe = `package internal

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestNoLiveServiceIsWithinReach(t *testing.T) {
	if socket := os.Getenv("SPARKWING_API_SOCKET"); socket != "" {
		reach := "an unreachable path"
		if conn, err := net.Dial("unix", socket); err == nil {
			_ = conn.Close()
			reach = "a listening daemon"
		}
		t.Fatalf("SPARKWING_API_SOCKET=%s reached the suite and names %s", socket, reach)
	}
	for _, name := range []string{"SPARKWING_CONTROLLER_URL", "SPARKWING_LOGS_URL"} {
		if url := os.Getenv(name); url != "" {
			t.Fatalf("%s=%s reached the suite", name, url)
		}
	}
	if os.Getenv("SPARKWING_DEV_ENV_DISABLE") != "" {
		return
	}
	home := os.Getenv("SPARKWING_HOME")
	if home == "" {
		t.Fatal("the dev.env fallback is armed and nothing pins the home it reads")
	}
	if _, err := os.Stat(filepath.Join(home, "dev.env")); err == nil {
		t.Fatalf("the dev.env fallback is armed and %s carries one", home)
	}
}
`

package jobs

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"regexp"
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
	if testing.Short() {
		t.Skip("slow: 0.7s of real work; the fast class runs under -short")
	}
	root := gateFixtureRepo(t)
	ctx := context.Background()

	writeGoFile(t, filepath.Join(root, "internal", "ambient_probe_test.go"), ambientServiceProbe)
	gitAddAll(t, root)

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "dev.env"),
		[]byte("SPARKWING_CONTROLLER_URL=http://127.0.0.1:4344\n"), 0o600); err != nil {
		t.Fatalf("write dev.env: %v", err)
	}
	t.Setenv(productTestHomeVar, home)
	t.Setenv(wingwire.APISocketEnv, listeningUnixSocket(t))
	t.Setenv("SPARKWING_CONTROLLER_URL", "http://127.0.0.1:4344")
	t.Setenv("SPARKWING_LOGS_URL", "http://127.0.0.1:4345")
	t.Setenv("SPARKWING_CACHE_URL", "http://127.0.0.1:8090")
	t.Setenv("SPARKWING_AGENT_TOKEN", "an inherited bearer")
	unsetForTest(t, devEnvDisableVar)

	if err := forEachGoModule(ctx, "go test", "go test ./...", ""); err == nil {
		t.Fatal("the probe must fail while the ambient bindings reach it, or the pass below proves nothing")
	}
	if err := runTest(ctx); err != nil {
		t.Fatalf("the test step handed its suites a live service or the dev.env fallback: %v", err)
	}
}

func TestTheDevEnvKillSwitchNamesTheProductConstant(t *testing.T) {
	want := `const DevEnvDisableEnv = "` + devEnvDisableVar + `"`
	if source := readProductSource(t, "internal", "orchestrator", "devenv.go"); !strings.Contains(source, want) {
		t.Fatalf("the pipeline module pins %q, which internal/orchestrator no longer declares as %s",
			devEnvDisableVar, want)
	}
}

// safety: the names the node injector reaches through a constant the pipeline
// module cannot resolve. The wingwire values compile in; the runner's own is
// pinned against its declaration below.
var nodeEnvConstants = map[string]string{
	"wingwire.APISocketEnv":       wingwire.APISocketEnv,
	"wingwire.LeaseTokenEnv":      wingwire.LeaseTokenEnv,
	"wingwire.ChildLeaseTokenEnv": wingwire.ChildLeaseTokenEnv,
	"ParentLivenessFDEnv":         "SPARKWING_PARENT_LIVENESS_FD",
}

var envConstantReference = regexp.MustCompile(`\b(?:wingwire\.)?[A-Z][A-Za-z0-9]*Env\b`)

var envNameLiteral = regexp.MustCompile(`"(SPARKWING_[A-Z0-9_]+)"`)

func readProductSource(t *testing.T, parts ...string) string {
	t.Helper()
	root := sourceTreeRoot()
	if root == "" {
		t.Fatal("could not locate the repository root from this file's compile-time path; " +
			"if this package moved, check sourceTreeRoot's markers")
	}
	data, err := os.ReadFile(filepath.Join(append([]string{root}, parts...)...))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(parts...), err)
	}
	return string(data)
}

func TestEveryVariableTheNodeInjectorSetsIsScrubbedPinnedOrRecorded(t *testing.T) {
	if want := `ParentLivenessFDEnv = "` + nodeEnvConstants["ParentLivenessFDEnv"] + `"`; !strings.Contains(
		readProductSource(t, "internal", "runners", "local", "local.go"), want) {
		t.Fatalf("the local runner no longer declares %s", want)
	}

	injector := readProductSource(t, "internal", "runners", "local", "env.go")
	injected := map[string]bool{}
	for _, m := range envNameLiteral.FindAllStringSubmatch(injector, -1) {
		injected[m[1]] = true
	}
	for _, reference := range envConstantReference.FindAllString(injector, -1) {
		name, ok := nodeEnvConstants[reference]
		if !ok {
			t.Errorf("the node injector reads %s, a constant this contract cannot resolve; "+
				"add it to nodeEnvConstants with its value", reference)
			continue
		}
		injected[name] = true
	}
	if len(injected) == 0 {
		t.Fatal("read no variable names out of the node injector, so this contract checks nothing")
	}

	handled := map[string]bool{}
	for _, name := range productTestUnset {
		handled[name] = true
	}
	for _, pin := range productTestPins(t.TempDir()) {
		name, _, _ := strings.Cut(pin, "=")
		handled[name] = true
	}
	for name := range productTestKept {
		handled[name] = true
	}

	for name := range injected {
		if !handled[name] {
			t.Errorf("the node injector hands a child %s, which no test step clears or pins; "+
				"add it to productTestUnset, or to productTestKept with the reason it is harmless", name)
		}
	}
	for name, why := range productTestKept {
		if !injected[name] {
			t.Errorf("productTestKept keeps %s (%s), which the node injector no longer sets; drop it", name, why)
		}
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
	for _, name := range []string{
		"SPARKWING_CONTROLLER_URL",
		"SPARKWING_LOGS_URL",
		"SPARKWING_CACHE_URL",
		"SPARKWING_AGENT_TOKEN",
		"SPARKWING_RUN_ID",
		"SPARKWING_NODE_ID",
	} {
		if value := os.Getenv(name); value != "" {
			t.Fatalf("%s=%s reached the suite", name, value)
		}
	}
	if os.Getenv("SPARKWING_DEV_ENV_DISABLE") == "" {
		t.Fatal("the dev.env fallback is armed, so an unset URL still resolves to a service")
	}
	home := os.Getenv("SPARKWING_HOME")
	if home == "" {
		home = filepath.Join(os.Getenv("HOME"), ".sparkwing")
	}
	if _, err := os.Stat(filepath.Join(home, "dev.env")); err == nil {
		t.Fatalf("the home %s carries a dev.env this suite can read", home)
	}
}
`

//go:build e2e && !windows

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_PinnedPipelineRunsWithTheNetworkDenied runs a pipeline whose spark is
// pinned to an exact tag twice: once where modules may be downloaded, then again
// where the only reachable module proxy and the only reachable HTTP proxy are a
// recorder that answers 502 and logs what asked. The second run has to be green.
//
// Denial and observation are one mechanism. GOPROXY is the recorder, so a module
// fetch is refused and logged by the same request; HTTP_PROXY and HTTPS_PROXY
// are the recorder, so git over https is too; GIT_CONFIG_GLOBAL and
// GIT_CONFIG_NOSYSTEM empty the git configuration that could rewrite an https
// remote into ssh. HOME and GOMODCACHE are the fixture's own, so every module
// byte the denied runs read was put there by the first run, and the first run
// takes those bytes from the host module cache rather than the network.
func TestRun_PinnedPipelineRunsWithTheNetworkDenied(t *testing.T) {
	if testing.Short() {
		t.Skip("the offline guarantee downloads modules and compiles a pipeline binary; run without -short")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	cli := buildSubmitCLI(t)

	fixtureHome := t.TempDir()
	offlineUnlockModuleCache(t, filepath.Join(fixtureHome, "go", "pkg", "mod"))
	sparkwingHome := t.TempDir()
	t.Setenv("SPARKWING_HOME", sparkwingHome)
	offlineStopDaemon(t, sparkwingHome)

	// safety: a sparkwing on the developer's PATH would host the admission
	// daemon for this home, and the operator's binary is not this test's to
	// start; the fixture PATH carries the two tools a run actually needs.
	toolPath := offlineToolPath(t, goBin, gitBin)
	repoDir, sparkwingDir := offlineWriteFixture(t, gitBin, toolPath, fixtureHome)
	marker := filepath.Join(t.TempDir(), "ran.txt")

	connected := offlineConnectedEnv(t, fixtureHome, toolPath, sparkwingHome, marker)
	if out, tidyErr := offlineRunGo(goBin, sparkwingDir, connected, "mod", "tidy"); tidyErr != nil {
		t.Fatalf("the host module cache does not hold the fixture's modules; "+
			"run `go mod download all` in the repository first: %v\n%s", tidyErr, out)
	}

	firstOut, err := offlineRunCLI(cli, repoDir, connected, "run", "offline")
	if err != nil {
		t.Fatalf("first run failed after its modules downloaded: %v\n%s", err, firstOut)
	}
	if !strings.Contains(firstOut, "compiling .sparkwing/") {
		t.Fatalf("the first run did not compile the pipeline binary, so the second proves nothing:\n%s", firstOut)
	}

	denier := offlineNewDenier(t)
	denied := offlineDeniedEnv(t, fixtureHome, toolPath, sparkwingHome, marker, denier.url)

	cached := denier.mark()
	secondOut, err := offlineRunCLI(cli, repoDir, denied, "run", "offline")
	if err != nil {
		t.Fatalf("the second run failed with no network reachable: %v\n%s", err, secondOut)
	}
	if got := offlineMarkerLines(t, marker); len(got) != 2 || got[0] != offlineSparkGreeting || got[1] != offlineSparkGreeting {
		t.Fatalf("pipeline body wrote %q, want the spark greeting once per run", got)
	}
	if strings.Contains(secondOut, "compiling .sparkwing/") {
		t.Fatalf("the second run rebuilt the pipeline binary instead of reusing the cached one:\n%s", secondOut)
	}
	if reached := denier.since(cached); len(reached) > 0 {
		t.Fatalf("the second run tried to reach the network: %v\n%s", reached, secondOut)
	}

	read := denier.mark()
	listing, err := offlineRunCLI(cli, repoDir, denied, "runs", "list", "-o", "json")
	if err != nil {
		t.Fatalf("reading the run history with no network reachable: %v\n%s", err, listing)
	}
	sources := offlineBinarySources(t, listing)
	if len(sources) != 2 {
		t.Fatalf("run history holds %d runs, want the compiled one and the cached one: %v", len(sources), sources)
	}
	if sources[0] != "cached" || sources[1] != "compiled" {
		t.Fatalf("binary sources newest first = %v, want [cached compiled]", sources)
	}
	if reached := denier.since(read); len(reached) > 0 {
		t.Fatalf("reading the run history tried to reach the network: %v", reached)
	}

	// safety: a latest pin asks the module proxy, which is the recorder, so
	// this phase is also the proof that the denial is armed.
	offlineTrackLatest(t, sparkwingDir)
	tracking := denier.mark()
	trackingOut, err := offlineRunCLI(cli, repoDir, denied, "run", "offline")
	if err == nil {
		t.Fatalf("a latest pin resolved with no network reachable:\n%s", trackingOut)
	}
	if !strings.Contains(trackingOut, "--sw-no-update") {
		t.Fatalf("the offline failure does not name the flag that skips resolution:\n%s", trackingOut)
	}
	if len(denier.since(tracking)) == 0 {
		t.Fatalf("a latest pin reached no proxy, so the denied environment is not in the request path:\n%s", trackingOut)
	}

	// safety: the manifest changed, so this run compiles instead of reusing the
	// cached binary, and it must do so from the cache the first run filled.
	skipped := denier.mark()
	skippedOut, err := offlineRunCLI(cli, repoDir, denied, "run", "offline", "--sw-no-update")
	if err != nil {
		t.Fatalf("--sw-no-update did not carry a latest pin through an offline run: %v\n%s", err, skippedOut)
	}
	if !strings.Contains(skippedOut, "compiling .sparkwing/") {
		t.Fatalf("the offline compile did not happen, so it proves nothing about building without a proxy:\n%s", skippedOut)
	}
	if got := offlineMarkerLines(t, marker); len(got) != 3 {
		t.Fatalf("pipeline body ran %d times, want one per run", len(got))
	}
	if reached := denier.since(skipped); len(reached) > 0 {
		t.Fatalf("the offline compile tried to reach the network: %v\n%s", reached, skippedOut)
	}
}

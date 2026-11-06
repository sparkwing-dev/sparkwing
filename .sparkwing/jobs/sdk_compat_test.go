package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestSDKCompatibilitySelectsContractChanges(t *testing.T) {
	for _, file := range []string{
		"internal/nodehost/sdk_planning.go", "internal/nodeproto/protocol.go", "internal/sdkserve/hostclient.go",
		"sparkwing/exec.go", "sdk/typescript/src/command.ts", "sdk/typescript/package-lock.json",
		"internal/procenv/build.go", "internal/procgroup/group_unix.go", "internal/sessionledger/ledger.go",
		"internal/execdiag/diagnostics.go", "internal/secrets/mask.go", "internal/envredact/env.go",
		"internal/engine/resolve.go", "internal/sdkref/ref.go", "pkg/scaffold/version.go",
		"testdata/sdk-compat/manifest.json", "internal/sdkcompat/fixture.go",
		"go.mod", "go.sum", ".sparkwing/jobs/sdk_compat.go",
	} {
		if got := sdkCompatibilityChange([]string{file}); got != file {
			t.Errorf("change %s selected %q", file, got)
		}
	}
	for _, file := range []string{
		"DELIVERY.md", "docs/sdk.md", "internal/nodehost/HOSTING.md", "web/src/main.tsx",
		"internal/store/billing.go", "pkg/color/color.go", "bin/check-changelog.sh",
	} {
		if got := sdkCompatibilityChange([]string{file}); got != "" {
			t.Errorf("unrelated change %s selected %q", file, got)
		}
	}
}

func TestSDKCompatibilityUsesAffectedScope(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeGoFile(t, filepath.Join(root, "docs", "change.md"), "docs\n")
	gitAddAll(t, root)
	if !skipSDKCompatibilityUnlessTouched(t.Context()) {
		t.Fatal("a documentation-only change runs historical SDK acquisition")
	}
	writeGoFile(t, filepath.Join(root, "internal", "nodehost", "host.go"), "package nodehost\n")
	gitAddAll(t, root)
	if skipSDKCompatibilityUnlessTouched(t.Context()) {
		t.Fatal("host-only changes skip the historical SDK matrix")
	}
}

func TestSDKCompatibilityRunsWarmAndColdWithoutCachedTestResults(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "base")
	bin := t.TempDir()
	log := filepath.Join(root, "go-args")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SPARKWING_CONTROLLER_URL", "http://must-not-reach-tests.invalid")
	for _, mode := range []string{"warm", "cold"} {
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(log) + "\n" +
			"test \"$GOWORK\" = off && test -n \"$SPARKWING_HOME\" && test -z \"$SPARKWING_CONTROLLER_URL\"\n"
		writeGoFile(t, filepath.Join(bin, "go"), script)
		if err := os.Chmod(filepath.Join(bin, "go"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := runSDKCompatibility(context.Background(), mode); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		for _, arg := range []string{"-v\n", "-count=1\n", "-timeout\n15m\n", "./internal/nodehost\n", "^TestHistoricalSDKCompatibility$\n", "-sdk-compat=" + mode + "\n", "-sdk-compat-cache=" + filepath.Join(root, ".sparkwing", "cache", "sdk-compat") + "\n"} {
			if !strings.Contains(string(data), arg) {
				t.Errorf("%s invocation %q lacks %q", mode, data, arg)
			}
		}
	}
	writeGoFile(t, filepath.Join(bin, "go"), "#!/bin/sh\nexit 9\n")
	if err := runSDKCompatibilityWarm(t.Context()); err == nil || !strings.Contains(err.Error(), "historical SDK compatibility (warm)") {
		t.Fatalf("matrix failure = %v", err)
	}
}

func TestGateDeclaresHistoricalSDKCompatibility(t *testing.T) {
	plan := inspectPlan(t, func(plan *sparkwing.Plan) error {
		return (&Gate{}).Plan(t.Context(), plan, GateArgs{}, sparkwing.RunContext{Pipeline: "gate"})
	})
	for _, job := range plan.Jobs {
		if job.ID == "gate" {
			if findStep(job, "sdk-compat") == nil {
				t.Fatal("gate omits the historical SDK matrix")
			}
			return
		}
	}
	t.Fatal("gate job missing")
}

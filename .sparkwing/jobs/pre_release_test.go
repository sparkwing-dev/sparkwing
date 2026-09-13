package jobs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestMarkdownlintCommandIsPinnedAndSelfProvisioning(t *testing.T) {
	const want = "npx --yes markdownlint-cli2@0.23.2"
	if markdownlintCommand != want {
		t.Fatalf("markdownlint command = %q, want exactly %q", markdownlintCommand, want)
	}
	if err := runMarkdownlint(context.Background()); err != nil {
		t.Fatalf("self-provisioned markdown lint failed: %v", err)
	}
}

func TestActionlintCommandIsPinned(t *testing.T) {
	const want = "go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12"
	if actionlintCommand != want {
		t.Fatalf("actionlint command = %q, want exactly %q", actionlintCommand, want)
	}
}

func TestInstallToGreenHarnessRunsOnTheCheckoutAndRecordsRatherThanGates(t *testing.T) {
	const want = "bash bin/install-to-green.sh --build --output json"
	if installToGreenCommand != want {
		t.Fatalf("install-to-green command = %q, want exactly %q", installToGreenCommand, want)
	}
	if _, err := os.Stat(filepath.Join("..", "..", "bin", "install-to-green.sh")); err != nil {
		t.Fatalf("the release lane runs a harness that is not in the checkout: %v", err)
	}
}

func TestInstallToGreenRecordsAnUnreachableProxyAsSkippedAndFailsEverythingElse(t *testing.T) {
	record := `{"total_seconds":45.2,"green":true}`
	measured, err := installToGreenOutcome(record+"\n", nil)
	if err != nil {
		t.Fatalf("a measured run must not fail the lane: %v", err)
	}
	if measured != record {
		t.Errorf("the lane logged %q, want the harness record %q", measured, record)
	}

	unreachable := &sparkwing.ExecError{Command: installToGreenCommand, ExitCode: installToGreenUnavailable}
	measured, err = installToGreenOutcome("", unreachable)
	if err != nil {
		t.Fatalf("an unreachable proxy must not fail the lane: %v", err)
	}
	if !strings.Contains(measured, "skipped") {
		t.Errorf("an unreachable proxy logged %q, which does not say the measurement was skipped", measured)
	}

	notGreen := &sparkwing.ExecError{Command: installToGreenCommand, ExitCode: 1}
	if _, err := installToGreenOutcome("", notGreen); err == nil {
		t.Error("a demo path that never reached green passed the lane")
	}
}

func TestDogfoodPipelineModuleIsTidy(t *testing.T) {
	cmd := exec.Command("go", "mod", "tidy", "-diff")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf(".sparkwing module is not tidy:\n%s", out)
	}
}

func TestReplaceBanReadsEveryCommittedGoMod(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()

	if err := checkNoReplaceDirectivesInCommittedGoMods(ctx); err != nil {
		t.Fatalf("a fixture with no replace lines must pass: %v", err)
	}

	writeGoFile(t, filepath.Join(root, "tools", "go.mod"),
		"module fixture/tools\n\ngo 1.25\n\nreplace example.com/dep => ../dep\n")
	gitAddAll(t, root)

	if err := checkNoReplaceDirectivesInCommittedGoMods(ctx); err == nil {
		t.Fatal("the replace ban skipped a committed module outside the root and .sparkwing/")
	}
}

func TestReplaceBanAllowsTheDogfoodSelfReplace(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()

	writeGoFile(t, filepath.Join(root, ".sparkwing", "go.mod"),
		"module fixture-pipelines\n\ngo 1.25\n\nreplace github.com/sparkwing-dev/sparkwing => ..\n")
	gitAddAll(t, root)

	if err := checkNoReplaceDirectivesInCommittedGoMods(ctx); err != nil {
		t.Fatalf("the dogfood self-replace must be allowed: %v", err)
	}
}

func TestIsTestdataPath(t *testing.T) {
	inside := []string{
		"testdata/trial-repo/go.mod",
		"internal/agenttrial/testdata/trial-repo/go.mod",
		"a/b/testdata/c/go.mod",
	}
	for _, p := range inside {
		if !isTestdataPath(p) {
			t.Errorf("isTestdataPath(%q) = false, want true", p)
		}
	}

	outside := []string{
		"go.mod",
		".sparkwing/go.mod",
		"web/go.mod",
		"internal/testdatabase/go.mod",
	}
	for _, p := range outside {
		if isTestdataPath(p) {
			t.Errorf("isTestdataPath(%q) = true, want false", p)
		}
	}
}

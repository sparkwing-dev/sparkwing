package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func readRunnerTools(t *testing.T, path string) []string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tools []string
	for _, line := range strings.Split(string(body), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			tools = append(tools, line)
		}
	}
	return tools
}

func TestRunnerImageDeclaresTheCloudToolset(t *testing.T) {
	t.Parallel()

	declared := readRunnerTools(t, "../../build/runner-tools")
	if !slices.Equal(declared, match.CloudTools) {
		t.Fatalf("build/runner-tools lists %v but match.CloudTools is %v; change both together", declared, match.CloudTools)
	}
	for _, tool := range declared {
		if !match.IsKnownTool(tool) {
			t.Errorf("build/runner-tools lists %q, which is not in match.KnownTools", tool)
		}
	}
	dockerfile, err := os.ReadFile("../../build/Dockerfile.runner")
	if err != nil {
		t.Fatal(err)
	}
	instructions := dockerfileInstructions(dockerfile)
	copyAt := slices.Index(instructions, "COPY build/runner-tools /usr/local/share/sparkwing/runner-tools")
	checkAt := slices.Index(instructions, "RUN /bin/sh /usr/local/bin/check-runner-image.sh")
	if copyAt < 0 || checkAt < copyAt {
		t.Error("the runner image does not copy build/runner-tools ahead of the image check")
	}
	workflow, err := os.ReadFile("../../.github/workflows/release.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(workflow), ".release-tools/build/runner-tools build/") {
		t.Error("the release workflow does not copy build/runner-tools into the image build context")
	}
}

func TestRunnerImageCheckFailsOnADeclaredToolTheImageLacks(t *testing.T) {
	t.Parallel()

	tools := filepath.Join(t.TempDir(), "runner-tools")
	if err := os.WriteFile(tools, []byte("# comment\nsparkwing-no-such-tool\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "../../bin/check-runner-image.sh")
	cmd.Env = append(os.Environ(), "RUNNER_TOOLS="+tools)
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "declares sparkwing-no-such-tool in build/runner-tools but lacks it") {
		t.Fatalf("image check passed a missing declared tool: err=%v output=%s", err, out)
	}
}

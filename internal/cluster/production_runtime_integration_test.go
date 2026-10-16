//go:build rewrite_integration

package cluster

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstalledRunnerRuntimeComposition(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_RUNNER_ENTRYPOINT") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{"sparkwing-runner"}, os.Args[i+1:]...)
				MainWithVersion("v1.2.3")
				os.Exit(0)
			}
		}
		os.Exit(2)
	}
	options, _ := bootstrapPlanFile(t)
	directory := t.TempDir()
	config := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(config, []byte("runner:\n  controller: https://controller.example\n  repos: [github.com/team/*]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	image := "example.com/image@sha256:" + strings.Repeat("a", 64)
	commands := map[string][]string{
		"start":        {"--config=" + config},
		"warm":         {"--controller=https://controller.example"},
		"launch":       {"--controller=https://controller.example", "--image=" + image, "--docker-image=" + image, "--cpu-ceiling=1", "--memory-ceiling=1Gi", "--credentials-dir=" + directory},
		"fetch-source": bootstrapArguments(options),
		"run-node":     bootstrapArguments(options),
	}
	for name, args := range commands {
		t.Run(name, func(t *testing.T) {
			child := exec.CommandContext(t.Context(), os.Args[0], append([]string{"-test.run=^TestInstalledRunnerRuntimeComposition$", "--", name}, args...)...)
			child.Env = append(os.Environ(), "SPARKWING_TEST_RUNNER_ENTRYPOINT=1", "HOME="+directory, "SPARKWING_HOME="+filepath.Join(directory, "state"), "XDG_CACHE_HOME="+filepath.Join(directory, "cache"))
			output, err := child.CombinedOutput()
			t.Logf("entrypoint result: %s", output)
			if err == nil {
				t.Fatal("runner exited without attempting its configured production operation")
			}
			for _, missing := range []string{"runner runtime is unavailable", "launcher runtime is unavailable", "requires the source-access service binding", "requires the shared host service binding"} {
				if strings.Contains(string(output), missing) {
					t.Fatalf("installed entrypoint has no production composition: %s", output)
				}
			}
		})
	}
}

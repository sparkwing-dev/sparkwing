//go:build rewrite_integration

package cluster

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/source"
)

const productionHTTPBoundary = "production HTTP boundary"

func TestInstalledRunnerRuntimeComposition(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_RUNNER_ENTRYPOINT") == "1" {
		http.DefaultTransport = productionTransport(func(r *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("%s: %s %s%s", productionHTTPBoundary, r.Method, r.URL.Host, r.URL.Path)
		})
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{"sparkwing-runner"}, os.Args[i+1:]...)
				MainWithVersion("v1.2.3")
				os.Exit(0)
			}
		}
		os.Exit(2)
	}
	options, award := bootstrapPlanFile(t)
	award.Source = source.Spec{RepoKey: "gh:42", RemoteURL: "https://github.com/acme/project", SHA: strings.Repeat("a", 40), PipelineDir: ".sparkwing", DefaultBranch: "main"}
	metadata, err := json.Marshal(award)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(options.BootstrapFile), "award.json"), metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	credentials := runnerCredentials(t)
	config := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(config, []byte("runner:\n  controller: https://controller.example\n  repos: [github.com/team/*]\n  credentials_dir: "+credentials+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	image := "example.com/image@sha256:" + strings.Repeat("a", 64)
	commands := map[string][]string{
		"start":        {"--config=" + config},
		"warm":         {"--controller=https://controller.example", "--credentials-dir=" + credentials},
		"launch":       {"--controller=https://controller.example", "--image=" + image, "--docker-image=" + image, "--cpu-ceiling=1", "--memory-ceiling=1Gi", "--credentials-dir=" + credentials},
		"fetch-source": bootstrapArguments(options),
		"run-node":     bootstrapArguments(options),
	}
	operations := map[string]string{
		"start":        "GET sparkwing.dev/versions.json",
		"warm":         "GET sparkwing.dev/versions.json",
		"fetch-source": "POST controller.example/api/v1/runs/run/source-credential",
		"run-node":     "POST controller.example/api/v1/claims/exchange",
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
			want := productionHTTPBoundary
			if operation, ok := operations[name]; ok {
				want += ": " + operation
			}
			if !strings.Contains(string(output), want) {
				t.Fatalf("installed entrypoint did not reach its production HTTP operation: %s", output)
			}
		})
	}
}

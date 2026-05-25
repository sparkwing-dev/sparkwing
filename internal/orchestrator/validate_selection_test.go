package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

var _ = context.Background // imported for fakeClusterRunner signature

// writeSourcesYAML nests a sources.File body under the sparkwing.yaml
// sources: section so the projectconfig-backed resolver picks it up.
func writeSourcesYAML(t *testing.T, dir, body string) {
	t.Helper()
	swDir := filepath.Join(dir, ".sparkwing")
	if err := os.MkdirAll(swDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var nested strings.Builder
	nested.WriteString("sources:\n")
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if line == "" {
			nested.WriteString("\n")
			continue
		}
		nested.WriteString("  " + line + "\n")
	}
	if err := os.WriteFile(filepath.Join(swDir, "sparkwing.yaml"), []byte(nested.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestValidateTargetSelection(t *testing.T) {
	cases := []struct {
		name    string
		opts    Options
		wantErr string
	}{
		{
			name: "no pipelines.yaml",
			opts: Options{Pipeline: "p", Target: "x"},
		},
		{
			name: "pipeline declares no targets, --for set",
			opts: Options{
				Pipeline:     "lint",
				Target:       "anything",
				PipelineYAML: &pipelines.Pipeline{Name: "lint"},
			},
			wantErr: "does not declare any targets",
		},
		{
			name: "pipeline with targets, --for empty",
			opts: Options{
				Pipeline: "release",
				PipelineYAML: &pipelines.Pipeline{
					Name:    "release",
					Targets: map[string]pipelines.Target{"prod": {}, "staging": {}},
				},
			},
		},
		{
			name: "pipeline with targets, --for matches",
			opts: Options{
				Pipeline: "release",
				Target:   "prod",
				PipelineYAML: &pipelines.Pipeline{
					Name:    "release",
					Targets: map[string]pipelines.Target{"prod": {}, "staging": {}},
				},
			},
		},
		{
			name: "pipeline with targets, --for unknown",
			opts: Options{
				Pipeline: "release",
				Target:   "production",
				PipelineYAML: &pipelines.Pipeline{
					Name:    "release",
					Targets: map[string]pipelines.Target{"prod": {}, "staging": {}},
				},
			},
			wantErr: "no target \"production\"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTargetSelection(tc.opts)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("got %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("got %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateSourceRunnerPortability_RejectsLaptopOnlyOnCluster(t *testing.T) {
	dir := t.TempDir()
	writeSourcesYAML(t, dir, `
entries:
  dotenv:
    type: file
    path: .sparkwing/secrets.local.env
`)
	opts := Options{
		Target:       "dev",
		SparkwingDir: filepath.Join(dir, ".sparkwing"),
		PipelineYAML: &pipelines.Pipeline{
			Targets: map[string]pipelines.Target{
				"dev": {Source: "dotenv"},
			},
		},
	}
	err := validateSourceRunnerPortability(opts, fakeClusterRunner{})
	if err == nil || !strings.Contains(err.Error(), "laptop-only") {
		t.Errorf("got %v", err)
	}
}

func TestValidateSourceRunnerPortability_AcceptsLocalRunner(t *testing.T) {
	dir := t.TempDir()
	writeSourcesYAML(t, dir, `
entries:
  dotenv:
    type: file
    path: .sparkwing/secrets.local.env
`)
	opts := Options{
		Target:       "dev",
		SparkwingDir: filepath.Join(dir, ".sparkwing"),
		PipelineYAML: &pipelines.Pipeline{
			Targets: map[string]pipelines.Target{
				"dev": {Source: "dotenv"},
			},
		},
	}
	// nil runner == local (RunLocal default).
	if err := validateSourceRunnerPortability(opts, nil); err != nil {
		t.Errorf("nil runner should pass, got %v", err)
	}
	if err := validateSourceRunnerPortability(opts, &InProcessRunner{}); err != nil {
		t.Errorf("InProcessRunner should pass, got %v", err)
	}
}

func TestValidateSourceRunnerPortability_AcceptsRemoteController(t *testing.T) {
	dir := t.TempDir()
	writeSourcesYAML(t, dir, `
entries:
  prod-vault:
    type: profile
    profile: prod
`)
	opts := Options{
		Target:       "prod",
		SparkwingDir: filepath.Join(dir, ".sparkwing"),
		PipelineYAML: &pipelines.Pipeline{
			Targets: map[string]pipelines.Target{
				"prod": {Source: "prod-vault"},
			},
		},
	}
	if err := validateSourceRunnerPortability(opts, fakeClusterRunner{}); err != nil {
		t.Errorf("remote-controller source on cluster runner should pass, got %v", err)
	}
}

func TestValidateSourceRunnerPortability_AcceptsEnvSource(t *testing.T) {
	dir := t.TempDir()
	writeSourcesYAML(t, dir, `
entries:
  shell-env:
    type: env
    prefix: SW_
`)
	opts := Options{
		Target:       "dev",
		SparkwingDir: filepath.Join(dir, ".sparkwing"),
		PipelineYAML: &pipelines.Pipeline{
			Targets: map[string]pipelines.Target{
				"dev": {Source: "shell-env"},
			},
		},
	}
	if err := validateSourceRunnerPortability(opts, fakeClusterRunner{}); err != nil {
		t.Errorf("env source on cluster runner should pass (env vars are portable), got %v", err)
	}
}

// fakeClusterRunner stands in for a cluster pool / k8s runner: no
// "local" label, satisfies runner.Runner with a no-op RunNode.
type fakeClusterRunner struct{}

func (fakeClusterRunner) RunNode(context.Context, runner.Request) runner.Result {
	return runner.Result{}
}

func (fakeClusterRunner) AdvertisedLabels() []string {
	return []string{"cloud-linux", "os=linux"}
}

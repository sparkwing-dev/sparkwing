package cluster

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

// A chart runner sets GOCACHE and GOMODCACHE on its cache volume; a pipeline
// compile must use both, or the build cache is cold after every restart.
func TestRunnerInstallsUseTheRunnersGoCaches(t *testing.T) {
	t.Setenv("GOCACHE", "/tmp/sparkwing/gocache")
	for _, owner := range []bool{false, true} {
		env, err := procenv.Build(procenv.Install, runnerInstallBase(owner, "/tmp/sparkwing/gomodcache"), procenv.Runtime{})
		if err != nil {
			t.Fatal(err)
		}
		if got := env.Lookup("GOCACHE"); got != "/tmp/sparkwing/gocache" {
			t.Errorf("owner=%v install GOCACHE = %q, want the runner's cache", owner, got)
		}
		if got := env.Lookup("GOMODCACHE"); got != "/tmp/sparkwing/gomodcache" {
			t.Errorf("owner=%v install GOMODCACHE = %q, want the runner's module cache", owner, got)
		}
	}
}

func TestCloudProcessesKeepTheSidecarDockerHost(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///tmp/docker.sock")
	for _, kind := range []procenv.Kind{procenv.Plan, procenv.Eval, procenv.Attempt, procenv.Install} {
		env, err := procenv.Build(kind, runnerInstallBase(false, "/tmp/modules"), procenv.Runtime{})
		if err != nil {
			t.Fatal(err)
		}
		if got := env.Lookup("DOCKER_HOST"); got != "unix:///tmp/docker.sock" {
			t.Errorf("kind=%v DOCKER_HOST=%q, want the Cloud sidecar", kind, got)
		}
	}
}

func TestSharedRunnerDoesNotInheritAnUnconfiguredDockerHost(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///operator/docker.sock")
	env, err := procenv.Build(procenv.Attempt, runnerInstallBase(false, ""), procenv.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if got := env.Lookup("DOCKER_HOST"); got != "" {
		t.Fatalf("inherited operator DOCKER_HOST=%q", got)
	}
}

// Pipeline compiles default to CGO_ENABLED=0; the runner service's own
// CGO_ENABLED=1 is how an operator builds pipelines that link C.
func TestRunnerInstallsKeepTheOperatorsCgoSetting(t *testing.T) {
	t.Setenv("CGO_ENABLED", "1")
	for _, owner := range []bool{false, true} {
		env, err := procenv.Build(procenv.Install, runnerInstallBase(owner, ""), procenv.Runtime{})
		if err != nil {
			t.Fatal(err)
		}
		if got := env.Lookup("CGO_ENABLED"); got != "1" {
			t.Errorf("owner=%v install CGO_ENABLED = %q, want the runner's setting", owner, got)
		}
	}
}

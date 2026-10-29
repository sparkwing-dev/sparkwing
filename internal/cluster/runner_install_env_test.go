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

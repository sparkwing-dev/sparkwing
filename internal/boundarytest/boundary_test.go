// Package boundarytest enforces the open-core dependency boundary in
// the public sparkwing repo: nothing reachable from the CLI binary
// roots may pull github.com/sparkwing-dev/sparkwing-platform (the
// private engine) or k8s.io/* into its import closure. LOCAL-006
// stood up internal/local as a fork of the engine's controller pkg
// precisely so the CLI could go public; this test is the regression
// gate for that boundary.
package boundarytest_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestCLIBinaryBoundary(t *testing.T) {
	roots := []struct {
		name string
		pkg  string
	}{
		{"cmd/sparkwing", "github.com/sparkwing-dev/sparkwing/cmd/sparkwing"},
		{"cmd/sparkwing-local-ws", "github.com/sparkwing-dev/sparkwing/cmd/sparkwing-local-ws"},
		{"cmd/sparkwing-web", "github.com/sparkwing-dev/sparkwing/cmd/sparkwing-web"},
		{"cmd/sparkwing-cache", "github.com/sparkwing-dev/sparkwing/cmd/sparkwing-cache"},
		{"cmd/sparkwing-logs", "github.com/sparkwing-dev/sparkwing/cmd/sparkwing-logs"},
		{"cmd/sparkwing-runner", "github.com/sparkwing-dev/sparkwing/cmd/sparkwing-runner"},
		{"pkg/localws", "github.com/sparkwing-dev/sparkwing/pkg/localws"},
		{"orchestrator", "github.com/sparkwing-dev/sparkwing/orchestrator"},
		{"sparkwing", "github.com/sparkwing-dev/sparkwing/sparkwing"},
		{"controller/client", "github.com/sparkwing-dev/sparkwing/controller/client"},
	}

	forbiddenPrefixes := []string{
		"github.com/sparkwing-dev/sparkwing-platform",
		"github.com/sparkwing-dev/sparkwing-cli",
	}

	for _, r := range roots {
		t.Run(r.name, func(t *testing.T) {
			cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", r.pkg)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("go list -deps %s: %v\n%s", r.pkg, err, out)
			}
			for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				dep = strings.TrimSpace(dep)
				if dep == "" {
					continue
				}
				for _, p := range forbiddenPrefixes {
					if strings.HasPrefix(dep, p) {
						t.Errorf("%s transitively imports forbidden package %q (matches %q)", r.pkg, dep, p)
					}
				}
			}
		})
	}
}

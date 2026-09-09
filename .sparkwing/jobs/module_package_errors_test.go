package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestModulePackageDiscoveryRejectsProductErrorsBeforeCommand(t *testing.T) {
	for _, source := range []string{"package\n", "package broken\nimport _ \"fixture/missing\"\n"} {
		t.Run(source, func(t *testing.T) {
			root := gateFixtureRepo(t)
			writeGoFile(t, filepath.Join(root, "broken", "source.go"), source)
			gitAddAll(t, root)
			err := forEachGoModule(context.Background(), "probe", "touch tool-ran; true ./...", nil, true)
			if err == nil || !strings.Contains(err.Error(), "list packages") {
				t.Errorf("gate error = %v, want package discovery failure", err)
			}
			if _, err := os.Stat(filepath.Join(root, "tool-ran")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("command ran after package discovery failed: %v", err)
			}
		})
	}
}

func TestModulePackageDiscoveryExcludesBrokenNodeModules(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "web", "node_modules", "dependency", "broken.go"), "package\n")
	packages, err := modulePackageArgs(context.Background(), ".", true)
	if err != nil || len(packages) != 1 || packages[0] != `"fixture/internal"` {
		t.Errorf("packages = %v, %v; want only product package", packages, err)
	}
	empty, err := moduleHasNoPackages(context.Background(), ".")
	if err != nil || empty {
		t.Errorf("classification = %t, %v; want populated product module", empty, err)
	}
}

func TestModulePackageDiscoveryPreservesCommandContext(t *testing.T) {
	gateFixtureRepo(t)
	ctx := sparkwing.WithCommandEnv(context.Background(), map[string]string{"GOFLAGS": "-fixture-invalid-flag"})
	_, err := modulePackageArgs(ctx, ".", true)
	var commandError *sparkwing.ExecError
	if !errors.As(err, &commandError) || !strings.Contains(err.Error(), "fixture-invalid-flag") {
		t.Fatalf("discovery error = %v, want command flag failure", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = modulePackageArgs(canceled, ".", true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("discovery error = %v, want cancellation", err)
	}
}

func TestModulePackageDiscoveryKeepsExcludedDependenciesOutOfLint(t *testing.T) {
	root := lintFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "web", "node_modules", "dependency", "broken.go"), "package\n")
	if err := runGolangciLint(context.Background()); err != nil {
		t.Fatalf("excluded dependency changed lint verdict: %v", err)
	}
}

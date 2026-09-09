package jobs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestModuleClassification(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "empty", "go.mod"), "module fixture/empty\n\ngo 1.25\n")
	writeGoFile(t, filepath.Join(root, "broken", "go.mod"), "invalid module declaration\n")
	for _, testCase := range []struct {
		name   string
		empty  bool
		failed bool
	}{
		{".", false, false}, {"empty", true, false}, {"broken", false, true}, {"missing", false, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			empty, err := moduleHasNoPackages(context.Background(), testCase.name)
			if (err != nil) != testCase.failed || empty != testCase.empty {
				t.Fatalf("classification = %t, %v; want empty %t, failure %t", empty, err, testCase.empty, testCase.failed)
			}
			if testCase.name == "broken" {
				var exitError *exec.ExitError
				if !errors.As(err, &exitError) {
					t.Errorf("classification lost process exit cause: %v", err)
				}
			}
		})
	}
}

func TestModuleClassificationPreservesCancellation(t *testing.T) {
	gateFixtureRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := moduleHasNoPackages(ctx, ".")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("classification error = %v, want cancellation", err)
	}
}

func TestModuleClassificationStopsGoTool(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "go.mod"), "invalid module declaration\n")
	err := forEachGoModule(context.Background(), "probe", "touch tool-ran", nil)
	if err == nil {
		t.Error("gate accepted a module classification failure")
	}
	if _, statErr := os.Stat(filepath.Join(root, "tool-ran")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("tool ran after classification failure: %v", statErr)
	}
}

func TestModuleClassificationStopsLint(t *testing.T) {
	root := lintFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "go.mod"), "invalid module declaration\n")
	err := runGolangciLint(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list packages") {
		t.Fatalf("lint error = %v, want package classification failure", err)
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Errorf("lint lost classification process exit cause: %v", err)
	}
}

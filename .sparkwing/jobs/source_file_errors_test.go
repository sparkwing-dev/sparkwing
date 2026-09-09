package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestSourcePoliciesRequireAbsoluteRoot(t *testing.T) {
	for _, rootValue := range []string{"", "."} {
		t.Run("root="+rootValue, func(t *testing.T) {
			root := gateFixtureRepo(t)
			t.Chdir(root)
			sparkwing.SetWorkDir(rootValue)
			for _, check := range []func(context.Context) error{checkHomeResolution, checkEmDashes, checkTrackerIDs} {
				if err := check(context.Background()); err == nil || !strings.Contains(err.Error(), "absolute working directory") {
					t.Errorf("source policy error = %v, want absolute working directory requirement", err)
				}
			}
		})
	}
}

func TestGoScopePreservesStatFailure(t *testing.T) {
	root := gateFixtureRepo(t)
	directory := filepath.Join(root, "internal")
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(directory, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := changeScope(context.Background(), "Go file(s)", goSourceFiles)
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("scope error = %v, want ENOTDIR", err)
	}
}

func TestSourcePoliciesPreserveReadFailure(t *testing.T) {
	for name, check := range map[string]func(context.Context) error{
		"home": checkHomeResolution, "dashes": checkEmDashes, "identifiers": checkTrackerIDs,
	} {
		t.Run(name, func(t *testing.T) {
			root := gateFixtureRepo(t)
			source := filepath.Join(root, "internal", "sound.go")
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(source, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := check(context.Background()); !errors.Is(err, syscall.EISDIR) {
				t.Fatalf("source policy error = %v, want EISDIR", err)
			}
		})
	}
}

func TestSourcePoliciesPreserveDeletedFiles(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	if err := os.Remove(filepath.Join(root, "internal", "sound.go")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_REGEX_SWEEP_ALL", "1")
	for _, check := range []func(context.Context) error{checkHomeResolution, checkEmDashes, checkTrackerIDs} {
		if err := check(context.Background()); err != nil {
			t.Errorf("deleted file blocked source policy: %v", err)
		}
	}
}

func TestSourcePoliciesRejectDanglingTrackedSymlink(t *testing.T) {
	root := gateFixtureRepo(t)
	source := filepath.Join(root, "internal", "sound.go")
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-source", source); err != nil {
		t.Fatal(err)
	}
	for name, check := range map[string]func(context.Context) error{
		"home": checkHomeResolution, "dashes": checkEmDashes, "identifiers": checkTrackerIDs,
	} {
		if err := check(context.Background()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s error = %v, want missing symlink target", name, err)
		}
	}
	_, _, err := changeScope(context.Background(), "Go file(s)", goSourceFiles)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Go scope error = %v, want missing symlink target", err)
	}
}

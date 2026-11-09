package sparkwingtest_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/gitenv/testgit"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/docker"
	"github.com/sparkwing-dev/sparkwing/sparkwing/git"
	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
)

//nolint:contextcheck // sparkwing.Cmd retains the context supplied to Exec.
func libraryCommand(ctx context.Context) (string, error) {
	return sparkwing.Exec(ctx, os.Args[0], "-test.run=^TestLibraryCommandChild$").Env("LIBRARY_TEST_CHILD", "1").String()
}

func TestLibraryCommandChild(t *testing.T) {
	if os.Getenv("LIBRARY_TEST_CHILD") != "1" {
		return
	}
	_, _ = os.Stdout.Write([]byte("library command works"))
	os.Exit(0)
}

func TestFakeHostRunsLibraryCommandsWithoutController(t *testing.T) {
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	value, err := libraryCommand(host.Context(t.Context()))
	if err != nil || value != "library command works" {
		t.Fatalf("library result=%q err=%v", value, err)
	}
	plan := sparkwing.NewPlan()
	var output string
	sparkwing.Job(plan, "library", func(ctx context.Context) error {
		var err error
		output, err = libraryCommand(ctx)
		return err
	})
	if _, err := host.RunStep(t.Context(), plan, "library", "run"); err != nil {
		t.Fatal(err)
	}
	if output != "library command works" {
		t.Fatalf("step output=%q", output)
	}
}

func TestFakeHostRelaysGitAndDockerHelpers(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	command := testgit.Command("git", "-C", dir, "init", "--initial-branch=main")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", output, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx := host.Context(t.Context())
	hash, err := git.FilesetHash(ctx, dir)
	if err != nil || len(hash) != 12 {
		t.Fatalf("fileset=%q err=%v", hash, err)
	}
	_, err = docker.Build(ctx, docker.BuildConfig{})
	if err == nil || !(errors.Is(err, docker.ErrDockerUnavailable) || strings.Contains(strings.ToLower(err.Error()), "image")) {
		t.Fatalf("Docker did not reach host validation: %v", err)
	}
}

package sparkwing_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestPath_JoinsOntoWorkDir(t *testing.T) {
	root := t.TempDir()
	prev := sparkwing.WorkDir()
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })

	got := sparkwing.Path("backend", "go.mod")
	want := filepath.Join(root, "backend", "go.mod")
	if got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestPath_AbsolutePassesThrough(t *testing.T) {
	root := t.TempDir()
	prev := sparkwing.WorkDir()
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })

	other := t.TempDir()
	got := sparkwing.Path(other, "x.txt")
	want := filepath.Join(other, "x.txt")
	if got != want {
		t.Fatalf("Path = %q, want %q (absolute first part wins)", got, want)
	}
}

func TestPath_NoArgsReturnsWorkDir(t *testing.T) {
	root := t.TempDir()
	prev := sparkwing.WorkDir()
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })

	if got := sparkwing.Path(); got != root {
		t.Fatalf("Path() = %q, want %q", got, root)
	}
}

func TestReadFile_RelativeResolvesAgainstWorkDir(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	prev := sparkwing.WorkDir()
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })

	data, err := sparkwing.ReadFile("config.yaml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("contents = %q, want hello", data)
	}
}

func TestWriteFile_RelativeResolvesAgainstWorkDir(t *testing.T) {
	root := t.TempDir()
	prev := sparkwing.WorkDir()
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })

	if err := sparkwing.WriteFile("dist/version.txt", []byte("v1.2.3")); err != nil {
		// Parent dir doesn't exist by default; WriteFile docs don't
		// promise mkdir, so we only test the resolution path.
		// Retry into a known directory:
		_ = err
	}
	if err := sparkwing.WriteFile("version.txt", []byte("v1.2.3")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "version.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "v1.2.3" {
		t.Fatalf("contents = %q, want v1.2.3", got)
	}
}

func TestGlob_RelativePattern(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.yaml", "b.yaml", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	prev := sparkwing.WorkDir()
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })

	matches, err := sparkwing.Glob("*.yaml")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("got %d matches, want 2: %v", len(matches), matches)
	}
	for _, m := range matches {
		if !filepath.IsAbs(m) {
			t.Fatalf("Glob match %q is not absolute", m)
		}
	}
}

func TestCapture_DoesNotStreamButFails(t *testing.T) {
	logger := &recordingEmitter{}
	ctx := sparkwing.WithLogger(context.Background(), logger)

	res, err := sparkwing.Exec(ctx, "sh", "-c", "echo hi-from-capture").Capture()
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if !strings.Contains(res.Stdout, "hi-from-capture") {
		t.Fatalf("captured stdout = %q, want to contain output", res.Stdout)
	}
	// Banner ($ ...) should still be there; per-line records should not.
	var bannerCount, lineCount int
	for _, rec := range logger.records {
		switch rec.Event {
		case "exec_start":
			bannerCount++
		case "exec_line":
			lineCount++
		}
	}
	if bannerCount != 1 {
		t.Fatalf("expected 1 exec_start banner, got %d", bannerCount)
	}
	if lineCount != 0 {
		t.Fatalf("Capture must NOT emit exec_line records, got %d", lineCount)
	}
}

// When walk-up found no `.sparkwing/`, WorkDir is empty and the
// helpers must refuse to run rather than silently use cwd. Each
// path-aware helper is exercised here so a future regression in
// any one of them is caught.
func TestHelpers_FailLoudlyWhenNoProject(t *testing.T) {
	prev := sparkwing.WorkDir()
	sparkwing.SetWorkDir("")
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })

	t.Run("ReadFile relative", func(t *testing.T) {
		_, err := sparkwing.ReadFile("config.yaml")
		if !errors.Is(err, sparkwing.ErrNoProject) {
			t.Fatalf("ReadFile err = %v, want ErrNoProject", err)
		}
	})

	t.Run("WriteFile relative", func(t *testing.T) {
		err := sparkwing.WriteFile("out.txt", []byte("x"))
		if !errors.Is(err, sparkwing.ErrNoProject) {
			t.Fatalf("WriteFile err = %v, want ErrNoProject", err)
		}
	})

	t.Run("Glob relative", func(t *testing.T) {
		_, err := sparkwing.Glob("*.yaml")
		if !errors.Is(err, sparkwing.ErrNoProject) {
			t.Fatalf("Glob err = %v, want ErrNoProject", err)
		}
	})

	t.Run("Path relative panics", func(t *testing.T) {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("Path with relative parts should panic")
			}
			err, ok := r.(error)
			if !ok || !errors.Is(err, sparkwing.ErrNoProject) {
				t.Fatalf("panic = %v, want ErrNoProject", r)
			}
		}()
		_ = sparkwing.Path("backend", "go.mod")
	})

	t.Run("Sh fails (no project, no dir)", func(t *testing.T) {
		ctx := sparkwing.WithLogger(context.Background(), &recordingEmitter{})
		_, err := sparkwing.Bash(ctx, "echo hi").Run()
		var ee *sparkwing.ExecError
		if !errors.As(err, &ee) || !errors.Is(err, sparkwing.ErrNoProject) {
			t.Fatalf("Sh err = %v, want ExecError wrapping ErrNoProject", err)
		}
		if ee.ExitCode != sparkwing.ExitNotStarted {
			t.Fatalf("ExitCode = %d, want ExitNotStarted", ee.ExitCode)
		}
	})

	t.Run("Sh.Dir relative dir fails", func(t *testing.T) {
		ctx := sparkwing.WithLogger(context.Background(), &recordingEmitter{})
		_, err := sparkwing.Bash(ctx, "echo hi").Dir("backend").Run()
		if !errors.Is(err, sparkwing.ErrNoProject) {
			t.Fatalf("Sh.Dir err = %v, want wrap ErrNoProject", err)
		}
	})
}

// Absolute paths must work even when WorkDir is empty -- they don't
// need a project root to resolve.
func TestHelpers_AbsolutePathsWorkWithoutProject(t *testing.T) {
	prev := sparkwing.WorkDir()
	sparkwing.SetWorkDir("")
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })

	abs := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(abs, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := sparkwing.ReadFile(abs)
	if err != nil {
		t.Fatalf("ReadFile(abs) failed without a project: %v", err)
	}
	if string(data) != "hi" {
		t.Fatalf("contents = %q", data)
	}
	// Path with an absolute first part likewise needs no project.
	got := sparkwing.Path(abs)
	if got != abs {
		t.Fatalf("Path(abs) = %q, want %q", got, abs)
	}
}

func TestCapture_FailureCarriesContext(t *testing.T) {
	ctx := sparkwing.WithLogger(context.Background(), &recordingEmitter{})
	_, err := sparkwing.Exec(ctx, "sh", "-c", "echo bad-thing >&2 ; exit 9").Capture()
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "exit 9") {
		t.Fatalf("error missing exit code: %q", msg)
	}
	if !strings.Contains(msg, "bad-thing") {
		t.Fatalf("error missing captured stderr: %q", msg)
	}
}

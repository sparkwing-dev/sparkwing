package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func cronPinnedFixture(t *testing.T) (repoDir string, cache *localCompileCache, logger *slog.Logger) {
	t.Helper()
	t.Setenv("SPARKWING_HOME", t.TempDir())
	repoDir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	cache = &localCompileCache{}
	t.Cleanup(func() { _ = cache.Close() })
	return repoDir, cache, slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestDispatchLocalTrigger_RunsThePinnedBinaryInsteadOfCompiling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture is a shell script")
	}
	repoDir, cache, logger := cronPinnedFixture(t)
	record := filepath.Join(t.TempDir(), "argv")
	pinned := filepath.Join(t.TempDir(), "pipeline")
	script := "#!/bin/sh\nprintf '%s|%s\\n' \"$*\" \"$PWD\" > " + record + "\n"
	if err := os.WriteFile(pinned, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	// safety: the .sparkwing/ here holds no module, so a dispatch that compiled
	// instead of running the pin would fail rather than pass quietly.
	err := dispatchLocalTrigger(context.Background(), &store.Trigger{
		ID:         "run-pinned",
		Pipeline:   "nightly",
		TriggerEnv: map[string]string{SubmitRepoDirKey: repoDir, crons.PinnedBinaryEnvKey: pinned},
	}, "", repoDir, cache, logger, nil)
	if err != nil {
		t.Fatalf("dispatch the pinned binary: %v", err)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the pinned binary did not run: %v", err)
	}
	argv, pwd, _ := strings.Cut(strings.TrimSpace(string(raw)), "|")
	if argv != "handle-trigger --local run-pinned" {
		t.Errorf("the pinned binary was given %q", argv)
	}
	if pwd != repoDir {
		t.Errorf("working directory = %q, want the repo checkout %q", pwd, repoDir)
	}
}

func TestDispatchLocalTrigger_FailsWhenThePinnedBinaryIsGone(t *testing.T) {
	repoDir, cache, logger := cronPinnedFixture(t)
	missing := filepath.Join(t.TempDir(), "pipeline")

	err := dispatchLocalTrigger(context.Background(), &store.Trigger{
		ID:         "run-missing",
		Pipeline:   "nightly",
		TriggerEnv: map[string]string{SubmitRepoDirKey: repoDir, crons.PinnedBinaryEnvKey: missing},
	}, "", repoDir, cache, logger, nil)
	if err == nil {
		t.Fatal("a pin whose binary is gone dispatched anyway")
	}
	for _, want := range []string{missing, "crons install", "crons unlock"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

func TestDispatchLocalTrigger_RefusesAPinThatIsNotExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes do not gate execution on Windows")
	}
	repoDir, cache, logger := cronPinnedFixture(t)
	pinned := filepath.Join(t.TempDir(), "pipeline")
	if err := os.WriteFile(pinned, []byte("not a program"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := dispatchLocalTrigger(context.Background(), &store.Trigger{
		ID:         "run-not-executable",
		Pipeline:   "nightly",
		TriggerEnv: map[string]string{SubmitRepoDirKey: repoDir, crons.PinnedBinaryEnvKey: pinned},
	}, "", repoDir, cache, logger, nil)
	if err == nil {
		t.Fatal("a pin that is not executable dispatched anyway")
	}
	if !strings.Contains(err.Error(), "not executable") {
		t.Errorf("error %q does not say the pin is not executable", err)
	}
}

func TestDispatchLocalTrigger_RefusesAPinReplacedSinceItWasArmed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture is a shell script")
	}
	repoDir, cache, logger := cronPinnedFixture(t)
	pinned := filepath.Join(t.TempDir(), "pipeline")
	if err := os.WriteFile(pinned, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	armed, err := crons.FileDigest(pinned)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		SubmitRepoDirKey:         repoDir,
		crons.PinnedBinaryEnvKey: pinned,
		crons.PinnedDigestEnvKey: armed,
	}

	// safety: the control -- an untouched pin must still run, or the check below
	// proves nothing.
	if derr := dispatchLocalTrigger(context.Background(), &store.Trigger{
		ID: "run-intact", Pipeline: "nightly", TriggerEnv: env,
	}, "", repoDir, cache, logger, nil); derr != nil {
		t.Fatalf("an untouched pin was refused: %v", derr)
	}

	if werr := os.WriteFile(pinned, []byte("#!/bin/sh\necho swapped\nexit 0\n"), 0o700); werr != nil {
		t.Fatal(werr)
	}
	derr := dispatchLocalTrigger(context.Background(), &store.Trigger{
		ID: "run-swapped", Pipeline: "nightly", TriggerEnv: env,
	}, "", repoDir, cache, logger, nil)
	if derr == nil {
		t.Fatal("a pinned binary replaced since arming ran anyway")
	}
	for _, want := range []string{pinned, "replaced", armed, "crons install"} {
		if !strings.Contains(derr.Error(), want) {
			t.Errorf("error %q is missing %q", derr, want)
		}
	}
}

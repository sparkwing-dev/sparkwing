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
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func cronPinnedFixture(t *testing.T) (repoDir string, cache *localCompileCache, logger *slog.Logger) {
	t.Helper()
	t.Setenv("SPARKWING_HOME", t.TempDir())
	// safety: a process started inside the temporary directory reports the resolved
	// path, which on darwin is not the one TempDir hands back.
	repoDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	cache = &localCompileCache{}
	t.Cleanup(func() { _ = cache.Close() })
	fakeTriggerHost(t)
	return repoDir, cache, slog.New(slog.NewTextHandler(io.Discard, nil))
}

// hack: a fake stands in for the sparkwing CLI that hosts a claimed trigger's run; it runs the program it is
// handed in the trigger's checkout, with the trigger id, as the host's nodes run it there.
func fakeTriggerHost(t *testing.T) {
	t.Helper()
	host := filepath.Join(t.TempDir(), "sparkwing")
	script := "#!/bin/sh\n[ \"$1 $2\" = \"" + TriggerHostVerb + " --program\" ] || exit 64\nexec \"$3\" \"$4\"\n"
	if err := os.WriteFile(host, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(wingdclient.HostBinEnv, host)
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
	}, repoDir, cache, logger, nil)
	if err != nil {
		t.Fatalf("dispatch the pinned binary: %v", err)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the pinned binary did not run: %v", err)
	}
	argv, pwd, _ := strings.Cut(strings.TrimSpace(string(raw)), "|")
	if argv != "run-pinned" {
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
	}, repoDir, cache, logger, nil)
	if err == nil {
		t.Fatal("a pin whose binary is gone dispatched anyway")
	}
	for _, want := range []string{missing, "crons install", "crons set <name> --unpin"} {
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
	}, repoDir, cache, logger, nil)
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
	}, repoDir, cache, logger, nil); derr != nil {
		t.Fatalf("an untouched pin was refused: %v", derr)
	}

	if werr := os.WriteFile(pinned, []byte("#!/bin/sh\necho swapped\nexit 0\n"), 0o700); werr != nil {
		t.Fatal(werr)
	}
	derr := dispatchLocalTrigger(context.Background(), &store.Trigger{
		ID: "run-swapped", Pipeline: "nightly", TriggerEnv: env,
	}, repoDir, cache, logger, nil)
	if derr == nil {
		t.Fatal("a pinned binary replaced since arming ran anyway")
	}
	for _, want := range []string{pinned, "replaced", armed, "crons install"} {
		if !strings.Contains(derr.Error(), want) {
			t.Errorf("error %q is missing %q", derr, want)
		}
	}
}

// A pipeline binary from an older SDK runs handle-trigger without the pin
// check, so the dispatcher refuses a pinned pipeline before it starts one. The
// fixture is a script, standing in for such a binary.
func TestDispatchLocalTrigger_RefusesAPinnedPipelineBeforeStartingTheBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture is a shell script")
	}
	for name, config := range map[string]string{
		"pinned":     "pipelines:\n  - name: nightly\n    entrypoint: Nightly\n    profile: prod\n",
		"unreadable": "defaults:\n  profile: prod\npipelines:\n  - name: nightly\n    entrypoint: Nightly\n",
	} {
		t.Run(name, func(t *testing.T) {
			repoDir, cache, logger := cronPinnedFixture(t)
			if err := os.WriteFile(filepath.Join(repoDir, ".sparkwing", "sparkwing.yaml"), []byte(config), 0o644); err != nil {
				t.Fatal(err)
			}
			record := filepath.Join(t.TempDir(), "ran")
			binary := filepath.Join(t.TempDir(), "pipeline")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\ntouch "+record+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			err := dispatchLocalTrigger(context.Background(), &store.Trigger{
				ID:         "run-" + name,
				Pipeline:   "nightly",
				TriggerEnv: map[string]string{SubmitRepoDirKey: repoDir, crons.PinnedBinaryEnvKey: binary},
			}, repoDir, cache, logger, nil)
			if err == nil || (name == "pinned" && !strings.Contains(err.Error(), "pinned to profile prod")) {
				t.Fatalf("dispatch = %v, want a refusal", err)
			}
			if _, statErr := os.Stat(record); statErr == nil {
				t.Fatal("the pipeline binary started for a pipeline pinned to a controller")
			}
		})
	}
}

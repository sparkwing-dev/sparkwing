//go:build unix

package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func (h *hostedTrigger) hostInCheckout(t *testing.T, dir string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	return HostTriggerRun(ctx, TriggerHost{
		ControllerURL: h.url, Token: h.token, Trigger: h.trig, Entry: pipelineentry.Binary(h.bin), Dir: dir,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func checkoutWithYAML(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".sparkwing"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".sparkwing", "sparkwing.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The checkout's sparkwing.yaml supplies an argument the trigger left out,
// the pipeline's entry over the project defaults, and the run row records the
// arguments the run planned with, which every node host plans from.
func TestTriggerHost_TheCheckoutsYAMLFillsTheTriggersArgs(t *testing.T) {
	dir := t.TempDir()
	wrong, entry := filepath.Join(dir, "default"), filepath.Join(dir, "entry")
	yaml := "defaults:\n  args:\n    ranged: " + wrong + "\npipelines:\n  - name: podnode\n    entrypoint: Podnode\n    args:\n      ranged: " + entry + "\n"
	h := newHostedTrigger(t, map[string]string{"envout": filepath.Join(dir, "env")})
	if err := h.hostInCheckout(t, checkoutWithYAML(t, yaml)); err != nil {
		t.Fatalf("host: %v", err)
	}
	if run := h.run(t); run.Status != "success" || run.Args["ranged"] != entry {
		t.Fatalf("run = %s (%s) with args %v, want success recording the entry's ranged", run.Status, run.Error, run.Args)
	}
	if _, err := os.Stat(entry); err != nil {
		t.Fatalf("the entry's ranged file was not written: %v", err)
	}
	if _, err := os.Stat(wrong); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the project default beat the pipeline entry: %v", err)
	}
}

// safety: a guard in the checkout's sparkwing.yaml refuses a triggered run before it plans, as it refuses a local one.
func TestTriggerHost_TheCheckoutsGuardRefusesTheRun(t *testing.T) {
	dir := t.TempDir()
	ranged := filepath.Join(dir, "ranged")
	yaml := "pipelines:\n  - name: podnode\n    entrypoint: Podnode\n    guards:\n      reject: [\"arg:group=true\"]\n"
	h := newHostedTrigger(t, map[string]string{"envout": filepath.Join(dir, "env"), "ranged": ranged, "group": "true"})
	err := h.hostInCheckout(t, checkoutWithYAML(t, yaml))
	if err == nil || !strings.Contains(err.Error(), `rejected by guard "arg:group=true"`) {
		t.Fatalf("host = %v, want the guard's refusal", err)
	}
	if _, err := os.Stat(ranged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a node ran past the guard: %v", err)
	}
	if run := h.run(t); run.Status != "pending" {
		t.Fatalf("run = %s, want it never started", run.Status)
	}
}

// safety: a controller-dispatched run's planning claim and each node's Job layer the checkout's arguments under
// the run row's alike, so the node plans as the claim did; the claim refuses what a guard rejects.
func TestLaunchedNode_LayersTheCheckoutsYAMLArgsAndGuards(t *testing.T) {
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	dir := t.TempDir()
	entry := filepath.Join(dir, "entry")
	r := newLaunchedRun(t, map[string]string{"envout": filepath.Join(dir, "env")})
	yaml := "pipelines:\n  - name: podnode\n    entrypoint: Podnode\n    args:\n      ranged: " + entry + "\n"
	if err := os.MkdirAll(filepath.Join(r.src, ".sparkwing"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.src, ".sparkwing", "sparkwing.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.host(t, store.PlanNodeID); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := r.host(t, "prep"); err != nil {
		t.Fatalf("host prep: %v", err)
	}
	if n := r.node(t, "prep"); n.Outcome != string(sparkwing.Success) {
		t.Fatalf("prep = %s/%s (%s), want success planned as the claim planned", n.Status, n.Outcome, n.Error)
	}
	if got, err := os.ReadFile(entry); err != nil || strings.TrimSpace(string(got)) != "prep" {
		t.Fatalf("the entry's ranged file = %q (%v), want prep's line", got, err)
	}

	refused := r.another(t, "run-guarded", map[string]string{"envout": filepath.Join(dir, "env"), "group": "true"})
	guarded := "pipelines:\n  - name: podnode\n    entrypoint: Podnode\n    guards:\n      reject: [\"arg:group=true\"]\n"
	if err := os.WriteFile(filepath.Join(r.src, ".sparkwing", "sparkwing.yaml"), []byte(guarded), 0o600); err != nil {
		t.Fatal(err)
	}
	refused.bin = r.bin
	if _, err := refused.host(t, store.PlanNodeID); err == nil || !strings.Contains(err.Error(), "rejected by guard") {
		t.Fatalf("plan = %v, want the guard's refusal", err)
	}
}

// safety: a guard judges the arguments the run plans with, so leaving out a flag whose SDK default the guard rejects
// does not slip past it, on the runner host and on the launcher's planning claim alike.
func TestTriggerHost_AGuardJudgesTheSDKsDefaults(t *testing.T) {
	dir := t.TempDir()
	ranged := filepath.Join(dir, "ranged")
	yaml := "pipelines:\n  - name: podnode\n    entrypoint: Podnode\n    guards:\n      reject: [\"arg:stage=prod\"]\n"
	h := newHostedTrigger(t, map[string]string{"envout": filepath.Join(dir, "env"), "ranged": ranged})
	if err := h.hostInCheckout(t, checkoutWithYAML(t, yaml)); err == nil || !strings.Contains(err.Error(), `rejected by guard "arg:stage=prod"`) {
		t.Fatalf("host = %v, want the guard's refusal of the default stage", err)
	}
	if _, err := os.Stat(ranged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a node ran past the guard: %v", err)
	}
}

func TestLaunchedNode_AGuardJudgesTheSDKsDefaults(t *testing.T) {
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	r := newLaunchedRun(t, map[string]string{"envout": filepath.Join(t.TempDir(), "env")})
	yaml := "pipelines:\n  - name: podnode\n    entrypoint: Podnode\n    guards:\n      reject: [\"arg:stage=prod\"]\n"
	if err := os.MkdirAll(filepath.Join(r.src, ".sparkwing"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.src, ".sparkwing", "sparkwing.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.host(t, store.PlanNodeID); err == nil || !strings.Contains(err.Error(), "rejected by guard") {
		t.Fatalf("plan = %v, want the guard's refusal of the default stage", err)
	}
}

// safety: git:branch=default compares the triggered branch with the repository's default branch, which a GitHub
// App trigger records and any other trigger's checkout names as origin's HEAD.
func TestTriggerHost_ADefaultBranchGuardKnowsTheDefaultBranch(t *testing.T) {
	yaml := "pipelines:\n  - name: podnode\n    entrypoint: Podnode\n    guards:\n      reject: [\"git:branch=default\"]\n"
	t.Run("recorded by the webhook", func(t *testing.T) {
		h := newHostedTriggerWith(t, map[string]string{"envout": filepath.Join(t.TempDir(), "env")}, func(trig *store.Trigger) {
			trig.TriggerEnv = map[string]string{"GITHUB_DEFAULT_BRANCH": "main"}
		})
		if err := h.hostInCheckout(t, checkoutWithYAML(t, yaml)); err == nil || !strings.Contains(err.Error(), "rejected by guard") {
			t.Fatalf("host = %v, want the default-branch guard to refuse main", err)
		}
	})
	t.Run("named by the checkout", func(t *testing.T) {
		h := newHostedTrigger(t, map[string]string{"envout": filepath.Join(t.TempDir(), "env")})
		checkout := checkoutWithYAML(t, yaml)
		gitIn(t, checkout, "init", "-q", "-b", "main")
		gitIn(t, checkout, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
		if err := h.hostInCheckout(t, checkout); err == nil || !strings.Contains(err.Error(), "rejected by guard") {
			t.Fatalf("host = %v, want the default-branch guard to refuse main", err)
		}
	})
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

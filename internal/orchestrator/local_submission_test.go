package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func consumerTestStore(t *testing.T, home string) *store.Store {
	t.Helper()
	p := PathsAt(home)
	if err := p.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := teststore.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSubmittedTriggerRepoDir_SelectsTheSubmittingCheckout(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	trig := &store.Trigger{
		ID: "run-1", Pipeline: "lint",
		TriggerEnv: map[string]string{SubmitRepoDirKey: repoDir},
	}
	got, err := locateTriggerRepo(context.Background(), trig, t.TempDir())
	if err != nil {
		t.Fatalf("locateTriggerRepo: %v", err)
	}
	if filepath.Clean(got) != filepath.Clean(repoDir) {
		t.Fatalf("located %q, want the submitting checkout %q", got, repoDir)
	}
}

func TestSubmittedTriggerRepoDir_FailsClosedWhenTheCheckoutIsGone(t *testing.T) {
	trig := &store.Trigger{
		ID: "run-1", Pipeline: "lint",
		TriggerEnv: map[string]string{SubmitRepoDirKey: filepath.Join(t.TempDir(), "gone")},
	}
	_, err := locateTriggerRepo(context.Background(), trig, "")
	if err == nil {
		t.Fatal("a submitted trigger whose checkout vanished resolved anyway")
	}
	if !strings.Contains(err.Error(), "no longer has a .sparkwing/") {
		t.Fatalf("error does not name the cause: %v", err)
	}
}

func TestSubmittedTriggerRepoDir_AbsentOnOrdinaryTriggers(t *testing.T) {
	dir, err := submittedTriggerRepoDir(&store.Trigger{ID: "run-1", Pipeline: "lint"})
	if err != nil || dir != "" {
		t.Fatalf("submittedTriggerRepoDir = %q, %v; want empty and no error", dir, err)
	}
}

func TestEnsureRunLogDir_OnlyNamesADirectoryThatExists(t *testing.T) {
	p := PathsAt(t.TempDir())
	dir := EnsureRunLogDir(p, "run-1")
	if dir == "" {
		t.Fatal("EnsureRunLogDir returned empty for a writable home")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("EnsureRunLogDir named %q, which is not a directory: %v", dir, err)
	}

	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := EnsureRunLogDir(PathsAt(blocked), "run-1"); got != "" {
		t.Fatalf("EnsureRunLogDir = %q for an uncreatable home, want empty", got)
	}
}

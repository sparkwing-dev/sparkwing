package controller_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Sequence B. A retry of a victim's run produces a run carrying the victim's
// pipeline, which pipeline-scoped secrets alone would not stop. Retry is an
// operator action, so a runner token cannot reach it, and neither can a token
// that only carries the scope for starting its own work.
func TestDeclaredRepo_ARunnerCannotRetryAnotherPrincipalsRun(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)

	seedSecret(t, f.store, "DEPLOY_KEY", "victim-key", "victim-deploy", false)
	seedRunNode(t, f.store, "run-victim", "build")
	setRunPipeline(t, f.store, "run-victim", "victim-deploy")

	if got := f.do(t, raw, http.MethodPost, "/api/v1/runs/run-victim/retry", ""); got != http.StatusForbidden {
		t.Errorf("runner retry = %d, want 403", got)
	}
	if got := f.do(t, raw, http.MethodPost, "/api/v1/runs/run-victim/cancel", ""); got != http.StatusForbidden {
		t.Errorf("runner cancel = %d, want 403", got)
	}

	submitter, _, err := f.store.CreateToken("submitter", store.TokenKindService,
		[]string{controller.ScopeRunsWrite}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken submitter: %v", err)
	}
	if got := f.do(t, submitter, http.MethodPost, "/api/v1/runs/run-victim/retry", ""); got != http.StatusForbidden {
		t.Errorf("runs.write retry = %d, want 403; retry is an operator action", got)
	}

	operator, _, err := f.store.CreateToken("operator", store.TokenKindUser,
		[]string{controller.ScopeRunsControl}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken operator: %v", err)
	}
	if got := f.do(t, operator, http.MethodPost, "/api/v1/runs/run-victim/retry", ""); got != http.StatusAccepted {
		t.Errorf("operator retry = %d, want 202", got)
	}
}

// A runner token reports state on work it already holds and cannot start work
// of its own choosing, so it has no way to obtain a claim on a pipeline it was
// never given.
func TestDeclaredRepo_ARunnerCannotManufactureARunOfAnotherPipeline(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)

	body := `{"pipeline":"victim-deploy","repo":"victim/private"}`
	if got := f.do(t, raw, http.MethodPost, "/api/v1/triggers", body); got != http.StatusForbidden {
		t.Errorf("runner trigger submit = %d, want 403", got)
	}
}

func setRunDeclaredRepo(t *testing.T, st *store.Store, runID, repo string) {
	t.Helper()
	if _, err := st.DB().ExecContext(context.Background(),
		`UPDATE runs SET declared_repo = ? WHERE id = ?`, repo, runID); err != nil {
		t.Fatal(err)
	}
}

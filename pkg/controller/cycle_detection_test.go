package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestCycleDetect_RejectsSelfCycle(t *testing.T) {
	_, h, claim := childIntakeFixture(t)
	w := engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "ci", runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}), 409)
	if !strings.Contains(w.Body.String(), "cycle") {
		t.Fatalf("cycle refusal: %s", w.Body)
	}
}

func TestCycleDetect_AllowsIndirectNonCycle(t *testing.T) {
	e, h, claim := childIntakeFixture(t)
	if err := teststore.SeedRun(e.Store, t.Context(), "a", store.Run{ID: "root", Pipeline: "root", Status: "success"}); err != nil {
		t.Fatal(err)
	}
	setChildAncestor(t, e, "run", "root")
	engineSQL(t, e.Store, `UPDATE teams SET next_run_number=3 WHERE name='a'`)
	engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "deploy", runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}), 201)
}

func TestCycleDetect_RejectsDeepCycle(t *testing.T) {
	e, h, claim := childIntakeFixture(t)
	for _, r := range []store.Run{{ID: "root", Pipeline: "root", Status: "success"}, {ID: "mid", Pipeline: "mid", Status: "running", ParentRunID: "root"}} {
		if err := teststore.SeedRun(e.Store, t.Context(), "a", r); err != nil {
			t.Fatal(err)
		}
	}
	setChildAncestor(t, e, "mid", "root")
	setChildAncestor(t, e, "run", "mid")
	engineSQL(t, e.Store, `UPDATE teams SET next_run_number=4 WHERE name='a'`)
	engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "root", runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}), 409)
}

func TestTrigger_ParentRepoInheritance(t *testing.T) {
	e, h, claim := childIntakeFixture(t)
	remote, err := client.NewNodeHostClient(client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: childRecorderTransport{h}}, Bearer: func(context.Context) (string, error) { return claim, nil }}, "run", "job")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := remote.Close(); err != nil {
			t.Error(err)
		}
	})
	principal, err := remote.Principal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	process, err := remote.ProcessClient(t.Context(), principal, "job")
	if err != nil {
		t.Fatal(err)
	}
	id, err := process.StartChild(t.Context(), "deploy", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	if run.Request.Request.Git != parent.Request.Request.Git || run.Request.Request.Source != parent.Request.Request.Source {
		t.Fatal("implicit child did not inherit sealed parent source")
	}
	git := run.Request.Request.Git
	if git.Repository.Key != "gh:1" || git.Ref != "main" || git.SHA != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("source: %+v", git)
	}
	if run.Request.Request.Origin != runspec.OriginChild || run.Lineage.ParentRunID != "run" || run.Lineage.ParentJobID != "job" {
		t.Fatalf("lineage: %+v", run.Lineage)
	}
}

func TestCycleDetect_ParentNotFound(t *testing.T) {
	_, h, claim := childIntakeFixture(t)
	engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/missing/children", childIntakeBody(t, "deploy", runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}), 404)
}

func TestChildCycleWalkStaysInsideItsTeamProperty(t *testing.T) {
	e, h, claim := childIntakeFixture(t)
	if err := e.Store.AsOperator().CreateTeam(t.Context(), "foreign"); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedRun(e.Store, t.Context(), "foreign", store.Run{ID: "private-root", Pipeline: "private-pipeline", Status: "success"}); err != nil {
		t.Fatal(err)
	}
	setChildAncestor(t, e, "run", "private-root")
	property := func(pipeline string) bool {
		w := engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "safe-"+pipeline, runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}), 404)
		return !strings.Contains(w.Body.String(), "private-pipeline") && !strings.Contains(w.Body.String(), "private-root")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

type childRecorderTransport struct{ handler http.Handler }

func (c childRecorderTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

func setChildAncestor(t *testing.T, e *EngineController, id, parent string) {
	t.Helper()
	lineage, err := json.Marshal(runspec.Lineage{ParentRunID: parent})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE runs SET lineage_json=? WHERE team='a' AND id=?`, lineage, id)
}

// 10#1: a child runs in its parent's repository even when the claim's repository
// scope names another, so it cannot borrow another repository's source token.
func TestChildRunsOnlyInItsParentRepository(t *testing.T) {
	e, h, claim := childIntakeFixture(t)
	childSourceRepositories(t, e, claim, []string{"gh:1", "gh:2"})
	for _, git := range []runspec.Git{
		{Repository: runspec.Repository{Key: "gh:2"}},
		{Repository: runspec.Repository{Key: "gh:2", Display: "gh:2"}, Ref: "main", SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	} {
		engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "deploy", git), 403)
	}
}

func TestChildCycleWalkRefusesRepeatedAncestorIDs(t *testing.T) {
	e, h, claim := childIntakeFixture(t)
	if err := teststore.SeedRun(e.Store, t.Context(), "a", store.Run{ID: "root", Pipeline: "root", Status: "success"}); err != nil {
		t.Fatal(err)
	}
	setChildAncestor(t, e, "root", "run")
	setChildAncestor(t, e, "run", "root")
	engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "fresh", runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}), 409)
	var count int
	if err := e.Store.DB().QueryRow(`SELECT COUNT(*) FROM runs WHERE team='a'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("cyclic lineage persisted a child: %d: %v", count, err)
	}
}

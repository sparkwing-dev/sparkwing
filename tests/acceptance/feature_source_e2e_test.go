package acceptance_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

// safety: source authentication is a fixture input; execution and every retry still use the external controller.
func seedFeatureSource(t *testing.T, s *store.Store, id string, number int64, placement *runspec.RepositoryPlacement) string {
	t.Helper()
	now := time.Now().UTC()
	app := runspec.AppFacts{RepositoryID: 1, Repository: "feature/fixture", Ref: "refs/heads/main", SHA: strings.Repeat("a", 40), Event: "push", DefaultBranch: "main", DefaultBranchPush: true, DeliveryKey: "fixture-delivery", VerifiedAt: now}
	provenance, err := runspec.AppProvenance(app, runspec.Optional[runspec.PullRequest]{}, "feature")
	if err != nil {
		t.Fatal(err)
	}
	request := runspec.Request{Version: 1, Team: string(store.DefaultTeam), Pipeline: "feature", Origin: runspec.OriginApp, Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchController, Actor: runspec.Actor{Kind: "github", ID: "fixture"}, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/feature/fixture"}, Ref: app.Ref, SHA: app.SHA, App: runspec.Optional[runspec.AppFacts]{Set: true, Value: app}}, Source: runspec.Source{Ref: app.SHA, Location: runspec.SourceLocation{RemoteURL: "https://github.com/feature/fixture.git", PipelineDir: ".sparkwing"}}}
	request.Placement = placement
	request.Options.OwnerExecutionTimeout = runspec.Setting[time.Duration]{Optional: runspec.Optional[time.Duration]{Set: true, Value: time.Hour}}
	sealed, err := runspec.SealRequest(request, runspec.IntakeFacts{RunID: id, AdmittedAt: now, Provenance: provenance})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	_, err = teststore.Exec(t.Context(), s, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days)
VALUES (?,?,?,'feature','pending',?,?,?,'gh:1','https://github.com/feature/fixture','controller','execute','fixture',?,'r30',30)`, store.DefaultTeam, id, number, body, sealed.Hash, now.UnixNano(), now.UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	_, err = teststore.Exec(t.Context(), s, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) VALUES (?,?,'plan',?,?,?,?,?)`, store.DefaultTeam, id, now.UnixNano(), now.UnixNano(), now.Add(time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.Exec(t.Context(), s, `UPDATE teams SET next_run_number=? WHERE name=?`, number+1, store.DefaultTeam); err != nil {
		t.Fatal(err)
	}
	return sealed.RunID
}

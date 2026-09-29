package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A claim is issued at most MaxSourceMints source credentials, so a failed or
// lost mint can be asked for again: one more ask, an ask once its attempt has
// started, an ask from another team's handle and an ask after its run is
// cancelled are all refused, and each issue leaves an audit row.
func TestSpendSourceCredential_IssuesAFewPerClaimBeforeItsAttempt(t *testing.T) {
	ctx := context.Background()
	f := newDispatchRun(t, "run-spend")
	plan, err := f.authorize(t, f.claimRaw(t, store.PlanNodeID, store.ClaimTokenPlan), store.ClaimSensitive)
	if err != nil {
		t.Fatal(err)
	}
	team, err := f.s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	spend := func(tok store.ClaimToken) (store.SourceSpec, error) {
		out, err := team.SpendSourceCredential(ctx, tok, "github_app", "github.com", "claim:"+tok.NodeID, time.Now())
		return out.Spec, err
	}
	for i := range store.MaxSourceMints {
		if spec, err := spend(plan); err != nil || spec != (store.SourceSpec{Depth: 1}) {
			t.Fatalf("plan claim ask %d = %+v, %v; want one commit and nothing else", i, spec, err)
		}
	}
	if _, err := spend(plan); !errors.Is(err, store.ErrSourceCredentialSpent) {
		t.Fatalf("ask past the limit: err = %v, want ErrSourceCredentialSpent", err)
	}

	if _, err := f.accept(plan, `{"pipeline":"demo","source":{"depth":0,"tags":true,"submodules":true},"nodes":[`+
		`{"id":"a","deps":[],"spec_hash":"`+hashA+`"},{"id":"b","deps":[],"spec_hash":"`+hashA+`"}]}`); err != nil {
		t.Fatal(err)
	}
	a, err := f.authorize(t, f.claimRaw(t, "a", store.ClaimTokenWork), store.ClaimSensitive)
	if err != nil {
		t.Fatal(err)
	}
	if spec, err := spend(a); err != nil || spec != (store.SourceSpec{Depth: 0, Tags: true, Submodules: true}) {
		t.Fatalf("work claim = %+v, %v; want the plan's full-history ask", spec, err)
	}
	b, err := f.authorize(t, f.claimRaw(t, "b", store.ClaimTokenWork), store.ClaimSensitive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB().ExecContext(ctx, storetest.Rebind(f.s,
		`UPDATE nodes SET execution_started_at = ? WHERE run_id = ? AND node_id = ?`), time.Now().UnixNano(), f.run, "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := spend(b); !errors.Is(err, store.ErrSourceCredentialSpent) {
		t.Fatalf("after the attempt started: err = %v, want ErrSourceCredentialSpent", err)
	}
	other := a
	other.Team = "bravo"
	if _, err := spend(other); !errors.Is(err, store.ErrClaimNotLive) {
		t.Fatalf("another team: err = %v", err)
	}
	if err := f.s.RequestCancel(ctx, f.run); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CheckClaimSensitive(ctx, a, time.Now()); !errors.Is(err, store.ErrClaimCancelRequested) {
		t.Fatalf("check after cancel: err = %v", err)
	}
	rels, err := team.GitCredentialReleases(ctx, 10)
	if err != nil || len(rels) != store.MaxSourceMints+1 {
		t.Fatalf("audit rows = %d, %v; want the plan's %d and a's", len(rels), err, store.MaxSourceMints)
	}
}

// A plan that asks for a negative depth is refused at accept.
func TestAcceptPlan_RefusesANegativeSourceDepth(t *testing.T) {
	f := newDispatchRun(t, "run-depth")
	body := `{"pipeline":"demo","source":{"depth":-1},"nodes":[{"id":"a","deps":[],"spec_hash":"` + hashA + `"}]}`
	if _, err := f.accept(f.claim(t, store.PlanNodeID, store.ClaimTokenPlan), body); !errors.Is(err, store.ErrPlanInvalid) {
		t.Fatalf("err = %v, want ErrPlanInvalid", err)
	}
}

// The launcher takes a node whose selector the Cloud runner image satisfies
// and leaves one that needs a tool or label the image lacks.
func TestClaimLaunch_MatchesSelectorsAgainstTheCloudImage(t *testing.T) {
	ctx := context.Background()
	f := newDispatchRun(t, "run-tools")
	f.mustAccept(t, planOf(`go|"modifiers":{"runs_on":["tool:go","tool:git"]}`,
		`docker|"modifiers":{"runs_on":["tool:docker"]}`, `gpu|"modifiers":{"runs_on":["gpu"]}`, `bare`))
	var got []string
	for range 5 {
		c, err := f.s.ClaimLaunch(ctx, launcherIdentity, launchRequest(), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if c == nil {
			break
		}
		got = append(got, c.NodeID)
	}
	if len(got) != 2 || !(got[0] == "go" && got[1] == "bare" || got[0] == "bare" && got[1] == "go") {
		t.Fatalf("launched %v, want go and bare only", got)
	}
}

// An extra repository carries the GitHub ID recorded when an owner approved
// it, and 0 until one is.
func TestGitHubAppExtraRepoRefs_CarryTheRecordedIDs(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	acme := teamHandle(t, st, "acme")
	if _, err := acme.SetGitHubAppExtraRepos(ctx, "acme/app", []string{"acme/lib", "acme/proto"}, "owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := acme.SetGitHubAppExtraRepoIDs(ctx, "Acme/App", map[string]int64{"Acme/Lib": 42}); err != nil {
		t.Fatal(err)
	}
	refs, err := acme.GitHubAppExtraRepoRefs(ctx, "acme/app")
	if err != nil || len(refs) != 2 || refs[0].Slug() != "acme/lib" || refs[0].ID != 42 || refs[1].ID != 0 {
		t.Fatalf("refs = %+v, %v", refs, err)
	}
}

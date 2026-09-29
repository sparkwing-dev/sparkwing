package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A claim token's credential release is a sensitive write: it commits only
// while the claim is live at the token's generation and its run has no cancel
// request, checked inside the transaction that writes the audit row.
func TestReleaseGitCredential_FencesAClaimToken(t *testing.T) {
	ctx := context.Background()
	f := newDispatchRun(t, "run-fence")
	tok, err := f.authorize(t, f.claimRaw(t, store.PlanNodeID, store.ClaimTokenPlan), store.ClaimSensitive)
	if err != nil {
		t.Fatal(err)
	}
	team, err := f.s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := team.PutGitCredential(ctx, sshCredential("SHA256:one"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := team.ConfirmGitCredential(ctx, "gitlab.example.com", "SHA256:one", time.Now()); err != nil {
		t.Fatal(err)
	}
	releaseFor := func(tok store.ClaimToken) error {
		_, err := team.ReleaseGitCredential(ctx, "gitlab.example.com", store.GitCredentialRelease{
			RunID: f.run, Runner: "claim:" + tok.RunID, TokenPrefix: tok.Prefix, Claim: &tok,
		}, time.Now())
		return err
	}
	if err := releaseFor(tok); err != nil {
		t.Fatalf("live claim: %v", err)
	}
	if got, err := f.s.CheckClaimSensitive(ctx, tok, time.Now()); err != nil || got.Kind != store.ClaimTokenPlan || got.Prefix != tok.Prefix {
		t.Fatalf("check = %+v, %v", got, err)
	}

	other := tok
	other.Team = "bravo"
	later := tok
	later.Generation++
	for name, c := range map[string]store.ClaimToken{"another team": other, "a later generation": later} {
		if err := releaseFor(c); err == nil {
			t.Fatalf("%s: released", name)
		}
	}
	if err := f.s.RequestCancel(ctx, f.run); err != nil {
		t.Fatal(err)
	}
	if err := releaseFor(tok); !errors.Is(err, store.ErrClaimCancelRequested) {
		t.Fatalf("after cancel: err = %v, want ErrClaimCancelRequested", err)
	}
	if _, err := f.s.CheckClaimSensitive(ctx, tok, time.Now()); !errors.Is(err, store.ErrClaimCancelRequested) {
		t.Fatalf("check after cancel: err = %v", err)
	}
	rels, err := team.GitCredentialReleases(ctx, 10)
	if err != nil || len(rels) != 1 {
		t.Fatalf("audit rows = %d, %v; want the one live release", len(rels), err)
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

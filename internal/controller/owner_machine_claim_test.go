package controller_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestClaimMarksOnlyTheRequestersPersonalMachineAsOwnerMachine(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	for _, tc := range []struct {
		run      string
		personal bool
	}{{"run-personal", true}, {"run-shared", false}} {
		ctx := context.Background()
		f.createRun(olga, tc.run, "https://github.com/acme/widgets.git", 701, runspec.PrincipalWriter("account", olga.id))
		if err := teststore.SeedNode(f.store, ctx, store.Node{RunID: tc.run, NodeID: "compile", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := teststore.SeedNodeReady(f.store, ctx, tc.run, "compile"); err != nil {
			t.Fatal(err)
		}
		bearer, _ := f.enrollRunner(olga, "r-"+tc.run, tc.personal)
		var n controllerwire.ClaimResponse
		if code := f.call("POST", "/api/v1/runners/claim", bearer, githubClaimBody(capability.UnitNode, tc.run, "compile"), &n); code != http.StatusOK || n.Claim == nil {
			t.Fatalf("%s claim = %d %+v", tc.run, code, n)
		}
		if n.Claim.OwnerMachine != tc.personal {
			t.Fatalf("%s owner machine = %t, want %t", tc.run, n.Claim.OwnerMachine, tc.personal)
		}
	}
}

func TestChildOfAPersonalMachineJobStaysOnItsOwnersMachines(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.enrollRunner(olga, "olga-laptop", true)
	f.enrollRunner(olga, "olga-shared", false)
	ctx := context.Background()
	for _, tc := range []struct {
		run, submitter, claimant string
		personal                 bool
		want                     int
	}{
		{"child-own", "olga-laptop", "olga-desktop", true, http.StatusOK},
		{"child-shared", "olga-shared", "olga-desktop-2", true, http.StatusForbidden},
		{"child-shared-runner", "olga-laptop", "team-runner", false, http.StatusOK},
	} {
		f.createRun(olga, tc.run, "https://github.com/acme/widgets.git", 701, runspec.PrincipalWriter("machine", tc.submitter))
		if err := teststore.SeedNode(f.store, ctx, store.Node{RunID: tc.run, NodeID: "compile", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := teststore.SeedNodeReady(f.store, ctx, tc.run, "compile"); err != nil {
			t.Fatal(err)
		}
		bearer, _ := f.enrollRunner(olga, tc.claimant, tc.personal)
		var n controllerwire.ClaimResponse
		code := f.call("POST", "/api/v1/runners/claim", bearer, githubClaimBody(capability.UnitNode, tc.run, "compile"), &n)
		if tc.want != http.StatusOK {
			if code == http.StatusOK && n.Claim != nil {
				t.Fatalf("%s: a personal machine claimed a child of a shared runner's job", tc.run)
			}
			continue
		}
		if code != http.StatusOK || n.Claim == nil {
			t.Fatalf("%s claim = %d %+v", tc.run, code, n)
		}
		if n.Claim.OwnerMachine != tc.personal {
			t.Fatalf("%s owner machine = %t, want %t", tc.run, n.Claim.OwnerMachine, tc.personal)
		}
	}
}

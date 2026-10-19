package controller_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
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
		f.createRun(olga, tc.run, "https://github.com/acme/widgets.git", 701)
		if _, err := f.store.DB().ExecContext(ctx, `UPDATE runs SET created_principal=?,created_subject_kind='account' WHERE team=? AND id=?`, olga.id, olga.team, tc.run); err != nil {
			t.Fatal(err)
		}
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

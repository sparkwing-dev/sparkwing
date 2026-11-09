package acceptance_test

import (
	"context"
	"errors"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func FuzzStoreQueueClaimAuthorityW8(f *testing.F) {
	for mode := uint8(0); mode < 8; mode++ {
		f.Add(mode, false)
		f.Add(mode, true)
	}
	f.Fuzz(func(t *testing.T, mode uint8, named bool) {
		server, root := newStoredRunnerW8(t, capability.UnitTrigger)
		kinds := []capability.UnitKind{capability.UnitTrigger}
		raw := root.Raw
		want := hosterr.Forbidden
		switch mode % 8 {
		case 0:
			want = ""
		case 1, 2:
			kinds = []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}
			if mode%8 == 2 {
				kinds = []capability.UnitKind{capability.UnitTrigger, capability.UnitEval}
			}
			principal, err := server.Store.AsOperator().LookupCapability(t.Context(), root.Capability.Digest)
			if err != nil {
				t.Fatal(err)
			}
			issued, err := server.Store.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{
				Parent: principal.Authority, Audience: capability.Controller, Grants: []capability.Grant{capability.TriggersClaim}, Binding: principal.Capability.Binding, Repositories: principal.Capability.Repositories,
			})
			if err != nil {
				t.Fatal(err)
			}
			raw = issued.Raw
		case 3:
			kinds = []capability.UnitKind{capability.UnitTrigger, capability.UnitTrigger}
		case 4:
			kinds = []capability.UnitKind{"unknown"}
		case 5:
			kinds = []capability.UnitKind{}
		case 6:
			raw += "changed"
			want = hosterr.Unauthenticated
		case 7:
			ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Capability.Prefix}
			revokeStoredRunnerW8(t, server.Store, ref)
			want = hosterr.Unauthenticated
		}
		queue, err := cloudStoreClientW8(t, server)(client.EngineOptions{URL: "https://controller.example", Bearer: func(context.Context) (string, error) { return raw, nil }})
		if err != nil {
			t.Fatal(err)
		}
		body := controllerwire.ClaimRequest{RunnerVersion: "v0.71.0", Kinds: kinds}
		if named {
			body.RunID = "run"
		}
		response, err := queue.Claim(t.Context(), body)
		if want == "" {
			if err != nil || response.Claim == nil || response.Claim.RunID != "run" || response.Claim.Unit != capability.UnitTrigger || response.Claim.Capability == raw {
				t.Fatalf("authorized queue claim: %+v: %v", response, err)
			}
			return
		}
		var refusal *apierr.Error
		clientRefusal := mode%8 == 2 && named || mode%8 >= 3 && mode%8 <= 5
		if err == nil || response.Claim != nil || !clientRefusal && (!errors.As(err, &refusal) || refusal.Code != want) {
			t.Fatalf("queue claim: %+v: %v; want %s", response, err, want)
		}
		var claims int
		if err := teststore.SQL(t, server.Store).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM triggers WHERE status='claimed'`).Scan(&claims); err != nil || claims != 0 {
			t.Fatalf("refused claim changed the queue: count=%d: %v", claims, err)
		}
	})
}

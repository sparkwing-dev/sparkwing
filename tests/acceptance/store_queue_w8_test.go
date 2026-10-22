package acceptance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
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
		want := apierr.Forbidden
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
			want = apierr.Unauthenticated
		case 7:
			ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Capability.Prefix}
			revokeStoredRunnerW8(t, server.Store, ref)
			want = apierr.Unauthenticated
		}
		runner, err := runners.NewStoreRunner(server.EngineController, runners.StoreRunnerConfig{
			Bearer: func(context.Context) (string, error) { return raw, nil },
			Loop:   runners.LoopConfig{Version: "v0.71.0", MaxConcurrent: 1},
			Bind: func(context.Context, runners.StoreBinding) (nodehost.NativeServices, error) {
				t.Fatal("claim executed pipeline code")
				return nodehost.NativeServices{}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		body := controllerwire.ClaimRequest{RunnerVersion: "v0.71.0", Kinds: kinds, AvailableCores: 2, AvailableMemoryBytes: 1 << 30}
		if named {
			body.RunID = "run"
		}
		response, err := runner.Controller.Claim(t.Context(), body)
		if want == "" {
			if err != nil || response.Claim == nil || response.Claim.RunID != "run" || response.Claim.Unit != capability.UnitTrigger || response.Claim.Capability == raw {
				t.Fatalf("authorized queue claim: %+v: %v", response, err)
			}
			return
		}
		var refusal *apierr.Error
		if !errors.As(err, &refusal) || refusal.Code != want || response.Claim != nil {
			t.Fatalf("queue claim: %+v: %v; want %s", response, err, want)
		}
		var claims int
		if err := teststore.SQL(t, server.Store).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM triggers WHERE status='claimed'`).Scan(&claims); err != nil || claims != 0 {
			t.Fatalf("refused claim changed the queue: count=%d: %v", claims, err)
		}
	})
}

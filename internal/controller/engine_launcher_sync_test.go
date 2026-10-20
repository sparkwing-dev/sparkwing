package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type launcherSyncFunc func(context.Context, capability.AuthorityRef, []controllerwire.LauncherObservation) (controllerwire.LauncherSyncResponse, error)

func (f launcherSyncFunc) ReadLauncherSync(ctx context.Context, ref capability.AuthorityRef, observed []controllerwire.LauncherObservation, _ []capability.LeaseRef) (controllerwire.LauncherSyncResponse, error) {
	return f(ctx, ref, observed)
}

func TestEngineLauncherSyncNativeFenceProperty(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100_000_000, Reference: "launcher-test", CreatedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE runs SET dispatch='controller'`)
	engineSQL(t, e.Store, `UPDATE triggers SET unit_kind='plan'`)
	raw := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	e.claimProfiles = engineProfileFake{}
	e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	h := New(e.Store, slog.New(slog.NewTextHandler(io.Discard, nil))).WithEngineController(e.EngineController).Handler()
	w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["plan"],"available_cores":1}`, 200)
	var claim controllerwire.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &claim); err != nil || claim.Claim == nil {
		t.Fatalf("claim: %s, %v", w.Body.String(), err)
	}
	observation := controllerwire.LauncherObservation{Award: capability.LeaseRef{Kind: capability.UnitPlan, ID: claim.Claim.UnitID, Holder: claim.Claim.Binding.Holder, Generation: claim.Claim.Binding.Generation}, Job: controllerwire.LauncherJobRef{Namespace: "ci", Name: "plan-job", UID: "job-uid"}, NodeUID: "node-uid"}
	random := rand.New(rand.NewPCG(410, 291))
	for range 64 {
		input := observation
		live := false
		switch random.IntN(4) {
		case 0:
			live = true
		case 1:
			input.Award.Generation++
		case 2:
			input.Award.Holder += "-other"
		case 3:
			input.Award.ID += "-other"
		}
		body, err := json.Marshal(controllerwire.LauncherSyncRequest{Observed: []controllerwire.LauncherObservation{input}})
		if err != nil {
			t.Fatal(err)
		}
		w = engineClaimHTTP(t, h, raw, "POST", "/api/v1/launcher/sync", string(body), 200)
		var result controllerwire.LauncherSyncResponse
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Awards) != btoi(live) || len(result.Remove) != btoi(!live) || strings.Contains(w.Body.String(), claim.Claim.Capability) {
			t.Fatalf("sync: %s, %v", w.Body.String(), err)
		}
		if !live && result.Remove[0] != input.Job {
			t.Fatal("cleanup selected a different Kubernetes instance")
		}
	}
	engineClaimHTTP(t, h, claim.Claim.Capability, "POST", "/api/v1/claims/exchange", `{}`, 200)
	body, err := json.Marshal(controllerwire.LauncherSyncRequest{Observed: []controllerwire.LauncherObservation{observation}})
	if err != nil {
		t.Fatal(err)
	}
	w = engineClaimHTTP(t, h, raw, "POST", "/api/v1/launcher/sync", string(body), 200)
	var result controllerwire.LauncherSyncResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Awards) != 1 || len(result.Remove) != 0 || result.Awards[0].Delivery != capability.DeliveryBootstrap {
		t.Fatalf("exchanged award disappeared: %s, %v", w.Body.String(), err)
	}
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestEngineLauncherSyncHTTPUsesAuthenticatedRootAndNeverDeliversBearer(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	raw := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	h := New(e.Store, slog.New(slog.NewTextHandler(io.Discard, nil))).WithEngineController(e.EngineController).Handler()
	body := `{"observed":[{"award":{"kind":"node","id":"unit","holder":"launcher","generation":1},"job":{"namespace":"ci","name":"job","uid":"job-uid"},"node_uid":"node"}]}`
	engineClaimHTTP(t, h, "", "POST", "/api/v1/launcher/sync", body, 401)
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/launcher/sync", body, 200)
	called := 0
	e.Launcher = launcherSyncFunc(func(_ context.Context, ref capability.AuthorityRef, observed []controllerwire.LauncherObservation) (controllerwire.LauncherSyncResponse, error) {
		called++
		if ref != (capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "launcher"}) || !reflect.DeepEqual(observed, []controllerwire.LauncherObservation{{Award: capability.LeaseRef{Kind: capability.UnitNode, ID: "unit", Holder: "launcher", Generation: 1}, Job: controllerwire.LauncherJobRef{Namespace: "ci", Name: "job", UID: "job-uid"}, NodeUID: "node"}}) {
			t.Fatalf("sync inputs: %v %+v", ref, observed)
		}
		return controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{{UnitID: "unit", Capability: "private-bootstrap"}}, Remove: []controllerwire.LauncherJobRef{{Namespace: "ci", Name: "job", UID: "job-uid"}}}, nil
	})
	w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/launcher/sync", body, 200)
	var got controllerwire.LauncherSyncResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Awards) != 1 || got.Awards[0].Capability != "" || len(got.Remove) != 1 || called != 1 || strings.Contains(w.Body.String(), "private-bootstrap") {
		t.Fatalf("unsafe sync response: %s (%v)", w.Body.String(), err)
	}
	e.Launcher = launcherSyncFunc(func(context.Context, capability.AuthorityRef, []controllerwire.LauncherObservation) (controllerwire.LauncherSyncResponse, error) {
		return controllerwire.LauncherSyncResponse{}, errors.New("private-snapshot-failure")
	})
	w = engineClaimHTTP(t, h, raw, "POST", "/api/v1/launcher/sync", body, 500)
	if strings.Contains(w.Body.String(), "private-snapshot-failure") {
		t.Fatal("sync disclosed a private storage failure")
	}
}

func FuzzEngineLauncherSyncOmitsBearer(f *testing.F) {
	f.Add("swk_private-bootstrap")
	f.Add("")
	f.Fuzz(func(t *testing.T, raw string) {
		original := controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{{UnitID: "unit", Capability: raw}}}
		e := &EngineController{Launcher: launcherSyncFunc(func(context.Context, capability.AuthorityRef, []controllerwire.LauncherObservation) (controllerwire.LauncherSyncResponse, error) {
			return original, nil
		})}
		got, err := e.SyncLauncher(t.Context(), capability.Authorized{}, controllerwire.LauncherSyncRequest{})
		if err != nil || len(got.Awards) != 1 || got.Awards[0].Capability != "" || original.Awards[0].Capability != raw || got.Remove == nil {
			t.Fatal("sync leaked bearer material or mutated the stored snapshot")
		}
	})
}

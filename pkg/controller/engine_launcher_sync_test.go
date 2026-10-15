package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

type launcherSyncFunc func(context.Context, capability.AuthorityRef, []controllerwire.LauncherObservation) (controllerwire.LauncherSyncResponse, error)

func (f launcherSyncFunc) ReadLauncherSync(ctx context.Context, ref capability.AuthorityRef, observed []controllerwire.LauncherObservation) (controllerwire.LauncherSyncResponse, error) {
	return f(ctx, ref, observed)
}

func TestEngineLauncherSyncHTTPUsesAuthenticatedRootAndNeverDeliversBearer(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	raw := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	h := New(e.Store, slog.New(slog.NewTextHandler(io.Discard, nil))).WithEngineController(e).Handler()
	body := `{"observed":[{"award":{"kind":"node","id":"unit","holder":"launcher","generation":1},"job":{"namespace":"ci","name":"job","uid":"job-uid"},"node_uid":"node"}]}`
	engineClaimHTTP(t, h, "", "POST", "/api/v1/launcher/sync", body, 401)
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/launcher/sync", body, 500)
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

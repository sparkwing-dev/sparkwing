package acceptance_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type storeLauncherSyncW8 func(context.Context, capability.AuthorityRef, []controllerwire.LauncherObservation) (controllerwire.LauncherSyncResponse, error)

func (f storeLauncherSyncW8) ReadLauncherSync(ctx context.Context, ref capability.AuthorityRef, observed []controllerwire.LauncherObservation, _ []capability.LeaseRef) (controllerwire.LauncherSyncResponse, error) {
	return f(ctx, ref, observed)
}

func TestStoreLauncherW8(t *testing.T) {
	server, root := storedCloudClaimantW8(t)
	kube, boundary := cloudKubeW8(), &cloudBoundaryFake{}
	_, cfg, _ := cloudFixture(launcher.CloudWork)
	handler := controller.New(server.Store, nil).WithEngineController(server).Handler()
	var paths []string
	runtime, err := launcher.NewLauncher(t.Context(), launcher.RuntimeConfig{
		Kube: kube, Boundary: boundary, Jobs: cfg,
		Demand: controllerwire.ClaimRequest{RunnerVersion: "v0.71.0", Kinds: []capability.UnitKind{capability.UnitNode}, AvailableCores: 2, AvailableMemoryBytes: 1 << 30},
		Bearer: func(context.Context) (string, error) { return root.Raw, nil },
		HTTP: &http.Client{Transport: surfaceTransport(func(request *http.Request) (*http.Response, error) {
			if request.Header.Get("Authorization") != "Bearer "+root.Raw {
				t.Fatal("launcher lost its service credential on the wire")
			}
			paths = append(paths, request.URL.Path)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			return response.Result(), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := runtime.LaunchOne(t.Context())
	if err != nil || !claimed {
		t.Fatalf("store launcher: claimed=%t error=%v", claimed, err)
	}
	jobs, err := kube.BatchV1().Jobs(cfg.Namespace).List(t.Context(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 || jobs.Items[0].Spec.Suspend == nil || *jobs.Items[0].Spec.Suspend {
		t.Fatalf("store award did not activate its Job: %v", err)
	}
	secrets, err := kube.CoreV1().Secrets(cfg.Namespace).List(t.Context(), metav1.ListOptions{})
	if err != nil || len(secrets.Items) != 1 {
		t.Fatalf("private bootstrap: %v", err)
	}
	bootstrap, err := launcher.ReadBootstrapAward(writeBootstrapW8(t, secrets.Items[0].Data))
	if err != nil || bootstrap.Binding.Holder == "" || string(bootstrap.Bootstrap) == root.Raw || bootstrap.Source.SHA != strings.Repeat("a", 40) {
		t.Fatalf("bootstrap lost its store award: %v", err)
	}
	bootstrapClient, err := cloudStoreClientW8(t, server)(client.EngineOptions{URL: cfg.ControllerURL, Bearer: func(context.Context) (string, error) { return string(bootstrap.Bootstrap), nil }})
	if err != nil {
		t.Fatal(err)
	}
	exchanged, err := bootstrapClient.Exchange(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	unit := exchanged.Claim
	work, err := cloudStoreClientW8(t, server)(client.EngineOptions{URL: cfg.ControllerURL, Bearer: func(context.Context) (string, error) { return unit.Capability, nil }})
	if err != nil {
		t.Fatal(err)
	}
	started, err := work.TransitionJob(t.Context(), unit.RunID, unit.JobID, controllerwire.JobTransitionRequest{To: "running", Cause: "start", SourceAttempt: &unit.Binding.Attempt})
	if err != nil || started.Attempt != 1 {
		t.Fatalf("Cloud start: %+v: %v", started, err)
	}
	if err := runtime.Sync(t.Context()); err != nil || boundary.released != 0 {
		t.Fatalf("live exchanged claim lost its Job: releases=%d: %v", boundary.released, err)
	}
	if _, err := work.TransitionJob(t.Context(), unit.RunID, unit.JobID, controllerwire.JobTransitionRequest{To: "success", Cause: "report", SourceAttempt: &started.Attempt, Result: &controllerwire.JobResult{Outcome: "success"}}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Sync(t.Context()); err != nil || boundary.released != 1 {
		t.Fatalf("store-backed cleanup: releases=%d error=%v", boundary.released, err)
	}
	if len(paths) < 2 || paths[0] != "/api/v1/launcher/claim" || slices.ContainsFunc(paths[1:], func(path string) bool { return path != "/api/v1/launcher/sync" }) {
		t.Fatalf("launcher did not use the shared claim/sync routes: %v", paths)
	}
}

func FuzzStoreLauncherAuthorityW8(f *testing.F) {
	for mode := uint8(0); mode < 6; mode++ {
		f.Add(mode, true, "changed")
		f.Add(mode, false, "changed")
	}
	f.Fuzz(func(t *testing.T, mode uint8, sync bool, mutation string) {
		if mutation == "" {
			mutation = "changed"
		}
		server, root := storedCloudClaimantW8(t)
		raw := root.Raw
		providerFailure := errors.New("credential provider failed")
		options := client.EngineOptions{URL: "https://controller.example", Launcher: true, Bearer: func(context.Context) (string, error) { return raw, nil }}
		called := false
		server.Launcher = storeLauncherSyncW8(func(_ context.Context, ref capability.AuthorityRef, _ []controllerwire.LauncherObservation) (controllerwire.LauncherSyncResponse, error) {
			called = true
			if ref.ID != root.Capability.Prefix {
				t.Fatal("sync accepted another root")
			}
			return controllerwire.LauncherSyncResponse{}, nil
		})
		want := apierr.Forbidden
		switch mode % 6 {
		case 0:
			want = ""
		case 1:
			raw += mutation
			want = apierr.Unauthenticated
		case 2:
			principal, err := server.Store.LookupCapability(t.Context(), root.Capability.Digest)
			if err != nil {
				t.Fatal(err)
			}
			issued, err := server.Store.DelegateCapability(t.Context(), capability.DelegateSpec{Parent: principal.Authority, Audience: capability.Controller, Grants: []capability.Grant{}, Binding: capability.Binding{Kind: capability.BindingNone}, ExpiresAt: root.Capability.ExpiresAt})
			if err != nil {
				t.Fatal(err)
			}
			raw = issued.Raw
		case 3:
			options.Launcher = false
			if sync {
				want = ""
			}
		case 4:
			var prefix string
			if err := teststore.SQL(t, server.Store).QueryRowContext(t.Context(), `SELECT prefix FROM capabilities WHERE subject_id='operator'`).Scan(&prefix); err != nil {
				t.Fatal(err)
			}
			actor := store.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}}
			if err := server.Store.AsOperator().RevokeAuthority(t.Context(), actor, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Capability.Prefix}); err != nil {
				t.Fatal(err)
			}
			want = apierr.Unauthenticated
		case 5:
			options.Bearer = func(context.Context) (string, error) { return "", providerFailure }
		}
		wire, err := cloudStoreClientW8(t, server)(options)
		if err != nil {
			t.Fatal(err)
		}
		if sync {
			_, err = wire.SyncLauncher(t.Context(), controllerwire.LauncherSyncRequest{})
		} else {
			_, err = wire.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: "v0.71.0", Kinds: []capability.UnitKind{capability.UnitNode}, AvailableCores: 2, AvailableMemoryBytes: 1 << 30})
		}
		if mode%6 == 5 {
			if !errors.Is(err, providerFailure) || called {
				t.Fatalf("lost credential provider error: %v", err)
			}
			return
		}
		if want == "" {
			if err != nil || sync && !called {
				t.Fatalf("authorized launcher request: %v", err)
			}
			return
		}
		var refusal *apierr.Error
		if !errors.As(err, &refusal) || refusal.Code != want || called {
			t.Fatalf("launcher authorization: %v, want %s", err, want)
		}
		var claimedBy string
		if err := teststore.SQL(t, server.Store).QueryRowContext(t.Context(), `SELECT COALESCE(claimed_by,'') FROM nodes WHERE run_id='run' AND node_id='job'`).Scan(&claimedBy); err != nil || claimedBy != "" {
			t.Fatalf("denied request claimed work: holder=%q error=%v", claimedBy, err)
		}
	})
}

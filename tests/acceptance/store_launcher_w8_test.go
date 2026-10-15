package acceptance_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type storeLauncherSyncW8 func(context.Context, capability.AuthorityRef, []controllerwire.LauncherObservation) (controllerwire.LauncherSyncResponse, error)

func (f storeLauncherSyncW8) ReadLauncherSync(ctx context.Context, ref capability.AuthorityRef, observed []controllerwire.LauncherObservation) (controllerwire.LauncherSyncResponse, error) {
	return f(ctx, ref, observed)
}

func TestStoreLauncherW8(t *testing.T) {
	server, root := storedCloudClaimantW8(t)
	kube, boundary := fake.NewClientset(), &cloudBoundaryFake{}
	kube.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		action.(ktesting.CreateAction).GetObject().(*batchv1.Job).UID = "job-uid"
		return false, nil, nil
	})
	_, cfg, _ := cloudFixture(launcher.CloudWork)
	runtime, err := launcher.NewStoreLauncher(t.Context(), server, launcher.StoreRuntimeConfig{
		Kube: kube, Boundary: boundary, Jobs: cfg,
		Demand: controllerwire.ClaimRequest{RunnerVersion: "v0.71.0", Kinds: []capability.UnitKind{capability.UnitNode}, AvailableCores: 2, AvailableMemoryBytes: 1 << 30},
		Bearer: func(context.Context) (string, error) { return root.Raw, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	reads := 0
	remove := false
	server.Launcher = storeLauncherSyncW8(func(ctx context.Context, ref capability.AuthorityRef, observed []controllerwire.LauncherObservation) (controllerwire.LauncherSyncResponse, error) {
		reads++
		if ref.ID != root.Capability.Prefix || len(observed) != 1 || observed[0].NodeUID != "uid" || observed[0].Job.UID != "job-uid" {
			t.Fatal("launcher lost its authenticated root or persisted Job")
		}
		if remove {
			return controllerwire.LauncherSyncResponse{Remove: []controllerwire.LauncherJobRef{observed[0].Job}}, nil
		}
		tenant, err := server.Store.ForTeam(ctx, store.DefaultTeam)
		if err != nil {
			return controllerwire.LauncherSyncResponse{}, err
		}
		row, err := tenant.ReadRun(ctx, "run")
		if err != nil {
			return controllerwire.LauncherSyncResponse{}, err
		}
		node, err := tenant.ReadNode(ctx, store.NodeRef{RunID: "run", NodeID: "job"})
		if err != nil {
			return controllerwire.LauncherSyncResponse{}, err
		}
		principal, err := server.Store.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: node.CapabilityPrefix})
		if err != nil {
			return controllerwire.LauncherSyncResponse{}, err
		}
		want := capability.LeaseRef{Kind: capability.UnitNode, ID: principal.Capability.UnitID, Holder: node.ClaimedBy, Generation: node.Generation}
		if observed[0].Award != want {
			t.Fatal("launcher changed its persisted award fence")
		}
		return controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{{
			Unit: capability.UnitNode, UnitID: principal.Capability.UnitID, RunID: "run", JobID: "job", Purpose: capability.PurposeWork,
			Delivery: capability.DeliveryBootstrap, Binding: principal.Capability.Binding, Request: row.Request,
			OwnerDeadline: principal.Capability.Binding.Deadline, ParentCapabilityExpiry: root.Capability.ExpiresAt, Capability: "reader-must-not-return-this",
		}}}, nil
	})
	claimed, err := runtime.LaunchOne(t.Context())
	if err != nil || !claimed || reads != 1 {
		t.Fatalf("store launcher: claimed=%t reads=%d error=%v", claimed, reads, err)
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
	remove = true
	if err := runtime.Sync(t.Context()); err != nil || boundary.released != 1 || reads != 2 {
		t.Fatalf("store-backed cleanup: releases=%d reads=%d error=%v", boundary.released, reads, err)
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
		client := runners.StoreController{Engine: server, Launcher: true, Bearer: func(context.Context) (string, error) { return raw, nil }}
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
			client.Launcher = false
		case 4:
			var prefix string
			if err := server.Store.DB().QueryRowContext(t.Context(), `SELECT prefix FROM capabilities WHERE subject_id='cloud-operator'`).Scan(&prefix); err != nil {
				t.Fatal(err)
			}
			actor := store.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}}
			if err := server.Store.AsOperator().RevokeAuthority(t.Context(), actor, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Capability.Prefix}); err != nil {
				t.Fatal(err)
			}
			want = apierr.Unauthenticated
		case 5:
			client.Bearer = func(context.Context) (string, error) { return "", providerFailure }
		}
		var err error
		if sync {
			_, err = client.SyncLauncher(t.Context(), controllerwire.LauncherSyncRequest{})
		} else {
			_, err = client.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: "v0.71.0", Kinds: []capability.UnitKind{capability.UnitNode}, AvailableCores: 2, AvailableMemoryBytes: 1 << 30})
		}
		if mode%6 == 5 {
			if !errors.Is(err, providerFailure) || called {
				t.Fatalf("lost credential provider error: %v", err)
			}
			return
		}
		if mode%6 == 3 && sync {
			if err == nil || called {
				t.Fatal("runner transport reached launcher sync")
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
		if err := server.Store.DB().QueryRowContext(t.Context(), `SELECT COALESCE(claimed_by,'') FROM nodes WHERE run_id='run' AND node_id='job'`).Scan(&claimedBy); err != nil || claimedBy != "" {
			t.Fatalf("denied request claimed work: holder=%q error=%v", claimedBy, err)
		}
	})
}

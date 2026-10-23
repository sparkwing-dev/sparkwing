package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestStoreLauncherReservationW8(t *testing.T) {
	for _, obstruction := range []string{"none", "job", "container"} {
		t.Run(obstruction, func(t *testing.T) {
			runtime, kube, server, resources, clears := reservationFixtureW8(t)
			cfg := runtime.Config
			var err error
			if err := runtime.Sync(t.Context()); err != nil || *clears != 0 {
				t.Fatalf("live reservation: clears=%d: %v", *clears, err)
			}
			if _, err := teststore.SQL(t, server.Store).ExecContext(t.Context(), `UPDATE nodes SET lease_expires_at=? WHERE run_id='run' AND node_id='job'`, time.Now().Add(-time.Hour).UnixNano()); err != nil {
				t.Fatal(err)
			}
			switch obstruction {
			case "job":
				_, err = kube.BatchV1().Jobs(cfg.Namespace).Create(t.Context(), &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: resources.Job.Name, Namespace: cfg.Namespace}}, metav1.CreateOptions{})
			case "container":
				_, err = kube.CoreV1().Pods(cfg.Namespace).Create(t.Context(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "docker", Namespace: cfg.Namespace}, Spec: corev1.PodSpec{NodeName: "dedicated"}}, metav1.CreateOptions{})
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := runtime.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			if obstruction != "none" {
				if *clears != 0 {
					t.Fatal("released before Job absence and container vacancy")
				}
				if obstruction == "job" {
					err = kube.BatchV1().Jobs(cfg.Namespace).Delete(t.Context(), resources.Job.Name, metav1.DeleteOptions{})
				} else {
					err = kube.CoreV1().Pods(cfg.Namespace).Delete(t.Context(), "docker", metav1.DeleteOptions{})
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := runtime.Sync(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if *clears != 1 {
				t.Fatalf("expired award retained vacant capacity: clears=%d", *clears)
			}
			if err := runtime.Sync(t.Context()); err != nil || *clears != 1 {
				t.Fatalf("cleanup replay: clears=%d: %v", *clears, err)
			}
		})
	}
}

func reservationFixtureW8(t *testing.T) (*launcher.Launcher, *fake.Clientset, *storedEngineW8, launcher.CloudJobResources, *int) {
	t.Helper()
	server, root := storedCloudClaimantExpiryW8(t, nil)
	_, cfg, _ := cloudFixture(launcher.CloudWork)
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "dedicated", UID: "node-uid", Labels: map[string]string{launcher.TeamNodeLabel: launcher.TeamNodeValue("cloud", "default")}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	karpenter := cloudProvisionerW8(node, "default")
	kube := cloudKubeW8(node)
	clears := 0
	boundary := &launcher.KubernetesBoundary{
		Kube: kube, Karpenter: karpenter, Namespace: cfg.Namespace,
		Probe: func(_ context.Context, r launcher.CloudNodeReservation) (launcher.CloudNetworkEvidence, error) {
			return launcher.CloudNetworkEvidence{Namespace: cfg.Namespace, NodeUID: r.NodeUID, PolicyRevision: "verified", DefaultDenyEgressEnforced: true, RequiredDestinationsAllowed: true, DaemonSetPortsInventoried: true, EscapeProbePassed: true}, nil
		}, Clear: func(context.Context, launcher.CloudNodeReservation) error { clears++; return nil },
	}
	runtime, err := launcher.NewLauncher(t.Context(), launcher.RuntimeConfig{
		Kube: kube, Boundary: boundary, Jobs: cfg,
		Demand: controllerwire.ClaimRequest{RunnerVersion: "v0.71.0", Kinds: []capability.UnitKind{capability.UnitNode}},
		Bearer: func(context.Context) (string, error) { return root.Raw, nil },
		NewController: func(options client.EngineOptions) (launcher.LauncherController, error) {
			return cloudStoreClientW8(t, server)(options)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	award, err := runtime.Control.Claim(t.Context(), "cloud", nil)
	if err != nil || award == nil {
		t.Fatalf("claim: %v", err)
	}
	resources, err := launcher.BuildJob(t.Context(), time.Now(), cfg, *award, boundary)
	if err != nil {
		t.Fatalf("build reservation: %v", err)
	}
	return runtime, kube, server, resources, &clears
}

func FuzzStoreLauncherReservationFenceW8(f *testing.F) {
	for mode := uint8(0); mode < 10; mode++ {
		f.Add(mode, []byte("foreign"))
	}
	f.Fuzz(func(t *testing.T, mode uint8, input []byte) {
		runtime, kube, server, resources, clears := reservationFixtureW8(t)
		mode %= 10
		if mode == 9 {
			wire, err := cloudStoreClientW8(t, server)(client.EngineOptions{URL: runtime.Config.ControllerURL, Bearer: func(context.Context) (string, error) { return string(resources.BootstrapSecret.Data["bootstrap"]), nil }})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := wire.Exchange(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := teststore.SQL(t, server.Store).ExecContext(t.Context(), `UPDATE nodes SET lease_expires_at=? WHERE run_id='run' AND node_id='job'`, time.Now().Add(-time.Hour).UnixNano()); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(input)
		foreign := "foreign-" + hex.EncodeToString(sum[:8])
		node, err := kube.CoreV1().Nodes().Get(t.Context(), "dedicated", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var held launcher.CloudNodeReservation
		if err := json.Unmarshal([]byte(node.Annotations["sparkwing.dev/node-reservation"]), &held); err != nil {
			t.Fatal(err)
		}
		binding := resources.Job.Annotations["sparkwing.dev/award-binding"]
		var award launcher.CloudJobBinding
		if err := json.Unmarshal([]byte(binding), &award); err != nil {
			t.Fatal(err)
		}
		switch mode {
		case 1:
			held.Award.Holder, award.Holder = foreign, foreign
		case 2:
			held.Award.Generation++
			award.Generation++
		case 3:
			held.Award.ID, award.UnitID = foreign, foreign
		case 4:
			held.NodeUID = foreign
		case 5:
			held.Award.Kind, award.Kind = capability.UnitEval, launcher.CloudEval
		case 6:
			if _, err := teststore.SQL(t, server.Store).ExecContext(t.Context(), `UPDATE nodes SET generation=generation+1, claimed_by=? WHERE run_id='run' AND node_id='job'`, foreign); err != nil {
				t.Fatal(err)
			}
		case 7:
			if _, err := teststore.SQL(t, server.Store).ExecContext(t.Context(), `UPDATE nodes SET cancel_requested_at=? WHERE run_id='run' AND node_id='job'`, time.Now().UnixNano()); err != nil {
				t.Fatal(err)
			}
		case 8:
			ref := server.Bootstrap
			other, err := server.Store.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionService, RegistrationID: "service:" + foreign, Authority: &ref, Subject: capability.Subject{Kind: capability.SubjectService, ID: foreign}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.ClaimsLaunch}})
			if err != nil {
				t.Fatal(err)
			}
			wire, err := cloudStoreClientW8(t, server)(client.EngineOptions{URL: runtime.Config.ControllerURL, Bearer: func(context.Context) (string, error) { return other.Raw, nil }, Launcher: true})
			if err != nil {
				t.Fatal(err)
			}
			runtime.Control, err = launcher.NewControllerControl(wire, controllerwire.ClaimRequest{RunnerVersion: "v0.71.0", Kinds: []capability.UnitKind{capability.UnitNode}})
			if err != nil {
				t.Fatal(err)
			}
		}
		if mode == 1 || mode == 2 || mode == 3 || mode == 5 {
			var prefix string
			if err := teststore.SQL(t, server.Store).QueryRowContext(t.Context(), `SELECT prefix FROM capabilities WHERE subject_id='launcher' AND unit_id IS NULL`).Scan(&prefix); err != nil {
				t.Fatal(err)
			}
			ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}
			result, _, err := server.Store.AsOperator().ReconcileLauncherAwards(t.Context(), ref, nil, []capability.LeaseRef{held.Award}, nil, nil, nil)
			if err == nil || len(result) != 0 {
				t.Fatalf("store authorized a fabricated award: mode=%d result=%+v: %v", mode, result, err)
			}
		}
		body, err := json.Marshal(held)
		if err != nil {
			t.Fatal(err)
		}
		node.Annotations["sparkwing.dev/node-reservation"] = string(body)
		body, err = json.Marshal(award)
		if err != nil {
			t.Fatal(err)
		}
		node.Annotations["sparkwing.dev/award-binding"] = string(body)
		if _, err := kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		err = runtime.Sync(t.Context())
		if mode == 0 || mode == 6 || mode == 7 || mode == 9 {
			if err != nil || *clears != 1 {
				t.Fatalf("own stale reservation retained: mode=%d clears=%d: %v", mode, *clears, err)
			}
		} else if err == nil || *clears != 0 {
			t.Fatalf("foreign fence authorized release: mode=%d clears=%d: %v", mode, *clears, err)
		}
	})
}

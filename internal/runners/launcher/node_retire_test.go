package launcher

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type terminalControl struct {
	CloudControl
	terminal bool
	err      error
}

func (c terminalControl) RunTerminal(context.Context, string, runspec.RunID) (bool, error) {
	return c.terminal, c.err
}

func TestRestartRetiresOnlyEmptySpentNodesOfTerminalRuns(t *testing.T) {
	for _, scenario := range []string{"terminal", "running", "controller unavailable", "daemon pod", "work pod", "terminal pod", "warm pod", "foreign team", "foreign run", "unlabeled", "missing annotation", "foreign pool", "claim UID changed", "node replaced", "concurrent reservation"} {
		t.Run(scenario, func(t *testing.T) {
			boundary, kube := boundaryFixture()
			binding := boundaryBinding("team")
			held, err := boundary.Reserve(t.Context(), binding, jobName(binding), CloudPlacement{RequestedClass: "2"})
			if err != nil {
				t.Fatal(err)
			}
			if err = boundary.Release(t.Context(), held); err != nil {
				t.Fatal(err)
			}
			l, _, _, control := fixture()
			l.Kube, l.Boundary = kube, boundary
			l.Control = terminalControl{CloudControl: control, terminal: scenario != "running"}
			if scenario == "controller unavailable" {
				l.Control = terminalControl{CloudControl: control, err: errors.New("unavailable")}
			}
			node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "foreign team":
				node.Labels[TeamNodeLabel] = TeamNodeValue("cloud", "foreign")
			case "foreign run":
				node.Labels[usedRunLabel] = runNodeValue("cloud", "foreign")
			case "unlabeled":
				delete(node.Labels, usedRunLabel)
			case "missing annotation":
				delete(node.Annotations, usedRunAnnotation)
			case "foreign pool":
				node.Labels[PoolNamespaceLabel] = "foreign"
			case "claim UID changed":
				node.OwnerReferences[0].UID = "replacement"
			}
			if _, err = kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if scenario == "node replaced" || scenario == "concurrent reservation" {
				kube.PrependReactor("get", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
					current, err := kube.Tracker().Get(corev1.SchemeGroupVersion.WithResource("nodes"), "", "node")
					if err != nil {
						return true, nil, err
					}
					changed := current.(*corev1.Node).DeepCopy()
					if scenario == "node replaced" {
						changed.UID = "replacement"
					} else {
						changed.ResourceVersion = "999"
						changed.Annotations[reservationAnnotation] = "new reservation"
					}
					return true, changed, nil
				})
			}
			if scenario == "daemon pod" || scenario == "work pod" || scenario == "terminal pod" || scenario == "warm pod" {
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "occupant", Namespace: "foreign"}, Spec: corev1.PodSpec{NodeName: "node"}}
				if scenario == "daemon pod" {
					pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "daemon", UID: "daemon-uid", Controller: ptr(true)}}
				}
				if scenario == "terminal pod" {
					pod.Status.Phase = corev1.PodSucceeded
				}
				if scenario == "warm pod" {
					pod = sharedWarmPod()
				}
				if _, err = kube.CoreV1().Pods(pod.Namespace).Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			karpenter := boundary.Karpenter.(*dynamicfake.FakeDynamicClient)
			karpenter.PrependReactor("delete", "nodeclaims", func(action ktesting.Action) (bool, runtime.Object, error) {
				options := action.(ktesting.DeleteAction).GetDeleteOptions()
				if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != "claim-uid" {
					t.Fatalf("unfenced deletion: %+v", options)
				}
				return false, nil, nil
			})
			err = l.Sync(t.Context())
			if scenario == "controller unavailable" {
				if err == nil {
					t.Fatal("lost controller error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = boundary.Karpenter.Resource(nodeClaims).Get(t.Context(), "claim", metav1.GetOptions{})
			retired := scenario == "terminal" || scenario == "daemon pod"
			if apierrors.IsNotFound(err) != retired {
				t.Fatalf("retired=%v want=%v: %v", apierrors.IsNotFound(err), retired, err)
			}
			if err = l.Sync(t.Context()); err != nil {
				t.Fatalf("repeated reconciliation: %v", err)
			}
		})
	}
}

func TestRunRetirementQueryThroughController(t *testing.T) {
	e, _ := cloudResourceStore(t)
	raw := cloudResourceRoot(t, e)
	handler := controller.New(e.Store, nil).WithEngineController(e.EngineController).Handler()
	transport := &http.Client{Transport: cloudResourceTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})}
	wire, err := client.NewEngineClient(client.EngineOptions{URL: "https://controller.example", Launcher: true, Bearer: func(context.Context) (string, error) { return raw, nil }, HTTP: transport})
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewControllerControl(wire, controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{capability.UnitPlan}})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"running", "success", "failed", "cancelled", "dry_run"} {
		var finished any
		if status != "running" {
			finished = int64(1)
		}
		cloudResourceSQL(t, e.Store, `UPDATE runs SET status=?,finished_at=? WHERE team='a' AND id='run'`, status, finished)
		terminal, err := control.RunTerminal(t.Context(), "a", "run")
		if err != nil || terminal != (status != "running") {
			t.Fatalf("%s: terminal=%v, %v", status, terminal, err)
		}
	}
	for _, ref := range []controllerwire.LauncherRun{{Team: "foreign", RunID: "run"}, {Team: "a", RunID: "missing"}} {
		terminal, err := control.RunTerminal(t.Context(), ref.Team, runspec.RunID(ref.RunID))
		if err != nil || terminal {
			t.Fatalf("foreign/missing %+v: %v, %v", ref, terminal, err)
		}
	}
	if _, err = wire.SyncLauncher(t.Context(), controllerwire.LauncherSyncRequest{RetireRun: &controllerwire.LauncherRun{Team: "a", RunID: "run"}, WarmPools: map[string]int64{"sparkwing-2cpu": 0}}); err == nil {
		t.Fatal("mixed retirement query accepted")
	}
	cloudResourceSQL(t, e.Store, `UPDATE capabilities SET revoked_at=1 WHERE prefix='launcher'`)
	if terminal, err := control.RunTerminal(t.Context(), "a", "run"); err == nil || terminal {
		t.Fatalf("revoked launcher: %v, %v", terminal, err)
	}
}

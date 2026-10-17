package launcher

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func boundaryBinding(team string) CloudJobBinding {
	return CloudJobBinding{Kind: CloudPlan, UnitID: "plan", Team: team, RunID: "run", Holder: "holder", Generation: 1, OwnerDeadline: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func boundaryFixture() (*KubernetesBoundary, *fake.Clientset) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "uid", ResourceVersion: "1", OwnerReferences: []metav1.OwnerReference{{APIVersion: "karpenter.sh/v1", Kind: "NodeClaim", Name: "claim", UID: "claim-uid"}}, Labels: map[string]string{TeamNodeLabel: TeamNodeValue("team")}}, Spec: corev1.NodeSpec{ProviderID: "aws:///zone/i-node"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	kube := fake.NewClientset(node)
	var mu sync.Mutex
	kube.PrependReactor("update", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		candidate := action.(ktesting.UpdateAction).GetObject().(*corev1.Node).DeepCopy()
		gvr := corev1.SchemeGroupVersion.WithResource("nodes")
		current, err := kube.Tracker().Get(gvr, "", candidate.Name)
		if err != nil {
			return true, nil, err
		}
		if current.(*corev1.Node).ResourceVersion != candidate.ResourceVersion {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, candidate.Name, errors.New("changed"))
		}
		revision, err := strconv.Atoi(candidate.ResourceVersion)
		if err != nil {
			return true, nil, err
		}
		candidate.ResourceVersion = strconv.Itoa(revision + 1)
		if err = kube.Tracker().Update(gvr, candidate, ""); err != nil {
			return true, nil, err
		}
		return true, candidate, nil
	})
	return &KubernetesBoundary{Kube: kube, Karpenter: karpenterFixture(), Namespace: "cloud", Probe: func(_ context.Context, r CloudNodeReservation) (CloudNetworkEvidence, error) {
		return CloudNetworkEvidence{Namespace: "cloud", NodeUID: r.NodeUID, PolicyRevision: "revision", DefaultDenyEgressEnforced: true, RequiredDestinationsAllowed: true, DaemonSetPortsInventoried: true, EscapeProbePassed: true}, nil
	}, Clear: func(context.Context, CloudNodeReservation) error { return nil }}, kube
}

func TestBoundaryCompetingLaunchersCannotShareNode(t *testing.T) {
	b, kube := boundaryFixture()
	var wins atomic.Int32
	var wg sync.WaitGroup
	for _, job := range []string{"job-a", "job-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := b.Reserve(t.Context(), boundaryBinding("team"), job)
			if err == nil {
				wins.Add(1)
			} else if !errors.Is(err, ErrNoDedicatedNode) {
				t.Errorf("reserve: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("reserved node %d times", wins.Load())
	}
	node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if node.Annotations[reservationAnnotation] == "" || len(node.Spec.Taints) != 1 {
		t.Fatal("reservation was not durable or scheduling was not fenced")
	}
}

func TestBoundaryRequiresTeardownAndNodeCleanup(t *testing.T) {
	b, kube := boundaryFixture()
	r, _, err := b.Reserve(t.Context(), boundaryBinding("team"), "job")
	if err != nil {
		t.Fatal(err)
	}
	cleared := false
	b.Clear = func(context.Context, CloudNodeReservation) error { cleared = true; return nil }
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "old", Namespace: "cloud"}, Spec: corev1.PodSpec{NodeName: "node"}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}
	if _, err = kube.CoreV1().Pods("cloud").Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = b.Release(t.Context(), r); !errors.Is(err, ErrContainersRunning) {
		t.Fatalf("release while pod remained=%v", err)
	}
	if cleared {
		t.Fatal("node cleanup ran before Kubernetes removed the pod")
	}
	if err = kube.CoreV1().Pods("cloud").Delete(t.Context(), "old", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = b.Release(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if !cleared {
		t.Fatal("node state was not cleared")
	}
	if _, _, err = b.Reserve(t.Context(), boundaryBinding("team"), "next"); err != nil {
		t.Fatal(err)
	}
	if err = b.Release(t.Context(), r); err == nil {
		t.Fatal("stale cleanup released the next reservation")
	}
}

func TestBoundaryProbeFailureRefusesAndReleases(t *testing.T) {
	b, kube := boundaryFixture()
	b.Probe = func(context.Context, CloudNodeReservation) (CloudNetworkEvidence, error) {
		return CloudNetworkEvidence{}, errors.New("escape probe failed")
	}
	if _, _, err := b.Reserve(t.Context(), boundaryBinding("team"), "job"); err == nil {
		t.Fatal("missing enforcement admitted work")
	}
	node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if node.Annotations[reservationAnnotation] != "" {
		t.Fatal("failed probe leaked reservation")
	}
}

func TestBoundaryRefusesOtherTeamAndCrossNamespaceReuse(t *testing.T) {
	b, kube := boundaryFixture()
	if _, _, err := b.Reserve(t.Context(), boundaryBinding("other"), "job"); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatalf("other team=%v", err)
	}
	if _, _, err := b.Reserve(t.Context(), boundaryBinding("team"), "job"); err != nil {
		t.Fatal(err)
	}
	other := *b
	other.Kube = kube
	other.Namespace = "elsewhere"
	if _, _, err := other.Reserve(t.Context(), boundaryBinding("team"), "job"); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatalf("cross namespace=%v", err)
	}
}

func TestBoundaryCannotRecoverFromAnExpiryAnnotation(t *testing.T) {
	b, kube := boundaryFixture()
	binding := boundaryBinding("team")
	if _, _, err := b.Reserve(t.Context(), binding, "orphan"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Reserve(t.Context(), binding, "next"); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatalf("live orphan was stolen: %v", err)
	}
	node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Annotations["sparkwing.dev/reservation-expiry"] = "2000-01-01T00:00:00Z"
	if _, err = kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Reserve(t.Context(), binding, "next"); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatalf("annotation authorized recovery: %v", err)
	}
}

func TestBoundaryRefusesKubeletRelabeledNode(t *testing.T) {
	b, kube := boundaryFixture()
	node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Labels[TeamNodeLabel] = TeamNodeValue("other")
	if _, err = kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = b.Reserve(t.Context(), boundaryBinding("other"), "foreign-job"); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatalf("relabeled node admitted another team: %v", err)
	}
}

func TestBoundaryNeverReassignsUsedNode(t *testing.T) {
	b, kube := boundaryFixture()
	r, _, err := b.Reserve(t.Context(), boundaryBinding("team"), "original")
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Release(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if node.Labels[TeamNodeLabel] != TeamNodeValue("team") {
		t.Fatal("Clear changed the team's label")
	}
	node.Labels[TeamNodeLabel] = TeamNodeValue("other")
	claim, err := b.Karpenter.Resource(nodeClaims).Get(t.Context(), "claim", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	claim.SetLabels(map[string]string{TeamNodeLabel: TeamNodeValue("other"), "karpenter.sh/nodepool": "pool"})
	if _, err = b.Karpenter.Resource(nodeClaims).Update(t.Context(), claim, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	pool, err := b.Karpenter.Resource(nodePools).Get(t.Context(), "pool", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = unstructured.SetNestedStringMap(pool.Object, map[string]string{TeamNodeLabel: TeamNodeValue("other")}, "spec", "template", "metadata", "labels"); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Karpenter.Resource(nodePools).Update(t.Context(), pool, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = b.Reserve(t.Context(), boundaryBinding("other"), "foreign-job"); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatalf("used node admitted another team: %v", err)
	}
}

func FuzzBoundaryNeverCrossesTeams(f *testing.F) {
	f.Add("team-y", false)
	f.Add("team-z", true)
	f.Fuzz(func(t *testing.T, other string, used bool) {
		if other == "" || other == "team" {
			t.Skip()
		}
		b, kube := boundaryFixture()
		if used {
			r, _, err := b.Reserve(t.Context(), boundaryBinding("team"), "original")
			if err != nil {
				t.Fatal(err)
			}
			if err = b.Release(t.Context(), r); err != nil {
				t.Fatal(err)
			}
		}
		node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		node.Labels[TeamNodeLabel] = TeamNodeValue(other)
		if _, err = kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, _, err = b.Reserve(t.Context(), boundaryBinding(other), "foreign-job"); !errors.Is(err, ErrNoDedicatedNode) {
			t.Fatalf("cross-team reservation: used=%v err=%v", used, err)
		}
	})
}

func karpenterFixture() dynamic.Interface {
	claim := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.sh/v1", "kind": "NodeClaim",
		"metadata": map[string]any{"name": "claim", "uid": "claim-uid", "labels": map[string]any{TeamNodeLabel: TeamNodeValue("team"), "karpenter.sh/nodepool": "pool"}, "ownerReferences": []any{map[string]any{"apiVersion": "karpenter.sh/v1", "kind": "NodePool", "name": "pool", "uid": "pool-uid"}}},
		"status":   map[string]any{"nodeName": "node", "providerID": "aws:///zone/i-node"},
	}}
	pool := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.sh/v1", "kind": "NodePool", "metadata": map[string]any{"name": "pool", "uid": "pool-uid"},
		"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{TeamNodeLabel: TeamNodeValue("team")}}}},
	}}
	return dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), claim, pool)
}

func TestTeamLabelIsNodeRestrictionProtected(t *testing.T) {
	if TeamNodeLabel != "node-restriction.kubernetes.io/sparkwing-team-node" {
		t.Fatalf("unprotected team label: %s", TeamNodeLabel)
	}
}

func TestBoundaryRetiresRejectedNode(t *testing.T) {
	b, kube := boundaryFixture()
	r, _, err := b.Reserve(t.Context(), boundaryBinding("team"), "job")
	if err != nil {
		t.Fatal(err)
	}
	b.Karpenter.(*dynamicfake.FakeDynamicClient).PrependReactor("delete", "nodeclaims", func(action ktesting.Action) (bool, runtime.Object, error) {
		options := action.(ktesting.DeleteAction).GetDeleteOptions()
		if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != "claim-uid" {
			t.Fatal("NodeClaim deletion lost UID fence")
		}
		return false, nil, nil
	})
	if err = b.Retire(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	node, err := kube.CoreV1().Nodes().Get(t.Context(), r.NodeName, metav1.GetOptions{})
	if err != nil || !node.Spec.Unschedulable {
		t.Fatalf("node was not cordoned: %v", err)
	}
	if _, err = b.Karpenter.Resource(nodeClaims).Get(t.Context(), "claim", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("NodeClaim survived: %v", err)
	}
	if err = b.Retire(t.Context(), r); err != nil {
		t.Fatalf("retirement replay failed: %v", err)
	}
	if err = b.Release(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if _, _, err = b.Reserve(t.Context(), boundaryBinding("team"), "next"); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatalf("retired node was reused: %v", err)
	}
}

func FuzzBoundaryRequiresMatchingProvisioner(f *testing.F) {
	for mode := uint8(0); mode < 8; mode++ {
		f.Add(mode, "foreign-team")
	}
	f.Fuzz(func(t *testing.T, mode uint8, other string) {
		if other == "" || other == "team" {
			t.Skip()
		}
		b, kube := boundaryFixture()
		node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		claim, err := b.Karpenter.Resource(nodeClaims).Get(t.Context(), "claim", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		pool, err := b.Karpenter.Resource(nodePools).Get(t.Context(), "pool", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		switch mode % 8 {
		case 0:
			node.Labels[TeamNodeLabel] = TeamNodeValue(other)
		case 1:
			node.OwnerReferences[0].UID = "replacement"
		case 2:
			node.Spec.ProviderID = "foreign-provider"
		case 3:
			if err = unstructured.SetNestedField(claim.Object, "other-node", "status", "nodeName"); err != nil {
				t.Fatal(err)
			}
		case 4:
			pool.SetUID("replacement")
		case 5:
			if err = unstructured.SetNestedStringMap(pool.Object, map[string]string{TeamNodeLabel: TeamNodeValue(other)}, "spec", "template", "metadata", "labels"); err != nil {
				t.Fatal(err)
			}
		case 6:
			claim.SetLabels(map[string]string{TeamNodeLabel: TeamNodeValue(other), "karpenter.sh/nodepool": "pool"})
		case 7:
			node.Labels[usedTeamLabel] = TeamNodeValue(other)
		}
		if _, err = b.Karpenter.Resource(nodeClaims).Update(t.Context(), claim, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err = b.Karpenter.Resource(nodePools).Update(t.Context(), pool, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err = kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		probes := 0
		b.Probe = func(context.Context, CloudNodeReservation) (CloudNetworkEvidence, error) {
			probes++
			return CloudNetworkEvidence{}, nil
		}
		if _, _, err = b.Reserve(t.Context(), boundaryBinding("team"), "job"); !errors.Is(err, ErrNoDedicatedNode) {
			t.Fatalf("foreign provisioner accepted: mode=%d err=%v", mode, err)
		}
		if probes != 0 {
			t.Fatal("foreign node reached probe execution")
		}
	})
}

func FuzzRetirementNeverTouchesReplacement(f *testing.F) {
	for mode := uint8(0); mode < 6; mode++ {
		f.Add(mode, "foreign")
	}
	f.Fuzz(func(t *testing.T, mode uint8, value string) {
		b, kube := boundaryFixture()
		r, _, err := b.Reserve(t.Context(), boundaryBinding("team"), "job")
		if err != nil {
			t.Fatal(err)
		}
		candidate := r
		switch mode % 6 {
		case 0:
			candidate.NodeUID += "x" + value
		case 1:
			candidate.Team += "x" + value
		case 2:
			candidate.ReservationID += "x" + value
		case 3:
			candidate.Award.Generation++
		case 4:
			candidate.Award.Holder += "x" + value
		case 5:
			candidate.JobName += "x" + value
		}
		if err = b.Retire(t.Context(), candidate); err == nil {
			t.Fatal("foreign reservation authorized retirement")
		}
		node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
		if err != nil || node.Spec.Unschedulable {
			t.Fatalf("replacement was cordoned: %v", err)
		}
		if _, err = b.Karpenter.Resource(nodeClaims).Get(t.Context(), "claim", metav1.GetOptions{}); err != nil {
			t.Fatalf("replacement NodeClaim removed: %v", err)
		}
	})
}

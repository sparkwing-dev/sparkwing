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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func boundaryBinding(team string) CloudJobBinding {
	return CloudJobBinding{Kind: CloudPlan, UnitID: "plan", Team: team, RunID: "run", Holder: "holder", Generation: 1, OwnerDeadline: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func boundaryFixture() (*KubernetesBoundary, *fake.Clientset) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "uid", ResourceVersion: "1", Labels: map[string]string{TeamNodeLabel: TeamNodeValue("team")}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
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
	return &KubernetesBoundary{Kube: kube, Namespace: "cloud", Probe: func(_ context.Context, r CloudNodeReservation) (CloudNetworkEvidence, error) {
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

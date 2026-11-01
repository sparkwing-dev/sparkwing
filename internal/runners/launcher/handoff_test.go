package launcher

import (
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestSuccessorWaitsForPredecessorNode(t *testing.T) {
	l, kube, _, control := fixture()
	boundary, nodes := boundaryFixture()
	node, err := nodes.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = kube.Tracker().Add(node); err != nil {
		t.Fatal(err)
	}
	boundary.Kube = kube
	l.Boundary = boundary
	l.Config.MemoryCeiling = 16 << 30
	control.award.CPUCores = 2
	control.award.Memory = match.CloudSizeMemoryBytes(2)
	predecessor := *control.award
	if _, err = l.launch(t.Context(), &predecessor); err != nil {
		t.Fatal(err)
	}
	job, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(predecessor.Binding), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: *job.Spec.Template.ObjectMeta.DeepCopy(), Spec: *job.Spec.Template.Spec.DeepCopy()}
	pod.Name, pod.Namespace, pod.UID = "predecessor", "cloud", "predecessor-pod"
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr(true)}}
	pod.Status.Phase = corev1.PodRunning
	if _, err = kube.CoreV1().Pods("cloud").Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	control.remove = true
	if err = l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A ready fresh node must not win the handoff while the old one is deleting.
	fresh := node.DeepCopy()
	fresh.Name, fresh.UID = "a-fresh", "fresh-uid"
	fresh.OwnerReferences[0].Name, fresh.OwnerReferences[0].UID = "fresh-claim", "fresh-claim-uid"
	claim, err := boundary.Karpenter.Resource(nodeClaims).Get(t.Context(), "claim", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	claim.SetName("fresh-claim")
	claim.SetUID("fresh-claim-uid")
	if err = unstructured.SetNestedField(claim.Object, fresh.Name, "status", "nodeName"); err != nil {
		t.Fatal(err)
	}
	if _, err = boundary.Karpenter.Resource(nodeClaims).Create(t.Context(), claim, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = kube.CoreV1().Nodes().Create(t.Context(), fresh, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	successor := predecessor
	successor.Binding.UnitID, successor.Binding.NodeID = "successor", "successor"
	control.award, control.remove = &successor, false
	if _, err = l.launch(t.Context(), &successor); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatalf("pending teardown: %v", err)
	}
	requests, err := l.capacityPods(t.Context())
	if err != nil || len(requests) != 0 {
		t.Fatalf("successor provisioned during teardown: %d requests, %v", len(requests), err)
	}
	if err = kube.CoreV1().Pods("cloud").Delete(t.Context(), pod.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = l.placeWaiting(t.Context()); err != nil {
		t.Fatal(err)
	}
	next, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(successor.Binding), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, reservation, err := jobOwnership(*next)
	if err != nil || reservation.NodeUID != string(node.UID) {
		t.Fatalf("successor lost original node: %+v, %v", reservation, err)
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "create" && action.GetResource().Resource == "pods" && action.(ktesting.CreateAction).GetObject().(*corev1.Pod).Labels[capacityLabel] == "true" {
			t.Fatal("handoff created a provisioning Pod")
		}
	}
	requests, err = l.capacityPods(t.Context())
	if err != nil || len(requests) != 0 {
		t.Fatalf("unexpected capacity request: %d, %v", len(requests), err)
	}
}

func TestTeardownWaitIsBoundedAndRunScoped(t *testing.T) {
	for _, scenario := range []string{"same run", "other run", "other team", "live predecessor", "foreign pod", "wrong class", "quarantined", "wrong binding", "expired wait"} {
		t.Run(scenario, func(t *testing.T) {
			l, kube, _, control := fixture()
			boundary, nodes := boundaryFixture()
			node, err := nodes.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err = kube.Tracker().Add(node); err != nil {
				t.Fatal(err)
			}
			boundary.Kube, l.Boundary = kube, boundary
			l.Config.MemoryCeiling = 16 << 30
			predecessor := *control.award
			predecessor.CPUCores, predecessor.Memory = 2, match.CloudSizeMemoryBytes(2)
			if _, err = l.launch(t.Context(), &predecessor); err != nil {
				t.Fatal(err)
			}
			job, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(predecessor.Binding), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "live predecessor" {
				foreground := metav1.DeletePropagationForeground
				if err = kube.BatchV1().Jobs("cloud").Delete(t.Context(), job.Name, metav1.DeleteOptions{PropagationPolicy: &foreground, Preconditions: &metav1.Preconditions{UID: &job.UID}}); err != nil {
					t.Fatal(err)
				}
			}
			successor := predecessor
			successor.Binding.UnitID, successor.Binding.NodeID = "next", "next"
			switch scenario {
			case "other run":
				successor.Binding.RunID = "other"
			case "other team":
				successor.Binding.Team = "other"
			case "expired wait":
				successor.teardownDeadline = time.Now().Add(-time.Second)
			case "wrong class":
				successor.CPUCores, successor.Memory = 4, match.CloudSizeMemoryBytes(4)
				l.Config.MemoryCeiling = 32 << 30
			case "foreign pod":
				_, err = kube.CoreV1().Pods("foreign").Create(t.Context(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "foreign", Namespace: "foreign"}, Spec: corev1.PodSpec{NodeName: "node"}}, metav1.CreateOptions{})
			case "quarantined", "wrong binding":
				node, err = kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "quarantined" {
					node.Annotations[quarantineAnnotation] = "fault"
				} else {
					node.Annotations[bindingAnnotation] = "{}"
				}
				_, err = kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{})
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "wrong class" {
				_, err = BuildJob(t.Context(), time.Now(), l.Config, successor, boundary)
				if !errors.Is(err, ErrNoDedicatedNode) || errors.Is(err, errPredecessorTeardown) {
					t.Fatalf("wrong-class teardown deferred: %v", err)
				}
				return
			}
			if _, err = l.launch(t.Context(), &successor); !errors.Is(err, ErrNoDedicatedNode) {
				t.Fatalf("launch: %v", err)
			}
			requests, err := l.capacityPods(t.Context())
			want := 1
			if scenario == "same run" {
				want = 0
			}
			if err != nil || len(requests) != want {
				t.Fatalf("requests=%d want=%d: %v", len(requests), want, err)
			}
		})
	}
}

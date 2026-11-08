package launcher

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestRunRechecksPredecessorExitBeforeNextClaimPoll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
		l.Poll = 5 * time.Second
		predecessor := *control.award
		predecessor.CPUCores, predecessor.Memory = 2, match.CloudSizeMemoryBytes(2)
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
		successor := predecessor
		successor.Binding.UnitID, successor.Binding.NodeID = "successor", "successor"
		control.award, control.remove = nil, false
		if _, err = l.launch(t.Context(), &successor); !errors.Is(err, ErrNoDedicatedNode) {
			t.Fatalf("pending teardown: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error)
		go func() { done <- l.Run(ctx) }()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}()
		synctest.Wait()
		if _, err = kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(successor.Binding), metav1.GetOptions{}); err == nil {
			t.Fatal("successor overlapped live predecessor")
		}
		time.Sleep(500 * time.Millisecond)
		if err = kube.CoreV1().Pods("cloud").Delete(t.Context(), pod.Name, metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		next, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(successor.Binding), metav1.GetOptions{})
		if err != nil {
			t.Fatalf("successor still waiting after predecessor exit: %v", err)
		}
		if _, reservation, err := jobOwnership(*next); err != nil || reservation.NodeUID != string(node.UID) {
			t.Fatalf("successor lost original node: %+v, %v", reservation, err)
		}
		if control.claims != 1 {
			t.Fatalf("fast reconciliation made %d claims, want one", control.claims)
		}
		for _, action := range kube.Actions() {
			if action.GetVerb() == "create" && action.GetResource().Resource == "pods" && action.(ktesting.CreateAction).GetObject().(*corev1.Pod).Labels[capacityLabel] == "true" {
				t.Fatal("handoff requested fresh capacity")
			}
		}
		time.Sleep(4 * time.Second)
		synctest.Wait()
		if control.claims != 2 {
			t.Fatalf("claims at next poll=%d, want two", control.claims)
		}
		t.Log("predecessor exit at 500ms; successor placed by 1s; next claim remains at 5s")
	})
}

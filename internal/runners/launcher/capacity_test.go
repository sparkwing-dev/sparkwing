package launcher

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

func capacityPods(t *testing.T, kube *fake.Clientset) []corev1.Pod {
	t.Helper()
	pods, err := kube.CoreV1().Pods("cloud").List(t.Context(), metav1.ListOptions{LabelSelector: capacityLabel + "=true"})
	if err != nil {
		t.Fatal(err)
	}
	return pods.Items
}

func TestLaunchWithoutATeamNodeRequestsOneNodePerUnit(t *testing.T) {
	l, kube, boundary, control := fixture()
	boundary.reserveErr = ErrNoDedicatedNode
	if _, err := l.LaunchOne(t.Context()); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatalf("launch without a node: %v", err)
	}
	pods := capacityPods(t, kube)
	if len(pods) != 1 {
		t.Fatalf("capacity requests = %d, want 1", len(pods))
	}
	pod := pods[0]
	spec := pod.Spec
	if spec.NodeName != "" || !reflect.DeepEqual(spec.NodeSelector, map[string]string{TeamNodeLabel: TeamNodeValue("team")}) || !reflect.DeepEqual(spec.Tolerations, teamTolerations("team")) {
		t.Fatalf("request is not confined to the team pool: %+v", spec)
	}
	if spec.ServiceAccountName != ServiceAccount || spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken || len(spec.Volumes) != 0 || len(spec.InitContainers) != 0 || len(spec.Containers) != 1 {
		t.Fatalf("request carries more than a sized placeholder: %+v", spec)
	}
	container := spec.Containers[0]
	want := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1500m"), corev1.ResourceMemory: *resource.NewQuantity(2<<30, resource.BinarySI), corev1.ResourceEphemeralStorage: *resource.NewQuantity(limits.JobScratchBytes, resource.BinarySI)}
	for name, quantity := range want {
		if got := container.Resources.Requests[name]; got.Cmp(quantity) != 0 {
			t.Fatalf("request %s = %s, want %s", name, got.String(), quantity.String())
		}
	}
	if len(container.Env) != 0 || len(container.EnvFrom) != 0 || len(container.VolumeMounts) != 0 || container.Image != l.Config.RunnerImage {
		t.Fatalf("request container = %+v", container)
	}
	control.award.Binding.Generation, control.award.Binding.Holder = 2, "next-holder"
	if _, err := l.LaunchOne(t.Context()); !errors.Is(err, ErrNoDedicatedNode) || len(capacityPods(t, kube)) != 1 {
		t.Fatalf("a re-award asked for a second node: %v", err)
	}
	control.award.Binding.UnitID, control.award.Binding.NodeID = "unit-2", "job-2"
	if _, err := l.LaunchOne(t.Context()); !errors.Is(err, ErrNoDedicatedNode) || len(capacityPods(t, kube)) != 2 {
		t.Fatalf("a second waiting unit did not ask for its own node: %v", err)
	}
	jobs, err := kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 0 {
		t.Fatalf("a job started without a reserved node: %v", err)
	}
}

func TestCapacityRequestCarriesNoAwardCredentialProperty(t *testing.T) {
	l, _, _, control := fixture()
	property := func(secret [24]byte, team string) bool {
		if team == "" {
			return true
		}
		award := *control.award
		award.Bootstrap = []byte("swk_" + hex.EncodeToString(secret[:]))
		award.Binding.Team = team
		pod, err := capacityPod(l.Config, award, time.Now())
		if err != nil {
			t.Log(err)
			return false
		}
		raw, err := json.Marshal(pod)
		return err == nil && !bytes.Contains(raw, award.Bootstrap) && pod.Spec.NodeSelector[TeamNodeLabel] == TeamNodeValue(team) && len(pod.Spec.Volumes) == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
}

func TestSyncDeletesCapacityRequestsOnceServed(t *testing.T) {
	l, kube, boundary, control := fixture()
	boundary.reserveErr = ErrNoDedicatedNode
	if _, err := l.LaunchOne(t.Context()); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatal(err)
	}
	control.award.Binding.UnitID, control.award.Binding.NodeID = "unit-2", "job-2"
	if _, err := l.LaunchOne(t.Context()); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatal(err)
	}
	pods := capacityPods(t, kube)
	bound, waiting := pods[0], pods[1]
	bound.Spec.NodeName = "booting"
	if _, err := kube.CoreV1().Pods("cloud").Update(t.Context(), &bound, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "booting", UID: "booting-uid", Labels: map[string]string{TeamNodeLabel: TeamNodeValue("team")}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}}}
	if _, err := kube.CoreV1().Nodes().Create(t.Context(), node, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := l.Sync(t.Context()); err != nil || len(capacityPods(t, kube)) != 2 {
		t.Fatalf("a request was dropped before its node was Ready: %v", err)
	}
	node.Status.Conditions[0].Status = corev1.ConditionTrue
	if _, err := kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if left := capacityPods(t, kube); len(left) != 1 || left[0].Name != waiting.Name {
		t.Fatalf("served request was not removed: %+v", left)
	}
	waiting.Annotations[deadlineAnnotation] = time.Now().Add(-time.Second).Format(time.RFC3339Nano)
	if _, err := kube.CoreV1().Pods("cloud").Update(t.Context(), &waiting, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := l.Sync(t.Context()); err != nil || len(capacityPods(t, kube)) != 0 {
		t.Fatalf("a request outlived its award deadline: %v", err)
	}
}

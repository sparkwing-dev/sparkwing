package acceptance_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestLauncherPodRejectionCommitsFailedAwardW8(t *testing.T) {
	for name, mutate := range map[string]func(*corev1.Pod){
		"added_container": func(p *corev1.Pod) {
			p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "injected", Image: "other"})
		},
		"changed_image":          func(p *corev1.Pod) { p.Spec.Containers[0].Image = "other" },
		"removed_security_field": func(p *corev1.Pod) { p.Spec.InitContainers[0].SecurityContext.Privileged = nil },
		"extra_hostPath": func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			runtime, kube, server, resources, _ := reservationFixtureW8(t)
			resources.Job.Spec.Suspend = new(bool)
			job, err := kube.BatchV1().Jobs("cloud").Create(t.Context(), resources.Job, metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			yes := true
			pod := &corev1.Pod{ObjectMeta: *job.Spec.Template.ObjectMeta.DeepCopy(), Spec: *job.Spec.Template.Spec.DeepCopy(), Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}
			pod.Name, pod.Namespace = "work", "cloud"
			pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: &yes, BlockOwnerDeletion: &yes}}
			mutate(pod)
			if _, err = kube.CoreV1().Pods("cloud").Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			if err = runtime.Sync(t.Context()); err == nil {
				t.Fatal("Pod mutation accepted")
			}
			if _, err = kube.BatchV1().Jobs("cloud").Get(t.Context(), job.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatalf("Job survived: %v", err)
			}
			tenant, err := server.Store.ForTeam(t.Context(), "default")
			if err != nil {
				t.Fatal(err)
			}
			node, err := tenant.ReadNode(t.Context(), store.NodeRef{RunID: "run", NodeID: "job"})
			if err != nil || node.Outcome != store.NodeFailed || node.ClaimedBy != "" {
				t.Fatalf("award survived rejection: %+v %v", node, err)
			}
		})
	}
}

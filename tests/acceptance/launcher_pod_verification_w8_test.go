package acceptance_test

import (
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// hack: the fake tracker has no finalizers or garbage collection; the API server keeps
// a deleted Job until its last finalizer is removed and collects its Pods first.
func retainFinalizedJobsW8(kube *fake.Clientset) {
	jobs, pods := batchv1.SchemeGroupVersion.WithResource("jobs"), corev1.SchemeGroupVersion.WithResource("pods")
	tracker := kube.Tracker()
	kube.PrependReactor("delete", "jobs", func(action ktesting.Action) (bool, k8sruntime.Object, error) {
		obj, err := tracker.Get(jobs, action.GetNamespace(), action.(ktesting.DeleteAction).GetName())
		if apierrors.IsNotFound(err) {
			return false, nil, nil
		}
		if err != nil {
			return true, nil, err
		}
		if len(obj.(*batchv1.Job).Finalizers) == 0 {
			return false, nil, nil
		}
		job := obj.(*batchv1.Job).DeepCopy()
		if job.DeletionTimestamp != nil {
			return true, nil, nil
		}
		job.DeletionTimestamp = &metav1.Time{Time: time.Now()}
		owned, err := tracker.List(pods, corev1.SchemeGroupVersion.WithKind("Pod"), job.Namespace)
		if err != nil {
			return true, nil, err
		}
		for _, pod := range owned.(*corev1.PodList).Items {
			if owner := metav1.GetControllerOf(&pod); owner != nil && owner.UID == job.UID {
				if err := tracker.Delete(pods, pod.Namespace, pod.Name); err != nil {
					return true, nil, err
				}
			}
		}
		return true, nil, tracker.Update(jobs, job, job.Namespace)
	})
	kube.PrependReactor("patch", "jobs", func(action ktesting.Action) (bool, k8sruntime.Object, error) {
		handled, obj, err := ktesting.ObjectReaction(tracker)(action)
		if job, ok := obj.(*batchv1.Job); err == nil && ok && job.DeletionTimestamp != nil && len(job.Finalizers) == 0 {
			return true, job, tracker.Delete(jobs, job.Namespace, job.Name)
		}
		return handled, obj, err
	})
}

func TestLauncherPodRejectionCommitsFailedAwardW8(t *testing.T) {
	for name, mutate := range map[string]func(*corev1.Pod){
		"added_container": func(p *corev1.Pod) {
			p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "injected", Image: "other"})
		},
		"changed_image": func(p *corev1.Pod) { p.Spec.Containers[0].Image = "other" },
		"changed_size":  func(p *corev1.Pod) { p.Spec.Resources.Limits[corev1.ResourceMemory] = resource.MustParse("1Mi") },
		"other_team":    func(p *corev1.Pod) { p.Labels[launcher.TeamLabel] = launcher.TeamNodeValue("cloud", "other-team") },
	} {
		t.Run(name, func(t *testing.T) {
			runtime, kube, server, resources, _ := reservationFixtureW8(t)
			retainFinalizedJobsW8(kube)
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
			physical, err := kube.CoreV1().Nodes().Get(t.Context(), "dedicated", metav1.GetOptions{})
			if err != nil || !physical.Spec.Unschedulable {
				t.Fatalf("rejected node not cordoned: %v", err)
			}
			boundary := runtime.Boundary.(*launcher.KubernetesBoundary)
			if _, err = boundary.Karpenter.Resource(schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodeclaims"}).Get(t.Context(), "claim", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatalf("rejected NodeClaim survived: %v", err)
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

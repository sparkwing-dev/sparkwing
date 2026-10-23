package launcher

import (
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
)

func requestedPod(t *testing.T) (*Launcher, *corev1.Pod) {
	t.Helper()
	l, _, boundary, control := fixture()
	r, err := BuildJob(t.Context(), time.Now(), l.Config, *control.award, boundary)
	if err != nil {
		t.Fatal(err)
	}
	p := &corev1.Pod{ObjectMeta: *r.Job.Spec.Template.ObjectMeta.DeepCopy(), Spec: *r.Job.Spec.Template.Spec.DeepCopy()}
	p.Name, p.Namespace, p.UID = "job-pod", "cloud", "pod-uid"
	p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: r.Job.Name, UID: "job-uid", Controller: ptr(true), BlockOwnerDeletion: ptr(true)}}
	return l, p
}

func awardMutations() map[string]func(*corev1.Pod) {
	return map[string]func(*corev1.Pod){
		"image": func(p *corev1.Pod) { p.Spec.Containers[0].Image = "registry/other@sha256:" + strings.Repeat("c", 64) },
		"sidecar_image": func(p *corev1.Pod) {
			p.Spec.InitContainers[0].Image = "registry/other@sha256:" + strings.Repeat("c", 64)
		},
		"pod_cpu":    func(p *corev1.Pod) { p.Spec.Resources.Limits[corev1.ResourceCPU] = resource.MustParse("500m") },
		"pod_memory": func(p *corev1.Pod) { p.Spec.Resources.Requests[corev1.ResourceMemory] = resource.MustParse("1Gi") },
		"scratch": func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Limits[corev1.ResourceEphemeralStorage] = resource.MustParse("1Gi")
		},
		"container_cpu": func(p *corev1.Pod) {
			p.Spec.InitContainers[1].Resources.Limits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}
		},
		"team_label":     func(p *corev1.Pod) { p.Labels[TeamLabel] = admission.TeamNodeHash("other-team") },
		"team_selector":  func(p *corev1.Pod) { p.Spec.NodeSelector[TeamNodeLabel] = admission.TeamNodeHash("other-team") },
		"node":           func(p *corev1.Pod) { p.Spec.NodeName = "other-node" },
		"container_name": func(p *corev1.Pod) { p.Spec.InitContainers[1].Name = "renamed" },
	}
}

func TestAwardMismatchDeletesJobAndFailsAward(t *testing.T) {
	for name, mutate := range awardMutations() {
		t.Run(name, func(t *testing.T) {
			l, p := requestedPod(t)
			kube := l.Kube
			control := l.Control.(*controlFake)
			if _, err := l.LaunchOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			mutate(p)
			if _, err := kube.CoreV1().Pods("cloud").Create(t.Context(), p, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			rejected := false
			l.Control = publicationControl{CloudControl: control, sync: func(obs []CloudJobObservation) ([]CloudJobState, error) {
				states := make([]CloudJobState, len(obs))
				for i, o := range obs {
					rejected = rejected || o.Rejected
					states[i] = CloudJobState{Binding: o.Binding, LeaseExpiresAt: time.Now().Add(time.Hour), Delete: o.Rejected}
				}
				return states, nil
			}}
			if err := l.Sync(t.Context()); err == nil {
				t.Fatal("mismatch accepted")
			}
			if !rejected {
				t.Fatal("award was not failed")
			}
			if l.Boundary.(*boundaryFake).retirements == 0 {
				t.Fatal("rejected node returned to the pool")
			}
			if !jobRemoved(t, kube, jobName(control.award.Binding)) {
				t.Fatal("Job survived")
			}
		})
	}
}

func TestBackstopAllowsEKSMutations(t *testing.T) {
	l, p := requestedPod(t)
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	eksMutate(p)
	if _, err := l.Kube.CoreV1().Pods("cloud").Create(t.Context(), p, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	job, err := l.Kube.BatchV1().Jobs("cloud").Get(t.Context(), p.OwnerReferences[0].Name, metav1.GetOptions{})
	if err != nil || job.DeletionTimestamp != nil || job.Annotations[rejectedAnnotation] != "" {
		t.Fatalf("EKS-mutated Job did not survive the backstop: %+v %v", job, err)
	}
}

func TestActivationRejectsMismatchedPod(t *testing.T) {
	for name, mutate := range awardMutations() {
		t.Run(name, func(t *testing.T) {
			l, p := requestedPod(t)
			kube := l.Kube.(*fake.Clientset)
			mutate(p)
			podOnRead(t, kube, p)
			rejected := false
			original := l.Control
			l.Control = publicationControl{CloudControl: original, sync: func(obs []CloudJobObservation) ([]CloudJobState, error) {
				for _, o := range obs {
					rejected = rejected || o.Rejected
				}
				return original.Sync(t.Context(), obs)
			}}
			if claimed, err := l.LaunchOne(t.Context()); !claimed || err == nil || !rejected {
				t.Fatalf("mismatched Pod activated: claimed=%v err=%v rejected=%v", claimed, err, rejected)
			}
			if !jobRemoved(t, kube, p.OwnerReferences[0].Name) {
				t.Fatal("Job survived")
			}
		})
	}
}

func TestActivationNeedsNoPodYet(t *testing.T) {
	l, _ := requestedPod(t)
	kube := l.Kube.(*fake.Clientset)
	kube.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{}, nil
	})
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	if l.Boundary.(*boundaryFake).retirements != 0 {
		t.Fatal("a Pod the Job controller has not created yet rejected the award")
	}
}

func TestSyncRejectsJobTemplateOffItsBinding(t *testing.T) {
	l, p := requestedPod(t)
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	job, err := l.Kube.BatchV1().Jobs("cloud").Get(t.Context(), p.OwnerReferences[0].Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	job.Spec.Template.Spec.Containers[0].Image = "registry/other@sha256:" + strings.Repeat("c", 64)
	p.Spec.Containers[0].Image = job.Spec.Template.Spec.Containers[0].Image
	if _, err = l.Kube.BatchV1().Jobs("cloud").Update(t.Context(), job, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = l.Kube.CoreV1().Pods("cloud").Create(t.Context(), p, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	restarted := &Launcher{Kube: l.Kube, Control: l.Control, Config: l.Config, Boundary: l.Boundary}
	if err = restarted.Sync(t.Context()); err == nil {
		t.Fatal("changed Job and Pod accepted")
	}
	if !jobRemoved(t, l.Kube, job.Name) {
		t.Fatal("changed Job survived")
	}
}

func jobRemoved(t *testing.T, kube kubernetes.Interface, name string) bool {
	t.Helper()
	job, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true
	}
	if err != nil {
		t.Fatal(err)
	}
	return job.DeletionTimestamp != nil
}

func podOnRead(t *testing.T, kube *fake.Clientset, pod *corev1.Pod) {
	t.Helper()
	kube.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{Items: []corev1.Pod{*pod}}, nil
	})
}

func TestPodListFailureDoesNotRejectAward(t *testing.T) {
	l, kube, _, control := fixture()
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("temporary API failure")
	kube.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, failure })
	rejected := false
	l.Control = publicationControl{CloudControl: control, sync: func(obs []CloudJobObservation) ([]CloudJobState, error) {
		for _, o := range obs {
			rejected = rejected || o.Rejected
		}
		return control.Sync(t.Context(), obs)
	}}
	if err := l.Sync(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("lost API error: %v", err)
	}
	if rejected {
		t.Fatal("API error rejected the award")
	}
	if _, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(control.award.Binding), metav1.GetOptions{}); err != nil {
		t.Fatalf("API error deleted Job: %v", err)
	}
}

func TestMalformedJobIsDeletedWithoutBlockingNextLaunch(t *testing.T) {
	for _, annotation := range []string{bindingAnnotation, reservationAnnotation, deadlineAnnotation, bootstrapAnnotation} {
		t.Run(annotation, func(t *testing.T) {
			l, kube, _, control := fixture()
			if _, err := l.LaunchOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			job, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(control.award.Binding), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			job.Annotations[annotation] = "invalid"
			if _, err = kube.BatchV1().Jobs("cloud").Update(t.Context(), job, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			control.remove = true
			if err = l.Sync(t.Context()); err != nil {
				t.Fatalf("malformed Job blocks claims: %v", err)
			}
			if _, err = kube.BatchV1().Jobs("cloud").Get(t.Context(), job.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatalf("malformed Job survived: %v", err)
			}
		})
	}
}

func TestRejectionCannotReleaseBeforeNodeRetirement(t *testing.T) {
	l, p := requestedPod(t)
	boundary := l.Boundary.(*boundaryFake)
	failure := errors.New("retirement unavailable")
	boundary.err = failure
	awardMutations()["image"](p)
	podOnRead(t, l.Kube.(*fake.Clientset), p)
	if _, err := l.LaunchOne(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("retirement failure lost: %v", err)
	}
	if boundary.retirements != 1 || boundary.releases != 0 {
		t.Fatalf("retirement failure released node: retirements=%d releases=%d", boundary.retirements, boundary.releases)
	}
	boundary.err = nil
	if err := l.Sync(t.Context()); !errors.Is(err, errContainersRunning) {
		t.Fatalf("persisted rejection released a node with a running Pod: %v", err)
	}
	if boundary.retirements != 2 || boundary.releases != 0 || !jobRemoved(t, l.Kube, p.OwnerReferences[0].Name) {
		t.Fatalf("retried rejection: retirements=%d releases=%d", boundary.retirements, boundary.releases)
	}
}

func TestActivationRetriesAPIErrorWithoutRejecting(t *testing.T) {
	l, _, boundary, control := fixture()
	kube := l.Kube.(*fake.Clientset)
	failure := errors.New("temporary Pod list failure")
	failed := true
	kube.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		if failed {
			return true, nil, failure
		}
		return false, nil, nil
	})
	if _, err := l.LaunchOne(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("API error lost: %v", err)
	}
	if boundary.retirements != 0 {
		t.Fatal("API error retired the node")
	}
	if _, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(control.award.Binding), metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
	failed = false
	if err := l.Sync(t.Context()); err != nil {
		t.Fatalf("reconciliation could not recover: %v", err)
	}
}

func TestUpgradedImagesFailUnstartedJobWithoutRetiringItsNode(t *testing.T) {
	for name, started := range map[string]bool{"unstarted": false, "in_flight": true} {
		t.Run(name, func(t *testing.T) {
			l, kube, boundary, control := fixture()
			if _, err := l.LaunchOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !started {
				kube.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, &corev1.PodList{}, nil
				})
			}
			l.Config.RunnerImage = "registry/upgraded@sha256:" + strings.Repeat("d", 64)
			rejected := false
			l.Control = publicationControl{CloudControl: control, sync: func(obs []CloudJobObservation) ([]CloudJobState, error) {
				for _, o := range obs {
					rejected = rejected || o.Rejected
				}
				return control.Sync(t.Context(), obs)
			}}
			err := l.Sync(t.Context())
			if started {
				if err != nil || rejected || jobRemoved(t, kube, jobName(control.award.Binding)) {
					t.Fatalf("a chart upgrade disturbed a running Job: %v rejected=%v", err, rejected)
				}
				return
			}
			if err == nil || !rejected || !jobRemoved(t, kube, jobName(control.award.Binding)) {
				t.Fatalf("a Job admission can no longer start kept its award: %v rejected=%v", err, rejected)
			}
			job, getErr := kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(control.award.Binding), metav1.GetOptions{})
			if getErr == nil && job.Annotations[rejectedAnnotation] != rejectedUnadmittable {
				t.Fatalf("rejection reason = %q", job.Annotations[rejectedAnnotation])
			}
			if boundary.retirements != 0 {
				t.Fatal("a node that never ran the Job was retired")
			}
		})
	}
}

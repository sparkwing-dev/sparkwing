package launcher

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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

func podMutations() map[string]func(*corev1.Pod) {
	return map[string]func(*corev1.Pod){
		"added_container": func(p *corev1.Pod) {
			p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "injected", Image: "other"})
		},
		"changed_image":          func(p *corev1.Pod) { p.Spec.Containers[0].Image = "other" },
		"removed_security_field": func(p *corev1.Pod) { p.Spec.InitContainers[0].SecurityContext.Privileged = nil },
		"extra_hostPath": func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}})
		},
		"removed_job_label": func(p *corev1.Pod) { delete(p.Labels, JobLabel) },
		"host_network":      func(p *corev1.Pod) { p.Spec.HostNetwork = true },
		"new_env": func(p *corev1.Pod) {
			p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "AWS_EVIL", Value: "yes"})
		},
		"security_context": func(p *corev1.Pod) { p.Spec.SecurityContext = nil },
		"annotation":       func(p *corev1.Pod) { p.Annotations["unknown.example/mutated"] = "true" },
		"readiness": func(p *corev1.Pod) {
			p.Spec.ReadinessGates = append(p.Spec.ReadinessGates, corev1.PodReadinessGate{ConditionType: "unknown/gate"})
		},
	}
}

func TestPodMutationDeletesJobAndFailsAward(t *testing.T) {
	for name, mutate := range podMutations() {
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
					states[i] = CloudJobState{Binding: o.Binding, Delete: o.Rejected}
				}
				return states, nil
			}}
			if err := l.Sync(t.Context()); err == nil {
				t.Fatal("mutation accepted")
			}
			if !rejected {
				t.Fatal("award was not failed")
			}
			if l.Boundary.(*boundaryFake).retirements != 1 {
				t.Fatal("rejected node returned to the pool")
			}
			if _, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(control.award.Binding), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatalf("Job survived: %v", err)
			}
		})
	}
}

func eksMutations() map[string]func(*corev1.Pod) {
	return map[string]func(*corev1.Pod){
		"region": func(p *corev1.Pod) {
			p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "AWS_REGION", Value: "us-west-2"}, corev1.EnvVar{Name: "AWS_DEFAULT_REGION", Value: "us-west-2"}, corev1.EnvVar{Name: "AWS_STS_REGIONAL_ENDPOINTS", Value: "regional"})
		},
		"vpc_resource_requests": func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Requests["vpc.amazonaws.com/pod-eni"] = resource.MustParse("1")
			p.Spec.Containers[0].Resources.Limits["vpc.amazonaws.com/pod-eni"] = resource.MustParse("1")
		},
		"vpc_annotations": func(p *corev1.Pod) {
			p.Annotations["vpc.amazonaws.com/pod-eni"] = `[{"eniId":"eni-12345678901234567","ifAddress":"02:00:00:00:00:01","privateIp":"10.0.1.2","vlanId":1,"subnetCidr":"10.0.0.0/16"}]`
		},
		"load_balancer": func(p *corev1.Pod) {
			p.Spec.ReadinessGates = append(p.Spec.ReadinessGates, corev1.PodReadinessGate{ConditionType: "target-health.elbv2.k8s.aws/k8s-ci-job-123"})
		},
	}
}

func credentialMutations() map[string]func(*corev1.Pod) {
	return map[string]func(*corev1.Pod){
		"pod_identity": func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "eks-pod-identity-token", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{DefaultMode: ptr(int32(420)), Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Audience: "pods.eks.amazonaws.com", ExpirationSeconds: ptr(int64(86400)), Path: "eks-pod-identity-token"}}}}}})
			for _, cs := range [][]corev1.Container{p.Spec.Containers, p.Spec.InitContainers} {
				for i := range cs {
					cs[i].Env = append(cs[i].Env, corev1.EnvVar{Name: "AWS_CONTAINER_CREDENTIALS_FULL_URI", Value: "http://169.254.170.23/v1/credentials"}, corev1.EnvVar{Name: "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE", Value: "/var/run/secrets/pods.eks.amazonaws.com/serviceaccount/eks-pod-identity-token"})
					cs[i].VolumeMounts = append(cs[i].VolumeMounts, corev1.VolumeMount{Name: "eks-pod-identity-token", MountPath: "/var/run/secrets/pods.eks.amazonaws.com/serviceaccount", ReadOnly: true})
				}
			}
		},
		"irsa": func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "aws-iam-token", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{DefaultMode: ptr(int32(420)), Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Audience: "sts.amazonaws.com", ExpirationSeconds: ptr(int64(86400)), Path: "token"}}}}}})
			for _, cs := range [][]corev1.Container{p.Spec.Containers, p.Spec.InitContainers} {
				for i := range cs {
					cs[i].Env = append(cs[i].Env, corev1.EnvVar{Name: "AWS_ROLE_ARN", Value: "arn:aws:iam::123456789012:role/jobs"}, corev1.EnvVar{Name: "AWS_WEB_IDENTITY_TOKEN_FILE", Value: "/var/run/secrets/eks.amazonaws.com/serviceaccount/token"})
					cs[i].VolumeMounts = append(cs[i].VolumeMounts, corev1.VolumeMount{Name: "aws-iam-token", MountPath: "/var/run/secrets/eks.amazonaws.com/serviceaccount", ReadOnly: true})
				}
			}
		},
	}
}

func TestPodRejectsAWSCredentials(t *testing.T) {
	for name, mutate := range credentialMutations() {
		t.Run(name, func(t *testing.T) {
			l, p := requestedPod(t)
			r, err := BuildJob(t.Context(), time.Now(), l.Config, *l.Control.(*controlFake).award, l.Boundary)
			if err != nil {
				t.Fatal(err)
			}
			r.Job.UID = "job-uid"
			mutate(p)
			if validateJobPod(r.Job, p) == nil {
				t.Fatal("customer Pod accepted AWS credentials")
			}
		})
	}
}

func TestPodAllowsEachEKSMutation(t *testing.T) {
	for name, mutate := range eksMutations() {
		t.Run(name, func(t *testing.T) {
			l, p := requestedPod(t)
			kube := l.Kube
			if _, err := l.LaunchOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			mutate(p)
			if _, err := kube.CoreV1().Pods("cloud").Create(t.Context(), p, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := l.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func FuzzPodRejectsUnexpectedMutation(f *testing.F) {
	f.Add(uint8(0), "mutated")
	f.Add(uint8(1), "other")
	f.Add(uint8(2), "extra")
	f.Fuzz(func(t *testing.T, selector uint8, value string) {
		l, p := requestedPod(t)
		r, err := BuildJob(context.Background(), time.Now(), l.Config, *l.Control.(*controlFake).award, l.Boundary)
		if err != nil {
			t.Fatal(err)
		}
		mutations := []func(*corev1.Pod){
			func(p *corev1.Pod) { p.Spec.Containers[0].Image += "x" + value },
			func(p *corev1.Pod) { p.Spec.InitContainers[0].SecurityContext.Privileged = nil },
			func(p *corev1.Pod) {
				p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/" + value}}})
			},
			func(p *corev1.Pod) {
				p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "UNEXPECTED_" + strings.ToUpper(value), Value: value})
			},
		}
		for _, mutate := range eksMutations() {
			mutate(p)
		}
		mutations[int(selector)%len(mutations)](p)
		r.Job.UID = "job-uid"
		if validateJobPod(r.Job, p) == nil {
			t.Fatal("unexpected mutation accepted alongside EKS injection")
		}
	})
}

func TestLaunchVerifiesPodAfterJobActivation(t *testing.T) {
	for name, mutate := range podMutations() {
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
				t.Fatalf("unexpected Pod activated: claimed=%v err=%v rejected=%v", claimed, err, rejected)
			}
			if _, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), p.OwnerReferences[0].Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatalf("Job survived: %v", err)
			}
		})
	}
}

func TestEKSAllowListRejectsAlteredInjectionShapes(t *testing.T) {
	for name, mutate := range map[string]func(*corev1.Pod){
		"hostPath_with_token_name": func(p *corev1.Pod) {
			p.Spec.Volumes[len(p.Spec.Volumes)-1].VolumeSource = corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}
		},
		"extra_token_projection": func(p *corev1.Pod) {
			p.Spec.Volumes[len(p.Spec.Volumes)-1].Projected.Sources = append(p.Spec.Volumes[len(p.Spec.Volumes)-1].Projected.Sources, corev1.VolumeProjection{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "other"}}})
		},
		"writable_token_mount": func(p *corev1.Pod) { cs := p.Spec.Containers[0].VolumeMounts; cs[len(cs)-1].ReadOnly = false },
		"credential_URI": func(p *corev1.Pod) {
			for i := range p.Spec.Containers[0].Env {
				if p.Spec.Containers[0].Env[i].Name == "AWS_CONTAINER_CREDENTIALS_FULL_URI" {
					p.Spec.Containers[0].Env[i].Value = "http://other/credentials"
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			l, p := requestedPod(t)
			r, err := BuildJob(t.Context(), time.Now(), l.Config, *l.Control.(*controlFake).award, l.Boundary)
			if err != nil {
				t.Fatal(err)
			}
			r.Job.UID = "job-uid"
			credentialMutations()["pod_identity"](p)
			mutate(p)
			if validateJobPod(r.Job, p) == nil {
				t.Fatal("malformed EKS injection accepted")
			}
		})
	}
}

func TestRestartComparesOriginalRequestedTemplate(t *testing.T) {
	l, p := requestedPod(t)
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	job, err := l.Kube.BatchV1().Jobs("cloud").Get(t.Context(), p.OwnerReferences[0].Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	job.Spec.Template.Spec.Containers[0].Image = "other"
	p.Spec.Containers[0].Image = "other"
	if _, err = l.Kube.BatchV1().Jobs("cloud").Update(t.Context(), job, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = l.Kube.CoreV1().Pods("cloud").Create(t.Context(), p, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	restarted := &Launcher{Kube: l.Kube, Control: l.Control, Config: l.Config, Boundary: l.Boundary}
	if err = restarted.Sync(t.Context()); err == nil {
		t.Fatal("changed Job and Pod accepted as the original request")
	}
	if _, err = l.Kube.BatchV1().Jobs("cloud").Get(t.Context(), job.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("changed Job survived: %v", err)
	}
}

func podOnRead(t *testing.T, kube *fake.Clientset, pod *corev1.Pod) {
	t.Helper()
	kube.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{Items: []corev1.Pod{*pod}}, nil
	})
}

func TestLaunchAllowsEKSMutationsAfterJobActivation(t *testing.T) {
	mutations := eksMutations()
	mutations["combined"] = func(p *corev1.Pod) {
		for _, mutate := range eksMutations() {
			mutate(p)
		}
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			l, p := requestedPod(t)
			mutate(p)
			podOnRead(t, l.Kube.(*fake.Clientset), p)
			if claimed, err := l.LaunchOne(t.Context()); !claimed || err != nil {
				t.Fatalf("EKS Pod refused: claimed=%v err=%v", claimed, err)
			}
			if err := l.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			job, err := l.Kube.BatchV1().Jobs(p.Namespace).Get(t.Context(), p.OwnerReferences[0].Name, metav1.GetOptions{})
			if err != nil || job.DeletionTimestamp != nil || job.Annotations[rejectedAnnotation] != "" {
				t.Fatalf("EKS Job did not survive verification: %+v %v", job, err)
			}
		})
	}
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

func TestActivationWaitsForOwnedPod(t *testing.T) {
	l, p := requestedPod(t)
	kube := l.Kube.(*fake.Clientset)
	lists := 0
	kube.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		lists++
		pods := &corev1.PodList{}
		if lists > 1 {
			pods.Items = []corev1.Pod{*p}
		}
		return true, pods, nil
	})
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	if lists < 2 {
		t.Fatalf("empty activation check succeeded after %d lists", lists)
	}
}

func TestMalformedJobIsDeletedWithoutBlockingNextLaunch(t *testing.T) {
	for _, annotation := range []string{bindingAnnotation, reservationAnnotation, templateAnnotation, deadlineAnnotation, bootstrapAnnotation} {
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

func TestPodUsesAPIDefaultedPriority(t *testing.T) {
	for _, priority := range []int32{1000, -1000} {
		for _, mutate := range []bool{false, true} {
			l, p := requestedPod(t)
			class := &schedulingv1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: "default-priority"}, Value: priority, GlobalDefault: true, PreemptionPolicy: ptr(corev1.PreemptNever)}
			if _, err := l.Kube.SchedulingV1().PriorityClasses().Create(t.Context(), class, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			p.Spec.PriorityClassName, p.Spec.Priority, p.Spec.PreemptionPolicy = class.Name, ptr(priority), class.PreemptionPolicy
			apiDefaultedPod(p)
			if mutate {
				p.Spec.Priority = ptr(priority + 1)
			}
			podOnRead(t, l.Kube.(*fake.Clientset), p)
			_, err := l.LaunchOne(t.Context())
			if (err != nil) != mutate {
				t.Fatalf("priority=%d mutate=%v err=%v", priority, mutate, err)
			}
		}
	}
}

func TestPodRejectsMalformedENIAnnotation(t *testing.T) {
	for _, value := range []string{"", "true", "[]", "[{}]", `[{"eniId":"eni-12345678901234567","ifAddress":"bad","privateIp":"10.0.1.2","vlanId":1,"subnetCidr":"10.0.0.0/16"}]`, `[{"eniId":"eni-12345678901234567","ifAddress":"02:00:00:00:00:01","privateIp":"127.0.0.1","vlanId":1,"subnetCidr":"10.0.0.0/16"}]`} {
		l, p := requestedPod(t)
		r, err := BuildJob(t.Context(), time.Now(), l.Config, *l.Control.(*controlFake).award, l.Boundary)
		if err != nil {
			t.Fatal(err)
		}
		r.Job.UID = "job-uid"
		p.Annotations["vpc.amazonaws.com/pod-eni"] = value
		if validateJobPod(r.Job, p) == nil {
			t.Fatalf("malformed ENI annotation accepted: %q", value)
		}
	}
}

func FuzzPodNeverAcceptsAWSCredentialInjection(f *testing.F) {
	for i := uint8(0); i < 6; i++ {
		f.Add(i, "credential")
	}
	f.Fuzz(func(t *testing.T, mode uint8, value string) {
		l, p := requestedPod(t)
		r, err := BuildJob(t.Context(), time.Now(), l.Config, *l.Control.(*controlFake).award, l.Boundary)
		if err != nil {
			t.Fatal(err)
		}
		r.Job.UID = "job-uid"
		switch mode % 6 {
		case 0:
			p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "AWS_ROLE_ARN", Value: "arn:aws:iam::123456789012:role/" + value})
		case 1:
			p.Spec.InitContainers[0].Env = append(p.Spec.InitContainers[0].Env, corev1.EnvVar{Name: "AWS_WEB_IDENTITY_TOKEN_FILE", Value: value})
		case 2:
			p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "AWS_CONTAINER_CREDENTIALS_FULL_URI", Value: value})
		case 3:
			p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE", Value: value})
		case 4:
			credentialMutations()["irsa"](p)
		case 5:
			credentialMutations()["pod_identity"](p)
		}
		if validateJobPod(r.Job, p) == nil {
			t.Fatal("AWS credentials accepted")
		}
	})
}

func TestRejectionCannotReleaseBeforeNodeRetirement(t *testing.T) {
	l, p := requestedPod(t)
	boundary := l.Boundary.(*boundaryFake)
	failure := errors.New("retirement unavailable")
	boundary.err = failure
	podMutations()["changed_image"](p)
	podOnRead(t, l.Kube.(*fake.Clientset), p)
	if _, err := l.LaunchOne(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("retirement failure lost: %v", err)
	}
	if boundary.retirements != 1 || boundary.releases != 0 {
		t.Fatalf("retirement failure released node: retirements=%d releases=%d", boundary.retirements, boundary.releases)
	}
}

func TestActivationRequiresExactlyOneOwnedPod(t *testing.T) {
	for _, count := range []int{0, 2} {
		synctest.Test(t, func(t *testing.T) {
			l, p := requestedPod(t)
			kube := l.Kube.(*fake.Clientset)
			kube.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
				pods := &corev1.PodList{}
				for i := 0; i < count; i++ {
					copy := p.DeepCopy()
					copy.Name += string(rune('a' + i))
					pods.Items = append(pods.Items, *copy)
				}
				return true, pods, nil
			})
			if _, err := l.LaunchOne(t.Context()); err == nil {
				t.Fatalf("activation accepted %d owned Pods", count)
			}
			if l.Boundary.(*boundaryFake).retirements != 1 {
				t.Fatal("rejected Pod count did not retire node")
			}
			if _, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), p.OwnerReferences[0].Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatalf("rejected Job survived: %v", err)
			}
		})
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

func apiDefaultedPod(p *corev1.Pod) {
	p.Spec.DNSPolicy = corev1.DNSClusterFirst
	p.Spec.SchedulerName = "default-scheduler"
	p.Spec.TerminationGracePeriodSeconds = ptr(int64(30))
	p.Spec.DeprecatedServiceAccount = p.Spec.ServiceAccountName
	p.Spec.Tolerations = append(p.Spec.Tolerations,
		corev1.Toleration{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: ptr(int64(300))},
		corev1.Toleration{Key: "node.kubernetes.io/unreachable", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: ptr(int64(300))})
	for _, containers := range [][]corev1.Container{p.Spec.Containers, p.Spec.InitContainers} {
		for i := range containers {
			c := &containers[i]
			c.TerminationMessagePath = "/dev/termination-log"
			c.TerminationMessagePolicy = corev1.TerminationMessageReadFile
			if c.StartupProbe != nil {
				c.StartupProbe.TimeoutSeconds = 1
				c.StartupProbe.SuccessThreshold = 1
			}
		}
	}
}

func TestThrottledControllerDoesNotPauseLocalPodVerification(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, p := requestedPod(t)
		control := l.Control.(*controlFake)
		if _, err := l.LaunchOne(t.Context()); err != nil {
			t.Fatal(err)
		}
		control.award = nil
		control.fail = &apierr.Error{Code: apierr.RateLimited, Message: "poll later", Details: map[string]any{"retry_after_ms": float64(60000)}}
		l.Poll = 5 * time.Second
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- l.Run(ctx) }()
		synctest.Wait()
		p.Spec.Containers[0].Image = "other"
		if _, err := l.Kube.CoreV1().Pods("cloud").Create(t.Context(), p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(6 * time.Second)
		synctest.Wait()
		retired := l.Boundary.(*boundaryFake).retirements
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if retired != 1 {
			t.Fatalf("a mutated Pod went unverified for a poll while the controller asked for 60s: retirements=%d", retired)
		}
	})
}

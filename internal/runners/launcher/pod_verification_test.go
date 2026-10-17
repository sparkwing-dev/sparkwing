package launcher

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
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
			if _, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(control.award.Binding), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatalf("Job survived: %v", err)
			}
		})
	}
}

func eksMutations() map[string]func(*corev1.Pod) {
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
		"region": func(p *corev1.Pod) {
			p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "AWS_REGION", Value: "us-west-2"}, corev1.EnvVar{Name: "AWS_DEFAULT_REGION", Value: "us-west-2"}, corev1.EnvVar{Name: "AWS_STS_REGIONAL_ENDPOINTS", Value: "regional"})
		},
		"vpc_resource_requests": func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Requests["vpc.amazonaws.com/pod-eni"] = resource.MustParse("1")
			p.Spec.Containers[0].Resources.Limits["vpc.amazonaws.com/pod-eni"] = resource.MustParse("1")
		},
		"vpc_annotations": func(p *corev1.Pod) {
			p.Annotations["vpc.amazonaws.com/pod-eni"] = `[{"eniId":"eni-123","ifAddress":"02:00:00:00:00:01","privateIp":"10.0.1.2","vlanId":1,"subnetCidr":"10.0.0.0/16"}]`
		},
		"load_balancer": func(p *corev1.Pod) {
			p.Spec.ReadinessGates = append(p.Spec.ReadinessGates, corev1.PodReadinessGate{ConditionType: "target-health.elbv2.k8s.aws/k8s-ci-job-123"})
		},
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
			podOnActivation(t, kube, p)
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
			eksMutations()["pod_identity"](p)
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

func podOnActivation(t *testing.T, kube *fake.Clientset, pod *corev1.Pod) {
	t.Helper()
	kube.PrependReactor("patch", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		if bytes.Contains(action.(ktesting.PatchAction).GetPatch(), []byte(`"suspend":false`)) {
			if err := kube.Tracker().Create(corev1.SchemeGroupVersion.WithResource("pods"), pod, pod.Namespace); err != nil {
				t.Fatal(err)
			}
		}
		return false, nil, nil
	})
}

func TestLaunchAllowsEKSMutationsAfterJobActivation(t *testing.T) {
	mutations := eksMutations()
	mutations["combined"] = func(p *corev1.Pod) {
		for _, mutate := range eksMutations() {
			mutate(p)
		}
	}
	mutations["pod_identity_ipv6"] = func(p *corev1.Pod) {
		eksMutations()["pod_identity"](p)
		for _, cs := range [][]corev1.Container{p.Spec.Containers, p.Spec.InitContainers} {
			for i := range cs {
				for j := range cs[i].Env {
					if cs[i].Env[j].Name == "AWS_CONTAINER_CREDENTIALS_FULL_URI" {
						cs[i].Env[j].Value = "http://[fd00:ec2::23]/v1/credentials"
					}
				}
			}
		}
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			l, p := requestedPod(t)
			mutate(p)
			podOnActivation(t, l.Kube.(*fake.Clientset), p)
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

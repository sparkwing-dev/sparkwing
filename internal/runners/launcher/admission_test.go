package launcher

import (
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
)

func TestLauncherRefusesAdmissionChangesBeforeBootstrap(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*batchv1.Job)
	}{
		{"node", func(j *batchv1.Job) { j.Spec.Template.Spec.NodeName = "shared-node" }},
		{"disruption", func(j *batchv1.Job) { delete(j.Spec.Template.Annotations, doNotDisrupt) }},
		{"cpu_band", func(j *batchv1.Job) { j.Spec.Template.Spec.Tolerations = nil }},
		{"team", func(j *batchv1.Job) { j.Spec.Template.Spec.NodeSelector = nil }},
		{"service_account", func(j *batchv1.Job) { j.Spec.Template.Spec.ServiceAccountName = "controller" }},
		{"api_token", func(j *batchv1.Job) { j.Spec.Template.Spec.AutomountServiceAccountToken = ptr(true) }},
		{"host_network", func(j *batchv1.Job) { j.Spec.Template.Spec.HostNetwork = true }},
		{"host_pid", func(j *batchv1.Job) { j.Spec.Template.Spec.HostPID = true }},
		{"host_ipc", func(j *batchv1.Job) { j.Spec.Template.Spec.HostIPC = true }},
		{"host_volume", func(j *batchv1.Job) {
			j.Spec.Template.Spec.Volumes = append(j.Spec.Template.Spec.Volumes, corev1.Volume{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}})
		}},
		{"docker_bootstrap_mount", func(j *batchv1.Job) {
			j.Spec.Template.Spec.InitContainers[0].VolumeMounts = append(j.Spec.Template.Spec.InitContainers[0].VolumeMounts, corev1.VolumeMount{Name: "bootstrap", MountPath: "/bootstrap"})
		}},
		{"bootstrap_mode", func(j *batchv1.Job) { j.Spec.Template.Spec.Volumes[1].Secret.DefaultMode = ptr(int32(0o644)) }},
		{"bootstrap_reference", func(j *batchv1.Job) { j.Spec.Template.Spec.Volumes[1].Secret.SecretName = "foreign" }},
		{"extra_container", func(j *batchv1.Job) {
			j.Spec.Template.Spec.Containers = append(j.Spec.Template.Spec.Containers, corev1.Container{Name: "injected", Image: "other"})
		}},
		{"docker_security", func(j *batchv1.Job) {
			j.Spec.Template.Spec.InitContainers[0].SecurityContext.SELinuxOptions = &corev1.SELinuxOptions{Type: "super_t"}
		}},
		{"pod_security", func(j *batchv1.Job) { j.Spec.Template.Spec.SecurityContext.RunAsUser = ptr(int64(1000)) }},
		{"docker_sidecar", func(j *batchv1.Job) { j.Spec.Template.Spec.InitContainers[0].RestartPolicy = nil }},
		{"source", func(j *batchv1.Job) { j.Spec.Template.Spec.InitContainers = j.Spec.Template.Spec.InitContainers[:1] }},
		{"host_command", func(j *batchv1.Job) { j.Spec.Template.Spec.Containers[0].Command = []string{"other"} }},
		{"host_arguments", func(j *batchv1.Job) {
			j.Spec.Template.Spec.Containers[0].Args = append(j.Spec.Template.Spec.Containers[0].Args, "--run", "foreign")
		}},
		{"host_environment", func(j *batchv1.Job) {
			j.Spec.Template.Spec.Containers[0].Env = append(j.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: "UNEXPECTED_BEARER", Value: "private"})
		}},
		{"scratch", func(j *batchv1.Job) { j.Spec.Template.Spec.Volumes[0].EmptyDir.SizeLimit = nil }},
		{"budget", func(j *batchv1.Job) { j.Spec.Template.Spec.Resources = nil }},
		{"parallelism", func(j *batchv1.Job) { j.Spec.Parallelism = ptr(int32(2)) }},
		{"completions", func(j *batchv1.Job) { j.Spec.Completions = ptr(int32(2)) }},
		{"backoff", func(j *batchv1.Job) { j.Spec.BackoffLimit = ptr(int32(1)) }},
		{"oom_retry", func(j *batchv1.Job) {
			j.Spec.PodReplacementPolicy = ptr(batchv1.Failed)
			j.Spec.PodFailurePolicy = &batchv1.PodFailurePolicy{Rules: []batchv1.PodFailurePolicyRule{{Action: batchv1.PodFailurePolicyActionIgnore, OnExitCodes: &batchv1.PodFailurePolicyOnExitCodesRequirement{ContainerName: ptr("run-node"), Operator: batchv1.PodFailurePolicyOnExitCodesOpIn, Values: []int32{137}}}}}
		}},
		{"deadline", func(j *batchv1.Job) { j.Spec.ActiveDeadlineSeconds = nil }},
		{"binding", func(j *batchv1.Job) { delete(j.Annotations, bindingAnnotation) }},
		{"cleanup_reference", func(j *batchv1.Job) { j.Annotations[bootstrapAnnotation] = `{"name":"unrelated","uid":"secret-uid"}` }},
		{"finalizer", func(j *batchv1.Job) { j.Finalizers = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			l, kube, boundary, _ := fixture()
			kube.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
				test.mutate(action.(ktesting.CreateAction).GetObject().(*batchv1.Job))
				return false, nil, nil
			})
			if claimed, err := l.LaunchOne(t.Context()); !claimed || err == nil {
				t.Fatalf("changed Job accepted: claimed=%t err=%v", claimed, err)
			}
			for _, action := range kube.Actions() {
				if action.GetVerb() == "create" && action.GetResource().Resource == "secrets" {
					t.Fatal("bootstrap published for a changed Job")
				}
			}
			if boundary.releases != 1 {
				t.Fatalf("reservation releases=%d, want one", boundary.releases)
			}
		})
	}
}

func TestLauncherAcceptsKubernetesDefaults(t *testing.T) {
	l, kube, _, _ := fixture()
	kube.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		secret := action.(ktesting.CreateAction).GetObject().(*corev1.Secret)
		secret.UID, secret.ResourceVersion = "secret-uid", "3"
		secret.Annotations = map[string]string{"admission.example/checked": "true"}
		return false, nil, nil
	})
	kube.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
		job.Spec.Parallelism, job.Spec.Completions = ptr(int32(1)), ptr(int32(1))
		job.Spec.Template.Labels["batch.kubernetes.io/controller-uid"] = "uid"
		pod := &job.Spec.Template.Spec
		pod.DNSPolicy, pod.SchedulerName = corev1.DNSClusterFirst, corev1.DefaultSchedulerName
		pod.TerminationGracePeriodSeconds = ptr(int64(30))
		pod.DeprecatedServiceAccount = pod.ServiceAccountName
		for _, containers := range [][]corev1.Container{pod.Containers, pod.InitContainers} {
			for i := range containers {
				containers[i].TerminationMessagePath = corev1.TerminationMessagePathDefault
				containers[i].TerminationMessagePolicy = corev1.TerminationMessageReadFile
				if probe := containers[i].StartupProbe; probe != nil {
					probe.TimeoutSeconds, probe.SuccessThreshold = 1, 1
				}
			}
		}
		return false, nil, nil
	})
	if claimed, err := l.LaunchOne(t.Context()); !claimed || err != nil {
		t.Fatalf("defaulted Job rejected: claimed=%t err=%v", claimed, err)
	}
}

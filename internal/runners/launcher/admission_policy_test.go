package launcher

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	testLauncher      = "system:serviceaccount:sparkwing:sparkwing-launcher"
	testJobController = "system:serviceaccount:kube-system:job-controller"
	testDockerImage   = "registry/docker@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type admissionEnv struct {
	admin, launcher, jobs, kcm, other kubernetes.Interface
	l                                 *Launcher
	resources                         CloudJobResources
	award                             CloudJobAward
}

func newAdmissionEnv(t *testing.T) *admissionEnv {
	t.Helper()
	cfg := apiServer(t)
	l, _, boundary, control := fixture()
	l.Config.DockerImage = testDockerImage
	e := &admissionEnv{admin: impersonate(t, cfg, "admin"), launcher: impersonate(t, cfg, testLauncher), jobs: impersonate(t, cfg, testJobController), kcm: impersonate(t, cfg, "system:kube-controller-manager"), other: impersonate(t, cfg, "system:serviceaccount:sparkwing:intruder"), l: l, award: *control.award}
	e.award.Binding.OwnerDeadline = time.Now().Add(time.Hour)
	e.award.Binding.ParentCapabilityExpiry = time.Now().Add(2 * time.Hour)
	ctx := t.Context()
	if _, err := e.admin.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cloud"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, account := range []string{ServiceAccount, "default"} {
		if _, err := e.admin.CoreV1().ServiceAccounts("cloud").Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: account}, AutomountServiceAccountToken: ptr(false)}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	policies, bindings := AdmissionPolicies(AdmissionConfig{Namespace: "cloud", Launcher: testLauncher, RunnerImage: l.Config.RunnerImage, DockerImage: testDockerImage})
	for i := range policies {
		if _, err := e.admin.AdmissionregistrationV1().ValidatingAdmissionPolicies().Create(ctx, policies[i], metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.admin.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Create(ctx, bindings[i], metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	var last error
	dry := metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}
	err := wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		_, pod := e.admin.CoreV1().Pods("cloud").Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unexpected"}, Spec: corev1.PodSpec{ServiceAccountName: ServiceAccount, Containers: []corev1.Container{{Name: "c", Image: "busybox"}}}}, dry)
		_, job := e.admin.BatchV1().Jobs("cloud").Create(ctx, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "unexpected"}, Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{ServiceAccountName: ServiceAccount, RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "c", Image: "busybox"}}}}}}, dry)
		last = errors.Join(pod, job)
		return pod != nil && job != nil && strings.Contains(pod.Error(), "denied request") && strings.Contains(job.Error(), "denied request"), nil
	})
	if err != nil {
		t.Fatalf("policies never enforced: %v; last response %v", err, last)
	}
	e.resources, err = BuildJob(ctx, time.Now(), l.Config, e.award, boundary)
	if err != nil {
		t.Fatal(err)
	}
	e.resources.Job.Spec.Suspend = ptr(true)
	return e
}

func (e *admissionEnv) job(t *testing.T) *batchv1.Job {
	t.Helper()
	job, err := e.launcher.BatchV1().Jobs("cloud").Create(t.Context(), e.resources.Job.DeepCopy(), metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func jobPod(job *batchv1.Job) *corev1.Pod {
	pod := &corev1.Pod{ObjectMeta: *job.Spec.Template.ObjectMeta.DeepCopy(), Spec: *job.Spec.Template.Spec.DeepCopy()}
	pod.GenerateName, pod.Namespace = job.Name+"-", job.Namespace
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr(true), BlockOwnerDeletion: ptr(true)}}
	return pod
}

func eksMutate(p *corev1.Pod) {
	main := &p.Spec.Containers[0]
	main.Env = append(main.Env, corev1.EnvVar{Name: "AWS_REGION", Value: "us-west-2"}, corev1.EnvVar{Name: "AWS_DEFAULT_REGION", Value: "us-west-2"}, corev1.EnvVar{Name: "AWS_STS_REGIONAL_ENDPOINTS", Value: "regional"})
	main.Resources.Requests["vpc.amazonaws.com/pod-eni"] = resource.MustParse("1")
	main.Resources.Limits["vpc.amazonaws.com/pod-eni"] = resource.MustParse("1")
	if p.Annotations == nil {
		p.Annotations = map[string]string{}
	}
	p.Annotations["vpc.amazonaws.com/pod-eni"] = `[{"eniId":"eni-12345678901234567","ifAddress":"02:00:00:00:00:01","privateIp":"10.0.1.2","vlanId":1,"subnetCidr":"10.0.0.0/16"}]`
	p.Spec.ReadinessGates = append(p.Spec.ReadinessGates, corev1.PodReadinessGate{ConditionType: "target-health.elbv2.k8s.aws/k8s-ci-job-123"})
}

func irsa(p *corev1.Pod) {
	p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "aws-iam-token", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Audience: "sts.amazonaws.com", ExpirationSeconds: ptr(int64(86400)), Path: "token"}}}}}})
	for _, cs := range [][]corev1.Container{p.Spec.Containers, p.Spec.InitContainers} {
		for i := range cs {
			cs[i].Env = append(cs[i].Env, corev1.EnvVar{Name: "AWS_ROLE_ARN", Value: "arn:aws:iam::123456789012:role/jobs"}, corev1.EnvVar{Name: "AWS_WEB_IDENTITY_TOKEN_FILE", Value: "/var/run/secrets/eks.amazonaws.com/serviceaccount/token"})
			cs[i].VolumeMounts = append(cs[i].VolumeMounts, corev1.VolumeMount{Name: "aws-iam-token", MountPath: "/var/run/secrets/eks.amazonaws.com/serviceaccount", ReadOnly: true})
		}
	}
}

func podIdentity(p *corev1.Pod) {
	p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "eks-pod-identity-token", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Audience: "pods.eks.amazonaws.com", ExpirationSeconds: ptr(int64(86400)), Path: "eks-pod-identity-token"}}}}}})
	main := &p.Spec.Containers[0]
	main.Env = append(main.Env, corev1.EnvVar{Name: "AWS_CONTAINER_CREDENTIALS_FULL_URI", Value: "http://169.254.170.23/v1/credentials"}, corev1.EnvVar{Name: "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE", Value: "/var/run/secrets/pods.eks.amazonaws.com/serviceaccount/eks-pod-identity-token"})
	main.VolumeMounts = append(main.VolumeMounts, corev1.VolumeMount{Name: "eks-pod-identity-token", MountPath: "/var/run/secrets/pods.eks.amazonaws.com/serviceaccount", ReadOnly: true})
}

func refused(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("admitted; want denial %q", want)
	}
	if !strings.Contains(err.Error(), "denied request") || !strings.Contains(err.Error(), want) {
		t.Fatalf("denied for another reason, want %q: %v", want, err)
	}
}

func TestAdmissionPolicyOnAPIServer(t *testing.T) {
	e := newAdmissionEnv(t)
	ctx := t.Context()
	pods := func(kube kubernetes.Interface) func(*corev1.Pod) error {
		return func(p *corev1.Pod) error {
			_, err := kube.CoreV1().Pods("cloud").Create(ctx, p, metav1.CreateOptions{})
			return err
		}
	}
	job := e.job(t)
	if err := validateAdmittedJob(e.resources.Job, job); err != nil {
		t.Fatalf("create-response check refused an API-defaulted Job: %v", err)
	}
	probes := &productionBoundary{cfg: e.l.Config}
	probeCtx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	r := e.resources.Reservation
	capacity := func() *corev1.Pod {
		pod, err := capacityPod(e.l.Config, demandFor(e.award))
		if err != nil {
			t.Fatal(err)
		}
		return pod
	}

	t.Run("allowed", func(t *testing.T) {
		for name, pod := range map[string]*corev1.Pod{
			"listener": probes.canary(probeCtx, r, "nonce"),
			"witness":  probes.witness(probeCtx, r, "nonce", "10.0.0.9"),
			"measure":  probes.measure(probeCtx, r, []string{"10.0.0.1:443"}, "10.0.0.9:18888"),
			"capacity": capacity(),
		} {
			if err := pods(e.launcher)(pod); err != nil {
				t.Errorf("%s refused: %v", name, err)
			}
		}
		plain := jobPod(job)
		if err := pods(e.jobs)(plain); err != nil {
			t.Fatalf("Job Pod refused: %v", err)
		}
		mutated := jobPod(job)
		eksMutate(mutated)
		if err := pods(e.jobs)(mutated); err != nil {
			t.Fatalf("EKS-mutated Job Pod refused: %v", err)
		}
		if err := pods(e.kcm)(jobPod(job)); err != nil {
			t.Fatalf("Job Pod from a shared controller-manager identity refused: %v", err)
		}
	})

	t.Run("refused_pods", func(t *testing.T) {
		for name, c := range map[string]struct {
			by     kubernetes.Interface
			pod    func() *corev1.Pod
			reason string
		}{
			"extra_container": {e.jobs, func() *corev1.Pod {
				p := jobPod(job)
				p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "injected", Image: e.l.Config.RunnerImage})
				return p
			}, "container set"},
			"image_change": {e.jobs, func() *corev1.Pod {
				p := jobPod(job)
				p.Spec.Containers[0].Image = "registry/other@sha256:" + strings.Repeat("c", 64)
				return p
			}, "container set"},
			"swapped_images": {e.jobs, func() *corev1.Pod {
				p := jobPod(job)
				p.Spec.InitContainers[0].Image = e.l.Config.RunnerImage
				return p
			}, "container set"},
			"hostPath": {e.jobs, func() *corev1.Pod {
				p := jobPod(job)
				p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}})
				return p
			}, "scratch emptyDir"},
			"other_secret": {e.jobs, func() *corev1.Pod {
				p := jobPod(job)
				p.Spec.Volumes[1].Secret.SecretName = "another-job"
				return p
			}, "scratch emptyDir"},
			"irsa": {e.jobs, func() *corev1.Pod { p := jobPod(job); irsa(p); return p }, "scratch emptyDir"},
			"irsa_env": {e.jobs, func() *corev1.Pod {
				p := jobPod(job)
				p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "AWS_ROLE_ARN", Value: "arn"})
				return p
			}, "never AWS credentials"},
			"pod_identity": {e.jobs, func() *corev1.Pod { p := jobPod(job); podIdentity(p); return p }, "scratch emptyDir"},
			"env_from_secret": {e.jobs, func() *corev1.Pod {
				p := jobPod(job)
				p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "AWS_REGION", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "x"}, Key: "k"}}})
				return p
			}, "never AWS credentials"},
			"service_account_token": {e.jobs, func() *corev1.Pod { p := jobPod(job); p.Spec.AutomountServiceAccountToken = ptr(true); return p }, "without an API token"},
			"other_service_account": {e.jobs, func() *corev1.Pod { p := jobPod(job); p.Spec.ServiceAccountName = "default"; return p }, "customer job service account"},
			"host_network":          {e.jobs, func() *corev1.Pod { p := jobPod(job); p.Spec.HostNetwork = true; return p }, "host namespaces"},
			"host_pid":              {e.jobs, func() *corev1.Pod { p := jobPod(job); p.Spec.HostPID = true; return p }, "host namespaces"},
			"privileged_workload": {e.jobs, func() *corev1.Pod {
				p := jobPod(job)
				p.Spec.Containers[0].SecurityContext.Privileged = ptr(true)
				return p
			}, "run privileged"},
			"added_capability": {e.jobs, func() *corev1.Pod {
				p := jobPod(job)
				p.Spec.Containers[0].SecurityContext.Capabilities = &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN"}}
				return p
			}, "add capabilities"},
			"missing_node_selector": {e.jobs, func() *corev1.Pod { p := jobPod(job); p.Spec.NodeSelector = nil; return p }, "own team's nodes"},
			"missing_toleration":    {e.jobs, func() *corev1.Pod { p := jobPod(job); p.Spec.Tolerations = p.Spec.Tolerations[:1]; return p }, "own team's nodes"},
			"wrong_team_label": {e.jobs, func() *corev1.Pod {
				p := jobPod(job)
				p.Labels[TeamLabel] = labelHash("other-team")
				return p
			}, "own team's nodes"},
			"injected_label": {e.jobs, func() *corev1.Pod { p := jobPod(job); p.Labels["escape"] = "true"; return p }, "launcher's labels"},
			"unknown_gate": {e.jobs, func() *corev1.Pod {
				p := jobPod(job)
				p.Spec.ReadinessGates = []corev1.PodReadinessGate{{ConditionType: "example.com/gate"}}
				return p
			}, "readiness gates"},
			"launcher_creates_job_pod":        {e.launcher, func() *corev1.Pod { return jobPod(job) }, "only the launcher and the Job controller"},
			"other_creates_job_pod":           {e.other, func() *corev1.Pod { return jobPod(job) }, "only the launcher and the Job controller"},
			"other_creates_capacity":          {e.other, capacity, "only the launcher and the Job controller"},
			"job_controller_creates_capacity": {e.jobs, capacity, "only the launcher and the Job controller"},
			"unowned_pod": {e.launcher, func() *corev1.Pod {
				p := jobPod(job)
				p.OwnerReferences, p.GenerateName, p.Name = nil, "", "free-pod"
				return p
			}, "only Cloud Job, capacity and boundary probe Pods"},
			"capacity_env": {e.launcher, func() *corev1.Pod {
				p := capacity()
				p.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "HOME", Value: "/tmp"}}
				return p
			}, "never AWS credentials"},
			"capacity_volume": {e.launcher, func() *corev1.Pod {
				p := capacity()
				p.Spec.Volumes = []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
				return p
			}, "scratch emptyDir"},
			"capacity_token": {e.launcher, func() *corev1.Pod { p := capacity(); p.Spec.AutomountServiceAccountToken = nil; return p }, "without an API token"},
			"capacity_bound": {e.launcher, func() *corev1.Pod { p := capacity(); p.Spec.NodeName = "node"; return p }, "capacity requests are not"},
			"capacity_privileged": {e.launcher, func() *corev1.Pod {
				p := capacity()
				p.Spec.Containers[0].SecurityContext.Privileged = ptr(true)
				p.Spec.Containers[0].SecurityContext.AllowPrivilegeEscalation = nil
				return p
			}, "run privileged"},
			"probe_host_network": {e.launcher, func() *corev1.Pod {
				p := probes.measure(probeCtx, r, nil, "10.0.0.9:18888")
				p.Spec.HostNetwork = true
				return p
			}, "host namespaces"},
		} {
			t.Run(name, func(t *testing.T) { refused(t, pods(c.by)(c.pod()), c.reason) })
		}
	})

	t.Run("pod_updates", func(t *testing.T) {
		created, err := e.jobs.CoreV1().Pods("cloud").Create(ctx, withFinalizer(jobPod(job)), metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		created.Annotations["vpc.amazonaws.com/pod-eni"] = "[]"
		if created, err = e.other.CoreV1().Pods("cloud").Update(ctx, created, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("VPC annotation refused: %v", err)
		}
		changed := created.DeepCopy()
		changed.Spec.Containers[0].Image = "registry/other@sha256:" + strings.Repeat("c", 64)
		_, err = e.admin.CoreV1().Pods("cloud").Update(ctx, changed, metav1.UpdateOptions{})
		refused(t, err, "immutable")
		relabeled := created.DeepCopy()
		relabeled.Labels[TeamLabel] = labelHash("other-team")
		_, err = e.admin.CoreV1().Pods("cloud").Update(ctx, relabeled, metav1.UpdateOptions{})
		refused(t, err, "immutable")
		debug := created.DeepCopy()
		debug.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", Image: e.l.Config.RunnerImage, TerminationMessagePolicy: corev1.TerminationMessageReadFile, ImagePullPolicy: corev1.PullIfNotPresent}}}
		_, err = e.admin.CoreV1().Pods("cloud").UpdateEphemeralContainers(ctx, debug.Name, debug, metav1.UpdateOptions{})
		refused(t, err, "ephemeral containers")
		created.Finalizers = nil
		if _, err = e.jobs.CoreV1().Pods("cloud").Update(ctx, created, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("Job controller finalizer removal refused: %v", err)
		}
	})

	t.Run("jobs", func(t *testing.T) {
		create := func(by kubernetes.Interface, mutate func(*batchv1.Job)) error {
			j := e.resources.Job.DeepCopy()
			j.Name = jobName(CloudJobBinding{UnitID: rand.Text()})
			j.Spec.Template.Labels[JobLabel] = j.Name
			j.Spec.Template.Spec.Volumes[1].Secret.SecretName = j.Name
			mutate(j)
			_, err := by.BatchV1().Jobs("cloud").Create(ctx, j, metav1.CreateOptions{})
			return err
		}
		if err := create(e.launcher, func(*batchv1.Job) {}); err != nil {
			t.Fatalf("launcher Job refused: %v", err)
		}
		refused(t, create(e.other, func(*batchv1.Job) {}), "only the launcher creates Jobs")
		refused(t, create(e.launcher, func(j *batchv1.Job) { j.Spec.Parallelism = ptr(int32(2)) }), "exactly one Pod")
		refused(t, create(e.launcher, func(j *batchv1.Job) { j.Spec.BackoffLimit = ptr(int32(3)) }), "exactly one Pod")
		refused(t, create(e.launcher, func(j *batchv1.Job) { j.Spec.Template.Labels[JobLabel] = "sw-other" }), "names its own Job")
		refused(t, create(e.launcher, func(j *batchv1.Job) {
			j.Spec.Template.Spec.InitContainers[1].Env = append(j.Spec.Template.Spec.InitContainers[1].Env, corev1.EnvVar{Name: "AWS_WEB_IDENTITY_TOKEN_FILE", Value: "/token"})
		}), "never AWS credentials")
		refused(t, create(e.launcher, func(j *batchv1.Job) { j.Spec.Template.Spec.Containers[0].Image = e.l.Config.DockerImage }), "container set")

		current, err := e.launcher.BatchV1().Jobs("cloud").Get(ctx, job.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		retemplated := current.DeepCopy()
		retemplated.Spec.Template.Spec.NodeSelector[TeamNodeLabel] = labelHash("other-team")
		_, err = e.launcher.BatchV1().Jobs("cloud").Update(ctx, retemplated, metav1.UpdateOptions{})
		refused(t, err, "never its template")
		resumed := current.DeepCopy()
		resumed.Spec.Suspend = ptr(false)
		_, err = e.other.BatchV1().Jobs("cloud").Update(ctx, resumed, metav1.UpdateOptions{})
		refused(t, err, "only the launcher changes a Job")
		widened := current.DeepCopy()
		widened.Spec.Parallelism = ptr(int32(3))
		_, err = e.launcher.BatchV1().Jobs("cloud").Update(ctx, widened, metav1.UpdateOptions{})
		refused(t, err, "exactly one Pod")
		patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{rejectedAnnotation: "true"}}, "spec": map[string]any{"suspend": false}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = e.launcher.BatchV1().Jobs("cloud").Patch(ctx, job.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			t.Fatalf("launcher activation refused: %v", err)
		}
	})
}

func TestAdmissionPolicyIgnoresOtherNamespaces(t *testing.T) {
	e := newAdmissionEnv(t)
	if _, err := e.admin.CoreV1().Namespaces().Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "elsewhere"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.admin.CoreV1().ServiceAccounts("elsewhere").Create(t.Context(), &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "free"}, Spec: corev1.PodSpec{HostNetwork: true, Containers: []corev1.Container{{Name: "c", Image: "busybox"}}}}
	if _, err := e.other.CoreV1().Pods("elsewhere").Create(t.Context(), pod, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil {
		t.Fatalf("job namespace policy reached another namespace: %v", err)
	}
}

func TestLauncherVerifiesLiveAdmissionPolicies(t *testing.T) {
	e := newAdmissionEnv(t)
	l := &Launcher{Kube: e.launcher, Config: e.l.Config}
	if err := l.verifyAdmission(t.Context()); err != nil {
		t.Fatalf("API-defaulted policies refused: %v", err)
	}
	other := &Launcher{Kube: e.launcher, Config: e.l.Config}
	other.Config.RunnerImage = "registry/other@sha256:" + strings.Repeat("c", 64)
	if err := other.verifyAdmission(t.Context()); err == nil {
		t.Fatal("policy for other images accepted")
	}
	impostor := &Launcher{Kube: e.other, Config: e.l.Config}
	if err := impostor.verifyAdmission(t.Context()); err == nil {
		t.Fatal("policy naming another launcher accepted")
	}
	bindings := e.admin.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings()
	binding, err := bindings.Get(t.Context(), AdmissionPolicyName("pods", "cloud"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	binding.Spec.ValidationActions = []admissionv1.ValidationAction{admissionv1.Warn}
	if _, err = bindings.Update(t.Context(), binding, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := l.verifyAdmission(t.Context()); err == nil {
		t.Fatal("warn-only binding accepted")
	}
	if err := bindings.Delete(t.Context(), binding.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := l.verifyAdmission(t.Context()); err == nil {
		t.Fatal("missing binding accepted")
	}
}

func withFinalizer(p *corev1.Pod) *corev1.Pod {
	p.Finalizers = []string{"batch.kubernetes.io/job-tracking"}
	return p
}

func TestRunRefusesMissingOrWeakenedAdmission(t *testing.T) {
	for name, weaken := range map[string]func(*fake.Clientset){
		"missing_policy": func(kube *fake.Clientset) {
			if err := kube.Tracker().Delete(admissionv1.SchemeGroupVersion.WithResource("validatingadmissionpolicies"), "", AdmissionPolicyName("jobs", "cloud")); err != nil {
				t.Fatal(err)
			}
		},
		"warn_only": func(kube *fake.Clientset) {
			binding, err := kube.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Get(t.Context(), AdmissionPolicyName("pods", "cloud"), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			binding.Spec.ValidationActions = []admissionv1.ValidationAction{admissionv1.Audit}
			if _, err := kube.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Update(t.Context(), binding, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
		},
		"other_namespace": func(kube *fake.Clientset) {
			policy, err := kube.AdmissionregistrationV1().ValidatingAdmissionPolicies().Get(t.Context(), AdmissionPolicyName("pods", "cloud"), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			policy.Spec.MatchConstraints.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] = "elsewhere"
			if _, err := kube.AdmissionregistrationV1().ValidatingAdmissionPolicies().Update(t.Context(), policy, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			l, kube, _, control := fixture()
			weaken(kube)
			if err := l.Run(t.Context()); err == nil || control.claims != 0 {
				t.Fatalf("launcher ran without its admission policy: %v claims=%d", err, control.claims)
			}
		})
	}
}

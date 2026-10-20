package launcher

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

type admissionEnv struct {
	*admissionFixture
	cfg     *rest.Config
	clients map[string]kubernetes.Interface
}

func (e *admissionEnv) as(t *testing.T, user string) kubernetes.Interface {
	t.Helper()
	if e.clients[user] == nil {
		e.clients[user] = impersonate(t, e.cfg, user)
	}
	return e.clients[user]
}

func newAdmissionEnv(t *testing.T) *admissionEnv {
	t.Helper()
	e := &admissionEnv{cfg: apiServer(t), clients: map[string]kubernetes.Interface{}}
	e.admissionFixture = newAdmissionFixture(t)
	admin, ctx := e.as(t, "admin"), t.Context()
	if _, err := admin.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cloud"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.NodeV1().RuntimeClasses().Create(ctx, &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "unsafe"}, Handler: "unsafe"}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, account := range []string{ServiceAccount, "default"} {
		if _, err := admin.CoreV1().ServiceAccounts("cloud").Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: account}, AutomountServiceAccountToken: ptr(false)}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: e.resources.Reservation.NodeName, Labels: map[string]string{corev1.LabelTopologyRegion: "us-west-2", corev1.LabelTopologyZone: "us-west-2a", corev1.LabelHostname: "node", "topology.k8s.io/rack": "r1"}}}
	if _, err := admin.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	policies, bindings := AdmissionPolicies(AdmissionConfig{Namespace: "cloud", Launcher: testLauncher, RunnerImage: e.l.Config.RunnerImage, DockerImage: testDockerImage})
	for i := range policies {
		if _, err := admin.AdmissionregistrationV1().ValidatingAdmissionPolicies().Create(ctx, policies[i], metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Create(ctx, bindings[i], metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	var last error
	dry := metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}
	err := wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		_, pod := admin.CoreV1().Pods("cloud").Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unexpected"}, Spec: corev1.PodSpec{ServiceAccountName: ServiceAccount, Containers: []corev1.Container{{Name: "c", Image: "busybox"}}}}, dry)
		_, job := admin.BatchV1().Jobs("cloud").Create(ctx, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "unexpected"}, Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{ServiceAccountName: ServiceAccount, RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "c", Image: "busybox"}}}}}}, dry)
		last = errors.Join(pod, job)
		return pod != nil && job != nil && strings.Contains(pod.Error(), "denied request") && strings.Contains(job.Error(), "denied request"), nil
	})
	if err != nil {
		t.Fatalf("policies never enforced: %v; last response %v", err, last)
	}
	return e
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
	job, err := e.as(t, testLauncher).BatchV1().Jobs("cloud").Create(ctx, e.resources.Job.DeepCopy(), metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAdmittedJob(e.resources.Job, job); err != nil {
		t.Fatalf("create-response check refused an API-defaulted Job: %v", err)
	}
	for _, c := range append(e.podCreates(t, job), e.jobCreates()...) {
		t.Run(c.name, func(t *testing.T) {
			var err error
			switch object := c.object.(type) {
			case *corev1.Pod:
				var admitted *corev1.Pod
				admitted, err = e.as(t, c.user).CoreV1().Pods("cloud").Create(ctx, object, metav1.CreateOptions{})
				if err == nil && object.Spec.NodeName != "" && admitted.Labels[corev1.LabelTopologyZone] != "us-west-2a" {
					t.Fatalf("API server did not copy node topology onto the Pod: %v", admitted.Labels)
				}
			case *batchv1.Job:
				_, err = e.as(t, c.user).BatchV1().Jobs("cloud").Create(ctx, object, metav1.CreateOptions{})
			}
			if c.reason == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			refused(t, err, c.reason)
		})
	}

	t.Run("pod_updates", func(t *testing.T) {
		created := jobPod(job)
		created.Finalizers = []string{"batch.kubernetes.io/job-tracking"}
		created, err := e.as(t, testJobController).CoreV1().Pods("cloud").Create(ctx, created, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		created.Annotations["vpc.amazonaws.com/pod-eni"] = "[]"
		if created, err = e.as(t, testIntruder).CoreV1().Pods("cloud").Update(ctx, created, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("VPC annotation refused: %v", err)
		}
		changed := created.DeepCopy()
		changed.Spec.Containers[0].Image = otherImage
		_, err = e.as(t, "admin").CoreV1().Pods("cloud").Update(ctx, changed, metav1.UpdateOptions{})
		refused(t, err, "immutable")
		reowned := created.DeepCopy()
		reowned.OwnerReferences[0].Name = "other-job"
		_, err = e.as(t, "admin").CoreV1().Pods("cloud").Update(ctx, reowned, metav1.UpdateOptions{})
		refused(t, err, "Pod ownership")
		debug := created.DeepCopy()
		debug.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", Image: e.l.Config.RunnerImage, TerminationMessagePolicy: corev1.TerminationMessageReadFile, ImagePullPolicy: corev1.PullIfNotPresent}}}
		_, err = e.as(t, "admin").CoreV1().Pods("cloud").UpdateEphemeralContainers(ctx, debug.Name, debug, metav1.UpdateOptions{})
		refused(t, err, "ephemeral containers")
		for _, sub := range []string{"exec", "attach", "portforward", "proxy"} {
			err = e.as(t, "admin").CoreV1().RESTClient().Post().Namespace("cloud").Resource("pods").Name(created.Name).SubResource(sub).Param("container", "run-node").Param("command", "sh").Param("stdout", "true").Param("ports", "80").Do(ctx).Error()
			refused(t, err, "exec, attach, port-forward and proxy")
		}
		created.Finalizers = nil
		if _, err = e.as(t, testJobController).CoreV1().Pods("cloud").Update(ctx, created, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("Job controller finalizer removal refused: %v", err)
		}
	})

	t.Run("job_updates", func(t *testing.T) {
		launcher := e.as(t, testLauncher)
		current, err := launcher.BatchV1().Jobs("cloud").Get(ctx, job.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		retemplated := current.DeepCopy()
		retemplated.Spec.Template.Spec.NodeSelector[TeamNodeLabel] = labelHash("other-team")
		_, err = launcher.BatchV1().Jobs("cloud").Update(ctx, retemplated, metav1.UpdateOptions{})
		refused(t, err, "template annotations")
		resumed := current.DeepCopy()
		resumed.Spec.Suspend = ptr(false)
		_, err = e.as(t, testIntruder).BatchV1().Jobs("cloud").Update(ctx, resumed, metav1.UpdateOptions{})
		refused(t, err, "only the launcher changes a Job")
		widened := current.DeepCopy()
		widened.Spec.Parallelism = ptr(int32(3))
		_, err = launcher.BatchV1().Jobs("cloud").Update(ctx, widened, metav1.UpdateOptions{})
		refused(t, err, "exactly one Pod")
		if _, err = launcher.BatchV1().Jobs("cloud").Update(ctx, resumed, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("launcher activation refused: %v", err)
		}
	})
}

func TestAdmissionPolicyIgnoresOtherNamespaces(t *testing.T) {
	e := newAdmissionEnv(t)
	if _, err := e.as(t, "admin").CoreV1().Namespaces().Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "elsewhere"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.as(t, "admin").CoreV1().ServiceAccounts("elsewhere").Create(t.Context(), &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "free"}, Spec: corev1.PodSpec{HostNetwork: true, Containers: []corev1.Container{{Name: "c", Image: "busybox"}}}}
	if _, err := e.as(t, testIntruder).CoreV1().Pods("elsewhere").Create(t.Context(), pod, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil {
		t.Fatalf("job namespace policy reached another namespace: %v", err)
	}
}

func TestLauncherVerifiesLiveAdmissionPolicies(t *testing.T) {
	e := newAdmissionEnv(t)
	l := &Launcher{Kube: e.as(t, testLauncher), Config: e.l.Config}
	if err := l.verifyAdmission(t.Context()); err != nil {
		t.Fatalf("API-defaulted policies refused: %v", err)
	}
	other := &Launcher{Kube: e.as(t, testLauncher), Config: e.l.Config}
	other.Config.RunnerImage = "registry/other@sha256:" + strings.Repeat("c", 64)
	if err := other.verifyAdmission(t.Context()); err == nil {
		t.Fatal("policy for other images accepted")
	}
	impostor := &Launcher{Kube: e.as(t, testIntruder), Config: e.l.Config}
	if err := impostor.verifyAdmission(t.Context()); err == nil {
		t.Fatal("policy naming another launcher accepted")
	}
	bindings := e.as(t, "admin").AdmissionregistrationV1().ValidatingAdmissionPolicyBindings()
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

func TestLaunchBoundariesRefuseMissingOrWeakenedAdmission(t *testing.T) {
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
			if launched, err := l.LaunchOne(t.Context()); launched || err == nil || control.claims != 0 {
				t.Fatalf("launcher claimed without admission policy: %v claims=%d", err, control.claims)
			}
			if err := l.Run(t.Context()); err == nil || control.claims != 0 {
				t.Fatalf("launcher ran without its admission policy: %v claims=%d", err, control.claims)
			}
		})
	}
}

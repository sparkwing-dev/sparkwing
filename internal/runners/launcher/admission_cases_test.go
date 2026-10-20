package launcher

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/plugin/cel"
	"k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/cel/environment"
)

const (
	testLauncher      = "system:serviceaccount:sparkwing:sparkwing-launcher"
	testJobController = "system:serviceaccount:kube-system:job-controller"
	testCollector     = "system:serviceaccount:kube-system:generic-garbage-collector"
	testIntruder      = "system:serviceaccount:sparkwing:intruder"
	testDockerImage   = "registry/docker@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	otherImage        = "registry/other@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

type admissionFixture struct {
	l         *Launcher
	resources CloudJobResources
	award     CloudJobAward
	probes    *productionBoundary
	probeCtx  context.Context
}

func newAdmissionFixture(t *testing.T) *admissionFixture {
	t.Helper()
	l, _, boundary, control := fixture()
	l.Config.DockerImage = testDockerImage
	f := &admissionFixture{l: l, award: *control.award, probes: &productionBoundary{cfg: l.Config}}
	f.award.Binding.OwnerDeadline = time.Now().Add(time.Hour)
	f.award.Binding.ParentCapabilityExpiry = time.Now().Add(2 * time.Hour)
	var cancel context.CancelFunc
	f.probeCtx, cancel = context.WithTimeout(t.Context(), time.Hour)
	t.Cleanup(cancel)
	var err error
	if f.resources, err = BuildJob(t.Context(), time.Now(), l.Config, f.award, boundary); err != nil {
		t.Fatal(err)
	}
	f.resources.Job.Spec.Suspend = ptr(true)
	return f
}

func (f *admissionFixture) policies() []*admissionv1.ValidatingAdmissionPolicy {
	policies, _ := AdmissionPolicies(AdmissionConfig{Namespace: "cloud", Launcher: testLauncher, RunnerImage: f.l.Config.RunnerImage, DockerImage: testDockerImage})
	return policies
}

func (f *admissionFixture) capacity(t *testing.T) *corev1.Pod {
	t.Helper()
	pod, err := capacityPod(f.l.Config, demandFor(f.award))
	if err != nil {
		t.Fatal(err)
	}
	return pod
}

func (f *admissionFixture) measure() *corev1.Pod {
	return f.probes.measure(f.probeCtx, f.resources.Reservation, []string{"10.0.0.1:443", "10.0.0.2:18888"}, "10.0.0.9:18888")
}

// hostUsers puts a Job Pod in the host user namespace, dropping the /proc
// unmasking the API server allows only inside a Pod's own.
func hostUsers(p *corev1.Pod) {
	p.Spec.HostUsers = nil
	p.Spec.InitContainers[0].SecurityContext.ProcMount = nil
}

func jobPod(job *batchv1.Job) *corev1.Pod {
	pod := &corev1.Pod{ObjectMeta: *job.Spec.Template.ObjectMeta.DeepCopy(), Spec: *job.Spec.Template.Spec.DeepCopy()}
	pod.GenerateName, pod.Namespace = job.Name+"-", job.Namespace
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr(true), BlockOwnerDeletion: ptr(true)}}
	return pod
}

// hack: an empty reason means the policies admit the request.
type admissionCase struct {
	name, user, operation, subresource string
	object, old                        runtime.Object
	reason                             string
}

func (f *admissionFixture) podCreates(t *testing.T, job *batchv1.Job) []admissionCase {
	t.Helper()
	pod := func(mutate func(*corev1.Pod)) *corev1.Pod {
		p := jobPod(job)
		mutate(p)
		return p
	}
	capacity := func(mutate func(*corev1.Pod)) *corev1.Pod {
		p := f.capacity(t)
		mutate(p)
		return p
	}
	measure := func(mutate func(*corev1.Pod)) *corev1.Pod {
		p := f.measure()
		mutate(p)
		return p
	}
	r := f.resources.Reservation
	cases := []admissionCase{
		{name: "listener", user: testLauncher, object: f.probes.canary(f.probeCtx, r, "nonce")},
		{name: "witness", user: testLauncher, object: f.probes.witness(f.probeCtx, r, "nonce", "10.0.0.9", "10.0.0.8")},
		{name: "peer", user: testLauncher, object: f.probes.peer(f.probeCtx, r, "nonce")},
		{name: "measure", user: testLauncher, object: f.measure()},
		{name: "capacity", user: testLauncher, object: f.capacity(t)},
		{name: "job_pod", user: testJobController, object: jobPod(job)},
		{name: "eks_mutated_job_pod", user: testJobController, object: pod(eksMutate)},
		{name: "node_topology_job_pod", user: testJobController, object: pod(nodeTopology)},
		{name: "node_topology_listener", user: testLauncher, object: func() *corev1.Pod {
			p := f.probes.canary(f.probeCtx, r, "nonce")
			nodeTopology(p)
			return p
		}()},
		{name: "node_topology_measure", user: testLauncher, object: measure(nodeTopology)},
		{name: "hostname_label", user: testJobController, object: pod(func(p *corev1.Pod) { nodeTopology(p); p.Labels[corev1.LabelHostname] = "node" }), reason: "launcher's labels"},
		{name: "probe_hostname_label", user: testLauncher, object: measure(func(p *corev1.Pod) { p.Labels[corev1.LabelHostname] = "node" }), reason: "launcher's labels"},
		{name: "capacity_topology", user: testLauncher, object: capacity(nodeTopology), reason: "launcher's labels"},
		{name: "extra_container", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "injected", Image: f.l.Config.RunnerImage})
		}), reason: "container set"},
		{name: "image_change", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.Containers[0].Image = otherImage }), reason: "container set"},
		{name: "swapped_images", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.InitContainers[0].Image = f.l.Config.RunnerImage }), reason: "container set"},
		{name: "hostPath", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}})
		}), reason: "scratch emptyDir"},
		{name: "other_secret", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.Volumes[1].Secret.SecretName = "another-job" }), reason: "scratch emptyDir"},
		{name: "irsa", user: testJobController, object: pod(irsa), reason: "scratch emptyDir"},
		{name: "irsa_env", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "AWS_ROLE_ARN", Value: "arn"})
		}), reason: "never AWS credentials"},
		{name: "pod_identity", user: testJobController, object: pod(podIdentity), reason: "scratch emptyDir"},
		{name: "env_from_secret", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "AWS_REGION", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "x"}, Key: "k"}}})
		}), reason: "never AWS credentials"},
		{name: "service_account_token", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.AutomountServiceAccountToken = ptr(true) }), reason: "without an API token"},
		{name: "other_service_account", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.ServiceAccountName = "default" }), reason: "customer job service account"},
		{name: "host_network", user: testJobController, object: pod(func(p *corev1.Pod) { hostUsers(p); p.Spec.HostNetwork = true }), reason: "host namespaces"},
		{name: "host_pid", user: testJobController, object: pod(func(p *corev1.Pod) { hostUsers(p); p.Spec.HostPID = true }), reason: "host namespaces"},
		{name: "privileged_workload", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: ptr(true)}
		}), reason: "runs privileged"},
		{name: "privileged_docker_sidecar", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.InitContainers[0].SecurityContext.Privileged = ptr(true) }), reason: "runs privileged"},
		{name: "host_users", user: testJobController, object: pod(hostUsers), reason: "own user namespace"},
		{name: "non_root_pod", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.SecurityContext.RunAsUser = ptr(int64(1000)) }), reason: "exactly its shape's security context"},
		{name: "non_root_docker_sidecar", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.InitContainers[0].SecurityContext.RunAsUser = ptr(int64(1000)) }), reason: "exactly its shape's security context"},
		{name: "docker_extra_capability", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.InitContainers[0].SecurityContext.Capabilities.Add = append(p.Spec.InitContainers[0].SecurityContext.Capabilities.Add, "SYS_MODULE")
		}), reason: "exactly its shape's security context"},
		{name: "masked_docker_sidecar", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.InitContainers[0].SecurityContext.ProcMount = nil }), reason: "exactly its shape's security context"},
		{name: "unconfined_docker_sidecar", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.InitContainers[0].SecurityContext.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined}
		}), reason: "exactly its shape's security context"},
		{name: "unconfined_workload", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.Containers[0].SecurityContext.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined}
		}), reason: "exactly its shape's security context"},
		{name: "escalating_workload", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.Containers[0].SecurityContext.AllowPrivilegeEscalation = ptr(true) }), reason: "exactly its shape's security context"},
		{name: "added_capability", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.Containers[0].SecurityContext.Capabilities = &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN"}}
		}), reason: "exactly its shape's security context"},
		{name: "pod_super_t", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.SecurityContext.SELinuxOptions = &corev1.SELinuxOptions{Type: "super_t"} }), reason: "exactly its shape's security context"},
		{name: "docker_super_t", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.InitContainers[0].SecurityContext.SELinuxOptions = &corev1.SELinuxOptions{Type: "super_t"}
		}), reason: "exactly its shape's security context"},
		{name: "workload_selinux_level", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.Containers[0].SecurityContext.SELinuxOptions = &corev1.SELinuxOptions{Level: "s0:c1,c2"}
		}), reason: "exactly its shape's security context"},
		{name: "pod_sysctl", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.SecurityContext.Sysctls = []corev1.Sysctl{{Name: "net.ipv4.ip_unprivileged_port_start", Value: "0"}}
		}), reason: "exactly its shape's security context"},
		{name: "pod_fs_group", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.SecurityContext.FSGroup = ptr(int64(1000)) }), reason: "exactly its shape's security context"},
		{name: "probe_super_t", user: testLauncher, object: measure(func(p *corev1.Pod) { p.Spec.SecurityContext.SELinuxOptions = &corev1.SELinuxOptions{Type: "super_t"} }), reason: "exactly its shape's security context"},
		{name: "probe_root", user: testLauncher, object: measure(func(p *corev1.Pod) {
			p.Spec.SecurityContext.RunAsUser = ptr(int64(0))
			p.Spec.SecurityContext.RunAsNonRoot = nil
		}), reason: "exactly its shape's security context"},
		{name: "capacity_capability", user: testLauncher, object: capacity(func(p *corev1.Pod) { p.Spec.Containers[0].SecurityContext.Capabilities = nil }), reason: "exactly its shape's security context"},
		{name: "runtime_class", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.RuntimeClassName = ptr("unsafe") }), reason: "default runtime class"},
		{name: "shared_process_namespace", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.ShareProcessNamespace = ptr(true) }), reason: "default runtime class"},
		{name: "probe_host", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.Containers[0].ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Host: "169.254.169.254", Port: intstr.FromInt32(80)}}}
		}), reason: "default runtime class"},
		{name: "tcp_probe_host", user: testLauncher, object: func() *corev1.Pod {
			p := f.probes.canary(f.probeCtx, r, "nonce")
			p.Spec.Containers[0].ReadinessProbe.TCPSocket.Host = "10.0.0.1"
			return p
		}(), reason: "default runtime class"},
		{name: "dns_config", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.DNSConfig = &corev1.PodDNSConfig{Options: []corev1.PodDNSConfigOption{{Name: "ndots", Value: ptr("1")}}}
		}), reason: "cluster DNS"},
		{name: "host_aliases", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.HostAliases = []corev1.HostAlias{{IP: "10.0.0.1", Hostnames: []string{"api.sparkwing.dev"}}}
		}), reason: "cluster DNS"},
		{name: "other_scheduler", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.SchedulerName = "custom" }), reason: "cluster DNS"},
		{name: "service_links", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.EnableServiceLinks = ptr(true) }), reason: "cluster DNS"},
		{name: "run_node_command", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.Containers[0].Command = []string{"/bin/sh"} }), reason: "sparkwing-runner commands"},
		{name: "run_node_flag", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.Containers[0].Args[11] = "--bootstrap-dir" }), reason: "sparkwing-runner commands"},
		{name: "fetch_source_sidecar", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.InitContainers[1].RestartPolicy = ptr(corev1.ContainerRestartPolicyAlways)
		}), reason: "sparkwing-runner commands"},
		{name: "host_path", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "tun", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/dev/net/tun"}}})
		}), reason: "scratch emptyDir"},
		{name: "probe_privileged", user: testLauncher, object: measure(func(p *corev1.Pod) {
			p.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: ptr(true)}
		}), reason: "runs privileged"},
		{name: "probe_host_pid", user: testLauncher, object: measure(func(p *corev1.Pod) { p.Spec.HostPID = true }), reason: "host namespaces"},
		{name: "missing_node_selector", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.NodeSelector = nil }), reason: "own team's nodes"},
		{name: "missing_toleration", user: testJobController, object: pod(func(p *corev1.Pod) { p.Spec.Tolerations = p.Spec.Tolerations[:1] }), reason: "own team's nodes"},
		{name: "wrong_team_label", user: testJobController, object: pod(func(p *corev1.Pod) { p.Labels[TeamLabel] = labelHash("other-team") }), reason: "own team's nodes"},
		{name: "injected_label", user: testJobController, object: pod(func(p *corev1.Pod) { p.Labels["escape"] = "true" }), reason: "launcher's labels"},
		{name: "unknown_gate", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.ReadinessGates = []corev1.PodReadinessGate{{ConditionType: "example.com/gate"}}
		}), reason: "readiness gates"},
		{name: "lifecycle_hook", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.Containers[0].Lifecycle = &corev1.Lifecycle{PostStart: &corev1.LifecycleHandler{Exec: &corev1.ExecAction{Command: []string{"sh", "-c", "curl attacker"}}}}
		}), reason: "hooks or exec probes"},
		{name: "exec_liveness_probe", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.Containers[0].LivenessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"sh"}}}}
		}), reason: "hooks or exec probes"},
		{name: "docker_command", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.InitContainers[0].Command = []string{"sh", "-c", "dockerd --tls=false -H tcp://0.0.0.0"}
		}), reason: "Docker sidecar"},
		{name: "docker_args", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.InitContainers[0].Args = append(p.Spec.InitContainers[0].Args, "--host=tcp://0.0.0.0:2375")
		}), reason: "Docker sidecar"},
		{name: "docker_exec_probe", user: testJobController, object: pod(func(p *corev1.Pod) {
			p.Spec.InitContainers[0].StartupProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"docker", "info"}}}}
		}), reason: "hooks or exec probes"},
		{name: "peer_readiness_changed", user: testLauncher, object: func() *corev1.Pod {
			p := f.probes.peer(f.probeCtx, r, "nonce")
			p.Spec.Containers[0].ReadinessProbe.Exec.Command = []string{"true"}
			return p
		}(), reason: "hooks or exec probes"},
		{name: "capacity_hook", user: testLauncher, object: capacity(func(p *corev1.Pod) {
			p.Spec.Containers[0].Lifecycle = &corev1.Lifecycle{PreStop: &corev1.LifecycleHandler{Exec: &corev1.ExecAction{Command: []string{"sh"}}}}
		}), reason: "hooks or exec probes"},
		{name: "probe_exec_readiness", user: testLauncher, object: func() *corev1.Pod {
			p := f.probes.canary(f.probeCtx, r, "nonce")
			p.Spec.Containers[0].ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"sh"}}}}
			return p
		}(), reason: "hooks or exec probes"},
		{name: "launcher_creates_job_pod", user: testLauncher, object: jobPod(job), reason: "only the launcher and the Job controller"},
		{name: "controller_manager_creates_job_pod", user: "system:kube-controller-manager", object: jobPod(job), reason: "only the launcher and the Job controller"},
		{name: "other_creates_job_pod", user: testIntruder, object: jobPod(job), reason: "only the launcher and the Job controller"},
		{name: "other_creates_capacity", user: testIntruder, object: f.capacity(t), reason: "only the launcher and the Job controller"},
		{name: "job_controller_creates_capacity", user: testJobController, object: f.capacity(t), reason: "only the launcher and the Job controller"},
		{name: "unowned_pod", user: testLauncher, object: pod(func(p *corev1.Pod) { p.OwnerReferences, p.GenerateName, p.Name = nil, "", "free-pod" }), reason: "only Cloud Job, capacity and boundary probe Pods"},
		{name: "capacity_env", user: testLauncher, object: capacity(func(p *corev1.Pod) { p.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "HOME", Value: "/tmp"}} }), reason: "never AWS credentials"},
		{name: "capacity_volume", user: testLauncher, object: capacity(func(p *corev1.Pod) {
			p.Spec.Volumes = []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
		}), reason: "scratch emptyDir"},
		{name: "capacity_token", user: testLauncher, object: capacity(func(p *corev1.Pod) { p.Spec.AutomountServiceAccountToken = nil }), reason: "without an API token"},
		{name: "capacity_bound", user: testLauncher, object: capacity(func(p *corev1.Pod) { p.Spec.NodeName = "node" }), reason: "capacity requests are not"},
		{name: "capacity_privileged", user: testLauncher, object: capacity(func(p *corev1.Pod) {
			p.Spec.Containers[0].SecurityContext.Privileged = ptr(true)
			p.Spec.Containers[0].SecurityContext.AllowPrivilegeEscalation = nil
		}), reason: "runs privileged"},
		{name: "probe_host_network", user: testLauncher, object: measure(func(p *corev1.Pod) { p.Spec.HostNetwork = true }), reason: "host namespaces"},
		{name: "probe_command", user: testLauncher, object: measure(func(p *corev1.Pod) { p.Spec.Containers[0].Command = []string{"/bin/true"} }), reason: "measurement commands"},
		{name: "probe_renamed", user: testLauncher, object: measure(func(p *corev1.Pod) { p.Spec.Containers[0].Name = "other" }), reason: "measurement commands"},
		{name: "listener_host_pid", user: testLauncher, object: func() *corev1.Pod {
			p := f.probes.canary(f.probeCtx, r, "nonce")
			p.Spec.HostPID = true
			return p
		}(), reason: "host namespaces"},
	}
	for i := range cases {
		cases[i].operation = string(admissionv1.Create)
	}
	return cases
}

func (f *admissionFixture) jobCreates() []admissionCase {
	job := func(mutate func(*batchv1.Job)) *batchv1.Job {
		j := f.resources.Job.DeepCopy()
		j.Name = jobName(CloudJobBinding{UnitID: rand.Text()})
		j.Spec.Template.Labels[JobLabel] = j.Name
		j.Spec.Template.Spec.Volumes[1].Secret.SecretName = j.Name
		mutate(j)
		return j
	}
	none := func(*batchv1.Job) {}
	cases := []admissionCase{
		{name: "launcher_job", user: testLauncher, object: job(none)},
		{name: "other_creates_job", user: testIntruder, object: job(none), reason: "only the launcher creates Jobs"},
		{name: "parallel_job", user: testLauncher, object: job(func(j *batchv1.Job) { j.Spec.Parallelism = ptr(int32(2)) }), reason: "exactly one Pod"},
		{name: "retrying_job", user: testLauncher, object: job(func(j *batchv1.Job) { j.Spec.BackoffLimit = ptr(int32(3)) }), reason: "exactly one Pod"},
		{name: "misnamed_template", user: testLauncher, object: job(func(j *batchv1.Job) { j.Spec.Template.Labels[JobLabel] = "sw-other" }), reason: "names its own Job"},
		{name: "template_credentials", user: testLauncher, object: job(func(j *batchv1.Job) {
			j.Spec.Template.Spec.InitContainers[1].Env = append(j.Spec.Template.Spec.InitContainers[1].Env, corev1.EnvVar{Name: "AWS_WEB_IDENTITY_TOKEN_FILE", Value: "/token"})
		}), reason: "never AWS credentials"},
		{name: "template_topology", user: testLauncher, object: job(func(j *batchv1.Job) { j.Spec.Template.Labels[corev1.LabelTopologyZone] = "us-west-2a" }), reason: "launcher's labels"},
		{name: "template_image", user: testLauncher, object: job(func(j *batchv1.Job) { j.Spec.Template.Spec.Containers[0].Image = testDockerImage }), reason: "container set"},
	}
	for i := range cases {
		cases[i].operation = string(admissionv1.Create)
	}
	return cases
}

func (f *admissionFixture) updates(job *batchv1.Job) []admissionCase {
	created := jobPod(job)
	created.Name = job.Name + "-pod"
	created.Finalizers = []string{"batch.kubernetes.io/job-tracking"}
	with := func(mutate func(*corev1.Pod)) *corev1.Pod {
		p := created.DeepCopy()
		mutate(p)
		return p
	}
	jobWith := func(mutate func(*batchv1.Job)) *batchv1.Job {
		j := job.DeepCopy()
		mutate(j)
		return j
	}
	cases := []admissionCase{
		{name: "vpc_annotation", user: testIntruder, old: created, object: with(func(p *corev1.Pod) { p.Annotations["vpc.amazonaws.com/pod-eni"] = "[]" })},
		{name: "finalizer_removal", user: testJobController, old: created, object: with(func(p *corev1.Pod) { p.Finalizers = nil })},
		{name: "orphaned_by_collector", user: testCollector, old: created, object: with(func(p *corev1.Pod) { p.OwnerReferences = nil })},
		{name: "image_update", user: testJobController, old: created, object: with(func(p *corev1.Pod) { p.Spec.Containers[0].Image = otherImage }), reason: "immutable"},
		{name: "relabel", user: testJobController, old: created, object: with(func(p *corev1.Pod) { p.Labels[TeamLabel] = labelHash("other-team") }), reason: "immutable"},
		{name: "reowned", user: testIntruder, old: created, object: with(func(p *corev1.Pod) { p.OwnerReferences[0].UID = "other-job" }), reason: "Pod ownership"},
		{name: "ephemeral", user: testIntruder, subresource: "ephemeralcontainers", old: created, object: with(func(p *corev1.Pod) {
			p.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", Image: f.l.Config.RunnerImage}}}
		}), reason: "ephemeral containers"},
		{name: "launcher_activates_job", user: testLauncher, old: job, object: jobWith(func(j *batchv1.Job) {
			j.Spec.Suspend = ptr(false)
		})},
		{name: "collector_marks_job", user: testCollector, old: job, object: jobWith(func(j *batchv1.Job) { j.Finalizers = append(j.Finalizers, "foregroundDeletion") })},
		{name: "launcher_annotates_job", user: testLauncher, old: job, object: jobWith(func(j *batchv1.Job) { j.Annotations[rejectedAnnotation] = rejectedPod })},
		{name: "other_annotates_job", user: testIntruder, old: job, object: jobWith(func(j *batchv1.Job) { j.Annotations[rejectedAnnotation] = rejectedUnadmittable }), reason: "only the launcher changes a Job"},
		{name: "retemplated_job", user: testLauncher, old: job, object: jobWith(func(j *batchv1.Job) { j.Spec.Template.Spec.NodeSelector[TeamNodeLabel] = labelHash("other-team") }), reason: "template annotations"},
		{name: "other_resumes_job", user: testIntruder, old: job, object: jobWith(func(j *batchv1.Job) { j.Spec.Suspend = ptr(false) }), reason: "only the launcher changes a Job"},
		{name: "widened_job", user: testLauncher, old: job, object: jobWith(func(j *batchv1.Job) { j.Spec.Parallelism = ptr(int32(3)) }), reason: "exactly one Pod"},
	}
	for i := range cases {
		cases[i].operation = string(admissionv1.Update)
	}
	for _, sub := range []string{"exec", "attach", "portforward", "proxy"} {
		cases = append(cases, admissionCase{name: "connect_" + sub, user: testLauncher, operation: string(admissionv1.Connect), subresource: sub, object: &corev1.PodExecOptions{}, reason: "exec, attach, port-forward and proxy"})
	}
	return cases
}

func celAdmit(t *testing.T, policies []*admissionv1.ValidatingAdmissionPolicy, c admissionCase) error {
	t.Helper()
	gvk, gvr, policy := corev1.SchemeGroupVersion.WithKind("Pod"), corev1.SchemeGroupVersion.WithResource("pods"), policies[0]
	if _, ok := c.object.(*batchv1.Job); ok {
		gvk, gvr, policy = batchv1.SchemeGroupVersion.WithKind("Job"), batchv1.SchemeGroupVersion.WithResource("jobs"), policies[1]
	}
	compiler, err := cel.NewCompositedCompiler(environment.MustBaseEnvSet(environment.DefaultCompatibilityVersion()))
	if err != nil {
		t.Fatal(err)
	}
	options := cel.OptionalVariableDeclarations{HasAuthorizer: true}
	variables := make([]cel.NamedExpressionAccessor, len(policy.Spec.Variables))
	for i, v := range policy.Spec.Variables {
		variables[i] = &validating.Variable{Name: v.Name, Expression: v.Expression}
	}
	compiler.CompileAndStoreVariables(variables, options, environment.StoredExpressions)
	validations := make([]cel.ExpressionAccessor, len(policy.Spec.Validations))
	for i, v := range policy.Spec.Validations {
		validations[i] = &validating.ValidationCondition{Expression: v.Expression, Message: v.Message}
	}
	none := compiler.CompileCondition(nil, options, environment.StoredExpressions)
	validator := validating.NewValidator(compiler.CompileCondition(validations, options, environment.StoredExpressions), nil, none, none, policy.Spec.FailurePolicy, nil)
	if err := validator.CompileError(); err != nil {
		t.Fatal(err)
	}
	var old runtime.Object
	if c.old != nil {
		old = c.old
	}
	meta, _ := c.object.(metav1.Object)
	name := ""
	if meta != nil {
		name = meta.GetName()
	}
	attributes := admission.NewAttributesRecord(c.object, old, gvk, "cloud", name, gvr, c.subresource, admission.Operation(c.operation), nil, false, &user.DefaultInfo{Name: c.user})
	versioned := &admission.VersionedAttributes{Attributes: attributes, VersionedKind: gvk, VersionedObject: c.object, VersionedOldObject: old}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cloud", Labels: map[string]string{"kubernetes.io/metadata.name": "cloud"}}}
	result := validator.Validate(t.Context(), gvr, versioned, nil, namespace, celconfig.RuntimeCELCostBudget, nil)
	var denials []error
	for _, decision := range result.Decisions {
		if decision.Action == validating.ActionDeny {
			denials = append(denials, errors.New("denied request: "+decision.Message))
		}
	}
	return errors.Join(denials...)
}

func TestAdmissionPolicyCEL(t *testing.T) {
	f := newAdmissionFixture(t)
	job := f.resources.Job.DeepCopy()
	job.UID = "job-uid"
	cases := append(append(f.podCreates(t, job), f.jobCreates()...), f.updates(job)...)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := celAdmit(t, f.policies(), c)
			if c.reason == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			refused(t, err, c.reason)
		})
	}
}

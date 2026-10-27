package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// Cloud Jobs use a separate API identity, and labels bind Kubernetes objects to their reservation.
const (
	jobHostUsers          = false
	ServiceAccount        = "sparkwing-customer-job"
	JobLabel              = "sparkwing.dev/job"
	JobBinary             = "sparkwing-runner"
	TeamLabel             = "sparkwing.dev/team"
	TeamNodeLabel         = "node-restriction.kubernetes.io/sparkwing-team-node"
	managedByLauncher     = "sparkwing-launcher"
	bindingAnnotation     = "sparkwing.dev/award-binding"
	deadlineAnnotation    = "sparkwing.dev/job-deadline"
	launchAnnotation      = "sparkwing.dev/job-launched-at"
	reservationAnnotation = "sparkwing.dev/node-reservation"
	cleanupFinalizer      = "sparkwing.dev/cloud-cleanup"
	doNotDisrupt          = "karpenter.sh/do-not-disrupt"
	bootstrapPath         = "/etc/sparkwing/bootstrap/bootstrap"
	bootstrapAnnotation   = "sparkwing.dev/bootstrap-cleanup"
)

var digestPinned = regexp.MustCompile(`^[a-z0-9][a-z0-9.:/_-]*@sha256:[0-9a-f]{64}$`)

// Validate rejects unpinned images and missing operator resource ceilings before reserving a node.
func (c CloudJobConfig) Validate() error {
	u, err := url.Parse(c.ControllerURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("controller URL must be an HTTP(S) origin without credentials, query or fragment")
	}
	if len(validation.IsDNS1123Label(c.Namespace)) != 0 {
		return errors.New("a valid namespace is required")
	}
	if !digestPinned.MatchString(c.RunnerImage) || !digestPinned.MatchString(c.DockerImage) {
		return errors.New("runner and Docker images must be pinned by SHA-256 digest")
	}
	if math.IsNaN(c.CPUCeiling) || math.IsInf(c.CPUCeiling, 0) || c.CPUCeiling < 0.001 || c.CPUCeiling >= float64(math.MaxInt64)/1000 || c.MemoryCeiling <= 0 {
		return errors.New("positive finite CPU and memory ceilings are required")
	}
	return nil
}

// BuildJob builds plan, eval and work Jobs after validating their award and reserving isolation.
func BuildJob(ctx context.Context, now time.Time, cfg CloudJobConfig, award CloudJobAward, boundary CloudBoundary) (CloudJobResources, error) {
	if err := cfg.Validate(); err != nil {
		return CloudJobResources{}, err
	}
	if err := validateAward(award); err != nil {
		return CloudJobResources{}, err
	}
	metadata, err := json.Marshal(award)
	if err != nil {
		return CloudJobResources{}, errors.New("invalid Cloud award metadata")
	}
	budget, err := cloudBudget(cfg, award)
	if err != nil {
		return CloudJobResources{}, err
	}
	if err := ctx.Err(); err != nil {
		return CloudJobResources{}, err
	}
	if boundary == nil {
		return CloudJobResources{}, errors.New("Cloud boundary is required")
	}
	b := award.Binding
	deadline, err := cloudLeaseDeadline(now, b, award.LeaseExpiresAt)
	if err != nil {
		return CloudJobResources{}, err
	}
	seconds := jobDeadlineSeconds(deadline, now)
	if seconds <= 0 {
		return CloudJobResources{}, limits.ErrNoTime
	}
	name := jobName(b)
	bindingJSON, err := json.Marshal(b)
	if err != nil {
		return CloudJobResources{}, fmt.Errorf("invalid award binding: %w", err)
	}
	reservation, err := boundary.Reserve(ctx, b, name)
	if err != nil {
		return CloudJobResources{}, fmt.Errorf("reserve Cloud boundary: %w", err)
	}
	if err = validateBoundary(b, name, reservation); err != nil {
		return CloudJobResources{}, errors.Join(err, boundary.Release(ctx, reservation))
	}
	labels := map[string]string{"app": "sparkwing-job", "app.kubernetes.io/managed-by": managedByLauncher, JobLabel: name, TeamLabel: TeamNodeValue(cfg.Namespace, b.Team)}
	reservationJSON, err := json.Marshal(reservation)
	if err != nil {
		return CloudJobResources{}, errors.Join(err, boundary.Release(ctx, reservation))
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cfg.Namespace, Labels: labels, Finalizers: []string{cleanupFinalizer}, Annotations: map[string]string{bindingAnnotation: string(bindingJSON), reservationAnnotation: string(reservationJSON), deadlineAnnotation: deadline.Format(time.RFC3339Nano), launchAnnotation: now.Format(time.RFC3339Nano)}}, Spec: batchv1.JobSpec{BackoffLimit: ptr(int32(0)), ActiveDeadlineSeconds: &seconds, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: map[string]string{doNotDisrupt: "true"}}, Spec: cloudPod(cfg, award, reservation, name, budget)}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cfg.Namespace, Labels: labels}, Immutable: ptr(true), Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"bootstrap": append([]byte(nil), award.Bootstrap...), awardFileName: metadata}}
	return CloudJobResources{Job: job, BootstrapSecret: secret, Reservation: reservation, JobDeadline: deadline}, nil
}

// Kubernetes starts this clock at StartTime, which can lag activation.
func jobDeadlineSeconds(deadline, start time.Time) int64 {
	return int64(deadline.Sub(start)/time.Second) + store.SliceSeconds
}

func cloudLeaseDeadline(now time.Time, binding CloudJobBinding, expires time.Time) (time.Time, error) {
	if expires.IsZero() {
		return time.Time{}, errors.New("Cloud award requires its committed lease deadline")
	}
	deadline, err := cloudDeadline(now, binding)
	if err != nil {
		return time.Time{}, err
	}
	if expires.Before(deadline) {
		deadline = expires
	}
	if !deadline.After(now) {
		return time.Time{}, limits.ErrNoTime
	}
	return deadline, nil
}

func cloudDeadline(now time.Time, binding CloudJobBinding) (time.Time, error) {
	deadline, err := limits.JobDeadline(now, binding.OwnerDeadline, binding.AttemptDeadline)
	if err != nil {
		return time.Time{}, err
	}
	if !binding.ParentCapabilityExpiry.IsZero() && !binding.ParentCapabilityExpiry.After(now) {
		return time.Time{}, limits.ErrNoTime
	}
	if !binding.ParentCapabilityExpiry.IsZero() && (deadline.IsZero() || binding.ParentCapabilityExpiry.Before(deadline)) {
		deadline = binding.ParentCapabilityExpiry
	}
	if deadline.IsZero() {
		return time.Time{}, errors.New("Cloud award requires a bounded execution deadline")
	}
	return deadline, nil
}

func cloudBudget(cfg CloudJobConfig, award CloudJobAward) (corev1.ResourceList, error) {
	if award.CPUCores > cfg.CPUCeiling {
		return nil, fmt.Errorf("requested CPU %g exceeds operator ceiling %g", award.CPUCores, cfg.CPUCeiling)
	}
	if award.Memory > cfg.MemoryCeiling {
		return nil, fmt.Errorf("requested memory %d exceeds operator ceiling %d", award.Memory, cfg.MemoryCeiling)
	}
	cpu, err := resource.ParseQuantity(strconv.FormatFloat(award.CPUCores, 'f', -1, 64))
	if err != nil {
		return nil, fmt.Errorf("invalid CPU %g: %w", award.CPUCores, err)
	}
	// safety: decimal conversion avoids binary-float drift adding a millicore to exact requests.
	rounded := resource.NewMilliQuantity(cpu.MilliValue(), resource.DecimalSI)
	if rounded.AsFloat64Slow() > cfg.CPUCeiling {
		return nil, fmt.Errorf("requested CPU %g requires %s at Kubernetes precision, exceeding operator ceiling %g", award.CPUCores, rounded.String(), cfg.CPUCeiling)
	}
	return corev1.ResourceList{corev1.ResourceCPU: *rounded, corev1.ResourceMemory: *resource.NewQuantity(award.Memory, resource.BinarySI)}, nil
}

func validateAward(a CloudJobAward) error {
	b := a.Binding
	if b.UnitID == "" || b.Team == "" || b.RunID == "" || b.Holder == "" || b.Generation <= 0 || b.Attempt < 0 || len(a.Bootstrap) == 0 {
		return errors.New("award requires unit, team, run, holder, generation and bootstrap, with a nonnegative attempt")
	}
	if math.IsNaN(a.CPUCores) || math.IsInf(a.CPUCores, 0) || a.CPUCores < 0.001 || a.Memory <= 0 {
		return errors.New("award requires positive finite CPU and memory")
	}
	switch b.Kind {
	case CloudPlan:
		if b.NodeID != "" || b.SourceNodeID != "" || b.SourceGeneration != 0 || b.Attempt != 0 {
			return errors.New("plan award requires a run binding without a job or expansion source")
		}
	case CloudEval:
		if b.NodeID == "" || b.SourceNodeID != b.NodeID || b.SourceGeneration < 0 {
			return errors.New("eval award requires its source generation")
		}
	case CloudWork:
		if b.NodeID == "" || b.SourceNodeID != "" || b.SourceGeneration != 0 {
			return errors.New("work award requires a job without an expansion source")
		}
	default:
		return errors.New("unknown Cloud unit kind")
	}
	if b.Kind != CloudPlan && (a.Engine == nil || a.Engine.Version == "" || a.Engine.Pin.Version == "" || a.Engine.ManifestDigest == "") {
		return errors.New("eval and work awards require the accepted engine identity")
	}
	return nil
}

func validateBoundary(binding CloudJobBinding, name string, r CloudNodeReservation) error {
	if r.NodeName == "" || r.NodeUID == "" || r.ReservationID == "" || r.Team != binding.Team || r.JobName != name || r.Award != binding.LeaseRef() {
		return errors.New("Cloud boundary did not reserve a dedicated node for this team and Job")
	}
	return nil
}

func cloudPod(cfg CloudJobConfig, a CloudJobAward, r CloudNodeReservation, secret string, budget corev1.ResourceList) corev1.PodSpec {
	scratch := resource.NewQuantity(limits.JobScratchBytes, resource.BinarySI)
	main := hostContainer(cfg, a, "run-node")
	// safety: one container's ephemeral limit establishes the aggregate pod budget,
	// including sidecar logs and writable layers.
	main.Resources = corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceEphemeralStorage: *scratch}, Limits: corev1.ResourceList{corev1.ResourceEphemeralStorage: *scratch}}
	return corev1.PodSpec{
		Tolerations: teamTolerations(cfg.Namespace, a.Binding.Team),
		NodeName:    r.NodeName, NodeSelector: map[string]string{TeamNodeLabel: TeamNodeValue(cfg.Namespace, a.Binding.Team)}, ServiceAccountName: ServiceAccount, AutomountServiceAccountToken: ptr(false), EnableServiceLinks: ptr(false), RestartPolicy: corev1.RestartPolicyNever,
		// safety: a user-namespaced Pod's IDs map to unprivileged host IDs, so
		// nothing in it, including the Docker sidecar's root, holds node root.
		HostUsers:                     ptr(jobHostUsers),
		SecurityContext:               jobPodSecurity(),
		TerminationGracePeriodSeconds: ptr(int64(0)),
		// safety: the pod cgroup shares the awarded CPU and memory with DinD instead of doubling the billed allocation.
		Resources:      &corev1.ResourceRequirements{Requests: budget, Limits: budget.DeepCopy()},
		Volumes:        []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: scratch}}}, {Name: "bootstrap", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secret, DefaultMode: ptr(int32(0o400))}}}},
		InitContainers: []corev1.Container{dockerContainer(cfg), hostContainer(cfg, a, "fetch-source")}, Containers: []corev1.Container{main},
	}
}

func hostContainer(cfg CloudJobConfig, a CloudJobAward, verb string) corev1.Container {
	b := a.Binding
	args := []string{verb, "--controller", cfg.ControllerURL, "--run", string(b.RunID), "--generation", strconv.FormatInt(int64(b.Generation), 10), "--source-dir", "/tmp/source", "--module-cache", "/tmp/modules", "--bootstrap-file", bootstrapPath}
	if b.NodeID != "" {
		args = append(args, "--job", string(b.NodeID))
	}
	return corev1.Container{Name: verb, Image: cfg.RunnerImage, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{JobBinary}, Args: args, WorkingDir: "/tmp", Env: []corev1.EnvVar{{Name: "HOME", Value: "/tmp"}, {Name: "DOCKER_HOST", Value: "unix:///tmp/docker.sock"}}, VolumeMounts: []corev1.VolumeMount{{Name: "scratch", MountPath: "/tmp"}, {Name: "bootstrap", MountPath: "/etc/sparkwing/bootstrap", ReadOnly: true}}, SecurityContext: workloadSecurity()}
}

// dockerCapabilities are namespaced to the Pod's user namespace. PTRACE lets
// dockerd join the network namespace of a container that runs as non-root.
var dockerCapabilities = []corev1.Capability{"SYS_ADMIN", "NET_ADMIN", "SYS_PTRACE"}

// DockerSeccompProfile is the Localhost profile, relative to the kubelet's
// seccomp root, that node bootstrap installs from the chart's
// files/seccomp/sparkwing-dind.json. A node without it cannot start Job Pods.
const DockerSeccompProfile = "profiles/sparkwing-dind.json"

var (
	dockerCommand = []string{"dockerd-entrypoint.sh"}
	dockerArgs    = []string{"dockerd", "--host=unix:///tmp/docker.sock", "--data-root=/tmp/docker", "--exec-root=/tmp/docker-exec", "--pidfile=/tmp/docker.pid", "--tls=false"}
)

func dockerContainer(cfg CloudJobConfig) corev1.Container {
	return corev1.Container{Name: "docker", Image: cfg.DockerImage, ImagePullPolicy: corev1.PullIfNotPresent, RestartPolicy: ptr(corev1.ContainerRestartPolicyAlways), Command: slices.Clone(dockerCommand), Args: slices.Clone(dockerArgs), SecurityContext: dockerSecurity(), VolumeMounts: []corev1.VolumeMount{{Name: "scratch", MountPath: "/tmp"}}}
}

// jobPodSecurity runs every Job container as root of the Pod's user
// namespace, which maps to an unprivileged host UID, so customer code keeps
// root (apt-get, the Docker socket) without holding node root.
func jobPodSecurity() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{RunAsUser: ptr(int64(0)), RunAsGroup: ptr(int64(0)), RunAsNonRoot: ptr(false), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
}

// workloadSecurity keeps namespace root's default capabilities and nothing more.
func workloadSecurity() *corev1.SecurityContext {
	return &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false)}
}

// dockerSecurity is dockerd's addition to namespace root, never privileged:
// its capabilities, the unmasked /proc it mounts for containers and the
// Unconfined AppArmor (AL2023 has no AppArmor LSM) stay inside the Pod's user
// namespace. Its seccomp profile is Docker's default with the CAP_SYS_ADMIN
// and CAP_SYS_PTRACE syscalls dind needs, plus pivot_root for nested runc,
// with io_uring, keyrings, clone3, bpf, perf, userfaultfd, kexec, modules,
// open_by_handle_at and fanotify refused. The filter covers every container
// the customer starts, because seccomp filters stack.
func dockerSecurity() *corev1.SecurityContext {
	return &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: slices.Clone(dockerCapabilities)}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeLocalhost, LocalhostProfile: ptr(DockerSeccompProfile)}, AppArmorProfile: &corev1.AppArmorProfile{Type: corev1.AppArmorProfileTypeUnconfined}, ProcMount: ptr(corev1.UnmaskedProcMount)}
}

func jobName(b CloudJobBinding) string {
	raw := fmt.Sprintf("%q/%q/%q/%q/%q/%d/%d/%q/%d", b.UnitID, b.Team, b.RunID, b.NodeID, b.Kind, b.Generation, b.Attempt, b.SourceNodeID, b.SourceGeneration)
	sum := sha256.Sum256([]byte(raw))
	return "sw-" + hex.EncodeToString(sum[:24])
}

func ptr[T any](value T) *T { return &value }

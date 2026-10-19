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
)

// Cloud Jobs use a separate API identity, and labels bind Kubernetes objects to their reservation.
const (
	ServiceAccount        = "sparkwing-customer-job"
	JobLabel              = "sparkwing.dev/job"
	JobBinary             = "sparkwing-runner"
	TeamLabel             = "sparkwing.dev/team"
	TeamNodeLabel         = "node-restriction.kubernetes.io/sparkwing-team-node"
	managedByLauncher     = "sparkwing-launcher"
	bindingAnnotation     = "sparkwing.dev/award-binding"
	deadlineAnnotation    = "sparkwing.dev/job-deadline"
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
	deadline, err := cloudDeadline(now, b)
	if err != nil {
		return CloudJobResources{}, err
	}
	seconds := int64(deadline.Sub(now) / time.Second)
	if seconds <= 0 {
		return CloudJobResources{}, limits.ErrNoTime
	}
	name := jobName(b)
	bindingJSON, err := json.Marshal(b)
	if err != nil {
		return CloudJobResources{}, fmt.Errorf("invalid award binding: %w", err)
	}
	reservation, network, err := boundary.Reserve(ctx, b, name)
	if err != nil {
		return CloudJobResources{}, fmt.Errorf("reserve Cloud boundary: %w", err)
	}
	if err = validateBoundary(cfg.Namespace, b, name, reservation, network); err != nil {
		return CloudJobResources{}, errors.Join(err, boundary.Release(ctx, reservation))
	}
	labels := map[string]string{"app": "sparkwing-job", "app.kubernetes.io/managed-by": managedByLauncher, JobLabel: name, TeamLabel: labelHash(b.Team)}
	reservationJSON, err := json.Marshal(reservation)
	if err != nil {
		return CloudJobResources{}, errors.Join(err, boundary.Release(ctx, reservation))
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cfg.Namespace, Labels: labels, Finalizers: []string{cleanupFinalizer}, Annotations: map[string]string{bindingAnnotation: string(bindingJSON), reservationAnnotation: string(reservationJSON), deadlineAnnotation: deadline.Format(time.RFC3339Nano)}}, Spec: batchv1.JobSpec{BackoffLimit: ptr(int32(0)), ActiveDeadlineSeconds: &seconds, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: map[string]string{doNotDisrupt: "true"}}, Spec: cloudPod(cfg, award, reservation, name, budget)}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cfg.Namespace, Labels: labels}, Immutable: ptr(true), Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"bootstrap": append([]byte(nil), award.Bootstrap...), awardFileName: metadata}}
	return CloudJobResources{Job: job, BootstrapSecret: secret, Reservation: reservation, Network: network, JobDeadline: deadline}, nil
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
		if b.NodeID != "" || b.SourceNodeID != "" || b.SourceGeneration != 0 {
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

func validateBoundary(namespace string, binding CloudJobBinding, name string, r CloudNodeReservation, n CloudNetworkEvidence) error {
	if r.NodeName == "" || r.NodeUID == "" || r.ReservationID == "" || r.Team != binding.Team || r.JobName != name || r.Award != binding.LeaseRef() {
		return errors.New("Cloud boundary did not reserve a dedicated node for this team and Job")
	}
	if n.Namespace != namespace || n.NodeUID != r.NodeUID || n.PolicyRevision == "" || !n.DefaultDenyEgressEnforced || !n.RequiredDestinationsAllowed || !n.DaemonSetPortsInventoried || !n.EscapeProbePassed {
		return errors.New("Cloud boundary has no complete network enforcement evidence")
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
		Tolerations: teamTolerations(a.Binding.Team),
		NodeName:    r.NodeName, NodeSelector: map[string]string{TeamNodeLabel: labelHash(a.Binding.Team)}, ServiceAccountName: ServiceAccount, AutomountServiceAccountToken: ptr(false), EnableServiceLinks: ptr(false), RestartPolicy: corev1.RestartPolicyNever,
		// safety: a user-namespaced Pod's IDs map to unprivileged host IDs, so
		// nothing in it, including the Docker sidecar's root, holds node root.
		HostUsers:       ptr(false),
		SecurityContext: &corev1.PodSecurityContext{RunAsUser: ptr(jobUser), RunAsGroup: ptr(jobUser), FSGroup: ptr(jobUser), RunAsNonRoot: ptr(true), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		// safety: the pod cgroup shares the awarded CPU and memory with DinD instead of doubling the billed allocation.
		Resources:      &corev1.ResourceRequirements{Requests: budget, Limits: budget.DeepCopy()},
		Volumes:        []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: scratch}}}, {Name: "bootstrap", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secret, DefaultMode: ptr(int32(0o440))}}}},
		InitContainers: []corev1.Container{dockerContainer(cfg), hostContainer(cfg, a, "fetch-source")}, Containers: []corev1.Container{main},
	}
}

func hostContainer(cfg CloudJobConfig, a CloudJobAward, verb string) corev1.Container {
	b := a.Binding
	args := []string{verb, "--controller", cfg.ControllerURL, "--run", b.RunID, "--generation", strconv.FormatInt(b.Generation, 10), "--source-dir", "/tmp/source", "--module-cache", "/tmp/modules", "--bootstrap-file", bootstrapPath}
	if b.NodeID != "" {
		args = append(args, "--job", b.NodeID)
	}
	return corev1.Container{Name: verb, Image: cfg.RunnerImage, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{JobBinary}, Args: args, WorkingDir: "/tmp", Env: []corev1.EnvVar{{Name: "HOME", Value: "/tmp"}, {Name: "DOCKER_HOST", Value: "unix:///tmp/docker.sock"}}, VolumeMounts: []corev1.VolumeMount{{Name: "scratch", MountPath: "/tmp"}, {Name: "bootstrap", MountPath: "/etc/sparkwing/bootstrap", ReadOnly: true}}}
}

// jobUser runs every Job container except the Docker sidecar.
const jobUser = int64(1000)

// dockerCapabilities are namespaced to the Pod's user namespace. PTRACE lets
// dockerd join the network namespace of a container that runs as non-root.
var dockerCapabilities = []corev1.Capability{"SYS_ADMIN", "NET_ADMIN", "SYS_PTRACE"}

var (
	// safety: given dockerd, the image's entrypoint adds no TCP listener.
	dockerCommand = []string{"dockerd-entrypoint.sh"}
	dockerArgs    = []string{"dockerd", "--host=unix:///tmp/docker.sock", "--data-root=/tmp/docker", "--exec-root=/tmp/docker-exec", "--pidfile=/tmp/docker.pid", "--tls=false"}
	dockerProbe   = []string{"docker", "--host=unix:///tmp/docker.sock", "info"}
)

func dockerContainer(cfg CloudJobConfig) corev1.Container {
	return corev1.Container{Name: "docker", Image: cfg.DockerImage, ImagePullPolicy: corev1.PullIfNotPresent, RestartPolicy: ptr(corev1.ContainerRestartPolicyAlways), Command: slices.Clone(dockerCommand), Args: slices.Clone(dockerArgs), SecurityContext: dockerSecurity(), VolumeMounts: []corev1.VolumeMount{{Name: "scratch", MountPath: "/tmp"}}, StartupProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: slices.Clone(dockerProbe)}}, PeriodSeconds: 1, FailureThreshold: math.MaxInt32}}
}

// dockerSecurity runs dockerd as root of the Pod's user namespace, never
// privileged: its capabilities, the Unconfined seccomp and AppArmor that let
// it mount and unshare, and the unmasked /proc it mounts for containers all
// stay inside that namespace. Node acceptance on AL2023 proved this set.
func dockerSecurity() *corev1.SecurityContext {
	return &corev1.SecurityContext{RunAsUser: ptr(int64(0)), RunAsGroup: ptr(int64(0)), RunAsNonRoot: ptr(false), Capabilities: &corev1.Capabilities{Add: slices.Clone(dockerCapabilities)}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined}, AppArmorProfile: &corev1.AppArmorProfile{Type: corev1.AppArmorProfileTypeUnconfined}, ProcMount: ptr(corev1.UnmaskedProcMount)}
}

func jobName(b CloudJobBinding) string {
	raw := fmt.Sprintf("%q/%q/%q/%q/%q/%d/%d/%q/%d", b.UnitID, b.Team, b.RunID, b.NodeID, b.Kind, b.Generation, b.Attempt, b.SourceNodeID, b.SourceGeneration)
	sum := sha256.Sum256([]byte(raw))
	return "sw-" + hex.EncodeToString(sum[:24])
}

func labelHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256-" + hex.EncodeToString(sum[:20])
}
func ptr[T any](value T) *T { return &value }

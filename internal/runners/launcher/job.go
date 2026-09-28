// Package launcher runs controller-dispatched nodes as Kubernetes Jobs. It
// claims each ready node from the controller and builds the node's Job in
// trusted code; it never runs a pipeline's code itself.
package launcher

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sparkwing-dev/sparkwing/internal/runners/k8s"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ServiceAccount is the one service account every launcher Job runs as. Its
// manifest carries no cloud-role annotation, and the Job mounts no token.
const ServiceAccount = "sparkwing-customer-job"

// JobLabel names every launcher Job's pod by its Job, so no node runs two.
const JobLabel = "sparkwing.dev/job"

// MaxDeadline bounds a Job's life, and with it the life of its claim token.
const MaxDeadline = store.MaxClaimTokenLifetime

// DeadlineMargin is taken off a claim token's lifetime for its Job's deadline.
const DeadlineMargin = 30 * time.Second

const (
	jobUID              = 65534
	jobTTLAfterFinished = 60
	scratchVolume       = "scratch"
	minimumClassCores   = 2
	managedByLauncher   = "sparkwing-launcher"
	jobAppName          = "sparkwing-job"
	runNodeVerb         = "run-node"
	jobBearerEnv        = "SPARKWING_AGENT_TOKEN"
	defaultScratchLimit = "20Gi"
)

var digestPinned = regexp.MustCompile(`^[a-z0-9][a-z0-9.:/_-]*@sha256:[0-9a-f]{64}$`)

// Config is the operator's whole say over a launcher Job. Nothing a pipeline
// declares reaches a Job except its node's cpu class, which these ceilings
// cap, and the pipeline's own run and node IDs.
type Config struct {
	Namespace     string
	Image         string
	ControllerURL string
	LogsURL       string
	CPUCeiling    float64
	MemoryCeiling int64
	Deadline      time.Duration
	ScratchLimit  string
}

// Validate refuses a Config that would let a Job run unpinned, uncapped or
// past its claim token.
func (c Config) Validate() error {
	var errs []error
	if c.Namespace == "" || c.ControllerURL == "" {
		errs = append(errs, errors.New("the namespace and the controller URL are required"))
	}
	if !digestPinned.MatchString(c.Image) {
		errs = append(errs, fmt.Errorf("image %q is not pinned by a sha256 digest", c.Image))
	}
	if c.CPUCeiling <= 0 || c.MemoryCeiling <= 0 {
		errs = append(errs, errors.New("a cpu and a memory ceiling are required"))
	}
	if c.Deadline <= 0 || c.Deadline > MaxDeadline {
		errs = append(errs, fmt.Errorf("the deadline must be positive and at most %s", MaxDeadline))
	}
	if _, err := resource.ParseQuantity(c.scratchLimit()); err != nil {
		errs = append(errs, fmt.Errorf("scratch limit: %w", err))
	}
	return errors.Join(errs...)
}

func (c Config) scratchLimit() string {
	if c.ScratchLimit == "" {
		return defaultScratchLimit
	}
	return c.ScratchLimit
}

// BuildJob returns the Job that runs claim's node. Every field is fixed here
// or by cfg; the claim contributes its IDs, its token, its class only through
// the ceilings, and its token's lifetime as the Job's deadline.
func BuildJob(cfg Config, claim store.LaunchClaim) *batchv1.Job {
	name := k8s.JobName(claim.RunID, claim.NodeID, int(claim.Generation))
	team := k8s.TeamLabelValue(string(claim.Team))
	labels := map[string]string{
		"app.kubernetes.io/name":       jobAppName,
		"app.kubernetes.io/managed-by": managedByLauncher,
		JobLabel:                       name,
		k8s.TeamLabel:                  team,
	}
	env := []corev1.EnvVar{
		{Name: "SPARKWING_CONTROLLER_URL", Value: cfg.ControllerURL},
		{Name: "SPARKWING_RUN_ID", Value: claim.RunID},
		{Name: "SPARKWING_NODE_ID", Value: claim.NodeID},
		{Name: jobBearerEnv, Value: claim.Token},
		{Name: "HOME", Value: "/tmp"},
		{Name: "SPARKWING_HOME", Value: "/tmp/sparkwing"},
		{Name: "GOCACHE", Value: "/tmp/go-build"},
		{Name: "GOMODCACHE", Value: "/tmp/go-mod"},
	}
	if cfg.LogsURL != "" {
		env = append(env, corev1.EnvVar{Name: "SPARKWING_LOGS_URL", Value: cfg.LogsURL})
	}
	scratch := resource.MustParse(cfg.scratchLimit())
	// safety: the lifetime runs from the claim on the controller's clock, so the
	// margin covers the response and the Job create, and the Job never outlives
	// the credential it carries whatever the launcher's clock says.
	deadline := max(claim.LifetimeSecs-int64(DeadlineMargin/time.Second), 1)
	backoff, ttl := int32(0), int32(jobTTLAfterFinished)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cfg.Namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			ActiveDeadlineSeconds:   &deadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					// safety: consolidation would evict a running node, and a
					// node runs once per claim.
					Annotations: map[string]string{k8s.KarpenterDoNotDisrupt: "true"},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					ServiceAccountName:           ServiceAccount,
					AutomountServiceAccountToken: ptr(false),
					// safety: service links would put every Service in the
					// namespace into the pipeline's environment.
					EnableServiceLinks: ptr(false),
					NodeSelector: map[string]string{
						k8s.CPUBandKey:    k8s.CPUBandSmall,
						k8s.TeamNodeLabel: team,
					},
					Tolerations: []corev1.Toleration{{
						Key: k8s.CPUBandKey, Operator: corev1.TolerationOpEqual,
						Value: k8s.CPUBandSmall, Effect: corev1.TaintEffectNoSchedule,
					}},
					Affinity: oneJobPerMachine(),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   ptr(true),
						RunAsUser:      ptr(int64(jobUID)),
						RunAsGroup:     ptr(int64(jobUID)),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:            "node",
						Image:           cfg.Image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{k8s.JobBinary},
						Args:            []string{runNodeVerb, claim.RunID, claim.NodeID},
						Env:             env,
						Resources:       classResources(cfg, claim.Class),
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr(false),
							ReadOnlyRootFilesystem:   ptr(true),
							RunAsNonRoot:             ptr(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
						VolumeMounts: []corev1.VolumeMount{{Name: scratchVolume, MountPath: "/tmp"}},
					}},
					Volumes: []corev1.Volume{{
						Name:         scratchVolume,
						VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &scratch}},
					}},
				},
			},
		},
	}
}

// safety: requests equal limits, so the pod is Guaranteed and no neighbour's
// burst can take what the class bills for; the ceilings cap both.
func classResources(cfg Config, class store.CPUClass) corev1.ResourceRequirements {
	cores := max(class.Cores, minimumClassCores)
	memory := class.MemoryBytes
	if memory <= 0 {
		memory = store.CPUClassMemoryBytes(cores)
	}
	cpu := resource.NewMilliQuantity(int64(min(float64(cores)-k8s.BandCPUHeadroom, cfg.CPUCeiling)*1000), resource.DecimalSI)
	mem := resource.NewQuantity(min(memory, cfg.MemoryCeiling), resource.BinarySI)
	list := corev1.ResourceList{corev1.ResourceCPU: *cpu, corev1.ResourceMemory: *mem}
	return corev1.ResourceRequirements{Requests: list, Limits: list.DeepCopy()}
}

// safety: a Job's machine is billed at its class and shared with no other Job,
// whatever namespace or team that Job belongs to.
func oneJobPerMachine() *corev1.Affinity {
	return &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			LabelSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: JobLabel, Operator: metav1.LabelSelectorOpExists},
			}},
			NamespaceSelector: &metav1.LabelSelector{},
			TopologyKey:       corev1.LabelHostname,
		}},
	}}
}

func ptr[T any](v T) *T { return &v }

// Package k8s is the K8s-Job-per-node Runner implementation.
//
// For each dispatched node, Runner.RunNode creates a batch/v1 Job
// named deterministically on (runID, nodeID, attempt) so duplicate
// dispatch collides on the API server rather than spawning a racing
// second pod. The pod runs `sparkwing run-node <runID> <nodeID>`, which
// executes the node against HTTP backends (state + logs + locks) and
// writes the terminal state. When the Job succeeds or fails, this
// runner reads the resulting node row from the controller and maps
// it to a runner.Result the orchestrator understands.
//
// v1 uses polling (not informers) to track Job status. Polling keeps
// the implementation small and has acceptable overhead at the node
// counts a single orchestrator sees; informers + a shared workqueue
// come in session 3 (warm pool) where every runner in a pool watches
// the same Job objects.
package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Config captures the knobs a caller tunes per deployment. Defaults
// are chosen for prod; a Kind-based dev loop might tune
// Namespace + Image and leave the rest.
type Config struct {
	// Namespace where Jobs are created. Must match the SA's binding.
	Namespace string

	// Image is the runner image containing the compiled `.sparkwing`
	// binary and whatever the pipeline's jobs need at runtime. Same
	// image used for the orchestrator pod today.
	Image string

	// ImagePullSecret, when set, is attached to every Job's pod spec.
	ImagePullSecret string

	// ServiceAccountName is the K8s SA that Job pods run under. Needs
	// only egress to the controller + logs-service; no K8s API access.
	ServiceAccountName string

	// ControllerURL + LogsURL are what the pod is told to talk to.
	// Typically in-cluster service URLs.
	ControllerURL string
	LogsURL       string

	// ArtifactStoreURL, when set, is stamped on every Job pod as
	// SPARKWING_CACHE_URL so the spawned runner opens the same
	// content-addressed artifact store the rest of the run uses to
	// publish node outputs and stage consumed inputs. Empty disables
	// artifacts for the pod.
	ArtifactStoreURL string

	// NodeSelector + Tolerations let the caller pin runner pods to a
	// specific pool (GPU nodes, spot nodes, etc.). v1 copies the same
	// selector to every Job; Requires-style per-job routing lands in a
	// later session.
	NodeSelector map[string]string
	Tolerations  []corev1.Toleration

	// CPU + Memory requests/limits applied to every runner pod. Keep
	// modest defaults; let pipeline authors override via pod-spec
	// overrides in a later session.
	CPURequest    string // e.g. "100m"
	CPULimit      string // e.g. "2"
	MemoryRequest string // e.g. "256Mi"
	MemoryLimit   string // e.g. "2Gi"

	// BackoffLimit is the Job-level retry cap. Sparkwing's own Retry
	// modifier runs inside the pod; this is a backstop for pod-level
	// failures (image pull errors, OOMKills). 0 means no K8s-side
	// retry, which matches sparkwing semantics: the node's Retry
	// modifier handles retries within a pod; if the pod dies entirely
	// that's a hard failure surfaced to the operator.
	BackoffLimit int32

	// AgentToken, when non-empty, is stamped on every Job pod as
	// SPARKWING_AGENT_TOKEN so the spawned runner can authenticate
	// its controller + logs-service calls. Closes the FOLLOWUPS #2
	// v0 gap where K8sRunner fallback Jobs 401'd under auth. The
	// worker passes its own token value through; per-Job tokens are
	// a later session.
	AgentToken string

	// PollInterval is how often we poll Job status. Matches the
	// reaper's granularity well; don't drop below 500ms or every
	// concurrent node hammers the API server.
	PollInterval time.Duration

	// TTLSecondsAfterFinished auto-cleans terminated Jobs. 5m is a
	// sane default: short enough to keep the cluster tidy, long
	// enough for an operator to poke at a failed pod.
	TTLSecondsAfterFinished int32
}

// Runner is a runner.Runner backed by one K8s Job per node.
type Runner struct {
	client        kubernetes.Interface
	ctrl          *client.Client
	cfg           Config
	logger        *slog.Logger
	labelInstance string // kubernetes.io/instance label per orchestrator
}

// New constructs a K8sRunner. `kcli` is the client-go kube interface
// (typically kubernetes.NewForConfig(rest.InClusterConfig())). `ctrl`
// is the state client used to read the node's terminal row after the
// Job succeeds.
func New(kcli kubernetes.Interface, ctrl *client.Client, cfg Config, logger *slog.Logger) *Runner {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.TTLSecondsAfterFinished == 0 {
		cfg.TTLSecondsAfterFinished = 300
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		client:        kcli,
		ctrl:          ctrl,
		cfg:           cfg,
		logger:        logger,
		labelInstance: "sparkwing-orchestrator",
	}
}

var _ runner.Runner = (*Runner)(nil)

// RunNode is the runner.Runner entry point. Creates the Job, polls
// until it terminates, reads the terminal node row from the
// controller, and maps to Result.
func (r *Runner) RunNode(ctx context.Context, req runner.Request) runner.Result {
	name := JobName(req.RunID, req.NodeID, 0)
	job := r.buildJob(name, req, r.resolveResources(ctx, req))

	// safety: idempotent on AlreadyExists; a racing orchestrator may have dispatched the same node
	_, err := r.client.BatchV1().Jobs(r.cfg.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return runner.Result{
			Outcome: sparkwing.Failed,
			Err:     fmt.Errorf("create Job %s: %w", name, err),
		}
	}

	defer func() {
		if ctx.Err() != nil {
			policy := metav1.DeletePropagationBackground
			delCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = r.client.BatchV1().Jobs(r.cfg.Namespace).Delete(delCtx, name,
				metav1.DeleteOptions{PropagationPolicy: &policy})
		}
	}()

	hbCtx, stopHB := context.WithCancel(ctx)
	defer stopHB()
	go heartbeatLoop(hbCtx, r.ctrl, req.RunID, req.NodeID, r.logger)

	_ = r.ctrl.UpdateNodeActivity(ctx, req.RunID, req.NodeID, "job created")
	var lastPhase string

	t := time.NewTicker(r.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return runner.Result{Outcome: sparkwing.Cancelled, Err: ctx.Err()}
		case <-t.C:
			j, err := r.client.BatchV1().Jobs(r.cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				r.logger.Warn("job poll failed", "job", name, "err", err)
				continue
			}
			if isJobDone(j) {
				return r.readFinalResult(ctx, req, j)
			}
			if phase := r.observePodPhase(ctx, name); phase != "" && phase != lastPhase {
				_ = r.ctrl.UpdateNodeActivity(ctx, req.RunID, req.NodeID, phase)
				lastPhase = phase
			}
		}
	}
}

// observePodPhase returns a human-readable phase string for the Job's
// pod, or "" when no pod exists yet / the API call fails. We pick the
// newest pod (owner-ref match) and fold container-waiting reasons in
// when the pod is in Pending/ContainerCreating, since those carry the
// useful signal ("ImagePullBackOff", "ErrImagePull") that a bare
// phase string would hide.
func (r *Runner) observePodPhase(ctx context.Context, jobName string) string {
	pods, err := r.client.CoreV1().Pods(r.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("batch.kubernetes.io/job-name=%s", jobName),
	})
	if err != nil || len(pods.Items) == 0 {
		return ""
	}
	// safety: use the newest pod; retries create new pods with fresh CreationTimestamps
	p := pods.Items[0]
	for _, cand := range pods.Items[1:] {
		if cand.CreationTimestamp.After(p.CreationTimestamp.Time) {
			p = cand
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
	}
	return string(p.Status.Phase)
}

// heartbeatLoop stamps last_heartbeat every 5s until ctx cancels. A
// missed heartbeat is a UI annoyance, not a correctness issue; log
// errors at debug so they don't drown out real warnings.
func heartbeatLoop(ctx context.Context, ctrl *client.Client, runID, nodeID string, logger *slog.Logger) {
	_ = ctrl.TouchNodeHeartbeat(ctx, runID, nodeID)
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := ctrl.TouchNodeHeartbeat(ctx, runID, nodeID); err != nil {
				logger.Debug("k8s: heartbeat failed",
					"run_id", runID, "node_id", nodeID, "err", err)
			}
		}
	}
}

// readFinalResult fetches the node row the pod wrote and maps it.
// If the pod crashed before writing, the node is still "running" on
// the controller side; we return a synthesized Failed result so the
// orchestrator sees something deterministic.
func (r *Runner) readFinalResult(ctx context.Context, req runner.Request, j *batchv1.Job) runner.Result {
	n, err := r.ctrl.GetNode(ctx, req.RunID, req.NodeID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return runner.Result{
				Outcome: sparkwing.Failed,
				Err:     fmt.Errorf("K8sRunner: Job %s finished but node row absent on controller", j.Name),
			}
		}
		return runner.Result{
			Outcome: sparkwing.Failed,
			Err:     fmt.Errorf("K8sRunner: read node %s/%s: %w", req.RunID, req.NodeID, err),
		}
	}

	// safety: pass Output as raw []byte; unmarshaling here would erase the typed shape and break Ref[T].Get
	oc := sparkwing.Outcome(n.Outcome)
	res := runner.Result{Outcome: oc}
	if n.Error != "" {
		res.Err = errors.New(n.Error)
	}
	if len(n.Output) > 0 {
		res.Output = n.Output
	}
	// safety: pod crashed before writing terminal state; synthesize Failed so the orchestrator sees something deterministic
	if n.Status != "done" {
		res.Outcome = sparkwing.Failed
		reason, exitCode := r.inspectTerminatedPod(ctx, j)
		errMsg := fmt.Sprintf("pod %s exited without writing terminal state", j.Name)
		if reason == store.FailureOOMKilled {
			errMsg = fmt.Sprintf("pod %s OOMKilled", j.Name)
		}
		if res.Err == nil {
			res.Err = errors.New(errMsg)
		}
		_ = r.ctrl.FinishNodeWithReason(ctx, req.RunID, req.NodeID,
			string(sparkwing.Failed), errMsg, nil, reason, exitCode)
	}
	return res
}

// inspectTerminatedPod returns the structured failure reason and
// exit code for the first terminated container it finds on the
// Job's pods. Returns (FailureUnknown, nil) when the lookup fails
// or no terminated state is visible; the caller falls back to a
// generic Failed outcome.
func (r *Runner) inspectTerminatedPod(ctx context.Context, j *batchv1.Job) (string, *int) {
	if j == nil {
		return store.FailureUnknown, nil
	}
	selector := fmt.Sprintf("job-name=%s", j.Name)
	pods, err := r.client.CoreV1().Pods(r.cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil || pods == nil || len(pods.Items) == 0 {
		return store.FailureUnknown, nil
	}
	for _, p := range pods.Items {
		for _, cs := range p.Status.ContainerStatuses {
			if cs.State.Terminated == nil {
				continue
			}
			term := cs.State.Terminated
			code := int(term.ExitCode)
			if term.Reason == "OOMKilled" {
				oom := code
				if oom == 0 {
					oom = 137
				}
				return store.FailureOOMKilled, &oom
			}
			if code != 0 {
				return store.FailureUnknown, &code
			}
		}
	}
	return store.FailureUnknown, nil
}

// JobName is the deterministic K8s name for one node's Job. Kept
// exported so tests (and the manifest reviewer) can reason about it.
// The attempt suffix is reserved for retry handling; today it's
// always 0.
//
// K8s names: lowercase alphanumerics + '-', ≤63 chars. We combine a
// short sha256 prefix of (runID+nodeID+attempt) with a human-readable
// suffix built from the truncated nodeID so operators can still
// eyeball which node a Job belongs to. The hash makes collisions
// between runs (even same-minute retries) impossible in practice.
func JobName(runID, nodeID string, attempt int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%d", runID, nodeID, attempt)))
	hashSeg := hex.EncodeToString(h[:])[:10]
	// safety: 47 cap keeps "sw-"(3)+hash(10)+"-"(1)+nodeSeg(≤47)+"-0"(2)=63 within K8s limit
	nodeSeg := sanitizeK8sName(truncate(nodeID, 47))
	name := fmt.Sprintf("sw-%s-%s-%d", hashSeg, nodeSeg, attempt)
	return truncate(name, 63)
}

const (
	// podCPULimitFactor sizes a runner pod's CPU limit above its request.
	// CPU is compressible -- the kernel throttles rather than kills -- so a
	// generous ceiling lets a bursty node use spare cores without letting a
	// runaway starve its neighbours.
	podCPULimitFactor = 2.0
	// podMemoryLimitFactor sizes a runner pod's memory limit above its
	// request. Memory is not compressible: overshoot means an OOM kill, so
	// the ceiling stays tight, just enough headroom to absorb a modest spike
	// past the measured peak.
	podMemoryLimitFactor = 1.25
	// podDefaultRefCPU is the machine size handed to capacity.Resolve for
	// the cold-start default tier. Its cores are unused -- the pod default
	// falls back to the configured request rather than a share of some
	// machine -- so any positive value serves.
	podDefaultRefCPU = 1
)

// resolveResources sizes a node's pod from the same resolution the local
// daemon uses: an explicit .Resources() pin wins, else the node's measured
// profile once it has enough samples, else the conservative configured
// default. It reports an applied pin back to the controller so cluster-side
// drift can judge it against measured peaks. Every controller lookup is
// best-effort: a failed profile read simply falls back to the pin or the
// default rather than failing the node.
func (r *Runner) resolveResources(ctx context.Context, req runner.Request) capacity.Resolution {
	pipeline := req.Pipeline
	if pipeline == "" {
		if run, err := r.ctrl.GetRun(ctx, req.RunID); err == nil && run != nil {
			pipeline = run.Pipeline
		}
	}
	pin := nodePin(req.Node)
	var profile *store.PipelineProfile
	if pipeline != "" {
		profile, _ = r.ctrl.GetPipelineProfile(ctx, pipeline, req.NodeID)
	}
	if pipeline != "" {
		if pin.Empty() {
			_ = r.ctrl.SetPipelinePin(ctx, pipeline, req.NodeID, 0, 0)
		} else {
			_ = r.ctrl.SetPipelinePin(ctx, pipeline, req.NodeID, pin.Cores, pin.MemoryBytes)
		}
	}
	return capacity.Resolve(pin, profile, podDefaultRefCPU, "")
}

// nodePin flattens a node's explicit .Resources() declaration to a
// capacity.Pin, or nil when the node (or its plan) declared none.
func nodePin(node *sparkwing.JobNode) *capacity.Pin {
	if node == nil {
		return nil
	}
	h := node.ResourceHints()
	if h == nil || (h.Cores <= 0 && h.MemoryBytes <= 0) {
		return nil
	}
	return &capacity.Pin{Cores: h.Cores, MemoryBytes: h.MemoryBytes}
}

func (r *Runner) buildJob(name string, req runner.Request, res capacity.Resolution) *batchv1.Job {
	env := []corev1.EnvVar{
		{Name: "SPARKWING_CONTROLLER_URL", Value: r.cfg.ControllerURL},
		{Name: "SPARKWING_RUN_ID", Value: req.RunID},
		{Name: "SPARKWING_NODE_ID", Value: req.NodeID},
		// safety: pod runs as nonroot; SPARKWING_HOME must be a writable path or DefaultPaths mkdir fails
		{Name: "SPARKWING_HOME", Value: "/tmp/sparkwing"},
	}
	if r.cfg.LogsURL != "" {
		env = append(env, corev1.EnvVar{Name: "SPARKWING_LOGS_URL", Value: r.cfg.LogsURL})
	}
	if r.cfg.ArtifactStoreURL != "" {
		env = append(env, corev1.EnvVar{Name: "SPARKWING_CACHE_URL", Value: r.cfg.ArtifactStoreURL})
	}
	if r.cfg.AgentToken != "" {
		env = append(env, corev1.EnvVar{Name: "SPARKWING_AGENT_TOKEN", Value: r.cfg.AgentToken})
	}

	container := corev1.Container{
		Name:            "runner",
		Image:           r.cfg.Image,
		ImagePullPolicy: corev1.PullAlways,
		Command:         []string{"sparkwing"},
		Args:            []string{"run-node", req.RunID, req.NodeID},
		Env:             env,
		Resources:       podResources(res, r.cfg),
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: boolPtr(false),
			RunAsNonRoot:             boolPtr(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
	}

	podSpec := corev1.PodSpec{
		RestartPolicy:      corev1.RestartPolicyNever,
		ServiceAccountName: r.cfg.ServiceAccountName,
		NodeSelector:       r.cfg.NodeSelector,
		Tolerations:        r.cfg.Tolerations,
		Containers:         []corev1.Container{container},
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot: boolPtr(true),
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		},
	}
	if r.cfg.ImagePullSecret != "" {
		podSpec.ImagePullSecrets = []corev1.LocalObjectReference{
			{Name: r.cfg.ImagePullSecret},
		}
	}

	labels := map[string]string{
		"app.kubernetes.io/name":       "sparkwing-runner",
		"app.kubernetes.io/managed-by": r.labelInstance,
		"sparkwing.dev/run-id":         sanitizeK8sName(truncate(req.RunID, 63)),
		"sparkwing.dev/node-id":        sanitizeK8sName(truncate(req.NodeID, 63)),
	}

	ttl := r.cfg.TTLSecondsAfterFinished
	backoff := r.cfg.BackoffLimit
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: r.cfg.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
}

func boolPtr(v bool) *bool { return &v }

// podResources maps a resolved admission cost onto a runner pod's
// requests and limits, so one .Resources() declaration drives both the
// laptop daemon and the kube scheduler. Per dimension: an explicit pin or a
// measured peak becomes the request, with a limit set by the policy
// (generous for compressible CPU, tight for memory that OOMs); the
// cold-start default tier, and any dimension a pin or profile leaves
// unset, falls back to the configured conservative request and limit so an
// unprofiled pipeline's first pods still carry sane figures.
func podResources(res capacity.Resolution, cfg Config) corev1.ResourceRequirements {
	req := corev1.ResourceList{}
	lim := corev1.ResourceList{}
	measured := res.Source != store.CostSourceDefault

	if measured && res.Cores > 0 {
		req[corev1.ResourceCPU] = *resource.NewMilliQuantity(int64(res.Cores*1000), resource.DecimalSI)
		lim[corev1.ResourceCPU] = *resource.NewMilliQuantity(int64(res.Cores*1000*podCPULimitFactor), resource.DecimalSI)
	} else {
		if cfg.CPURequest != "" {
			req[corev1.ResourceCPU] = resource.MustParse(cfg.CPURequest)
		}
		if cfg.CPULimit != "" {
			lim[corev1.ResourceCPU] = resource.MustParse(cfg.CPULimit)
		}
	}

	if measured && res.MemoryBytes > 0 {
		req[corev1.ResourceMemory] = *resource.NewQuantity(res.MemoryBytes, resource.BinarySI)
		lim[corev1.ResourceMemory] = *resource.NewQuantity(int64(float64(res.MemoryBytes)*podMemoryLimitFactor), resource.BinarySI)
	} else {
		if cfg.MemoryRequest != "" {
			req[corev1.ResourceMemory] = resource.MustParse(cfg.MemoryRequest)
		}
		if cfg.MemoryLimit != "" {
			lim[corev1.ResourceMemory] = resource.MustParse(cfg.MemoryLimit)
		}
	}
	return corev1.ResourceRequirements{Requests: req, Limits: lim}
}

// isJobDone returns true when the Job has a terminal condition.
// Sparkwing only cares whether the pod finished writing (success or
// fail); the specific status is read off the controller.
func isJobDone(j *batchv1.Job) bool {
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete, batchv1.JobFailed, batchv1.JobSuspended:
			return true
		}
	}
	// hack: some cluster versions surface status counts before setting a condition
	if j.Status.Succeeded > 0 || j.Status.Failed > 0 {
		return true
	}
	return false
}

// sanitizeK8sName coerces a runID/nodeID into something a Job name can
// contain: lowercase, digit-or-letter-or-hyphen, no leading/trailing
// hyphens. Characters outside that set collapse to '-'.
func sanitizeK8sName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

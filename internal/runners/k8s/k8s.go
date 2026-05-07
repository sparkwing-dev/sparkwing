// Package k8s is the K8s-Job-per-node Runner implementation.
//
// For each dispatched node, Runner.RunNode creates a batch/v1 Job
// named deterministically on (runID, nodeID, attempt) so duplicate
// dispatch collides on the API server rather than spawning a racing
// second pod. The pod runs `wing run-node <runID> <nodeID>`, which
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

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
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

	// NodeSelector + Tolerations let the caller pin runner pods to a
	// specific pool (GPU nodes, spot nodes, etc.). v1 copies the same
	// selector to every Job; RunsOn-style per-node routing lands in a
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
	job := r.buildJob(name, req)

	// Create the Job. If it already exists (duplicate dispatch from a
	// racing orchestrator), treat as idempotent and watch the
	// existing one. Any other error is fatal.
	_, err := r.client.BatchV1().Jobs(r.cfg.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return runner.Result{
			Outcome: sparkwing.Failed,
			Err:     fmt.Errorf("create Job %s: %w", name, err),
		}
	}

	// Ensure the Job is deleted on cancellation so downstream pods
	// stop quickly and don't linger consuming quota. Propagate
	// background deletion so the Job's owner-reference cleanup
	// removes the pod too.
	defer func() {
		if ctx.Err() != nil {
			policy := metav1.DeletePropagationBackground
			delCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = r.client.BatchV1().Jobs(r.cfg.Namespace).Delete(delCtx, name,
				metav1.DeleteOptions{PropagationPolicy: &policy})
		}
	}()

	// Heartbeat ticker runs for the life of the Job poll. A fresh
	// pod can spend 5-15s in ContainerCreating + ImagePull before it
	// even calls StartNode; without this loop the dashboard would
	// show "no heartbeat" through that whole window even though the
	// runner is actively waiting.
	hbCtx, stopHB := context.WithCancel(ctx)
	defer stopHB()
	go heartbeatLoop(hbCtx, r.ctrl, req.RunID, req.NodeID, r.logger)

	// Stamp an initial activity so the dashboard has something to
	// show before the pod schedules. phaseTracker ensures we only
	// write on actual transitions.
	_ = r.ctrl.UpdateNodeActivity(ctx, req.RunID, req.NodeID, "job created")
	var lastPhase string

	// Poll to terminal state.
	t := time.NewTicker(r.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return runner.Result{Outcome: sparkwing.Cancelled, Err: ctx.Err()}
		case <-t.C:
			j, err := r.client.BatchV1().Jobs(r.cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				// Transient API server hiccups happen; log and keep polling.
				r.logger.Warn("job poll failed", "job", name, "err", err)
				continue
			}
			if isJobDone(j) {
				return r.readFinalResult(ctx, req, j)
			}
			// Surface pod-phase transitions so operators can distinguish
			// Pending (scheduler wait) from ContainerCreating (image
			// pull) from Running (actually executing). Querying pods is
			// a second round-trip per tick; the runner is already polling
			// Jobs so the added cost is modest.
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
	// Use the newest pod; retries create new pods with fresh CreationTimestamps.
	p := pods.Items[0]
	for _, cand := range pods.Items[1:] {
		if cand.CreationTimestamp.After(p.CreationTimestamp.Time) {
			p = cand
		}
	}
	// Prefer a container-waiting reason over the bare phase: an
	// "ImagePullBackOff" in ContainerCreating is more informative
	// than "Pending".
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

	// Map controller outcome -> runner.Result. Output is handed back
	// as raw JSON bytes (not an unmarshaled `any`): the dispatcher
	// cannot know the job's typed output shape, so unmarshaling here
	// would produce map[string]interface{} and break Ref[T].Get's
	// type assertion. The dispatcher routes []byte outputs through
	// the JSON resolver path where Ref[T].Get unmarshals into the
	// caller's declared type.
	oc := sparkwing.Outcome(n.Outcome)
	res := runner.Result{Outcome: oc}
	if n.Error != "" {
		res.Err = errors.New(n.Error)
	}
	if len(n.Output) > 0 {
		res.Output = []byte(n.Output)
	}
	// Defensive: if the node is still "running" according to the
	// controller but the Job has finished, the pod crashed before
	// writing. Surface that as Failed and try to classify the cause
	// (OOMKilled / non-zero exit) from the pod's container status so
	// the dashboard can render a structured FailureReasonBadge
	// instead of a generic "unknown error".
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
		// Best-effort: record the structured reason + exit code on
		// the controller. Failure here leaves the node in its pre-
		// written state; the dispatcher still surfaces a Failed
		// outcome.
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
	// readable tail: sanitized, truncated nodeID. Total budget:
	// "sw-" (3) + hashSeg (10) + "-" (1) + nodeSeg (≤48) + "-0" (2) = 64
	// Cap nodeSeg at 47 to stay under 63.
	nodeSeg := sanitizeK8sName(truncate(nodeID, 47))
	name := fmt.Sprintf("sw-%s-%s-%d", hashSeg, nodeSeg, attempt)
	return truncate(name, 63)
}

func (r *Runner) buildJob(name string, req runner.Request) *batchv1.Job {
	env := []corev1.EnvVar{
		{Name: "SPARKWING_CONTROLLER_URL", Value: r.cfg.ControllerURL},
		{Name: "SPARKWING_RUN_ID", Value: req.RunID},
		{Name: "SPARKWING_NODE_ID", Value: req.NodeID},
		// Pod runs as nonroot; point SPARKWING_HOME at a writable
		// tmpfs so orchestrator.DefaultPaths doesn't try to mkdir
		// /.sparkwing in the root filesystem.
		{Name: "SPARKWING_HOME", Value: "/tmp/sparkwing"},
	}
	if r.cfg.LogsURL != "" {
		env = append(env, corev1.EnvVar{Name: "SPARKWING_LOGS_URL", Value: r.cfg.LogsURL})
	}
	if r.cfg.AgentToken != "" {
		// FOLLOWUPS #2: stamp the worker's own bearer on the Job pod
		// so its controller + logs calls authenticate. Without this,
		// K8sRunner fallback under auth crash-loops every Job on 401.
		env = append(env, corev1.EnvVar{Name: "SPARKWING_AGENT_TOKEN", Value: r.cfg.AgentToken})
	}

	container := corev1.Container{
		Name:            "runner",
		Image:           r.cfg.Image,
		ImagePullPolicy: corev1.PullAlways,
		Command:         []string{"wing"},
		Args:            []string{"run-node", req.RunID, req.NodeID},
		Env:             env,
		Resources:       r.resources(),
	}

	podSpec := corev1.PodSpec{
		RestartPolicy:      corev1.RestartPolicyNever,
		ServiceAccountName: r.cfg.ServiceAccountName,
		NodeSelector:       r.cfg.NodeSelector,
		Tolerations:        r.cfg.Tolerations,
		Containers:         []corev1.Container{container},
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

func (r *Runner) resources() corev1.ResourceRequirements {
	req := corev1.ResourceList{}
	lim := corev1.ResourceList{}
	if r.cfg.CPURequest != "" {
		req[corev1.ResourceCPU] = resource.MustParse(r.cfg.CPURequest)
	}
	if r.cfg.MemoryRequest != "" {
		req[corev1.ResourceMemory] = resource.MustParse(r.cfg.MemoryRequest)
	}
	if r.cfg.CPULimit != "" {
		lim[corev1.ResourceCPU] = resource.MustParse(r.cfg.CPULimit)
	}
	if r.cfg.MemoryLimit != "" {
		lim[corev1.ResourceMemory] = resource.MustParse(r.cfg.MemoryLimit)
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
	// Some cluster versions surface `.status.succeeded` / `.status.failed`
	// before setting a condition. Treat those as terminal too so we
	// don't hang on the transition window.
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

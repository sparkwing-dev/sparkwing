package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

type Config struct {
	Namespace string

	Image string

	// Labels are static capabilities every Job this runner creates can honor.
	Labels []string

	ImagePullSecret string

	ServiceAccountName string

	ControllerURL string
	LogsURL       string

	ArtifactStoreURL string
	// GitcacheURL is the cache the pod compiles a pipeline from when the
	// image does not carry it; empty leaves run-node unable to fall back.
	GitcacheURL string

	DependencyProxyURL string

	ImagePullPolicy corev1.PullPolicy

	NodeSelector map[string]string
	Tolerations  []corev1.Toleration

	// Team owns the run whose nodes this runner places. Its Jobs carry
	// [TeamLabel] and never share a node with another team's Jobs.
	Team string

	CPURequest    string
	CPULimit      string
	MemoryRequest string
	MemoryLimit   string

	// CPUCeiling is the operator's hard cap in cores on what one runner pod
	// may ask for, whatever a pipeline pinned or a profile measured. Zero
	// means no ceiling. Parse an operator string with [ParseCPUCeiling].
	CPUCeiling float64

	// MemoryCeiling is the operator's hard cap in bytes on what one runner
	// pod may ask for. Zero means no ceiling. Parse an operator string with
	// [ParseMemoryCeiling].
	MemoryCeiling int64

	BackoffLimit int32

	AgentToken string

	PollInterval time.Duration

	TTLSecondsAfterFinished int32

	MissingJobGracePeriod time.Duration

	// UnschedulableGracePeriod bounds how long a pod no machine of its shape
	// can ever hold sits before its node fails. Zero means
	// [UnschedulableGracePeriod].
	UnschedulableGracePeriod time.Duration

	// FleetFullWait bounds how long a node queues for a busy runner pool to
	// free a machine of the shape it needs. Zero means [FleetFullWait], which
	// is the longest the dispatcher's unrenewed claim covers, so an operator
	// raising this must raise the claim lease with it.
	FleetFullWait time.Duration

	// JobActiveDeadline bounds a fallback Job whose node declared no timeout.
	// Zero means [DefaultJobActiveDeadline]; a node's own timeout outranks it
	// up to the longer of this and [MaxDeclaredJobActiveDeadline].
	JobActiveDeadline time.Duration
}

type Runner struct {
	client        kubernetes.Interface
	ctrl          *client.Client
	cfg           Config
	logger        *slog.Logger
	labelInstance string
}

func New(kcli kubernetes.Interface, ctrl *client.Client, cfg Config, logger *slog.Logger) *Runner {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.TTLSecondsAfterFinished == 0 {
		cfg.TTLSecondsAfterFinished = 300
	}
	if cfg.MissingJobGracePeriod <= 0 {
		cfg.MissingJobGracePeriod = 30 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	cfg.Labels = sparkwingruntime.NormalizeLabels(cfg.Labels)
	return &Runner{
		client:        kcli,
		ctrl:          ctrl,
		cfg:           cfg,
		logger:        logger,
		labelInstance: "sparkwing-orchestrator",
	}
}

var (
	_ runner.Runner          = (*Runner)(nil)
	_ runner.LabelAdvertiser = (*Runner)(nil)
)

func (r *Runner) AdvertisedLabels() []string {
	return append([]string(nil), r.cfg.Labels...)
}

func (r *Runner) RunNode(ctx context.Context, req runner.Request) runner.Result {
	name := JobName(req.RunID, req.NodeID, 0)
	res := r.resolveResources(ctx, req)
	fence, class, refused := r.claimNode(ctx, req, name)
	if refused != nil {
		return r.refuseUnclaimedJob(ctx, req, refused)
	}
	// safety: the dispatcher reached here holding the run's trigger claim,
	// and the controller refuses a request that carries both identities.
	ctx = store.WithNodeClaimFence(store.WithoutClaimFences(ctx), fence)
	job := r.buildJob(name, req, res, class, fence)
	if msg := r.impossibleShape(ctx, job); msg != "" {
		r.failNode(ctx, req, msg, store.FailureUnknown, eventClassRefused)
		return runner.Result{Outcome: sparkwing.Failed, Err: errors.New(msg)}
	}

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
	var lastDetail string
	var unschedulableSince time.Time
	queued := false

	t := time.NewTicker(r.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return runner.Result{Outcome: sparkwing.Cancelled, Err: ctx.Err()}
		case <-t.C:
			j, err := r.client.BatchV1().Jobs(r.cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					return r.readMissingJobResult(ctx, req, name)
				}
				r.logger.Warn("job poll failed", "job", name, "err", err)
				continue
			}
			if isJobDone(j) {
				return r.readFinalResult(ctx, req, j)
			}
			detail := ""
			if pod := r.unschedulablePod(ctx, name); pod == nil {
				unschedulableSince = time.Time{}
				queued = false
			} else {
				now := time.Now()
				if unschedulableSince.IsZero() {
					unschedulableSince = now
				}
				why := unschedulableMessage(pod)
				budget := r.unschedulableGrace()
				if r.fleetRunsAShapeFor(ctx, pod) {
					budget = r.fleetFullWait()
					detail = queuedDetail(why)
					if !queued {
						queued = true
						r.noteQueued(ctx, req, why)
					}
				} else {
					queued = false
				}
				if now.Sub(unschedulableSince) >= budget {
					msg, reason := unschedulableFailure(queued, budget, why)
					r.failNode(ctx, req, msg, reason, unschedulableEvent(queued))
					return runner.Result{Outcome: sparkwing.Failed, Err: errors.New(msg)}
				}
			}
			if detail == "" {
				detail = r.observePodPhase(ctx, name)
			}
			if detail != "" && detail != lastDetail {
				r.reportActivity(ctx, req, detail)
				lastDetail = detail
			}
		}
	}
}

// UnschedulableGracePeriod is how long a pod may sit with no node willing to
// take it before its node fails. A class larger than the cluster keeps warm
// waits for a machine to boot, so the window covers that and stops well short
// of the Job's wall-clock deadline.
const UnschedulableGracePeriod = 5 * time.Minute

// safety: the dispatcher never renews the node claim, so a wait outliving it would have the node reaped and its
// reservation refunded while the Job still sat in the queue. One minute is the margin between the last poll inside
// the wait and the reaper.
const queuedWaitSlack = time.Minute

// FleetFullWait is how long a node queues for the runner fleet to free a
// machine of the shape it needs before it fails with [store.FailureQueueTimeout].
// It is the longest wait the claim the dispatcher holds can cover.
const FleetFullWait = ClaimLease - queuedWaitSlack

func (r *Runner) unschedulableGrace() time.Duration {
	if r.cfg.UnschedulableGracePeriod > 0 {
		return r.cfg.UnschedulableGracePeriod
	}
	return UnschedulableGracePeriod
}

func (r *Runner) fleetFullWait() time.Duration {
	if r.cfg.FleetFullWait > 0 {
		return r.cfg.FleetFullWait
	}
	return FleetFullWait
}

// safety: an unschedulable pod would otherwise sit until the Job deadline hours
// later with nothing saying why, so the scheduler's own message is what the
// node fails with.
func (r *Runner) observeUnschedulable(ctx context.Context, jobName string) string {
	return unschedulableMessage(r.unschedulablePod(ctx, jobName))
}

func (r *Runner) unschedulablePod(ctx context.Context, jobName string) *corev1.Pod {
	pods, err := r.client.CoreV1().Pods(r.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("batch.kubernetes.io/job-name=%s", jobName),
	})
	if err != nil || len(pods.Items) == 0 {
		return nil
	}
	for _, p := range pods.Items {
		if p.Status.Phase != corev1.PodPending {
			return nil
		}
	}
	for i := range pods.Items {
		for _, c := range pods.Items[i].Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse &&
				c.Reason == corev1.PodReasonUnschedulable {
				return &pods.Items[i]
			}
		}
	}
	return nil
}

func unschedulableMessage(pod *corev1.Pod) string {
	if pod == nil {
		return ""
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse &&
			c.Reason == corev1.PodReasonUnschedulable {
			if c.Message != "" {
				return c.Message
			}
			return string(corev1.PodReasonUnschedulable)
		}
	}
	return ""
}

// safety: the scheduler's own message cannot separate these, because a pod too large for every machine and a pod
// behind a full fleet are both rejected as "Insufficient cpu". Allocatable is the figure the scheduler fits against
// and it does not move as a machine fills, so measuring the pod against it asks that question with the neighbors
// removed. A pool running nothing answers false, because a fleet with no machines in it is not a full fleet.
func (r *Runner) fleetRunsAShapeFor(ctx context.Context, pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	cpu, memory := podRequestTotals(pod)
	nodes, err := r.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(pod.Spec.NodeSelector).String(),
	})
	if err != nil || len(nodes.Items) == 0 {
		return false
	}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		if n.Spec.Unschedulable {
			continue
		}
		alloc := n.Status.Allocatable
		if alloc.Cpu().Cmp(cpu) < 0 || alloc.Memory().Cmp(memory) < 0 {
			continue
		}
		return true
	}
	return false
}

// safety: the scheduler fits a pod by the larger of its init-container peak and
// the sum of its app containers, so the same arithmetic decides here.
func podRequestTotals(pod *corev1.Pod) (resource.Quantity, resource.Quantity) {
	var cpu, memory resource.Quantity
	for _, c := range pod.Spec.Containers {
		cpu.Add(*c.Resources.Requests.Cpu())
		memory.Add(*c.Resources.Requests.Memory())
	}
	for _, c := range pod.Spec.InitContainers {
		if q := c.Resources.Requests.Cpu(); q.Cmp(cpu) > 0 {
			cpu = *q
		}
		if q := c.Resources.Requests.Memory(); q.Cmp(memory) > 0 {
			memory = *q
		}
	}
	return cpu, memory
}

func queuedDetail(why string) string {
	return "queued: the runner fleet is full (" + firstLine(why) + ")"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

const (
	eventQueuedOnFleet  = "capacity_queued"
	eventQueueTimeout   = "capacity_queue_timeout"
	eventClassRefused   = "resource_class_refused"
	queuedFailureFormat = "K8sRunner: the runner fleet stayed full for %s, so no machine freed up for this node: %s"
)

// safety: the detail is the only thing telling the dashboard and the queue
// listing why a node is sitting still, so a write that fails is logged rather
// than dropped.
func (r *Runner) reportActivity(ctx context.Context, req runner.Request, detail string) {
	if err := r.ctrl.UpdateNodeActivity(ctx, req.RunID, req.NodeID, detail); err != nil {
		r.logger.Debug("k8s: reporting node activity failed",
			"run_id", req.RunID, "node_id", req.NodeID, "err", err)
	}
}

func (r *Runner) noteQueued(ctx context.Context, req runner.Request, why string) {
	if err := r.ctrl.AppendEvent(ctx, req.RunID, req.NodeID, eventQueuedOnFleet, []byte(why)); err != nil {
		r.logger.Warn("k8s: recording the capacity wait failed",
			"run_id", req.RunID, "node_id", req.NodeID, "err", err)
	}
}

func unschedulableFailure(queued bool, budget time.Duration, why string) (string, string) {
	if queued {
		return fmt.Sprintf(queuedFailureFormat, budget, why), store.FailureQueueTimeout
	}
	return fmt.Sprintf("K8sRunner: no node accepted this pod within %s: %s", budget, why),
		store.FailureUnknown
}

func unschedulableEvent(queued bool) string {
	if queued {
		return eventQueueTimeout
	}
	return eventClassRefused
}

// safety: a request larger than every matching node's allocatable capacity
// cannot become schedulable by waiting for current jobs to finish.
func (r *Runner) impossibleShape(ctx context.Context, job *batchv1.Job) string {
	pod := &corev1.Pod{Spec: job.Spec.Template.Spec}
	cpu, memory := podRequestTotals(pod)
	nodes, err := r.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(pod.Spec.NodeSelector).String(),
	})
	if err != nil || len(nodes.Items) == 0 {
		return ""
	}
	var maxCPU, maxMemory resource.Quantity
	for i := range nodes.Items {
		alloc := nodes.Items[i].Status.Allocatable
		if alloc.Cpu().Cmp(cpu) >= 0 && alloc.Memory().Cmp(memory) >= 0 {
			return ""
		}
		if alloc.Cpu().Cmp(maxCPU) > 0 {
			maxCPU = *alloc.Cpu()
		}
		if alloc.Memory().Cmp(maxMemory) > 0 {
			maxMemory = *alloc.Memory()
		}
	}
	return fmt.Sprintf("K8sRunner: pod requests %s cpu and %s memory, but no matching node has that allocatable capacity (largest cpu %s, memory %s); lower the resource pin or add a larger node",
		cpu.String(), memory.String(), maxCPU.String(), maxMemory.String())
}

func (r *Runner) failNode(ctx context.Context, req runner.Request, msg, reason, event string) {
	if err := r.ctrl.AppendEvent(ctx, req.RunID, req.NodeID, event, []byte(msg)); err != nil {
		r.logger.Warn("k8s: recording the refusal failed",
			"run_id", req.RunID, "node_id", req.NodeID, "err", err)
	}
	if err := r.ctrl.FinishNodeWithReason(ctx, req.RunID, req.NodeID,
		string(sparkwing.Failed), msg, nil, reason, nil); err != nil {
		r.logger.Warn("k8s: failing the node failed",
			"run_id", req.RunID, "node_id", req.NodeID, "err", err)
	}
}

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

// ClaimLease is the lease the dispatcher takes on a node it executes through a
// Job, and the lease the pod renews once it starts. The dispatcher never
// renews it: a pod that never runs must let its claim lapse rather than hold a
// reservation for as long as the dispatcher watches an ImagePullBackOff.
const ClaimLease = store.MaxLeaseDuration

// DefaultJobActiveDeadline bounds a fallback Job whose node declared no
// timeout, so a pod that wedges cannot outlive the run that wanted it.
const DefaultJobActiveDeadline = 6 * time.Hour

// safety: the node's own timeout must fire first, so it records an outcome
// rather than vanishing with the pod Kubernetes deleted.
const jobDeadlineSlack = 10 * time.Minute

// MaxDeclaredJobActiveDeadline is the longest a node's declared timeout can
// stretch its Job's deadline, unless the operator configured a longer one. A
// pod whose dispatcher died stops heartbeating and so stops being charged, and
// the timeout is the pipeline author's to choose.
const MaxDeclaredJobActiveDeadline = 24 * time.Hour

// safety: every refusal is returned, including a controller that does not
// serve the named-claim route, because the claim is where the credit check
// lives and an unfenced Job would run on compute nobody paid for.
func (r *Runner) claimNode(
	ctx context.Context, req runner.Request, jobName string,
) (store.NodeClaimFence, store.CPUClass, error) {
	holderID := "k8s-job:" + jobName
	n, err := r.ctrl.ClaimNodeByID(ctx, req.RunID, req.NodeID, holderID, ClaimLease, true)
	// safety: only the operator's metered pool may claim it sizes a node to its
	// cpu class, so an unmetered installation claims the node plainly and gets
	// the pod shape it always had.
	if errors.Is(err, store.ErrLockHeld) {
		n, err = r.ctrl.ClaimNodeByID(ctx, req.RunID, req.NodeID, holderID, ClaimLease, false)
	}
	if err != nil {
		r.logger.Warn("k8s: claiming the node for its Job failed, so no Job is created",
			"run_id", req.RunID, "node_id", req.NodeID, "holder_id", holderID, "err", err)
		return store.NodeClaimFence{}, store.CPUClass{}, err
	}
	return store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	}, store.CPUClass{Cores: n.CreditCPUClassCores, MemoryBytes: n.CreditCPUClassMemoryBytes}, nil
}

const eventClaimRefused = "job_claim_refused"

// safety: a node another holder took is that holder's to finish, so only the
// other refusals fail it here; the dispatcher still learns this Job never ran.
func (r *Runner) refuseUnclaimedJob(ctx context.Context, req runner.Request, refused error) runner.Result {
	msg := fmt.Sprintf("K8sRunner: the controller refused this node's claim, so no Job was created: %v", refused)
	if !errors.Is(refused, store.ErrLockHeld) {
		reason := store.FailureUnknown
		if errors.Is(refused, store.ErrInsufficientCredits) {
			reason = store.FailureCreditsExhausted
		}
		r.failNode(ctx, req, msg, reason, eventClaimRefused)
	}
	return runner.Result{Outcome: sparkwing.Failed, Err: errors.New(msg)}
}

// safety: this is a wall-clock backstop for a pod Kubernetes would otherwise
// keep forever, never the node's own deadline. A node's no-progress timeout
// measures silence rather than elapsed time, so it deliberately does not bound
// this; only a declared .Timeout() moves it.
func (r *Runner) jobActiveDeadline(req runner.Request) *int64 {
	deadline := r.cfg.JobActiveDeadline
	if deadline <= 0 {
		deadline = DefaultJobActiveDeadline
	}
	if req.Node != nil {
		if declared := req.Node.TimeoutDuration(); declared > 0 {
			deadline = min(declared+jobDeadlineSlack, max(deadline, MaxDeclaredJobActiveDeadline))
		}
	}
	secs := int64(deadline.Seconds())
	if secs < 1 {
		secs = 1
	}
	return &secs
}

func (r *Runner) readMissingJobResult(ctx context.Context, req runner.Request, jobName string) runner.Result {
	deadline := time.NewTimer(r.cfg.MissingJobGracePeriod)
	defer deadline.Stop()
	t := time.NewTicker(r.cfg.PollInterval)
	defer t.Stop()
	graceExpired := false
	msg := fmt.Sprintf("K8sRunner: Job %s disappeared before reaching a terminal condition", jobName)
	for {
		n, err := r.ctrl.GetNode(ctx, req.RunID, req.NodeID)
		if err == nil && runner.NodeTerminal(n) {
			return runner.ResultFromNode(n)
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			r.logger.Warn("node poll after missing job failed",
				"job", jobName, "run_id", req.RunID, "node_id", req.NodeID, "err", err)
		}
		if graceExpired {
			if err := r.ctrl.FinishNodeWithReason(ctx, req.RunID, req.NodeID,
				string(sparkwing.Failed), msg, nil, store.FailureUnknown, nil); err != nil {
				r.logger.Warn("finish node after missing job failed",
					"job", jobName, "run_id", req.RunID, "node_id", req.NodeID, "err", err)
			} else if n, err := r.ctrl.GetNode(ctx, req.RunID, req.NodeID); err == nil && runner.NodeTerminal(n) {
				return runner.ResultFromNode(n)
			} else if err != nil && !errors.Is(err, store.ErrNotFound) {
				r.logger.Warn("node poll after missing job finish failed",
					"job", jobName, "run_id", req.RunID, "node_id", req.NodeID, "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return runner.Result{Outcome: sparkwing.Cancelled, Err: ctx.Err()}
		case <-deadline.C:
			graceExpired = true
		case <-t.C:
		}
	}
}

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

	res := runner.ResultFromNode(n)
	// safety: synthesize Failed when a crashed pod leaves no terminal state.
	if !runner.NodeTerminal(n) {
		res.Outcome = sparkwing.Failed
		reason, exitCode := r.inspectTerminatedPod(ctx, j)
		errMsg := fmt.Sprintf("pod %s exited without writing terminal state", j.Name)
		if reason == store.FailureOOMKilled {
			errMsg = fmt.Sprintf("pod %s OOMKilled", j.Name)
		}
		// safety: Kubernetes deletes the pod it kills at the deadline, so the
		// Job condition is the only record of why the node wrote nothing.
		if deadlineKilledJob(j) {
			reason = store.FailureTimeout
			exitCode = nil
			errMsg = fmt.Sprintf("Job %s was killed at its active deadline (%s) before the node wrote a terminal state",
				j.Name, jobDeadlineText(j))
		}
		if res.Err == nil {
			res.Err = errors.New(errMsg)
		}
		_ = r.ctrl.FinishNodeWithReason(ctx, req.RunID, req.NodeID,
			string(sparkwing.Failed), errMsg, nil, reason, exitCode)
	}
	return res
}

// DeadlineExceededReason is the reason Kubernetes stamps on a Job it kills for
// outliving its activeDeadlineSeconds.
const DeadlineExceededReason = "DeadlineExceeded"

func deadlineKilledJob(j *batchv1.Job) bool {
	if j == nil {
		return false
	}
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue &&
			c.Reason == DeadlineExceededReason {
			return true
		}
	}
	return false
}

func jobDeadlineText(j *batchv1.Job) string {
	if j == nil || j.Spec.ActiveDeadlineSeconds == nil {
		return "its configured limit"
	}
	return (time.Duration(*j.Spec.ActiveDeadlineSeconds) * time.Second).String()
}

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

func JobName(runID, nodeID string, attempt int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%d", runID, nodeID, attempt)))
	hashSeg := hex.EncodeToString(h[:])[:10]
	// safety: 47 cap keeps "sw-"(3)+hash(10)+"-"(1)+nodeSeg(≤47)+"-0"(2)=63 within K8s limit
	nodeSeg := sanitizeK8sName(truncate(nodeID, 47))
	name := fmt.Sprintf("sw-%s-%s-%d", hashSeg, nodeSeg, attempt)
	return truncate(name, 63)
}

const scratchVolumeName = "scratch"

const (
	podCPULimitFactor = 2.0

	podMemoryLimitFactor = 1.25

	podDefaultRefCPU = 1
)

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
	res := capacity.Resolve(pin, profile, podDefaultRefCPU, "")
	if w := ceilingWarning(res, r.cfg.CPUCeiling, r.cfg.MemoryCeiling); w != "" {
		r.logger.Warn("resource ceiling clamped the pod",
			"pipeline", pipeline, "node", req.NodeID, "detail", w)
		_ = r.ctrl.AppendEvent(ctx, req.RunID, req.NodeID, "resource_clamped", []byte(w))
	}
	return res
}

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

// JobBinary is the executable a fallback Job runs. The runner image installs
// this one binary, so a Job that named any other would fail to start.
const JobBinary = "sparkwing-runner"

// Claim fence environment. The pod reads these to rebuild the claim the
// dispatcher took for it, which every node state write and log append is
// checked against. They are the variable names an agent-claimed node already
// travels under, so one reader in run-node serves both.
const (
	ClaimHolderEnv       = "SPARKWING_NODE_CLAIM_HOLDER"
	ClaimGenerationEnv   = "SPARKWING_NODE_CLAIM_GENERATION"
	ClaimMembershipEnv   = "SPARKWING_NODE_CLAIM_MEMBERSHIP"
	ClaimReservationEnv  = "SPARKWING_NODE_CLAIM_RESERVATION"
	ClaimLeaseSecondsEnv = "SPARKWING_NODE_CLAIM_LEASE_SECONDS"
)

func (r *Runner) buildJob(
	name string, req runner.Request, res capacity.Resolution, class store.CPUClass, fence store.NodeClaimFence,
) *batchv1.Job {
	env := []corev1.EnvVar{
		{Name: "SPARKWING_CONTROLLER_URL", Value: r.cfg.ControllerURL},
		{Name: "SPARKWING_RUN_ID", Value: req.RunID},
		{Name: "SPARKWING_NODE_ID", Value: req.NodeID},
		// safety: pod runs as nonroot; SPARKWING_HOME must be a writable path or DefaultPaths mkdir fails
		{Name: "SPARKWING_HOME", Value: "/tmp/sparkwing"},
		{Name: "HOME", Value: "/tmp"},
		{Name: "GOCACHE", Value: "/tmp/go-build"},
		{Name: "GOMODCACHE", Value: "/tmp/go-mod"},
		{Name: "SPARKWING_RUNNER_NAME", Value: name},
		{Name: "SPARKWING_RUNNER_TYPE", Value: "kubernetes"},
	}
	if len(r.cfg.Labels) > 0 {
		env = append(env, corev1.EnvVar{Name: "SPARKWING_RUNNER_LABELS", Value: strings.Join(r.cfg.Labels, ",")})
	}
	if r.cfg.LogsURL != "" {
		env = append(env, corev1.EnvVar{Name: "SPARKWING_LOGS_URL", Value: r.cfg.LogsURL})
	}
	if r.cfg.ArtifactStoreURL != "" {
		env = append(env, corev1.EnvVar{Name: "SPARKWING_CACHE_URL", Value: r.cfg.ArtifactStoreURL})
	}
	if r.cfg.GitcacheURL != "" {
		env = append(env, corev1.EnvVar{Name: "SPARKWING_GITCACHE_URL", Value: r.cfg.GitcacheURL})
	}
	if r.cfg.AgentToken != "" {
		env = append(env, corev1.EnvVar{Name: "SPARKWING_AGENT_TOKEN", Value: r.cfg.AgentToken})
	}
	// safety: the pod runs the team's code, so of the cache credentials it gets only
	// the run's grant, which opens that team's trees and no other's.
	if grant := os.Getenv(authwire.CacheGrantEnv); grant != "" {
		env = append(env, corev1.EnvVar{Name: authwire.CacheGrantEnv, Value: grant})
	}
	env = append(env, dependencyProxyEnv(r.cfg.DependencyProxyURL)...)
	env = append(env, claimFenceEnv(fence)...)

	container := corev1.Container{
		Name:            "runner",
		Image:           r.cfg.Image,
		ImagePullPolicy: pullPolicyOrDefault(r.cfg.ImagePullPolicy),
		Command:         []string{JobBinary},
		Args:            []string{"run-node", req.RunID, req.NodeID},
		Env:             env,
		Resources:       podResources(res, r.cfg),
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: boolPtr(false),
			RunAsNonRoot:             boolPtr(true),
			ReadOnlyRootFilesystem:   boolPtr(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
		// safety: the read-only root leaves no writable path, and HOME, the caches, and SPARKWING_HOME all live here.
		VolumeMounts: []corev1.VolumeMount{{Name: scratchVolumeName, MountPath: "/tmp"}},
	}

	team := TeamLabelValue(r.cfg.Team)
	band := cpuBand(class.Cores)
	selector := bandNodeSelector(r.cfg.NodeSelector, band)
	placed := ""
	if band != "" {
		// safety: the operator's value for this key wins the selector, so the
		// toleration follows the pool the pod selects; a toleration for another
		// value leaves it selecting a pool whose taint it does not tolerate.
		placed = selector[cpuBandKey]
	}
	podSpec := corev1.PodSpec{
		RestartPolicy:      corev1.RestartPolicyNever,
		ServiceAccountName: r.cfg.ServiceAccountName,
		// safety: pipeline code runs here, so the pod gets no API token
		AutomountServiceAccountToken: boolPtr(false),
		NodeSelector:                 selector,
		Tolerations:                  bandTolerations(r.cfg.Tolerations, placed),
		Affinity:                     teamAntiAffinity(team),
		Containers:                   []corev1.Container{container},
		Volumes: []corev1.Volume{{
			Name:         scratchVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		}},
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
		TeamLabel:                      team,
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
			ActiveDeadlineSeconds:   r.jobActiveDeadline(req),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
}

// safety: a band nodepool labels its nodes with this key and taints them with
// the same key and value, so these two strings must read the same as
// k8s/karpenter/nodepool-sparkwing-jobs.yaml in the kikd-infra repository. A
// Job that names neither the label nor the taint lands on no band node.
const (
	cpuBandKey   = "sparkwing.dev/cpu-band"
	cpuBandSmall = "small"
)

func cpuBand(cores int64) string {
	// safety: 2 and below is the warm pool's default reach, which
	// warm_cpu_class_cores moves, and the band pool starts at 4 either way, so
	// a Job at those sizes keeps the placement the operator configured.
	if cores <= 2 {
		return ""
	}
	return cpuBandSmall
}

func bandNodeSelector(static map[string]string, band string) map[string]string {
	if band == "" {
		return static
	}
	out := map[string]string{cpuBandKey: band}
	// safety: the operator's own selector is the deployment's last word, so a
	// cluster that already pins Jobs by this key keeps the value it chose.
	maps.Copy(out, static)
	return out
}

func bandTolerations(static []corev1.Toleration, placed string) []corev1.Toleration {
	if placed == "" {
		return static
	}
	for _, t := range static {
		// safety: an operator toleration on this key already says what the
		// deployment tolerates; a second entry would widen it behind his back.
		if t.Key == cpuBandKey {
			return static
		}
	}
	out := make([]corev1.Toleration, 0, len(static)+1)
	out = append(out, static...)
	return append(out, corev1.Toleration{
		Key:      cpuBandKey,
		Operator: corev1.TolerationOpEqual,
		Value:    placed,
		Effect:   corev1.TaintEffectNoSchedule,
	})
}

func claimFenceEnv(fence store.NodeClaimFence) []corev1.EnvVar {
	if fence.HolderID == "" || fence.ClaimGeneration < 1 {
		return nil
	}
	return []corev1.EnvVar{
		{Name: ClaimHolderEnv, Value: fence.HolderID},
		{Name: ClaimGenerationEnv, Value: strconv.FormatInt(fence.ClaimGeneration, 10)},
		{Name: ClaimMembershipEnv, Value: fence.MembershipID},
		{Name: ClaimReservationEnv, Value: fence.ReservationID},
		{Name: ClaimLeaseSecondsEnv, Value: strconv.Itoa(int(ClaimLease.Seconds()))},
	}
}

func boolPtr(v bool) *bool { return &v }

func ResolveDependencyProxy(explicit, cacheURL string) string {
	explicit = strings.TrimSpace(explicit)
	if strings.EqualFold(explicit, "off") {
		return ""
	}
	if explicit != "" {
		return explicit
	}
	if !strings.HasPrefix(cacheURL, "http://") && !strings.HasPrefix(cacheURL, "https://") {
		return ""
	}
	return cacheURL
}

func ParsePullPolicy(s string) (corev1.PullPolicy, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return corev1.PullIfNotPresent, nil
	case "always":
		return corev1.PullAlways, nil
	case "ifnotpresent":
		return corev1.PullIfNotPresent, nil
	case "never":
		return corev1.PullNever, nil
	}
	return "", fmt.Errorf("image pull policy %q: expected Always, IfNotPresent, or Never", s)
}

func pullPolicyOrDefault(p corev1.PullPolicy) corev1.PullPolicy {
	if p == "" {
		return corev1.PullIfNotPresent
	}
	return p
}

func dependencyProxyEnv(base string) []corev1.EnvVar {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil
	}
	return []corev1.EnvVar{
		{Name: "GOPROXY", Value: base + "/proxy/golang|https://proxy.golang.org,direct"},
		{Name: "npm_config_registry", Value: base + "/proxy/npm"},
		{Name: "PIP_INDEX_URL", Value: base + "/proxy/pypi/simple/"},
		{Name: "PIP_TRUSTED_HOST", Value: u.Host},
	}
}

// safety: cluster profiles use peak CPU because the resolved core value is a
// hard CFS limit here; sustained local-host demand would throttle spiky pods.
func podResources(res capacity.Resolution, cfg Config) corev1.ResourceRequirements {
	// safety: an operator ceiling outranks a pipeline pin, which is otherwise unbounded on this path
	res = capacity.ApplyCeiling(res, cfg.CPUCeiling, cfg.MemoryCeiling)
	req := corev1.ResourceList{}
	lim := corev1.ResourceList{}
	measured := res.Source != store.CostSourceDefault

	if measured && res.Cores > 0 {
		// safety: a sub-milli pin otherwise renders cpu: 0, which no quota counts
		cores := cappedCores(math.Max(res.Cores, capacity.MeasuredCoreFloor), cfg.CPUCeiling)
		req[corev1.ResourceCPU] = milliCores(cores)
		lim[corev1.ResourceCPU] = milliCores(cappedCores(cores*podCPULimitFactor, cfg.CPUCeiling))
	} else {
		cpuRequest := cfg.CPURequest
		if cpuRequest == "" {
			cpuRequest = "100m"
		}
		req[corev1.ResourceCPU] = resource.MustParse(cpuRequest)
		if cfg.CPULimit != "" {
			lim[corev1.ResourceCPU] = milliCores(cappedCores(quantityCores(cfg.CPULimit), cfg.CPUCeiling))
		}
	}

	if measured && res.MemoryBytes > 0 {
		req[corev1.ResourceMemory] = *resource.NewQuantity(res.MemoryBytes, resource.BinarySI)
		burst := int64(float64(res.MemoryBytes) * podMemoryLimitFactor)
		lim[corev1.ResourceMemory] = *resource.NewQuantity(cappedBytes(burst, cfg.MemoryCeiling), resource.BinarySI)
	} else {
		memoryRequest := cfg.MemoryRequest
		if memoryRequest == "" {
			memoryRequest = "128Mi"
		}
		req[corev1.ResourceMemory] = resource.MustParse(memoryRequest)
		if cfg.MemoryLimit != "" {
			lim[corev1.ResourceMemory] = *resource.NewQuantity(
				cappedBytes(quantityBytes(cfg.MemoryLimit), cfg.MemoryCeiling), resource.BinarySI)
		}
	}
	return corev1.ResourceRequirements{Requests: req, Limits: lim}
}

func milliCores(cores float64) resource.Quantity {
	return *resource.NewMilliQuantity(int64(cores*1000), resource.DecimalSI)
}

func cappedCores(cores, ceiling float64) float64 {
	if ceiling > 0 && cores > ceiling {
		return ceiling
	}
	return cores
}

func cappedBytes(bytes, ceiling int64) int64 {
	if ceiling > 0 && bytes > ceiling {
		return ceiling
	}
	return bytes
}

// ParseCPUCeiling reads an operator's CPU ceiling in Kubernetes quantity form
// ("8", "500m"). An empty string means no ceiling.
func ParseCPUCeiling(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0, fmt.Errorf("cpu ceiling %q: expected a Kubernetes quantity such as 8 or 500m", s)
	}
	cores := q.AsApproximateFloat64()
	if cores <= 0 {
		return 0, fmt.Errorf("cpu ceiling %q: expected a positive number of cores", s)
	}
	return cores, nil
}

// ParseJobDeadline reads an operator's wall-clock bound on one runner Job as a
// Go duration ("6h", "90m"). An empty string means [DefaultJobActiveDeadline].
func ParseJobDeadline(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("job deadline %q: expected a Go duration such as 6h or 90m", s)
	}
	if d < time.Minute {
		return 0, fmt.Errorf("job deadline %q: expected at least a minute", s)
	}
	return d, nil
}

// ParseMemoryCeiling reads an operator's memory ceiling in Kubernetes quantity
// form ("8Gi", "512Mi"). An empty string means no ceiling.
func ParseMemoryCeiling(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0, fmt.Errorf("memory ceiling %q: expected a Kubernetes quantity such as 8Gi", s)
	}
	bytes := q.Value()
	if bytes <= 0 {
		return 0, fmt.Errorf("memory ceiling %q: expected a positive quantity of memory", s)
	}
	return bytes, nil
}

func ceilingWarning(res capacity.Resolution, ceilingCores float64, ceilingBytes int64) string {
	clamped := capacity.ApplyCeiling(res, ceilingCores, ceilingBytes)
	switch {
	case clamped.Cores < res.Cores:
		return fmt.Sprintf("%s charge %.1f cores exceeds the runner ceiling %.1f, so the pod is sized at %.1f cores",
			res.Source, res.Cores, ceilingCores, clamped.Cores)
	case clamped.MemoryBytes < res.MemoryBytes:
		return fmt.Sprintf("%s charge %s exceeds the runner ceiling %s, so the pod is sized at %s",
			res.Source, gib(res.MemoryBytes), gib(ceilingBytes), gib(clamped.MemoryBytes))
	}
	return ""
}

func gib(bytes int64) string {
	return fmt.Sprintf("%.1fGi", float64(bytes)/float64(1<<30))
}

func quantityCores(s string) float64 {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return q.AsApproximateFloat64()
}

func quantityBytes(s string) int64 {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return q.Value()
}

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

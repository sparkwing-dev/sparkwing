package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
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

type Config struct {
	Namespace string

	Image string

	ImagePullSecret string

	ServiceAccountName string

	ControllerURL string
	LogsURL       string

	ArtifactStoreURL string

	DependencyProxyURL string

	ImagePullPolicy corev1.PullPolicy

	NodeSelector map[string]string
	Tolerations  []corev1.Toleration

	CPURequest    string
	CPULimit      string
	MemoryRequest string
	MemoryLimit   string

	BackoffLimit int32

	AgentToken string

	PollInterval time.Duration

	TTLSecondsAfterFinished int32

	MissingJobGracePeriod time.Duration
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
	return &Runner{
		client:        kcli,
		ctrl:          ctrl,
		cfg:           cfg,
		logger:        logger,
		labelInstance: "sparkwing-orchestrator",
	}
}

var _ runner.Runner = (*Runner)(nil)

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
				if apierrors.IsNotFound(err) {
					return r.readMissingJobResult(ctx, req, name)
				}
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
		if res.Err == nil {
			res.Err = errors.New(errMsg)
		}
		_ = r.ctrl.FinishNodeWithReason(ctx, req.RunID, req.NodeID,
			string(sparkwing.Failed), errMsg, nil, reason, exitCode)
	}
	return res
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
	return capacity.Resolve(pin, profile, podDefaultRefCPU, "")
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

func (r *Runner) buildJob(name string, req runner.Request, res capacity.Resolution) *batchv1.Job {
	env := []corev1.EnvVar{
		{Name: "SPARKWING_CONTROLLER_URL", Value: r.cfg.ControllerURL},
		{Name: "SPARKWING_RUN_ID", Value: req.RunID},
		{Name: "SPARKWING_NODE_ID", Value: req.NodeID},
		// safety: pod runs as nonroot; SPARKWING_HOME must be a writable path or DefaultPaths mkdir fails
		{Name: "SPARKWING_HOME", Value: "/tmp/sparkwing"},
		{Name: "HOME", Value: "/tmp"},
		{Name: "GOCACHE", Value: "/tmp/go-build"},
		{Name: "GOMODCACHE", Value: "/tmp/go-mod"},
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
	// safety: the pod reads and writes the cache's guarded /bin/ routes, which reject the controller bearer.
	if tok := bincache.CacheToken(); tok != "" {
		env = append(env, corev1.EnvVar{Name: "SPARKWING_CACHE_TOKEN", Value: tok})
	}
	env = append(env, dependencyProxyEnv(r.cfg.DependencyProxyURL)...)

	container := corev1.Container{
		Name:            "runner",
		Image:           r.cfg.Image,
		ImagePullPolicy: pullPolicyOrDefault(r.cfg.ImagePullPolicy),
		Command:         []string{"sparkwing"},
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

	podSpec := corev1.PodSpec{
		RestartPolicy:      corev1.RestartPolicyNever,
		ServiceAccountName: r.cfg.ServiceAccountName,
		// safety: pipeline code runs here, so the pod gets no API token
		AutomountServiceAccountToken: boolPtr(false),
		NodeSelector:                 r.cfg.NodeSelector,
		Tolerations:                  r.cfg.Tolerations,
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
	res = capacity.ApplyCeiling(res, quantityCores(cfg.CPULimit), quantityBytes(cfg.MemoryLimit))
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

package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	controllerclient "github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// CloudControl supplies awarded bindings from the controller and checks their continued authority.
// Sync must return exactly one decision for each supplied binding, including dead or cancelled claims.
type CloudControl interface {
	Prepare(context.Context, []capability.LeaseRef) (time.Time, error)
	Claim(context.Context, string, []string) (*CloudJobAward, error)
	Sync(context.Context, []CloudJobObservation) ([]CloudJobState, error)
}

// CloudCapacityObservation names a demand Pod, which is never an execution node.
type CloudCapacityObservation struct {
	PodUID       string
	Unconfigured bool
	// Unavailable reports that the Cloud budget breaker refuses new job nodes.
	Unavailable bool
	// Ceiling names the launcher's resource ceiling when the request exceeded it.
	Ceiling *match.Resources
}

// CloudJobObservation retains the identities needed to reconcile after launcher restart.
type CloudJobObservation struct {
	Binding       CloudJobBinding
	Capacity      *CloudCapacityObservation
	Activation    *CloudNodeReservation
	JobNamespace  string
	JobName       string
	JobUID        string
	NodeUID       string
	ReservationID string
	Rejected      bool
	Unheld        bool
}

// CloudJobState carries the controller's teardown decision and committed allocation for one immutable award.
type CloudJobState struct {
	Deferred       bool
	Binding        CloudJobBinding
	LeaseExpiresAt time.Time
	Delete         bool
	Resources      *match.Resources
}

// Launcher creates Cloud Jobs without executing pipeline code in the launcher process.
type Launcher struct {
	Kube     kubernetes.Interface
	Control  CloudControl
	Config   CloudJobConfig
	Boundary CloudBoundary
	Poll     time.Duration
	// IdleGrace is how long a used node may sit unreserved before retirement; zero means DefaultIdleGrace.
	IdleGrace time.Duration
	Logger    *slog.Logger
	// Breaker reports whether the Cloud budget breaker is closed. Nil means no
	// breaker, as on self-hosted clusters.
	Breaker func(context.Context) (bool, error)

	retirementMu     sync.Mutex
	idleSince        map[types.UID]time.Time
	retirementLogged time.Time

	// safety: unlocked because only Run's loop takes the warm sample.
	foreignClassLogged time.Time

	mu       sync.Mutex
	waiting  map[string]*CloudJobAward
	identity string
	checked  map[types.UID]map[types.UID]bool
}

func (l *Launcher) logger() *slog.Logger {
	if l.Logger == nil {
		return slog.Default()
	}
	return l.Logger
}

func (l *Launcher) validate() error {
	if err := l.Config.Validate(); err != nil {
		return err
	}
	if l.Kube == nil || l.Control == nil || l.Boundary == nil {
		return errors.New("launcher requires Kubernetes, controller and boundary")
	}
	return l.validateIdleGrace()
}

func (l *Launcher) claimPoll() time.Duration {
	if l.Poll <= 0 {
		return 5 * time.Second
	}
	return l.Poll
}

// Run verifies the job namespace admission policies once, catching a chart and
// launcher mismatch after an upgrade, then reconciles existing Jobs before
// claiming further work until ctx is cancelled.
func (l *Launcher) Run(ctx context.Context) error {
	if err := l.validate(); err != nil {
		return err
	}
	if err := l.verifyAdmission(ctx); err != nil {
		return err
	}
	poll := l.claimPoll()
	logger := l.logger()
	var retiring sync.WaitGroup
	defer retiring.Wait()
	retiring.Go(func() { l.retireLoop(ctx) })
	var cooldown time.Time
	firstClaim := true
	for ctx.Err() == nil {
		if err := l.reportWarmPools(ctx); err != nil {
			if err == controllerclient.ErrResponseNotObject { //nolint:errorlint // Joined faults must remain WARN.
				logger.Info("warm capacity observation deferred; the next complete sample replaces it", "error", err)
			} else {
				logger.Warn("warm capacity observation failed", "error", err)
			}
		}
		synced := l.syncActive(ctx)
		if blocking(synced) != nil {
			logger.Warn("launcher reconciliation failed", failureAttrs(synced)...)
			cooldown = time.Now().Add(retryWait(synced, 0))
		} else if synced != nil {
			logger.Error("Cloud node or Job teardown faults require operator inspection if they persist; suspect nodes are excluded from placement and quarantines use "+quarantineAnnotation+"; claiming continues", "error", synced)
		}
		if blocking(synced) == nil {
			// safety: held awards without a Job or capacity Pod renew only here, so claim backoff must not skip them.
			err := l.placeWaiting(ctx)
			startupRateLimit := false
			if err == nil && !time.Now().Before(cooldown) {
				var launched bool
				cooldown = time.Now().Add(poll)
				launched, err = l.claim(ctx)
				failure, ok := err.(*apierr.Error) //nolint:errorlint // Joined faults must remain WARN.
				startupRateLimit = firstClaim && !launched && ok && failure.Code == apierr.RateLimited
				firstClaim = false
			}
			if err != nil {
				if startupRateLimit {
					logger.Info("launcher initial claim deferred by controller rate limit", failureAttrs(err)...)
				} else if err == controllerclient.ErrResponseNotObject { //nolint:errorlint // Joined faults must remain WARN.
					logger.Info("launcher claim response unavailable; any undelivered award returns after lease expiry", failureAttrs(err)...)
				} else if err != ErrNoDedicatedNode { //nolint:errorlint // Joined provisioning faults must remain WARN.
					logger.Warn("launcher claim failed", failureAttrs(err)...)
				}
				// safety: a placement error during a controller-requested backoff must not shorten it.
				cooldown = later(cooldown, time.Now().Add(retryWait(err, 0)))
			}
		}
		wait := poll
		if blocking(synced) == nil {
			for _, award := range l.waitingAwards() {
				// perf: container exit should release a held successor before the next claim poll.
				if !award.teardownDeadline.IsZero() && time.Now().Before(award.teardownDeadline) {
					wait = min(wait, time.Second)
				}
			}
		}
		if remaining := time.Until(cooldown); remaining > 0 {
			wait = min(wait, remaining)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func retryWait(err error, poll time.Duration) time.Duration {
	if delay, ok := apierr.RetryDelay(err); ok {
		return max(delay, poll)
	}
	return poll
}

func failureAttrs(err error) []any {
	attrs := []any{"error", err}
	var failure *apierr.Error
	if errors.As(err, &failure) {
		attrs = append(attrs, "code", failure.Code)
		if failure.Hint != "" {
			attrs = append(attrs, "hint", failure.Hint)
		}
		if id, ok := failure.Details["request_id"].(string); ok {
			attrs = append(attrs, "controller_request", id)
		}
	}
	return attrs
}

// LaunchOne persists a suspended Job, creates its bootstrap Secret, then permits execution.
func (l *Launcher) LaunchOne(ctx context.Context) (launched bool, err error) {
	if err := l.validate(); err != nil {
		return false, err
	}
	if err := l.placeWaiting(ctx); err != nil {
		return false, err
	}
	return l.claim(ctx)
}

func (l *Launcher) claim(ctx context.Context) (bool, error) {
	blocked := make([]string, 0, len(l.Config.StoppedTeams))
	for _, team := range l.Config.StoppedTeams {
		blocked = append(blocked, TeamNodeValue(l.Config.Namespace, team))
	}
	award, err := l.Control.Claim(ctx, l.Config.Namespace, blocked)
	if err != nil || award == nil {
		return false, err
	}
	return l.launch(ctx, award)
}

func (l *Launcher) placeWaiting(ctx context.Context) error {
	var errs []error
	for _, award := range l.waitingAwards() {
		if slices.Contains(l.Config.StoppedTeams, award.Binding.Team) {
			continue
		}
		_, err := l.launch(ctx, award)
		var failure *apierr.Error
		if errors.Is(err, limits.ErrNoTime) || errors.As(err, &failure) && failure.Code == apierr.LockHeld {
			errs = append(errs, l.releaseCapacity(ctx, award.Binding))
		}
		if err != nil && err != ErrNoDedicatedNode { //nolint:errorlint // Retain faults joined with the expected wait.
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (l *Launcher) launch(ctx context.Context, award *CloudJobAward) (launched bool, err error) {
	prepared, stop, expires, err := l.keepPreparation(ctx, award.Binding.LeaseRef())
	if err != nil {
		return true, err
	}
	ctx = prepared
	defer func() {
		cause := context.Cause(ctx)
		stop()
		if cause != nil {
			err = errors.Join(err, cause)
		}
	}()
	current := *award
	current.LeaseExpiresAt = expires
	r, err := BuildJob(ctx, time.Now(), l.Config, current, l.Boundary)
	// perf: the controller can end a same-run predecessor after this pass's reconciliation.
	// Its live Job would hide the handoff, and the demand it prompted would leave Karpenter a
	// tainted node to replace. Retiring ended awards first makes reuse a teardown wait.
	if errors.Is(err, ErrNoDedicatedNode) && !errors.Is(err, errPredecessorTeardown) && !award.WaitedForFreshNode && award.teardownDeadline.IsZero() {
		// safety: a failed check falls back to fresh demand, which the reconciliation loop reports.
		if retired, retireErr := l.retireEndedPredecessors(ctx, award.Binding); retired && retireErr == nil {
			r, err = BuildJob(ctx, time.Now(), l.Config, current, l.Boundary)
		}
	}
	if errors.Is(err, errCapacityUnconfigured) {
		l.wait(award)
		return true, l.awaitCapacity(ctx, *award)
	}
	if errors.Is(err, ErrNoDedicatedNode) {
		if errors.Is(err, errPredecessorTeardown) {
			if award.teardownDeadline.IsZero() {
				award.teardownDeadline = time.Now().Add(30 * time.Second)
			}
			l.wait(award)
			return true, ErrNoDedicatedNode
		}
		firstWait := !award.WaitedForFreshNode
		award.WaitedForFreshNode = true
		l.wait(award)
		err := l.awaitCapacity(ctx, *award)
		if err == ErrNoDedicatedNode && firstWait { //nolint:errorlint // Joined provisioning faults are not an expected wait.
			l.logger().Info("Cloud award waiting for provisioned capacity", "team", award.Binding.Team, "run", award.Binding.RunID, "unit", award.Binding.UnitID)
		}
		return true, err
	}
	if err != nil {
		return true, err
	}
	if err := l.releaseCapacity(ctx, award.Binding); err != nil {
		return true, errors.Join(err, l.Boundary.Release(ctx, r.Reservation))
	}
	// safety: a suspended Job persists teardown ownership before any Secret exists or code can execute.
	r.Job.Spec.Suspend = ptr(true)
	created, err := l.Kube.BatchV1().Jobs(l.Config.Namespace).Create(ctx, r.Job, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return true, fmt.Errorf("Job already exists: %w", err)
		}
		_, lookup := l.Kube.BatchV1().Jobs(l.Config.Namespace).Get(ctx, r.Job.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(lookup) {
			return true, errors.Join(err, l.Boundary.Release(ctx, r.Reservation))
		}
		return true, errors.Join(err, lookup)
	}
	if err := validateAdmittedJob(r.Job, created); err != nil {
		created = created.DeepCopy()
		delete(created.Annotations, bootstrapAnnotation)
		return true, errors.Join(err, l.teardown(ctx, created, r.Reservation))
	}
	r.BootstrapSecret.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: created.Name, UID: created.UID, Controller: ptr(true), BlockOwnerDeletion: ptr(true)}}
	secret, err := l.Kube.CoreV1().Secrets(l.Config.Namespace).Create(ctx, r.BootstrapSecret, metav1.CreateOptions{})
	if err != nil {
		return true, errors.Join(err, l.teardown(ctx, created, r.Reservation))
	}
	validationErr := validateAdmittedBootstrap(r.BootstrapSecret, secret)
	if secret == nil || secret.Namespace != l.Config.Namespace || secret.Name == "" || secret.UID == "" {
		return true, errors.Join(errors.New("Kubernetes returned no owned bootstrap instance"), validationErr, l.teardown(ctx, created, r.Reservation))
	}
	retained, err := l.retainBootstrap(ctx, created, secret)
	if err != nil {
		return true, errors.Join(err, validationErr, l.deleteBootstrap(ctx, bootstrapReference{Name: secret.Name, UID: secret.UID}))
	}
	created = retained
	if validationErr != nil {
		return true, errors.Join(validationErr, l.teardown(ctx, created, r.Reservation))
	}
	// safety: publication may outlive the award's execution authority, so the award is checked again before activation.
	retirements, err := l.retirements(ctx, []CloudJobObservation{{Binding: award.Binding, JobNamespace: created.Namespace, JobName: created.Name, JobUID: string(created.UID), NodeUID: r.Reservation.NodeUID, Activation: &r.Reservation}})
	if err == nil && (retirements[created.Name].Delete || retirements[created.Name].Deferred) {
		err = errors.New("Cloud award ended before Job activation")
	}
	if err != nil {
		return true, errors.Join(err, l.teardown(ctx, created, r.Reservation))
	}
	state := retirements[created.Name]
	r.JobDeadline, err = cloudLeaseDeadline(time.Now(), award.Binding, state.LeaseExpiresAt)
	if err != nil {
		return true, errors.Join(err, l.teardown(ctx, created, r.Reservation))
	}
	launchedAt := time.Now()
	remaining := jobDeadlineSeconds(r.JobDeadline, launchedAt)
	if remaining <= 0 {
		return true, errors.Join(limits.ErrNoTime, l.teardown(ctx, created, r.Reservation))
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"uid": created.UID, "annotations": map[string]string{deadlineAnnotation: r.JobDeadline.Format(time.RFC3339Nano), launchAnnotation: launchedAt.Format(time.RFC3339Nano)}}, "spec": map[string]any{"suspend": false, "activeDeadlineSeconds": remaining}})
	if err != nil {
		return true, errors.Join(err, l.teardown(ctx, created, r.Reservation))
	}
	activated, err := l.Kube.BatchV1().Jobs(l.Config.Namespace).Patch(ctx, created.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return true, errors.Join(err, l.teardown(ctx, created, r.Reservation))
	}
	expected := created.DeepCopy()
	expected.Spec.Suspend, expected.Spec.ActiveDeadlineSeconds = ptr(false), &remaining
	expected.Annotations[deadlineAnnotation] = r.JobDeadline.Format(time.RFC3339Nano)
	expected.Annotations[launchAnnotation] = launchedAt.Format(time.RFC3339Nano)
	if err := validateAdmittedJob(expected, activated); err != nil {
		return true, errors.Join(err, l.rejectJob(ctx, created, award.Binding, r.Reservation, rejectedPod))
	}
	return true, l.enforceAward(ctx, activated, award.Binding, r.Reservation)
}

func (l *Launcher) retainBootstrap(ctx context.Context, job *batchv1.Job, secret *corev1.Secret) (*batchv1.Job, error) {
	// safety: persist the returned Secret UID before activation so restart cleanup cannot select a replacement.
	reference, err := json.Marshal(bootstrapReference{Name: secret.Name, UID: secret.UID, JobUID: job.UID})
	if err != nil {
		return nil, err
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"uid": job.UID, "annotations": map[string]string{bootstrapAnnotation: string(reference)}}})
	if err != nil {
		return nil, err
	}
	retained, err := l.Kube.BatchV1().Jobs(l.Config.Namespace).Patch(ctx, job.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err == nil {
		expected := job.DeepCopy()
		expected.Annotations[bootstrapAnnotation] = string(reference)
		// safety: activation validates both deadlines before execution, so suspended Jobs can accept concurrent renewal.
		if retained != nil {
			expected.Annotations[deadlineAnnotation] = retained.Annotations[deadlineAnnotation]
			expected.Spec.ActiveDeadlineSeconds = retained.Spec.ActiveDeadlineSeconds
		}
		err = validateAdmittedJob(expected, retained)
		if err == nil && retained.UID != job.UID {
			err = errors.New("Kubernetes changed the Cloud Job UID")
		}
	}
	if err != nil {
		return nil, fmt.Errorf("persist Cloud bootstrap cleanup target %s (%s); remove that Secret if cleanup also fails: %w", secret.Name, secret.UID, err)
	}
	return retained, nil
}

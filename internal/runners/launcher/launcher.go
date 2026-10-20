package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// CloudControl supplies awarded bindings from the controller and checks their continued authority.
// Sync must return exactly one decision for each supplied binding, including dead or cancelled claims.
type CloudControl interface {
	Prepare(context.Context, []capability.LeaseRef) (time.Time, error)
	Claim(context.Context) (*CloudJobAward, error)
	Sync(context.Context, []CloudJobObservation) ([]CloudJobState, error)
}

// CloudJobObservation retains the identities needed to reconcile after launcher restart.
type CloudJobObservation struct {
	Binding       CloudJobBinding
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
	Binding   CloudJobBinding
	Delete    bool
	Resources *match.Resources
}

// Launcher creates Cloud Jobs without executing pipeline code in the launcher process.
type Launcher struct {
	Kube     kubernetes.Interface
	Control  CloudControl
	Config   CloudJobConfig
	Boundary CloudBoundary
	Poll     time.Duration
	Logger   *slog.Logger

	mu       sync.Mutex
	waiting  map[string]*CloudJobAward
	identity string
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
	return nil
}

// Run verifies the job namespace admission policies, then reconciles existing Jobs before claiming further work until ctx is cancelled.
func (l *Launcher) Run(ctx context.Context) error {
	if err := l.validate(); err != nil {
		return err
	}
	if err := l.verifyAdmission(ctx); err != nil {
		return err
	}
	poll := l.Poll
	if poll <= 0 {
		poll = 5 * time.Second
	}
	logger := l.logger()
	var cooldown time.Time
	for ctx.Err() == nil {
		if !time.Now().Before(cooldown) {
			admitted := l.verifyAdmission(ctx)
			if admitted != nil {
				logger.Error("Cloud admission policies do not match this launcher; reconciling without claiming work", failureAttrs(admitted)...)
			}
			// safety: a faulty node is excluded from placement, so only
			// cluster-wide faults stop claims for every team.
			synced := l.sync(ctx)
			if blocking(synced) != nil {
				logger.Warn("launcher reconciliation failed", failureAttrs(synced)...)
				cooldown = time.Now().Add(retryWait(synced, 0))
			} else if synced != nil {
				logger.Error("Cloud node faults contained; quarantined nodes are cordoned and annotated "+quarantineAnnotation+"; claiming continues", "error", synced)
			}
			if blocking(synced) == nil && admitted == nil {
				if _, err := l.LaunchOne(ctx); err != nil {
					logger.Warn("launcher claim failed", failureAttrs(err)...)
					cooldown = time.Now().Add(retryWait(err, 0))
				}
			}
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
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
	award, err := l.Control.Claim(ctx)
	if err != nil || award == nil {
		return false, err
	}
	return l.launch(ctx, award)
}

func (l *Launcher) placeWaiting(ctx context.Context) error {
	var errs []error
	for _, award := range l.waitingAwards() {
		_, err := l.launch(ctx, award)
		var failure *apierr.Error
		if errors.Is(err, limits.ErrNoTime) || errors.As(err, &failure) && failure.Code == apierr.LockHeld {
			errs = append(errs, l.releaseCapacity(ctx, award.Binding))
		}
		if err != nil && !errors.Is(err, ErrNoDedicatedNode) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (l *Launcher) launch(ctx context.Context, award *CloudJobAward) (launched bool, err error) {
	prepared, stop, err := l.keepPreparation(ctx, award.Binding.LeaseRef())
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
	r, err := BuildJob(ctx, time.Now(), l.Config, *award, l.Boundary)
	if errors.Is(err, ErrNoDedicatedNode) {
		l.wait(award)
		return true, errors.Join(err, l.requestCapacity(ctx, *award))
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
	// safety: reservation probes and publication may outlive the award's execution authority.
	retirements, err := l.retirements(ctx, []CloudJobObservation{{Binding: award.Binding, JobNamespace: created.Namespace, JobName: created.Name, JobUID: string(created.UID), NodeUID: r.Reservation.NodeUID}})
	if err == nil && retirements[created.Name].Delete {
		err = errors.New("Cloud award ended before Job activation")
	}
	if err != nil {
		return true, errors.Join(err, l.teardown(ctx, created, r.Reservation))
	}
	remaining := int64(time.Until(r.JobDeadline) / time.Second)
	if remaining <= 0 {
		return true, errors.Join(limits.ErrNoTime, l.teardown(ctx, created, r.Reservation))
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]string{"resourceVersion": created.ResourceVersion}, "spec": map[string]any{"suspend": false, "activeDeadlineSeconds": remaining}})
	if err != nil {
		return true, errors.Join(err, l.teardown(ctx, created, r.Reservation))
	}
	activated, err := l.Kube.BatchV1().Jobs(l.Config.Namespace).Patch(ctx, created.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return true, errors.Join(err, l.teardown(ctx, created, r.Reservation))
	}
	expected := created.DeepCopy()
	expected.Spec.Suspend, expected.Spec.ActiveDeadlineSeconds = ptr(false), &remaining
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
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"uid": job.UID, "resourceVersion": job.ResourceVersion, "annotations": map[string]string{bootstrapAnnotation: string(reference)}}})
	if err != nil {
		return nil, err
	}
	retained, err := l.Kube.BatchV1().Jobs(l.Config.Namespace).Patch(ctx, job.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err == nil {
		expected := job.DeepCopy()
		expected.Annotations[bootstrapAnnotation] = string(reference)
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

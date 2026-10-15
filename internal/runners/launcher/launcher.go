package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// CloudControl supplies awarded bindings from the controller and checks their continued authority.
// Sync must return exactly one decision for each supplied binding, including dead or cancelled claims.
type CloudControl interface {
	Claim(context.Context) (*CloudJobAward, error)
	Sync(context.Context, []CloudJobObservation) ([]CloudJobState, error)
}

// CloudJobObservation retains the identities needed to reconcile after launcher restart.
type CloudJobObservation struct {
	Binding              CloudJobBinding
	JobName              string
	JobNamespace, JobUID string
	NodeUID              string
}

// CloudJobState carries the controller's teardown decision for one immutable award.
type CloudJobState struct {
	Binding CloudJobBinding
	Delete  bool
}

// Launcher creates Cloud Jobs without executing pipeline code in the launcher process.
type Launcher struct {
	Kube     kubernetes.Interface
	Control  CloudControl
	Config   CloudJobConfig
	Boundary CloudBoundary
	Poll     time.Duration
	Logger   *slog.Logger
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

// Run reconciles existing Jobs before claiming further work until ctx is cancelled.
func (l *Launcher) Run(ctx context.Context) error {
	if err := l.validate(); err != nil {
		return err
	}
	poll := l.Poll
	if poll <= 0 {
		poll = 5 * time.Second
	}
	logger := l.Logger
	if logger == nil {
		logger = slog.Default()
	}
	for ctx.Err() == nil {
		if err := l.Sync(ctx); err != nil {
			logger.Warn("launcher reconciliation failed", "error", err)
		} else if _, err := l.LaunchOne(ctx); err != nil {
			logger.Warn("launcher claim failed", "error", err)
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

// LaunchOne persists a suspended Job, creates its bootstrap Secret, then permits execution.
func (l *Launcher) LaunchOne(ctx context.Context) (bool, error) {
	if err := l.validate(); err != nil {
		return false, err
	}
	award, err := l.Control.Claim(ctx)
	if err != nil || award == nil {
		return false, err
	}
	r, err := BuildJob(ctx, time.Now(), l.Config, *award, l.Boundary)
	if err != nil {
		return true, err
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
	if err == nil {
		err = validateAdmittedBootstrap(r.BootstrapSecret, secret)
	}
	if err != nil {
		if secret != nil && secret.Namespace == l.Config.Namespace && secret.Name != "" && secret.Name != r.BootstrapSecret.Name {
			retained, retainErr := l.retainBootstrap(ctx, created, secret)
			if retainErr != nil {
				return true, errors.Join(err, retainErr, l.deleteBootstrap(ctx, bootstrapReference{Name: secret.Name, UID: secret.UID}))
			}
			created = retained
		}
		return true, errors.Join(err, l.teardown(ctx, created, r.Reservation))
	}
	// safety: reservation probes and publication may outlive the award's execution authority.
	retirements, err := l.retirements(ctx, []CloudJobObservation{{Binding: award.Binding, JobName: created.Name, JobNamespace: created.Namespace, JobUID: string(created.UID), NodeUID: r.Reservation.NodeUID}})
	if err == nil && retirements[created.Name] {
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
	_, err = l.Kube.BatchV1().Jobs(l.Config.Namespace).Patch(ctx, created.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return true, errors.Join(err, l.teardown(ctx, created, r.Reservation))
	}
	return true, nil
}

func (l *Launcher) retainBootstrap(ctx context.Context, job *batchv1.Job, secret *corev1.Secret) (*batchv1.Job, error) {
	// safety: a renamed, unowned bootstrap must remain discoverable when deletion fails or the launcher restarts.
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

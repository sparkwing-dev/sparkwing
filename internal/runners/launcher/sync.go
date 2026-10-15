package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ErrContainersRunning keeps the reservation until Kubernetes confirms every container has stopped.
var ErrContainersRunning = errors.New("Cloud Job containers are still running")

// Sync reconstructs ownership from Kubernetes and retires expired, terminal or cancelled Jobs.
func (l *Launcher) Sync(ctx context.Context) error {
	if err := l.validate(); err != nil {
		return err
	}
	jobs, err := l.Kube.BatchV1().Jobs(l.Config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=" + managedByLauncher})
	if err != nil {
		return fmt.Errorf("list Cloud Jobs: %w", err)
	}
	var errs []error
	for _, job := range jobs.Items {
		binding, reservation, err := jobOwnership(job)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		remove := job.DeletionTimestamp != nil || jobFinished(job) || (job.Spec.Suspend != nil && *job.Spec.Suspend)
		deadline, deadlineErr := time.Parse(time.RFC3339Nano, job.Annotations[deadlineAnnotation])
		if deadlineErr != nil {
			errs = append(errs, fmt.Errorf("Job %s has invalid deadline: %w", job.Name, deadlineErr))
			continue
		}
		if !deadline.After(time.Now()) {
			remove = true
		}
		if !remove {
			states, err := l.Control.Sync(ctx, []CloudJobBinding{binding})
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if len(states) != 1 || states[0].Binding != binding {
				errs = append(errs, errors.New("controller sync returned a mismatched binding"))
				continue
			}
			remove = states[0].Delete
		}
		if remove {
			if err := l.teardown(ctx, &job, reservation); err != nil && !errors.Is(err, ErrContainersRunning) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func jobOwnership(job batchv1.Job) (CloudJobBinding, CloudNodeReservation, error) {
	var binding CloudJobBinding
	var reservation CloudNodeReservation
	if err := json.Unmarshal([]byte(job.Annotations[bindingAnnotation]), &binding); err != nil {
		return binding, reservation, fmt.Errorf("Job %s has invalid binding: %w", job.Name, err)
	}
	if err := json.Unmarshal([]byte(job.Annotations[reservationAnnotation]), &reservation); err != nil {
		return binding, reservation, fmt.Errorf("Job %s has invalid reservation: %w", job.Name, err)
	}
	if job.Name != jobName(binding) || reservation.JobName != job.Name || reservation.Team != binding.Team || reservation.NodeName != job.Spec.Template.Spec.NodeName || reservation.NodeUID == "" || reservation.ReservationID == "" {
		return binding, reservation, errors.New("Cloud Job ownership does not match its reservation")
	}
	return binding, reservation, nil
}

func (l *Launcher) teardown(ctx context.Context, job *batchv1.Job, reservation CloudNodeReservation) error {
	namespace := l.Config.Namespace
	foreground := metav1.DeletePropagationForeground
	jobErr := l.Kube.BatchV1().Jobs(namespace).Delete(ctx, job.Name, metav1.DeleteOptions{PropagationPolicy: &foreground, Preconditions: &metav1.Preconditions{UID: &job.UID}})
	if apierrors.IsNotFound(jobErr) {
		jobErr = nil
	}
	if jobErr != nil {
		// safety: failed UID fencing cannot authorize deletion of a replacement Job's bootstrap.
		current, lookupErr := l.Kube.BatchV1().Jobs(namespace).Get(ctx, job.Name, metav1.GetOptions{})
		if lookupErr != nil || current.UID != job.UID {
			return errors.Join(jobErr, lookupErr)
		}
	}
	secretErr := l.Kube.CoreV1().Secrets(namespace).Delete(ctx, job.Name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(secretErr) {
		secretErr = nil
	}
	if err := errors.Join(jobErr, secretErr); err != nil {
		return err
	}
	pods, err := l.Kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: JobLabel + "=" + job.Name})
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		if !podStopped(pod) {
			return ErrContainersRunning
		}
	}
	if err = l.Boundary.Release(ctx, reservation); err != nil {
		return err
	}
	if !slices.Contains(job.Finalizers, cleanupFinalizer) {
		return nil
	}
	finalizers := slices.DeleteFunc(slices.Clone(job.Finalizers), func(s string) bool { return s == cleanupFinalizer })
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"uid": job.UID, "resourceVersion": job.ResourceVersion, "finalizers": finalizers}})
	if err != nil {
		return err
	}
	_, err = l.Kube.BatchV1().Jobs(namespace).Patch(ctx, job.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func podStopped(pod corev1.Pod) bool {
	// safety: an absent status on a live pod is not evidence that its privileged sidecar stopped.
	if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
		return false
	}
	statuses := append(slices.Clone(pod.Status.InitContainerStatuses), pod.Status.ContainerStatuses...)
	statuses = append(statuses, pod.Status.EphemeralContainerStatuses...)
	for _, status := range statuses {
		if status.State.Running != nil {
			return false
		}
	}
	return true
}

func jobFinished(job batchv1.Job) bool {
	for _, condition := range job.Status.Conditions {
		if (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed) && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

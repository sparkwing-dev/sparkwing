package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	rejectedAnnotation   = "sparkwing.dev/pod-rejected"
	rejectedPod          = "true"
	rejectedUnadmittable = "unadmittable"
)

// safety: the persisted annotation makes every later sync repeat rejection until teardown succeeds.
func (l *Launcher) rejectJob(ctx context.Context, job *batchv1.Job, binding CloudJobBinding, reservation CloudNodeReservation, reason string) error {
	job, containErr := l.containJob(ctx, job, reservation, reason)
	l.logger().Error("Cloud Pod rejected; deleting Job and failing award", "job", job.Name, "unit", binding.UnitID, "generation", binding.Generation, "reason", job.Annotations[rejectedAnnotation], "error", containErr)
	states, err := l.Control.Sync(ctx, []CloudJobObservation{{Binding: binding, JobNamespace: job.Namespace, JobName: job.Name, JobUID: string(job.UID), NodeUID: reservation.NodeUID, Rejected: true}})
	if err == nil && (len(states) != 1 || !sameBinding(states[0].Binding, binding) || !states[0].Delete) {
		err = errors.New("controller did not retire rejected Cloud award")
	}
	// safety: containment and teardown failures belong to this Job, which every sync rejects again; only a
	// controller failure stops claims. A failed containment skips teardown, which would release the node.
	if blocking(err) != nil {
		return errors.Join(rejectionFault(job.Name, containErr), err)
	}
	if containErr != nil {
		return rejectionFault(job.Name, containErr)
	}
	return rejectionFault(job.Name, l.teardown(ctx, job, reservation))
}

// rejectionFault marks a rejected Job's cleanup failure as a teardown fault, which keeps claims going, and
// unlike teardownFault still reports containers that are running, so the rejection stays visible until done.
func rejectionFault(job string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %s: %w", errJobTeardown, job, err)
}

func (l *Launcher) containJob(ctx context.Context, job *batchv1.Job, reservation CloudNodeReservation, reason string) (*batchv1.Job, error) {
	if job.Annotations[rejectedAnnotation] == "" {
		patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"uid": job.UID, "annotations": map[string]string{rejectedAnnotation: reason}}})
		var recorded *batchv1.Job
		if err == nil {
			recorded, err = l.Kube.BatchV1().Jobs(l.Config.Namespace).Patch(ctx, job.Name, types.MergePatchType, patch, metav1.PatchOptions{})
		}
		if err != nil {
			// safety: deleting an unrecorded rejection would let teardown release the node.
			return job, errors.Join(err, l.Boundary.Retire(ctx, reservation))
		}
		job = recorded
	}
	var retireErr error
	if job.Annotations[rejectedAnnotation] != rejectedUnadmittable {
		retireErr = l.Boundary.Retire(ctx, reservation)
	}
	foreground := metav1.DeletePropagationForeground
	deleteErr := l.Kube.BatchV1().Jobs(l.Config.Namespace).Delete(ctx, job.Name, metav1.DeleteOptions{PropagationPolicy: &foreground, Preconditions: &metav1.Preconditions{UID: &job.UID}})
	if apierrors.IsNotFound(deleteErr) {
		deleteErr = nil
	}
	return job, errors.Join(retireErr, deleteErr)
}

func (l *Launcher) discardMalformedJob(ctx context.Context, job *batchv1.Job, cause error) error {
	l.logger().Error("deleting malformed Cloud Job", "job", job.Name, "error", cause)
	foreground := metav1.DeletePropagationForeground
	err := l.Kube.BatchV1().Jobs(l.Config.Namespace).Delete(ctx, job.Name, metav1.DeleteOptions{PropagationPolicy: &foreground, Preconditions: &metav1.Preconditions{UID: &job.UID}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return l.removeCleanupFinalizer(ctx, job.Name, job.UID)
}

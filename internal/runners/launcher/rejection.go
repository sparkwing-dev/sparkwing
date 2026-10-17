package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const rejectedAnnotation = "sparkwing.dev/pod-rejected"

func (l *Launcher) rejectJob(ctx context.Context, job *batchv1.Job, binding CloudJobBinding, reservation CloudNodeReservation) error {
	if job.Annotations[rejectedAnnotation] == "" {
		patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"uid": job.UID, "resourceVersion": job.ResourceVersion, "annotations": map[string]string{rejectedAnnotation: "true"}}})
		retained, err := l.Kube.BatchV1().Jobs(l.Config.Namespace).Patch(ctx, job.Name, types.MergePatchType, patch, metav1.PatchOptions{})
		if err != nil {
			return err
		}
		job = retained
	}
	logger := l.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Error("Cloud Pod rejected; deleting Job and failing award", "job", job.Name, "unit", binding.UnitID, "generation", binding.Generation)
	foreground := metav1.DeletePropagationForeground
	deleteErr := l.Kube.BatchV1().Jobs(l.Config.Namespace).Delete(ctx, job.Name, metav1.DeleteOptions{PropagationPolicy: &foreground, Preconditions: &metav1.Preconditions{UID: &job.UID}})
	if apierrors.IsNotFound(deleteErr) {
		deleteErr = nil
	}
	states, err := l.Control.Sync(ctx, []CloudJobObservation{{Binding: binding, JobNamespace: job.Namespace, JobName: job.Name, JobUID: string(job.UID), NodeUID: reservation.NodeUID, Rejected: true}})
	if err != nil {
		return errors.Join(deleteErr, err)
	}
	if len(states) != 1 || !sameBinding(states[0].Binding, binding) || !states[0].Delete {
		return errors.Join(deleteErr, errors.New("controller did not retire rejected Cloud award"))
	}
	return errors.Join(deleteErr, l.teardown(ctx, job, reservation))
}
